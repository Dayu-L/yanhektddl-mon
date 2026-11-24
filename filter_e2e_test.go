package main

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestKindFilterMultiSelectUIE2E 真实浏览器验证「类型」多选:
// 单个类型可独立开关、多个类型能叠加、再点一次取消、「全部」清空,
// 并且数据源那一维仍是单选。
func TestKindFilterMultiSelectUIE2E(t *testing.T) {
	// 隔离快照:测试自己写一份三天内、三种类型的任务
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

	now := time.Now().In(TZ)
	soon := now.Add(2 * time.Hour).Format("2006-01-02T15:04:05-07:00")
	mid := now.Add(30 * time.Hour).Format("2006-01-02T15:04:05-07:00")
	later := now.Add(100 * time.Hour).Format("2006-01-02T15:04:05-07:00")
	snapshot := fmt.Sprintf(`{"fetched_at":%q,"courses":[{"zyId":"zy1","title":"数据结构","endAt":0}],`+
		`"items":[{"zy":"zy1","kind":"homework","id":11,"title":"作业甲","end":%q,"sc":0,"desc":""},`+
		`{"zy":"zy1","kind":"exam","id":12,"title":"测验乙","end":%q,"sc":0,"desc":""},`+
		`{"zy":"zy1","kind":"questionnaire","id":13,"title":"问卷丙","end":%q,"sc":0,"desc":""}]}`,
		now.Format("2006-01-02T15:04:05-07:00"), soon, mid, later)
	if err := os.WriteFile(dataPath, []byte(snapshot), 0o644); err != nil {
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

	// 类型三选:全部 + 作业 + 考试/测验 + 问卷
	waitEval(t, s, `(() => {
		const chips = document.querySelectorAll('#filters .chip[data-kind]').length;
		return String(chips === 4 && document.querySelectorAll('#main .r-item').length === 3);
	})()`, 20*time.Second)

	// 快照:当前按下的胶囊文字 + 主列行数
	state := func() string {
		t.Helper()
		out, err := s.eval(`(() => JSON.stringify({
			pressed: Array.from(document.querySelectorAll('#filters .chip[data-kind][aria-pressed="true"]'))
				.map(c => c.dataset.kind),
			rows: document.querySelectorAll('#main .r-item').length
		}))()`)
		if err != nil {
			t.Fatalf("读取筛选状态失败:%v", err)
		}
		return out
	}
	clickKind := func(kind string) {
		t.Helper()
		if _, err := s.eval(`(() => {
			document.querySelector('#filters .chip[data-kind="` + kind + `"]').click();
			return 'ok';
		})()`); err != nil {
			t.Fatalf("点击类型 %q 失败:%v", kind, err)
		}
		time.Sleep(300 * time.Millisecond)
	}

	clickKind("作业")
	if got := state(); got != `{"pressed":["作业"],"rows":1}` {
		t.Fatalf("单选作业后状态 = %s", got)
	}

	// 叠加「问卷」:两个类型同时生效,行数应为 2 而不是互相替换
	clickKind("问卷")
	if got := state(); got != `{"pressed":["作业","问卷"],"rows":2}` {
		t.Fatalf("作业+问卷叠加后状态 = %s", got)
	}

	// 再点一次「作业」= 取消该类型,只剩问卷
	clickKind("作业")
	if got := state(); got != `{"pressed":["问卷"],"rows":1}` {
		t.Fatalf("取消作业后状态 = %s(应只剩问卷)", got)
	}

	// 「全部」清空全部类型选择
	clickKind("all")
	if got := state(); got != `{"pressed":["all"],"rows":3}` {
		t.Fatalf("点全部后状态 = %s", got)
	}
}
