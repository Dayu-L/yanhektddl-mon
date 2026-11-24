package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// app.ico 打入 exe:浏览器窗口图标(favicon)
//
//go:embed assets/app.ico
var faviconIco []byte

//go:embed assets/app.png
var faviconPNG []byte

// app.css 打入 exe:三页共用的 reset / 设计令牌 / 共用控件
//
//go:embed web/app.css
var appCSS string

// ---------------------------------------------------------------- 速查页数据
// 把 ddl_data.json 变成 dashboard.html 使用的 JSON(对应 python snapshot())

type jrow struct {
	Course   string `json:"course"`
	Kind     string `json:"kind"`
	Platform string `json:"platform"` // 乐学 / 智慧树
	Title    string `json:"title"`
	End    string `json:"end"`
	Status string `json:"status"`
	URL    string `json:"url"`
	Done   bool   `json:"done"`
	NoNeed bool   `json:"no_need"`
	Desc   string `json:"desc"`
	Key    string `json:"key"`  // 忽略标记用:zy#id
	Auto   bool   `json:"auto"` // 已过期超 48 小时,自动忽略(不可手动恢复)
}

func jrows(rs []*Row) []jrow {
	out := make([]jrow, 0, len(rs))
	for _, r := range rs {
		out = append(out, jrow{r.Course, r.Kind, r.Platform, r.Title,
			r.End.Format(time.RFC3339), r.Status, r.URL, r.Done, r.NoNeed, r.Full,
			ignoreKey(r.Zy, r.ID), r.AutoIgnored})
	}
	return out
}

// apiData 加载快照并计算速查页数据;文件不存在时返回空结构(页面提示刷新)
func apiData() (map[string]interface{}, error) {
	snap, err := loadSnapshot()
	if err != nil {
		return map[string]interface{}{
			"fetched_at": nil,
			"missed":     []jrow{}, "urgent": []jrow{}, "week": []jrow{},
			"later": []jrow{}, "unknown": []jrow{}, "done": []jrow{},
			"ignored":      []jrow{},
			"course_count": 0, "item_count": 0,
		}, nil
	}
	submitted := snap.SubmittedExams
	if submitted == nil {
		submitted = map[string][]interface{}{}
	}
	rep := buildReport(snap.Courses, snap.Items, submitted, 3, loadIgnored())

	return map[string]interface{}{
		"fetched_at": snap.FetchedAt,
		"missed":     jrows(rep.Bucket.Missed),
		"urgent":     jrows(rep.Bucket.Urgent),
		"week":       jrows(rep.Bucket.Week),
		"later":      jrows(rep.Bucket.Later),
		"unknown":    jrows(rep.Bucket.Unknown),
		"done":       jrows(rep.Bucket.Done),
		"ignored":    jrows(rep.Bucket.Ignored),
		// 标题行概览用(最近截止等细节由 hero 卡承载,不重复)
		"course_count": len(snap.Courses), "item_count": len(rep.Rows),
	}, nil
}

// renderPage 把数据注入 dashboard.html 的 /*__DATA__*/ null 占位
func renderPage() ([]byte, error) {
	data, err := apiData()
	if err != nil {
		return nil, err
	}
	j, err := marshalNoEscape(data)
	if err != nil {
		return nil, err
	}
	html := strings.Replace(dashboardHTML, "/*__DATA__*/ null", string(j), 1)
	return []byte(html), nil
}

func marshalNoEscape(v interface{}) ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	s := b.String()
	return []byte(strings.TrimSuffix(s, "\n")), nil
}

// ---------------------------------------------------------------- HTTP 服务

var refreshState struct {
	mu            sync.Mutex
	running       bool
	deep          bool // 正在跑的是深度扫描(设置页据此显示"深度扫描中")
	loginRequired bool
}

// startRefresh 抢占刷新位并异步跑一次抓取。
// 已有刷新/登录在跑时返回 false(不排队:同一 profile 同时只能有一个浏览器)。
func startRefresh(days int, deep bool) bool {
	refreshState.mu.Lock()
	if refreshState.running {
		refreshState.mu.Unlock()
		return false
	}
	// 登录向导占着浏览器(可见窗口等用户输入,可能长达 10 分钟):
	// 直接拒绝,避免刷新排在 profile 锁后面白等
	if loginBusy() || loginSourceRunning() {
		refreshState.mu.Unlock()
		logger.Printf("[刷新] 登录流程进行中,请等登录完成后再刷新")
		return false
	}
	refreshState.running = true
	refreshState.deep = deep
	refreshState.mu.Unlock()
	go runRefresh(days, deep)
	return true
}

