package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// /api/ignore 往返:标记 -> 落盘可见 -> 取消 -> 消失;非法 key 返回 400。
// 测试隔离:先备份仓库目录可能存在的 ignored.json,结束恢复。
func TestIgnoreAPI(t *testing.T) {
	path := filepath.Join(base, "ignored.json")
	backup, err := os.ReadFile(path)
	had := err == nil
	os.Remove(path)
	t.Cleanup(func() {
		os.Remove(path)
		if had {
			os.WriteFile(path, backup, 0o644)
		}
	})

	srv := httptest.NewServer(newMux(portFirst))
	defer srv.Close()

	post := func(body string) *http.Response {
		resp, err := http.Post(srv.URL+"/api/ignore", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST /api/ignore 失败:%v", err)
		}
		resp.Body.Close()
		return resp
	}

	if resp := post(`{"key":"zy9#555","ignored":true}`); resp.StatusCode != 200 {
		t.Fatalf("标记忽略应返回 200,实际 %d", resp.StatusCode)
	}
	if !loadIgnored()["zy9#555"] {
		t.Fatalf("标记后 loadIgnored 应包含该 key")
	}

	if resp := post(`{"key":"zy9#555","ignored":false}`); resp.StatusCode != 200 {
		t.Fatalf("取消忽略应返回 200,实际 %d", resp.StatusCode)
	}
	if loadIgnored()["zy9#555"] {
		t.Fatalf("取消后 loadIgnored 不应再包含该 key")
	}

	if resp := post(`{"key":"","ignored":true}`); resp.StatusCode != 400 {
		t.Fatalf("空 key 应返回 400,实际 %d", resp.StatusCode)
	}

	// 速查页数据契约:不再有 closes,新增 ignored
	data, err := apiData()
	if err != nil {
		t.Fatalf("apiData 失败:%v", err)
	}
	if _, ok := data["closes"]; ok {
		t.Fatalf("apiData 不应再返回 closes(课程开放截止板块已删除)")
	}
	ig, ok := data["ignored"]
	if !ok {
		t.Fatalf("apiData 应返回 ignored 分组")
	}
	if _, ok := ig.([]jrow); !ok {
		t.Fatalf("ignored 应为行数组,实际 %T", ig)
	}
}

// 速查页渲染:忽略分组与行内 key 应进入页面数据;HTML 不再引用课程关闭板块。
func TestRenderPageIgnoreContract(t *testing.T) {
	if !strings.Contains(dashboardHTML, `data-ignore=`) {
		t.Fatalf("dashboard.html 应包含忽略按钮")
	}
	if strings.Contains(dashboardHTML, "课程开放截止") || strings.Contains(dashboardHTML, "cRowHTML") {
		t.Fatalf("dashboard.html 不应再包含课程开放截止板块")
	}

	// 快照文件与真实数据隔离:先备份,结束后恢复(不能直接删,否则会
	// 破坏用户目录里真实的抓取结果)
	dataPath := filepath.Join(base, "ddl_data.json")
	oldData, err := os.ReadFile(dataPath)
	hadData := err == nil
	f, err := os.Create(dataPath)
	if err != nil {
		t.Skipf("无法写快照文件,跳过渲染测试:%v", err)
	}
	t.Cleanup(func() {
		if hadData {
			os.WriteFile(dataPath, oldData, 0o644)
		} else {
			os.Remove(dataPath)
		}
	})
	f.WriteString(`{"fetched_at":"2026-11-22T20:00:00+08:00","courses":[` +
		`{"zyId":"zy1","title":"数据结构","endAt":0}],` +
		`"items":[{"zy":"zy1","kind":"homework","id":7,"title":"作业一",` +
		`"end":"2026-11-23T23:59:00+08:00","sc":0,"desc":""},` +
		`{"zy":"zy1","kind":"homework","id":8,"title":"旧作业",` +
		`"end":"2026-11-18T23:59:00+08:00","sc":0,"desc":""}]}`)
	f.Close()

	page, err := renderPage()
	if err != nil {
		t.Fatalf("renderPage 失败:%v", err)
	}
	s := string(page)
	if !strings.Contains(s, `"ignored":[`) {
		t.Fatalf("渲染数据应包含 ignored 分组")
	}
	if !strings.Contains(s, `"key":"zy1#7"`) {
		t.Fatalf("行数据应包含忽略标记 key")
	}
	if !strings.Contains(s, `"course_count":1`) || !strings.Contains(s, `"item_count":2`) {
		t.Fatalf("渲染数据应包含标题行概览计数")
	}
	if !strings.Contains(s, `"auto":true`) {
		t.Fatalf("过期超 48 小时的行应带 auto 标记")
	}
	if strings.Contains(s, `"closes"`) {
		t.Fatalf("渲染数据不应再包含 closes")
	}
}

