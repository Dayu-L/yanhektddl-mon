package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitEval 轮询执行 JS,直到返回非空/非 "false" 或超时。
func waitEval(t *testing.T, s *browserSession, js string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		out, err := s.eval(js)
		if err == nil && out != "" && out != "false" && out != "0" {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待页面状态超时(最后输出 %q,最后一次错误 %v)", out, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// TestIgnoreUIE2E 真实浏览器全链路:速查页点“忽略”图标 ->
// /api/ignore 落盘 -> 页面重渲染,任务移入「已忽略」并出现恢复按钮。
func TestIgnoreUIE2E(t *testing.T) {
	// 隔离 ignored.json 与快照
	ignPath := filepath.Join(base, "ignored.json")
	backup, err := os.ReadFile(ignPath)
	had := err == nil
	os.Remove(ignPath)
	t.Cleanup(func() {
		os.Remove(ignPath)
		if had {
			os.WriteFile(ignPath, backup, 0o644)
		}
	})

	dataPath := filepath.Join(base, "ddl_data.json")
	oldData, err := os.ReadFile(dataPath)
	hadData := err == nil
	t.Cleanup(func() {
		if hadData {
			os.WriteFile(dataPath, oldData, 0o644)
		} else {
			os.Remove(dataPath)
		}
	})
	if err := os.WriteFile(dataPath, []byte(`{"fetched_at":"2026-11-22T20:00:00+08:00","courses":[{"zyId":"zy1","title":"数据结构","endAt":0}],"items":[{"zy":"zy1","kind":"homework","id":7,"title":"作业一","end":"2026-11-23T23:59:00+08:00","sc":0,"desc":"说明文字"}]}`), 0o644); err != nil {
		t.Fatalf("写测试快照失败:%v", err)
	}

	srv := httptest.NewServer(newMux(portFirst))
	defer srv.Close()

	s, err := openBrowser(false)
	if err != nil {
		t.Skipf("无法启动测试浏览器:%v", err)
	}
	defer s.close()

	if err := s.navigate(srv.URL, 0); err != nil {
		t.Fatalf("打开速查页失败:%v", err)
	}

	// 页面向 /api/data 拉数据后渲染出忽略按钮
	title := waitEval(t, s, `(() => {
		const b = document.querySelector('[data-ignore]');
		return b ? b.closest('.row').querySelector('.r-title').textContent : '';
	})()`, 20*time.Second)

	if _, err := s.eval(`(() => { document.querySelector('[data-ignore]').click(); return 'ok'; })()`); err != nil {
		t.Fatalf("点击忽略失败:%v", err)
	}

	// 落盘:ignored.json 应包含 zy1#7
	deadline := time.Now().Add(10 * time.Second)
	for !loadIgnored()["zy1#7"] {
		if time.Now().After(deadline) {
			t.Fatalf("点击后 ignored.json 未写入 zy1#7")
		}
		time.Sleep(200 * time.Millisecond)
	}

	// 页面重渲染:该任务出现在「已忽略」,并给出“取消忽略”按钮
	got := waitEval(t, s, `(() => {
		const sec = document.getElementById('ignoredlist');
		if (!sec) return '';
		const row = sec.querySelector('.row');
		if (!row || !row.querySelector('[data-unignore]')) return '';
		return row.querySelector('.r-title').textContent;
	})()`, 20*time.Second)
	if strings.TrimSpace(got) != strings.TrimSpace(title) {
		t.Fatalf("已忽略行标题应保持 %q,实际 %q", title, got)
	}

	// 待办分组中不应再出现该任务
	if out, err := s.eval(`(() => String(document.getElementById('urgent').querySelectorAll('.row').length))()`); err != nil {
		t.Fatalf("查询待办分组失败:%v", err)
	} else if out != "0" {
		t.Fatalf("忽略后 3 天内截止组应为空,实际 %s 行", out)
	}

	// 取消忽略:回到待办分组
	if _, err := s.eval(`(() => { document.querySelector('[data-unignore]').click(); return 'ok'; })()`); err != nil {
		t.Fatalf("点击取消忽略失败:%v", err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for loadIgnored()["zy1#7"] {
		if time.Now().After(deadline) {
			t.Fatalf("取消忽略后 ignored.json 中仍存在 zy1#7")
		}
		time.Sleep(200 * time.Millisecond)
	}
	waitEval(t, s, `(() => String(document.getElementById('urgent').querySelectorAll('.row').length))()`, 20*time.Second)
}
