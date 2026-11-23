package main

import (
	"strings"
	"testing"
	"time"
)

// 截止时间:乐学给 RFC3339 UTC、智慧树给北京时间字符串,归一后必须是同一种格式。
func TestNormalizeItemEndAndID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2026-12-13T15:59:00Z", "2026-12-13 23:59:00"}, // 乐学 UTC -> 北京时间
		{"2026-11-30 10:25:00", "2026-11-30 10:25:00"},  // 智慧树本来就是本地
		{"2026-12-01", "2026-12-01 00:00:00"},
		{"", ""}, // 没有截止时间就保持空(进「未知」分组由报告处理)
	}
	for _, c := range cases {
		got := normalizeItem(Item{Zy: "1", Kind: kindHomework, ID: 1, End: c.in}, nil)
		if got.End != c.want {
			t.Fatalf("end %q 应归一为 %q,实际 %q", c.in, c.want, got.End)
		}
	}

	// id 统一成字符串(乐学是数字、智慧树是字符串)
	if got := normalizeItem(Item{Zy: "1", Kind: kindHomework, ID: 12345}, nil); got.ID != "12345" {
		t.Fatalf("id 应统一为字符串,实际 %#v", got.ID)
	}
	if got := normalizeItem(Item{Zy: "1", Kind: kindHomework, ID: "abc"}, nil); got.ID != "abc" {
		t.Fatalf("字符串 id 应保持,实际 %#v", got.ID)
	}
}

// 类型词表收敛:三个来源 -> homework / exam / questionnaire / topic。
func TestNormalizeItemKind(t *testing.T) {
	cases := map[string]string{
		"homework":      kindHomework,
		kindAIHomework:  kindHomework,
		"exam":          kindExam,
		kindAIExam:      kindExam,
		"questionnaire": kindQuestionnaire,
		kindAITopic:     kindTopic,
		"something-new": "something-new", // 未知类型原样保留,不吞掉
	}
	for in, want := range cases {
		if got := canonicalKind(in); got != want {
			t.Fatalf("kind %q 应收敛为 %q,实际 %q", in, want, got)
		}
	}
	// 归一后中文名走同一张表
	if kindNames[kindExam] != "考试/测验" || kindNames[kindHomework] != "作业" {
		t.Fatalf("类型中文名未统一:%v", kindNames)
	}
}

// 状态:显式值直接采信;否则按乐学的侧写推断,结果永远是三个规范值之一。
func TestNormalizeItemStatus(t *testing.T) {
	submitted := map[string][]interface{}{"zy1": {101, "102"}}

	cases := []struct {
		name string
		it   Item
		want string
	}{
		{"显式已完成", Item{Zy: "zy1", Kind: kindHomework, ID: 1, Status: statusDone}, statusDone},
		{"显式未完成", Item{Zy: "zy1", Kind: kindHomework, ID: 1, Status: statusUndone}, statusUndone},
		{"考试已提交", Item{Zy: "zy1", Kind: kindExam, ID: 101, SC: -1}, statusDone},
		{"考试已提交(字符串 id)", Item{Zy: "zy1", Kind: kindExam, ID: "102", SC: -1}, statusDone},
		{"考试未提交", Item{Zy: "zy1", Kind: kindExam, ID: 999, SC: -1}, statusUndone},
		{"作业有提交数", Item{Zy: "zy1", Kind: kindHomework, ID: 2, SC: 2}, statusDone},
		{"作业提交数为 0", Item{Zy: "zy1", Kind: kindHomework, ID: 2, SC: 0}, statusUndone},
		{"作业提交数未知", Item{Zy: "zy1", Kind: kindHomework, ID: 2, SC: -1}, statusUnknown},
		{"非法显式值回落到推断", Item{Zy: "zy1", Kind: kindHomework, ID: 2, SC: 1, Status: "搞定"}, statusDone},
	}
	for _, c := range cases {
		got := normalizeItem(c.it, submitted)
		if got.Status != c.want {
			t.Fatalf("%s:状态应为 %q,实际 %q", c.name, c.want, got.Status)
		}
	}
	// 幂等:再归一一次不变
	once := normalizeItem(Item{Zy: "zy1", Kind: kindAIHomework, ID: 7, End: "2026-12-13T15:59:00Z", SC: 1}, submitted)
	twice := normalizeItem(once, submitted)
	if once != twice {
		t.Fatalf("归一应幂等:\n once %+v\n twice %+v", once, twice)
	}
}

// 跳转地址:乐学按 eclass 规则拼,智慧树用接口给的;都必须是可直接打开的最终地址。
func TestNormalizeItemURL(t *testing.T) {
	hw := normalizeItem(Item{Zy: "22708", Kind: kindHomework, ID: 8489}, nil)
	if want := eclassBase + "/course/22708/learning-activity#/8489"; hw.URL != want {
		t.Fatalf("作业链接不符:\n got %s\nwant %s", hw.URL, want)
	}
	ex := normalizeItem(Item{Zy: "22708", Kind: "exam", ID: 777}, nil)
	if want := eclassBase + "/course/22708/learning-activity#/exam/777"; ex.URL != want {
		t.Fatalf("考试链接不符:\n got %s\nwant %s", ex.URL, want)
	}
	// 已带地址的不覆盖
	own := "https://example/task?taskType=3"
	if got := normalizeItem(Item{Zy: "zhs:ai:1", Kind: kindAITopic, ID: 3, URL: own}, nil); got.URL != own {
		t.Fatalf("已有地址不应被改写:%s", got.URL)
	}
}

