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
//go:embed app.ico
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
}

func jrows(rs []*Row) []jrow {
	out := make([]jrow, 0, len(rs))
	for _, r := range rs {
		out = append(out, jrow{r.Course, r.Kind, r.Title,
			r.End.Format(time.RFC3339), r.Status, r.URL, r.Done, r.NoNeed, r.Full})
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
			"closes": []interface{}{},
		}, nil
	}
	submitted := snap.SubmittedExams
	if submitted == nil {
		submitted = map[string][]interface{}{}
	}
	rep := buildReport(snap.Courses, snap.Items, submitted, 3)

	closes := make([]map[string]interface{}, 0, len(rep.Closes))
	for _, c := range rep.Closes {
		closes = append(closes, map[string]interface{}{
			"title": c.Title, "college": c.College,
			"end": c.End.Format(time.RFC3339), "left": c.Left, "passed": c.Passed,
		})
	}
	return map[string]interface{}{
		"fetched_at": snap.FetchedAt,
		"missed":     jrows(rep.Bucket.Missed),
		"urgent":     jrows(rep.Bucket.Urgent),
		"week":       jrows(rep.Bucket.Week),
		"later":      jrows(rep.Bucket.Later),
		"unknown":    jrows(rep.Bucket.Unknown),
		"done":       jrows(rep.Bucket.Done),
		"closes":     closes,
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
	rep := buildReport(snap.Courses, snap.Items, submitted, days)
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

func newMux() http.Handler {
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
	return mux
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