// 登录状态接口:没有任何记录时也要给出合法结构(设置页直接渲染它),
// 且检查按钮只接受 POST。
func TestLoginStateAPI(t *testing.T) {
	oldBase := base
	base = t.TempDir()
	loginStatusMu.Lock()
	loginStatusData = nil
	loginStatusMu.Unlock()
	t.Cleanup(func() {
		base = oldBase
		loginStatusMu.Lock()
		loginStatusData = nil
		loginStatusMu.Unlock()
	})

	srv := httptest.NewServer(newMux(portFirst))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/login-state")
	if err != nil {
		t.Fatalf("GET /api/login-state 失败:%v", err)
	}
	defer resp.Body.Close()
	var got map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("登录状态响应应为 JSON:%v", err)
	}
	if _, ok := got["sources"]; !ok {
		t.Fatalf("响应应包含 sources")
	}
	if checking, ok := got["checking"].(bool); !ok || checking {
		t.Fatalf("初始 checking 应为 false,实际 %v", got["checking"])
	}

	// 检查登录/单源登录都会真的起浏览器,这里只验证方法守卫与参数校验
	resp2, err := http.Get(srv.URL + "/api/check-login")
	if err != nil {
		t.Fatalf("GET /api/check-login 失败:%v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 404 {
		t.Fatalf("GET /api/check-login 应返回 404(只支持 POST),实际 %d", resp2.StatusCode)
	}

	resp3, err := http.Post(srv.URL+"/api/source-login", "application/json",
		strings.NewReader(`{"source":"bogus"}`))
	if err != nil {
		t.Fatalf("POST /api/source-login 失败:%v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != 409 {
		t.Fatalf("未知数据源应被拒绝(409),实际 %d", resp3.StatusCode)
	}

	resp4, err := http.Post(srv.URL+"/api/source-login", "application/json",
		strings.NewReader(`not json`))
	if err != nil {
		t.Fatalf("POST /api/source-login(坏 JSON)失败:%v", err)
	}
	resp4.Body.Close()
	if resp4.StatusCode != 400 {
		t.Fatalf("坏 JSON 应返回 400,实际 %d", resp4.StatusCode)
	}
}

// 共用样式表:三页都必须引用 /app.css,服务端也必须能给出它,
// 否则打包漏掉样式时页面会静默降级成无样式(而不是报错)。
func TestSharedStylesheet(t *testing.T) {
	pages := map[string]string{
		"dashboard.html": dashboardHTML,
		"login.html":     loginHTML,
		"settings.html":  settingsHTML,
	}
	for name, page := range pages {
		if !strings.Contains(page, `<link rel="stylesheet" href="/app.css">`) {
			t.Fatalf("%s 应引用共用样式表 /app.css", name)
		}
	}
	if !strings.Contains(appCSS, "@layer reset, base, components, page;") {
		t.Fatalf("app.css 应显式声明层顺序")
	}

	srv := httptest.NewServer(newMux(portFirst))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/app.css")
	if err != nil {
		t.Fatalf("GET /app.css 失败:%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/app.css 应返回 200,实际 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("/app.css 的 Content-Type 应为 text/css,实际 %q", ct)
	}
	for _, want := range []string{"--data-hue", ".btn-primary", ".spinner", "[hidden]"} {
		if !strings.Contains(appCSS, want) {
			t.Fatalf("app.css 应包含共用定义 %q", want)
		}
	}
}

// /api/refresh:已有刷新在跑时不能排队(同一 profile 只能有一个浏览器),
// 直接回 409;深扫/GitHub 深度入口靠 body {"deep":true} 区分。
func TestRefreshBusyGuard(t *testing.T) {
	srv := httptest.NewServer(newMux(portFirst))
	defer srv.Close()

	// 占用刷新位(不真正启动抓取)
	refreshState.mu.Lock()
	refreshState.running = true
	refreshState.deep = true
	refreshState.mu.Unlock()
	t.Cleanup(func() {
		refreshState.mu.Lock()
		refreshState.running = false
		refreshState.deep = false
		refreshState.mu.Unlock()
	})

	resp, err := http.Post(srv.URL+"/api/refresh", "application/json",
		strings.NewReader(`{"deep":true}`))
	if err != nil {
		t.Fatalf("POST /api/refresh 失败:%v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("已有刷新在跑时应返回 409,实际 %d", resp.StatusCode)
	}

	// /api/status 要能告诉设置页"正在深扫"
	sres, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status 失败:%v", err)
	}
	defer sres.Body.Close()
	var st map[string]bool
	if err := json.NewDecoder(sres.Body).Decode(&st); err != nil {
		t.Fatalf("/api/status 应为 JSON:%v", err)
	}
	if !st["running"] || !st["deep"] {
		t.Fatalf("status 应报告 running+deep,实际 %+v", st)
	}
}
