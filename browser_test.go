package main

import "testing"

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
