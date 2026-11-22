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

// ---------------------------------------------------------------- 速查页数据
// 把 ddl_data.json 变成 dashboard.html 使用的 JSON(对应 python snapshot())

type jrow struct {
	Course string `json:"course"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	End    string `json:"end"`
	Status string `json:"status"`
	URL    string `json:"url"`
	Done   bool   `json:"done"`
	NoNeed bool   `json:"no_need"`
	Desc   string `json:"desc"`
	Key    string `json:"key"` // 忽略标记用:zy#id
}

func jrows(rs []*Row) []jrow {
	out := make([]jrow, 0, len(rs))
	for _, r := range rs {
		out = append(out, jrow{r.Course, r.Kind, r.Title,
			r.End.Format(time.RFC3339), r.Status, r.URL, r.Done, r.NoNeed, r.Full,
			ignoreKey(r.Zy, r.ID)})
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
			"ignored": []jrow{},
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
	loginRequired bool
}

func runRefresh(days int) {
	refreshState.mu.Lock()
	if refreshState.running {
		refreshState.mu.Unlock()
		return
	}
	refreshState.running = true
	refreshState.mu.Unlock()
	defer func() {
		refreshState.mu.Lock()
		refreshState.running = false
		refreshState.mu.Unlock()
	}()

	logger.Printf("[刷新] 开始后台抓取…")
	snap, err := collectSnapshot(days, false)
	if err != nil {
		logger.Printf("[刷新] 抓取失败:%v", err)
		setLoginRequired(errors.Is(err, errNotLoggedIn) || strings.Contains(err.Error(), "登录"))
		return
	}
	setLoginRequired(false)
	submitted := snap.SubmittedExams
	if submitted == nil {
		submitted = map[string][]interface{}{}
	}
	rep := buildReport(snap.Courses, snap.Items, submitted, days, loadIgnored())
	writeReports(rep, days)
	logger.Printf("[刷新] 完成:%d 门课 %d 条任务", len(snap.Courses), len(snap.Items))
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
			"login_required": loginRequired(),
		})
	})
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/x-icon")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write(faviconIco)
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
	mux.HandleFunc("/api/refresh", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			sendText(w, 404, "not found")
			return
		}
		go runRefresh(3)
		sendJSON(w, 200, map[string]bool{"ok": true})
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
			if loginBusy() || refreshRunning() {
				continue // 登录/刷新进行中:等它干完再退
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