// 标题:压缩空白 + 限长(话题讨论的标题是整段问题)。
func TestNormalizeTitle(t *testing.T) {
	if got := normalizeTitle("  第一章\n\n  单元测试  "); got != "第一章 单元测试" {
		t.Fatalf("空白应被折叠,实际 %q", got)
	}
	long := strings.Repeat("问", maxTitleRunes+20)
	got := normalizeTitle(long)
	if r := []rune(got); len(r) != maxTitleRunes+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("超长标题应截到 %d 字并加省略号,实际 %d 字", maxTitleRunes, len(r))
	}
}

// 课程:旧快照没有 platform 字段时兜底为延河课堂;新数据保留自己的来源。
func TestNormalizeCourse(t *testing.T) {
	if got := normalizeCourse(Course{ZyID: "22532", EndAt: 1}); got.Platform != platformAita {
		t.Fatalf("旧快照课程应兜底为 %s,实际 %q", platformAita, got.Platform)
	}
	if got := normalizeCourse(Course{ZyID: "zhs:ai:1", Platform: platformZhs}); got.Platform != platformZhs {
		t.Fatalf("已有来源不应被改写:%q", got.Platform)
	}
}

// 智慧树两种课程关闭时间:共享课是毫秒时间戳,智慧课程是北京时间字符串。
func TestEndAtHelpers(t *testing.T) {
	if got := endAtFromMillis("1794758399000"); got != 1794758399 {
		t.Fatalf("毫秒应转成秒,实际 %d", got)
	}
	for _, bad := range []string{"", "0", "-1", "abc"} {
		if got := endAtFromMillis(bad); got != 0 {
			t.Fatalf("%q 应得到 0,实际 %d", bad, got)
		}
	}
	if got := endAtFromTime("2027-01-31 23:59:00"); got == 0 ||
		time.Unix(got, 0).In(TZ).Format("2006-01-02 15:04") != "2027-01-31 23:59" {
		t.Fatalf("时间字符串解析不符:%d", got)
	}
	if got := endAtFromTime(""); got != 0 {
		t.Fatalf("空字符串应得到 0,实际 %d", got)
	}
}

// 旧快照(--from-json 的老 ddl_data.json)也要能出正确报告:
// 那时没有 status/url,id 是数字,end 是 RFC3339 UTC。
func TestBuildReportLegacySnapshot(t *testing.T) {
	now := time.Now().In(TZ)
	items := []Item{
		{Zy: "22532", Kind: "homework", ID: 7, Title: "老格式作业",
			End: now.Add(2 * time.Hour).UTC().Format(time.RFC3339), SC: 0},
		{Zy: "22532", Kind: "exam", ID: 8, Title: "老格式考试",
			End: now.Add(3 * time.Hour).UTC().Format(time.RFC3339), SC: -1},
	}
	courses := []Course{{ZyID: "22532", Title: "老课程", EndAt: now.Add(72 * time.Hour).Unix()}}
	rep := buildReport(courses, items, map[string][]interface{}{"22532": {"8"}}, 3, nil)

	if len(rep.Bucket.Urgent) != 1 {
		t.Fatalf("两小时后的作业应在临期分组,实际 %+v", rep.Bucket.Urgent)
	}
	hw := rep.Bucket.Urgent[0]
	if hw.Status != statusUndone || hw.Kind != "作业" {
		t.Fatalf("老快照应正常归一(状态 %q / 类型 %q)", hw.Status, hw.Kind)
	}
	if want := eclassBase + "/course/22532/learning-activity#/7"; hw.URL != want {
		t.Fatalf("老快照应补出链接:%s", hw.URL)
	}
	if hw.End.Location() != TZ || hw.End.Format(endLayout) == "" {
		t.Fatalf("截止时间应归一到北京时间:%v", hw.End)
	}
	// 考试在 submitted 里 -> 已完成
	var exam *Row
	for _, r := range rep.Bucket.Done {
		if r.Title == "老格式考试" {
			exam = r
		}
	}
	if exam == nil {
		t.Fatal("已提交的老快照考试应进已完成分组")
	}
	if exam.Kind != "考试/测验" {
		t.Fatalf("考试类型中文名应为「考试/测验」,实际 %q", exam.Kind)
	}
	if exam.URL != eclassBase+"/course/22532/learning-activity#/exam/8" {
		t.Fatalf("考试链接不符:%s", exam.URL)
	}
}

// 「课程开放截止时间」表现在也收智慧树课程,靠平台列区分来源。
func TestCloseTablePlatform(t *testing.T) {
	now := time.Now().In(TZ)
	courses := []Course{
		{ZyID: "22532", Title: "延河课", EndAt: now.Add(24 * time.Hour).Unix()},
		{ZyID: "zhs:ai:1", Title: "智慧课", EndAt: now.Add(48 * time.Hour).Unix(), Platform: platformZhs},
	}
	rep := buildReport(courses, nil, nil, 3, nil)
	if len(rep.Closes) != 2 {
		t.Fatalf("两门课都应进关闭时间表,实际 %d", len(rep.Closes))
	}
	got := map[string]string{}
	for _, c := range rep.Closes {
		got[c.Title] = c.Platform
	}
	if got["延河课"] != platformAita || got["智慧课"] != platformZhs {
		t.Fatalf("平台标识不符:%v", got)
	}
}
