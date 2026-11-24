package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// probeApp 必须能分辨「自己的服务」和「占了同一端口的别的程序」。
// 背景:旧 Python 版 ddl_server.py 也在 8765 上实现 /api/status,
// 返回 {"running": false};只认 running 字段会把旧版误判成自己,
// 新 exe 于是不开服务、窗口指向旧速查页(没有设置页)。
func TestProbeAppDistinguishesLegacyPython(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"we are here", `{"app":"yanhektddl","running":false,"deep":false,"login_required":false}`, true},
		// 没有 app 字段的老版本 exe 仍然算自己(靠 deep/login_required 认得出来)
		{"older go build", `{"running":false,"deep":true,"login_required":false}`, true},
		{"legacy python server", `{"running":false}`, false},
		{"legacy python busy", `{"running":true}`, false},
		{"other json", `{"ok":true}`, false},
		{"not json", `<html>hello</html>`, false},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(c.body))
		}))
		got := probeApp(srv.URL)
		srv.Close()
		if got != c.want {
			t.Errorf("%s: probeApp = %v, want %v", c.name, got, c.want)
		}
	}
}

// 端口上没人监听时必须安全返回 false,让调用方走换端口启动。
func TestProbeAppNoServer(t *testing.T) {
	if probeApp("http://127.0.0.1:1/api/status") {
		t.Fatal("端口无人监听时 probeApp 应返回 false")
	}
}
