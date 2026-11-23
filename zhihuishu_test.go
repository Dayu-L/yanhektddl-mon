package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// secretStr 的加密口径必须与站点逐字节一致,否则所有 /gateway/t/* 接口都会
// 返回「aes加密参数异常」。下面三条密文取自站点真实请求(2026-11 实测),
// 换成别的 key/iv 或改了填充方式都会立刻失败。
func TestAesSecretVectors(t *testing.T) {
	cases := []struct{ payload, want string }{
		{`{}`, "yaVfYIJIdqrHDXsDL3vsYw=="},
		{`{"status":0,"pageNo":1,"pageSize":5}`,
			"NEuH3llD9woD4DQgu0k6Uvp5yyo8WT84Dq5lU2iPwvgKNatePzJ+vu/4PRaNYy+K"},
		{`{"status":1,"pageNo":1,"pageSize":5}`,
			"WfBsR9nN4rmJr8WYuVEa7gS3yO+TJHUNeLBjW3EEIZiRPdqZbDjacXj0jGbYkFAq"},
	}
	for _, c := range cases {
		got, err := aesSecret(c.payload, zhsSiteAESKey, zhsSiteAESIv)
		if err != nil {
			t.Fatalf("加密 %s 失败:%v", c.payload, err)
		}
		if got != c.want {
			t.Fatalf("加密 %s 结果不符:\n got %s\nwant %s", c.payload, got, c.want)
		}
	}

	// 参数非法时应当报错,而不是默默产出一个错密文
	if _, err := aesSecret("{}", zhsSiteAESKey, "short"); err == nil {
		t.Fatal("iv 长度不对时应返回错误")
	}
	if _, err := aesSecret("{}", "short-key", zhsSiteAESIv); err == nil {
		t.Fatal("key 长度不对时应返回错误")
	}
}

