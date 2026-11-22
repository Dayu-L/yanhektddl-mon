package main

import (
	"encoding/json"
	"testing"
	"time"
)

// 冒烟测试:真实启动 headless Edge → 导航 → 执行 JS → 干净退出。
// 不访问校园平台,无需登录。go test -run TestBrowserSmoke
func TestBrowserSmoke(t *testing.T) {
	s, err := openBrowser(false)
	if err != nil {
		t.Fatalf("启动浏览器失败:%v", err)
	}
	defer s.close()

	if err := s.navigate("about:blank", 0); err != nil {
		t.Fatalf("导航失败:%v", err)
	}
	out, err := s.eval("(() => 'smoke-ok')()")
	if err != nil {
		t.Fatalf("执行 JS 失败:%v", err)
	}
	if out != "smoke-ok" {
		t.Fatalf("JS 返回值异常:%q", out)
	}
}

// 复刻登录向导的两段式序列:headless 试探(未登录)-> 关闭 -> 立刻重开可见窗口。
// 这是 chromedp "chrome failed to start"(profile 锁未释放)最可能的发生点。
func TestReopenSequence(t *testing.T) {
	s, err := openBrowser(false)
	if err != nil {
		t.Fatalf("headless 启动失败:%v", err)
	}
	out, err := s.eval("(" + jsCheckLogin + ")()")
	if err != nil {
		s.close()
		t.Fatalf("登录探测失败:%v", err)
	}
	var chk struct {
		Ok bool `json:"ok"`
	}
	_ = json.Unmarshal([]byte(out), &chk)
	t.Logf("登录状态探测:ok=%v(未登录属预期)", chk.Ok)
	s.close()

	// 不加任何等待,模拟 collectSnapshot 里紧跟的可见窗口重启
	s2, err := openBrowser(true)
	if err != nil {
		t.Fatalf("可见窗口重启失败(chrome failed to start 复现?):%v", err)
	}
	defer s2.close()
	time.Sleep(500 * time.Millisecond)
}

// 验证 WebSocket 回退传输可用(旧版 Edge/Chrome、受限环境用这条路径)。
func TestBrowserWebSocketTransport(t *testing.T) {
	preferWebSocket.Store(true)
	defer preferWebSocket.Store(false)

	s, err := openBrowser(false)
	if err != nil {
		t.Fatalf("WebSocket 传输启动失败:%v", err)
	}
	defer s.close()

	if err := s.navigate("about:blank", 0); err != nil {
		t.Fatalf("导航失败:%v", err)
	}
	out, err := s.eval("(() => 'ws-ok')()")
	if err != nil {
		t.Fatalf("执行 JS 失败:%v", err)
	}
	if out != "ws-ok" {
		t.Fatalf("JS 返回值异常:%q", out)
	}
}
