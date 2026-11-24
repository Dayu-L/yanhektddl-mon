package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 登录态状态机:记录 -> 快照 -> 落盘 -> 清缓存重读,结论与原因都要保留。
func TestLoginStateRoundTrip(t *testing.T) {
	oldBase := base
	base = t.TempDir()
	reset := func() {
		loginStatusMu.Lock()
		loginStatusData = nil
		loginCheckMsg = ""
		loginStatusMu.Unlock()
	}
	reset()
	t.Cleanup(func() {
		base = oldBase
		reset()
	})

	recordSource(sourceEclass, true, "")
	recordSource(sourceAita, false, "会话失效,课程关闭时间沿用上次快照")

	src, ok := loginStatusSnapshot()["sources"].(map[string]sourceState)
	if !ok {
		t.Fatalf("sources 应为 map[string]sourceState")
	}
	if s := src[sourceEclass]; !s.OK || s.CheckedAt == "" {
		t.Fatalf("乐学应记为已登录且带检查时间,实际 %+v", s)
	}
	if s := src[sourceAita]; s.OK || s.Why == "" {
		t.Fatalf("延河课堂应记为未登录且带原因,实际 %+v", s)
	}
	if _, ok := src[sourceZhs]; ok {
		t.Fatalf("没探测过的数据源不应出现在快照里")
	}

	// 落盘可见
	if _, err := os.Stat(filepath.Join(base, "login_status.json")); err != nil {
		t.Fatalf("login_status.json 应已写入:%v", err)
	}

	// 清缓存后应能从盘上读回同一结论
	reset()
	again := loginStatusSnapshot()["sources"].(map[string]sourceState)
	if again[sourceEclass].CheckedAt != src[sourceEclass].CheckedAt {
		t.Fatalf("重读后乐学的检查时间应一致")
	}
	if again[sourceAita].Why != "会话失效,课程关闭时间沿用上次快照" {
		t.Fatalf("重读后延河课堂的原因应一致,实际 %q", again[sourceAita].Why)
	}
}

// 文件损坏不应把设置页带崩:按"没有记录"处理。
func TestLoginStateCorruptFile(t *testing.T) {
	oldBase := base
	base = t.TempDir()
	t.Cleanup(func() {
		base = oldBase
		loginStatusMu.Lock()
		loginStatusData = nil
		loginStatusMu.Unlock()
	})
	loginStatusMu.Lock()
	loginStatusData = nil
	loginStatusMu.Unlock()

	if err := os.WriteFile(filepath.Join(base, "login_status.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("写坏文件失败:%v", err)
	}
	src := loginStatusSnapshot()["sources"].(map[string]sourceState)
	if len(src) != 0 {
		t.Fatalf("损坏文件应被忽略,实际读到 %d 项", len(src))
	}
}

// 真实探测冒烟:起 headless Edge 依次访问三个平台,结论必须写全三路。
// 判定标准是「能给出结论」,不是「必须已登录」——未登录机器上全部 false 也算通过。
func TestCheckLoginSourcesSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过联网/浏览器冒烟")
	}
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

	if msg := checkLoginSources(); msg != "" {
		t.Skipf("本次探测未完成(环境限制):%s", msg)
	}
	src := loginStatusSnapshot()["sources"].(map[string]sourceState)
	for _, k := range []string{sourceEclass, sourceAita, sourceZhs} {
		s, ok := src[k]
		if !ok || s.CheckedAt == "" {
			t.Fatalf("%s 应写入带时间的结论,实际 %+v", k, s)
		}
		t.Logf("%s: ok=%v why=%q", k, s.OK, s.Why)
	}
}