// runRefresh 执行一次抓取(调用方必须已置 refreshState.running)。
func runRefresh(days int, deep bool) {
	defer func() {
		refreshState.mu.Lock()
		refreshState.running = false
		refreshState.deep = false
		refreshState.mu.Unlock()
	}()

	// 与登录向导/保温 ping 互斥:同一 profile 同时只能有一个浏览器
	// (openBrowser 的清锁逻辑会杀掉并发的同 profile Edge)
	profileMu.Lock()
	defer profileMu.Unlock()

	if deep {
		logger.Printf("[刷新] 开始深度扫描(逐门课,补齐已完成/已过期/问卷)…")
	} else {
		logger.Printf("[刷新] 开始后台抓取…")
	}
	// 后台刷新（含开机首轮与定时巡检）不弹登录窗:会话失效就只标记状态，
	// 由设置页的单源登录按钮决定要不要登录，避免窗口自己跳出来。
	snap, err := collectSnapshotBackground(days, deep)
	if err != nil {
		logger.Printf("[刷新] 抓取失败:%v", err)
		needLogin := errors.Is(err, errNotLoggedIn) || strings.Contains(err.Error(), "登录")
		setLoginRequired(needLogin)
		if needLogin {
			recordSource(sourceEclass, false, "会话已失效,需要重新登录")
		}
		return
	}
	setLoginRequired(false)
	writeReportsFromSnapshot(snap, days)
	logger.Printf("[刷新] 完成(deep=%v):%d 门课 %d 条任务", deep, len(snap.Courses), len(snap.Items))
}

// refreshDays 速查页/登录后的报告口径:与 /api/refresh 一致
const refreshDays = 3

// writeReportsFromSnapshot 把一次抓取结果写成 Markdown/CSV 报告。
// 后台刷新与「登录后立刻抓一次」共用。
func writeReportsFromSnapshot(snap *Snapshot, days int) {
	submitted := snap.SubmittedExams
	if submitted == nil {
		submitted = map[string][]interface{}{}
	}
	rep := buildReport(snap.Courses, snap.Items, submitted, days, loadIgnored())
	writeReports(rep, days)
}

func sendJSON(w http.ResponseWriter, code int, v interface{}) {
	j, _ := marshalNoEscape(v)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	w.Write(j)
}

func sendText(w http.ResponseWriter, code int, s string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	w.Write([]byte(s))
}

