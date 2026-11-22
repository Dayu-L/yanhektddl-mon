package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeSettings(t *testing.T) {
	ok := []Settings{
		{AutoCheck: true, CheckTime: "09:00", CloseBehavior: closeBehaviorTray},
		{AutoCheck: false, CheckTime: "00:00", CloseBehavior: closeBehaviorExit},
		{AutoCheck: true, CheckTime: "23:59", CloseBehavior: closeBehaviorExit},
	}
	for _, s := range ok {
		if _, err := normalizeSettings(s); err != nil {
			t.Errorf("normalizeSettings(%+v) 意外失败:%v", s, err)
		}
	}

	bad := []Settings{
		{CheckTime: "9:00", CloseBehavior: closeBehaviorTray},
		{CheckTime: "24:00", CloseBehavior: closeBehaviorTray},
		{CheckTime: "09:60", CloseBehavior: closeBehaviorTray},
		{CheckTime: "09:00", CloseBehavior: "minimize"},
	}
	for _, s := range bad {
		if _, err := normalizeSettings(s); err == nil {
			t.Errorf("normalizeSettings(%+v) 应当失败", s)
		}
	}
}

// 只验证读取路径与非法提交:不触发计划任务的创建/删除。
func TestSettingsPageAndAPI(t *testing.T) {
	srv := httptest.NewServer(newMux(portFirst))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/settings")
	if err != nil {
		t.Fatalf("GET /settings 失败:%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /settings 状态码 = %d", resp.StatusCode)
	}
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	if !strings.Contains(buf.String(), "自动检测") {
		t.Fatalf("设置页内容异常")
	}

	resp2, err := http.Get(srv.URL + "/api/settings")
	if err != nil {
		t.Fatalf("GET /api/settings 失败:%v", err)
	}
	defer resp2.Body.Close()
	var got Settings
	if err := json.NewDecoder(resp2.Body).Decode(&got); err != nil {
		t.Fatalf("解析设置失败:%v", err)
	}
	if !validCheckTime(got.CheckTime) {
		t.Fatalf("返回的检测时间无效:%q", got.CheckTime)
	}

	body := strings.NewReader(`{"auto_check":true,"check_time":"25:00","close_behavior":"tray"}`)
	resp3, err := http.Post(srv.URL+"/api/settings", "application/json", body)
	if err != nil {
		t.Fatalf("POST /api/settings 失败:%v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法时间应返回 400,实际 %d", resp3.StatusCode)
	}
}