// 课程列表里的 id 有时是字符串、有时是数字,classId 还会是数字;null 要能容忍。
func TestZhsCoursesUnmarshal(t *testing.T) {
	raw := `{"ok":true,"courses":[
	  {"courseId":"2100","classId":298312,"courseName":"示例课","className":"1班",
	   "teacherUserName":"老师","schoolName":"某大学","startTime":"2026-09-20 19:18:00",
	   "endTime":"2027-01-31 23:59:00","linkUrl":"https://ai.example/singleCourse/knowledgeStudy/2100/298312?mapUid=42"},
	  {"courseId":2101,"classId":null,"courseName":"另一门","linkUrl":""}
	]}`
	var res struct {
		OK      bool        `json:"ok"`
		Courses []zhsCourse `json:"courses"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("解析失败:%v", err)
	}
	if !res.OK || len(res.Courses) != 2 {
		t.Fatalf("解析结果不符:%+v", res)
	}
	c := res.Courses[0]
	if string(c.CourseID) != "2100" || string(c.ClassID) != "298312" {
		t.Fatalf("id 应统一成字符串:courseId=%q classId=%q", c.CourseID, c.ClassID)
	}
	if c.TeacherName != "老师" || c.SchoolName != "某大学" {
		t.Fatalf("字段映射不符:%+v", c)
	}
	if got := zhsMapUID(c.LinkURL); got != "42" {
		t.Fatalf("mapUid 应取到 42,实际 %q", got)
	}
	if c2 := res.Courses[1]; string(c2.CourseID) != "2101" || c2.ClassID != "" {
		t.Fatalf("数字 id 与 null 应能兼容:%+v", c2)
	}

	// 课程映射:zy 加前缀避免与延河 zhiyuan_id 撞车
	got := zhsCourseToCourse(c)
	if got.ZyID != "zhs:ai:2100" || got.Title != "示例课" || got.Teacher != "老师" {
		t.Fatalf("课程映射不符:%+v", got)
	}
	// 智慧课程也有课程关闭时间(endTime,北京时间字符串),归一后进「课程关闭时间」表
	if got.Platform != platformZhs {
		t.Fatalf("课程来源应为 %s,实际 %q", platformZhs, got.Platform)
	}
	if want := "2027-01-31 23:59"; got.EndAt == 0 ||
		time.Unix(got.EndAt, 0).In(TZ).Format("2006-01-02 15:04") != want {
		t.Fatalf("EndAt 应解析为 %s,实际 %d", want, got.EndAt)
	}
}

// 任务映射:类型、完成状态、跳转链接、描述前缀都要对。
func TestZhsItemFromTask(t *testing.T) {
	course := zhsCourse{
		CourseID: "2100", ClassID: "298312", CourseName: "示例课",
		LinkURL: "https://ai.example/singleCourse/knowledgeStudy/2100/298312?mapUid=42",
	}
	reply := true
	noReply := false

	cases := []struct {
		name      string
		task      zhsTask
		query     int
		wantKind  string
		wantDone  bool
		descHas   string
		urlHasAll []string
	}{
		{
			name:     "作业未完成",
			task:     zhsTask{ID: "1", Name: "第一章测验", Type: zhsTaskHomework, End: "2026-11-18 23:59:59", Desc: "<p>仅一次</p>"},
			query:    0,
			wantKind: kindAIHomework,
		},
		{
			name:     "作业已完成",
			task:     zhsTask{ID: "2", Name: "第二章测验", Type: zhsTaskHomework, Desc: "x"},
			query:    1,
			wantKind: kindAIHomework,
			wantDone: true,
		},
		{
			name:     "考试未完成",
			task:     zhsTask{ID: "3", Name: "期末考试", Type: zhsTaskExam},
			query:    0,
			wantKind: kindAIExam,
		},
		{
			name:     "话题未回复",
			task:     zhsTask{ID: "4", Name: "讨论题", Type: zhsTaskTopic, Reply: &noReply},
			query:    0,
			wantKind: kindAITopic,
			descHas:  "未回复",
		},
		{
			name:     "话题已回复",
			task:     zhsTask{ID: "5", Name: "讨论题2", Type: zhsTaskTopic, Reply: &reply},
			query:    0,
			wantKind: kindAITopic,
			wantDone: true,
			descHas:  "已回复",
		},
		{
			// 讨论已结束但从未回复:仍是漏交,不能因为“讨论结束”算已完成
			name:     "话题已结束但未回复",
			task:     zhsTask{ID: "6", Name: "讨论题3", Type: zhsTaskTopic, Status: 2, Reply: nil},
			query:    1,
			wantKind: kindAITopic,
			descHas:  "未回复",
		},
	}
	for _, ca := range cases {
		it := zhsItemFromTask(course, ca.task, ca.query)
		if it.Kind != ca.wantKind {
			t.Fatalf("%s:kind 应为 %s,实际 %s", ca.name, ca.wantKind, it.Kind)
		}
		done := it.Status == "已完成"
		if done != ca.wantDone {
			t.Fatalf("%s:完成状态不符(Status=%q)", ca.name, it.Status)
		}
		if it.Zy != "zhs:ai:2100" {
			t.Fatalf("%s:zy 应为 zhs:ai:2100,实际 %q", ca.name, it.Zy)
		}
		if ca.descHas != "" && !strings.Contains(it.Desc, ca.descHas) {
			t.Fatalf("%s:描述应包含 %q,实际 %q", ca.name, ca.descHas, it.Desc)
		}
		if !strings.Contains(it.URL, "/singleCourse/taskAndExam/2100/298312") ||
			!strings.Contains(it.URL, "mapUid=42") {
			t.Fatalf("%s:跳转地址不符:%s", ca.name, it.URL)
		}
	}
	// 链接要带上任务类型筛选,点开就落在对应的 tab
	if it := zhsItemFromTask(course, zhsTask{ID: "9", Type: zhsTaskTopic}, 0); !strings.Contains(it.URL, "taskType=3") {
		t.Fatalf("话题讨论的链接应带 taskType=3,实际 %s", it.URL)
	}
	// 取不到 mapUid 时不能拼出空参数
	noUID := zhsCourse{CourseID: "1", ClassID: "2"}
	if u := zhsTaskURL(noUID, zhsTaskHomework); strings.Contains(u, "mapUid") {
		t.Fatalf("没有 mapUid 时不应带该参数:%s", u)
	}
}

// 常态只拉待办侧;深扫才补已完成/已结束侧(与延河深扫口径一致)。
func TestZhsWantedLists(t *testing.T) {
	normal := zhsWantedLists(false)
	if len(normal) != 3 {
		t.Fatalf("常态应拉 3 组(作业/考试/话题的待办侧),实际 %d", len(normal))
	}
	for _, l := range normal {
		if l[1] != 0 {
			t.Fatalf("常态不应拉已完成侧:%v", l)
		}
	}
	deep := zhsWantedLists(true)
	if len(deep) != 5 {
		t.Fatalf("深扫应拉 5 组,实际 %d", len(deep))
	}
	if deep[3] != [2]int{zhsTaskHomework, 1} || deep[4] != [2]int{zhsTaskExam, 1} {
		t.Fatalf("深扫补的两组应为 作业/考试 的已完成侧,实际 %v", deep[3:])
	}
}

// 报告侧要认这两个新字段:显式 Status 决定完成与否,显式 URL 覆盖 eclass 拼出来的地址。
func TestBuildReportUsesExplicitStatusAndURL(t *testing.T) {
	now := time.Now().In(TZ)
	items := []Item{
		{Zy: "zhs:ai:2100", Kind: kindAITopic, ID: 1, Title: "未回复的讨论",
			End:    now.Add(2 * time.Hour).Format("2006-01-02 15:04:05"),
			Status: "未完成", URL: "https://ai.example/task?taskType=3"},
		{Zy: "zhs:ai:2100", Kind: kindAIHomework, ID: 2, Title: "已交的作业",
			End:    now.Add(2 * time.Hour).Format("2006-01-02 15:04:05"),
			Status: "已完成", URL: "https://ai.example/task?taskType=1"},
	}
	courses := []Course{{ZyID: "zhs:ai:2100", Title: "示例课"}}
	rep := buildReport(courses, items, nil, 3, nil)

	if len(rep.Bucket.Urgent) != 1 || rep.Bucket.Urgent[0].Title != "未回复的讨论" {
		t.Fatalf("未完成的智慧树任务应进临期分组,实际 %+v", rep.Bucket.Urgent)
	}
	if r := rep.Bucket.Urgent[0]; r.URL != "https://ai.example/task?taskType=3" {
		t.Fatalf("应使用数据源给的 URL,实际 %s", r.URL)
	}
	if len(rep.Bucket.Done) != 1 || rep.Bucket.Done[0].Status != "已完成" {
		t.Fatalf("显式标注已完成的任务应进 Done 组,实际 %+v", rep.Bucket.Done)
	}
	if k := rep.Bucket.Done[0].Kind; k != "作业" {
		t.Fatalf("kind 应显示为中文「作业」,实际 %q", k)
	}
	if rep.Bucket.Done[0].Course != "示例课" {
		t.Fatalf("课程名应对上,实际 %q", rep.Bucket.Done[0].Course)
	}
}

// 轮询用的探测表达式必须是「零参函数 -> Promise」,pollProbe 会包成
// (<expr>)() 后按 {ok:bool} 解析;同时 secretStr 必须被正确转义进 JS。
func TestZhsProbeExpr(t *testing.T) {
	secret, err := zhsSecret()
	if err != nil {
		t.Fatalf("计算 secretStr 失败:%v", err)
	}
	if secret != "yaVfYIJIdqrHDXsDL3vsYw==" {
		t.Fatalf("探测用的 secretStr 应为 {} 的密文,实际 %q", secret)
	}
	expr := zhsProbeExpr(secret)
	if !strings.HasPrefix(expr, "() => ") {
		t.Fatalf("应为零参函数表达式,实际开头:%s", expr[:20])
	}
	if !strings.Contains(expr, strconv.Quote(secret)) {
		t.Fatal("secretStr 未被正确转义进表达式")
	}
	if strings.Count(expr, "() =>") != 1 {
		t.Fatal("只应包一层零参函数")
	}
}

// 真实环境下的登录态探测:程序自己的 profile 通常还没登过智慧树,
// 此时必须判定为「未登录」(ok=false, err=nil),而不是异常 ——
// 否则登录向导会安静地跳过,用户永远等不到那段可选登录提示。
// 若该 profile 已登录,本用例只记日志不失败。
func TestZhsProbeUnauthenticated(t *testing.T) {
	if testing.Short() {
		t.Skip("联网冒烟测试,-short 跳过")
	}
	s, err := openBrowser(false)
	if err != nil {
		t.Fatalf("启动浏览器失败:%v", err)
	}
	defer s.close()

	secret, err := zhsSecret()
	if err != nil {
		t.Fatalf("计算 secretStr 失败:%v", err)
	}
	ok, perr := zhsProbe(s, secret)
	if perr != nil {
		t.Fatalf("探测应把「没登录」判成 ok=false,不该返回错误:%v", perr)
	}
	t.Logf("智慧树登录探测:已登录=%v", ok)
}