func newMux(port int) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/index.html", "/dashboard.html":
			if _, err := loadSnapshot(); err != nil {
				// 首次运行还没有数据 -> 只提供登录指引的向导页
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			page, err := renderPage()
			if err != nil {
				sendText(w, 500, err.Error())
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(page)
		default:
			sendText(w, 404, "not found")
		}
	})
	mux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		data, err := apiData()
		if err != nil {
			sendText(w, 500, err.Error())
			return
		}
		sendJSON(w, 200, data)
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		sendJSON(w, 200, map[string]bool{
			"running":        refreshRunning(),
			"deep":           refreshRunningDeep(),
			"login_required": loginRequired(),
		})
	})
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/x-icon")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write(faviconIco)
	})
	mux.HandleFunc("/app.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store") // 与页面同寿:升级 exe 后立刻生效
		w.Write([]byte(appCSS))
	})
	// 256px PNG 图标:同时供 <link rel=icon sizes=256> 使用。
	// Edge --app 窗口的任务栏按钮图标取自站点 favicon,声明大尺寸 PNG
	// 可让 Chromium 用它做光栅化源,避免任务栏图标发糊。
	mux.HandleFunc("/icon256.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write(faviconPNG)
	})
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte(loginHTML))
	})
	mux.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte(settingsHTML))
	})
	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			sendJSON(w, 200, currentSettings())
		case http.MethodPost:
			var in Settings
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				sendJSON(w, 400, map[string]string{"error": "请求格式错误"})
				return
			}
			if err := saveSettings(in); err != nil {
				sendJSON(w, 400, map[string]string{"error": err.Error()})
				return
			}
			logger.Printf("[设置] 已更新:自动检测=%v 时间=%s 关闭行为=%s",
				in.AutoCheck, in.CheckTime, in.CloseBehavior)
			sendJSON(w, 200, currentSettings())
		default:
			sendText(w, 405, "method not allowed")
		}
	})
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			sendText(w, 404, "not found")
			return
		}
		go runLogin()
		sendJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/login-status", func(w http.ResponseWriter, r *http.Request) {
		sendJSON(w, 200, loginSnapshot())
	})
	// 三路数据源的登录态(设置页):最近一次已知结论 + 是否正在检查
	mux.HandleFunc("/api/login-state", func(w http.ResponseWriter, r *http.Request) {
		sendJSON(w, 200, loginStatusSnapshot())
	})
	// 手动检查登录状态:起一次 headless Edge 逐个探测(异步,页面轮询 /api/login-state)
	mux.HandleFunc("/api/check-login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			sendText(w, 404, "not found")
			return
		}
		if !startLoginCheck() {
			sendJSON(w, 409, map[string]string{"error": "正在检查,请稍候"})
			return
		}
		logger.Printf("[检查] 开始探测三路会话…")
		sendJSON(w, 200, loginStatusSnapshot())
	})
	// 单源登录:只给查出未登录的那一路开可见窗口(异步,页面轮询 /api/login-state)
	mux.HandleFunc("/api/source-login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			sendText(w, 404, "not found")
			return
		}
		var body struct {
			Source string `json:"source"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			sendJSON(w, 400, map[string]string{"error": "请求格式错误"})
			return
		}
		if started, why := startSourceLogin(body.Source); !started {
			sendJSON(w, 409, map[string]string{"error": why})
			return
		}
		logger.Printf("[登录] 开始单源登录:%s", sourceName(body.Source))
		sendJSON(w, 200, loginStatusSnapshot())
	})
	// /api/refresh  body: {"deep":true} -> 强制深度扫描(设置页「深度扫描」入口);
	// 缺省走快路径。已有刷新在跑时返回 409。
	mux.HandleFunc("/api/refresh", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			sendText(w, 404, "not found")
			return
		}
		var body struct {
			Deep bool `json:"deep"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body) // 空 body = 快路径
		if !startRefresh(refreshDays, body.Deep) {
			sendJSON(w, 409, map[string]string{"error": "正在抓取,请稍候"})
			return
		}
		sendJSON(w, 200, map[string]interface{}{"ok": true, "deep": body.Deep})
	})
	mux.HandleFunc("/api/ignore", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			sendText(w, 404, "not found")
			return
		}
		var in struct {
			Key     string `json:"key"`
			Ignored bool   `json:"ignored"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			sendJSON(w, 400, map[string]string{"error": "请求格式错误"})
			return
		}
		if err := setIgnored(in.Key, in.Ignored); err != nil {
			sendJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		logger.Printf("[忽略] %s -> %v", in.Key, in.Ignored)
		sendJSON(w, 200, map[string]bool{"ok": true})
	})
	// 窗口存活探测:页面 EventSource 长连接,全部断开 -> 服务自动退出
	mux.HandleFunc("/api/live", liveServe)
	// 叫出窗口:重复启动 exe 时,新进程请求已有实例打开速查窗口后自己退出
	mux.HandleFunc("/api/open", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			sendText(w, 404, "not found")
			return
		}
		go openWindow(port)
		sendJSON(w, 200, map[string]bool{"ok": true})
	})
	// 手动退出(--quit):本机任意进程 POST 即可让服务优雅退出
	mux.HandleFunc("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			sendText(w, 404, "not found")
			return
		}
		sendJSON(w, 200, map[string]bool{"ok": true})
		logger.Printf(">> 收到退出请求,服务即将退出")
		go func() {
			time.Sleep(300 * time.Millisecond)
			quitFromWatchdog()
		}()
	})
	return mux
}

// ---------------------------------------------------------------- 窗口存活监控
// 桌面服务进程已脱离控制台,用户没有任何 GUI 手段感知/关闭它;因此把
// 「关闭速查窗口」定义为退出语义:页面用 EventSource 维持长连接(SSE,
// 不受浏览器后台节流影响),所有窗口断开后服务自动退出。

var live struct {
	mu       sync.Mutex
	n        int
	ever     bool
	lastSeen time.Time
	started  time.Time
}

func liveServe(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		sendText(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	flusher.Flush()

	live.mu.Lock()
	live.n++
	live.ever = true
	live.lastSeen = time.Now()
	live.mu.Unlock()
	defer func() {
		live.mu.Lock()
		live.n--
		if live.n == 0 {
			live.lastSeen = time.Now()
		}
		live.mu.Unlock()
	}()

	// 周期写注释行保活:连接死亡时写失败 -> 本协程退出 -> 计数减一
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// startLivenessWatchdog 存活看门狗。
//
// 普通模式(forceExit=false)只在设置选择“关闭即退出”时生效;选择
// “最小化到托盘”时窗口全关也保持服务运行。forceExit=true 用于托盘
// 创建失败的兜底:此时没有托盘入口,窗口全关必须退出。
func startLivenessWatchdog(forceExit bool) {
	live.mu.Lock()
	live.started = time.Now()
	live.mu.Unlock()
	go func() {
		for {
			time.Sleep(5 * time.Second)
			live.mu.Lock()
			n, ever := live.n, live.ever
			idle := time.Since(live.lastSeen)
			age := time.Since(live.started)
			live.mu.Unlock()

			if n > 0 {
				continue
			}
			if !forceExit && currentSettings().CloseBehavior != closeBehaviorExit {
				continue // 最小化到托盘:窗口关闭不退出
			}
			if !ever && age < 60*time.Second {
				continue // 尚无窗口连上:留 60s 给 Edge 首次弹窗
			}
			if loginBusy() || refreshRunning() || keepAliveBusy.Load() {
				continue // 登录/刷新/保温 ping 进行中:等它干完再退
			}
			if idle > 20*time.Second {
				logger.Printf(">> 所有速查窗口已关闭,按设置退出服务")
				quitFromWatchdog()
			}
		}
	}()
}

func refreshRunning() bool {
	refreshState.mu.Lock()
	defer refreshState.mu.Unlock()
	return refreshState.running
}

func refreshRunningDeep() bool {
	refreshState.mu.Lock()
	defer refreshState.mu.Unlock()
	return refreshState.running && refreshState.deep
}

func setLoginRequired(v bool) {
	refreshState.mu.Lock()
	refreshState.loginRequired = v
	refreshState.mu.Unlock()
}

func loginRequired() bool {
	refreshState.mu.Lock()
	defer refreshState.mu.Unlock()
	return refreshState.loginRequired
}
