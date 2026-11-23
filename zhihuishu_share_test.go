package main

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 共享课作业考试页的 recruitId 编码 = AES-128-ECB(key=zhsECBKey) + PKCS7 + Base64。
// 这条向量直接钉住「密钥 + 填充 + ECB 模式 + 编码」四件事,改了任何一处都会红。
func TestAesSecretECB(t *testing.T) {
	got, err := aesSecretECB("smoke-test-2026", zhsECBKey)
	if err != nil {
		t.Fatalf("ECB 加密失败:%v", err)
	}
	if want := "ENSbE5LOmxYeCo9MotuMSw=="; got != want {
		t.Fatalf("ECB 密文不符:\n got %s\nwant %s", got, want)
	}
	if _, err := aesSecretECB("x", "short-key"); err == nil {
		t.Fatal("密钥长度不对时应返回错误")
	}
}

// payload 必须与站点真实请求同形:recruitId 是字符串、courseId 是数字,
// 顺序与站点一致(便于和文档里的密文样本对照)。
func TestZhsSharePayloads(t *testing.T) {
	if got, want := zhsShareCoursePayload(0), `{"status":0,"pageNo":1,"pageSize":50}`; got != want {
		t.Fatalf("课程列表 payload 不符:\n got %s\nwant %s", got, want)
	}
	hw := zhsShareHomeworkPayload("123456", "1000000001", zhsFlagPending)
	if want := `{"recruitId":"123456","courseId":1000000001,"flag":1,"pageSize":100,"pageNum":0}`; hw != want {
		t.Fatalf("作业 payload 不符:\n got %s\nwant %s", hw, want)
	}
	ex := zhsShareExamPayload("123456", "1000000001", zhsFlagFinished)
	if want := `{"courseId":1000000001,"recruitId":"123456","flag":2}`; ex != want {
		t.Fatalf("考试 payload 不符:\n got %s\nwant %s", ex, want)
	}
	// 类型也要对:服务端按数字解析 courseId,一旦被引号包起来就会解密失败
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(hw), &m); err != nil {
		t.Fatalf("作业 payload 不是合法 JSON:%v", err)
	}
	if _, ok := m["courseId"].(float64); !ok {
		t.Fatalf("courseId 应为数字,实际 %T", m["courseId"])
	}
	if _, ok := m["recruitId"].(string); !ok {
		t.Fatalf("recruitId 应为字符串,实际 %T", m["recruitId"])
	}
}

// 作业考试页深链:recruitId 要编码、URL 要转义,取不到就退回学生首页。
func TestZhsSharePageURL(t *testing.T) {
	c := zhsShareCourse{CourseID: "1000000001", RecruitID: "123456"}
	code, err := zhsShareRecruitCode("123456")
	if err != nil {
		t.Fatalf("编码 recruitId 失败:%v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(code)
	if err != nil || len(raw) != 16 {
		t.Fatalf("编码结果应是 16 字节的 base64,实际 %q(%v)", code, err)
	}
	got := zhsSharePageURL(c)
	want := zhsShareExamPageURL + url.QueryEscape(code)
	if got != want {
		t.Fatalf("深链不符:\n got %s\nwant %s", got, want)
	}
	if strings.Contains(got, "//") == false || !strings.Contains(got, "onlineexamh5new") {
		t.Fatalf("深链应指向作业考试页:%s", got)
	}
}

// 三个接口的响应结构各异,解析要容忍数字/字符串混杂、空列表。
func TestParseZhsShareResponses(t *testing.T) {
	courses, err := parseZhsShareCourses(`{"result":{"totalCount":1,"courseOpenDtos":[
	  {"courseId":1000000001,"recruitId":123456,"courseName":"示例学分课",
	   "teacherName":"老师","schoolName":"某大学","progress":"100.0%","courseEndTime":1794758399000}]},
	  "code":200}`)
	if err != nil {
		t.Fatalf("解析课程列表失败:%v", err)
	}
	if len(courses) != 1 || string(courses[0].CourseID) != "1000000001" ||
		string(courses[0].RecruitID) != "123456" || courses[0].CourseName != "示例学分课" {
		t.Fatalf("课程字段不符:%+v", courses)
	}

	hw, err := parseZhsHomework(`{"currentTime":1,"msg":"请求成功","status":"200","rt":{
	  "studentHomeworkList":[{"id":11,"examId":"h11","examName":"第一章测验","cpOrderNumber":"第一章",
	  "startTime":"2026-11-01 08:00:00","endDate":"2026-12-01 23:59:59","state":4,"score":80,
	  "totalScore":100,"problemNum":10,"examUserName":"老师"}],"next":-1}}`)
	if err != nil {
		t.Fatalf("解析作业失败:%v", err)
	}
	if len(hw) != 1 || hw[0].ExamName != "第一章测验" || hw[0].EndDate != "2026-12-01 23:59:59" || hw[0].ProblemNum != 10 {
		t.Fatalf("作业字段不符:%+v", hw)
	}

	// 全部已交时站点只回 {"rt":{"next":-1}},不应报错
	if list, err := parseZhsHomework(`{"rt":{"next":-1},"status":"200"}`); err != nil || len(list) != 0 {
		t.Fatalf("空作业列表应解析为空,实际 %v %v", list, err)
	}

	ex, err := parseZhsExams(`{"currentTime":1,"msg":"请求成功","status":"200","rt":[
	  {"id":"e1","examId":"x1","examName":"期末考试","startTime":"2026-12-01 09:00:00",
	   "endDate":"2026-12-20 23:59:59","submitTime":"2026-12-02 10:00:00","state":4,
	   "limitTime":60,"problemNum":40,"examUserName":"老师"}]}`)
	if err != nil {
		t.Fatalf("解析考试失败:%v", err)
	}
	if len(ex) != 1 || ex[0].ExamName != "期末考试" || ex[0].LimitTime != 60 {
		t.Fatalf("考试字段不符:%+v", ex)
	}
	if list, err := parseZhsExams(`{"rt":[],"status":"200"}`); err != nil || len(list) != 0 {
		t.Fatalf("空考试列表应解析为空,实际 %v %v", list, err)
	}
}

// 作业/考试 -> Item:类型、状态、跳转地址、描述都要对。
func TestZhsShareItems(t *testing.T) {
	c := zhsShareCourse{CourseID: "1000000001", RecruitID: "123456", CourseName: "示例学分课"}

	hw := zhsShareHomeworkItem(c, zhsHomework{
		ID: "11", ExamName: "第一章测验", Chapter: "第一章", Teacher: "老师",
		EndDate: "2026-12-01 23:59:59", ProblemNum: 10,
	}, zhsFlagPending)
	if hw.Kind != kindAIHomework || hw.Status != "未完成" {
		t.Fatalf("作业映射不符:%+v", hw)
	}
	if hw.Zy != "zhs:share:1000000001" {
		t.Fatalf("zy 应为 zhs:share:<courseId>,实际 %q", hw.Zy)
	}
	if !strings.Contains(hw.Desc, "第一章") || !strings.Contains(hw.Desc, "10 题") {
		t.Fatalf("描述应含章节与题量,实际 %q", hw.Desc)
	}
	if !strings.Contains(hw.URL, "onlineexamh5new") {
		t.Fatalf("作业跳转地址不符:%s", hw.URL)
	}

	ex := zhsShareExamItem(c, zhsExam{
		ID: "e1", ExamName: "期末考试", EndDate: "2026-12-20 23:59:59",
		ProblemNum: 40, LimitTime: 60, Teacher: "老师",
	}, zhsFlagFinished)
	if ex.Kind != kindAIExam || ex.Status != "已完成" {
		t.Fatalf("考试映射不符:%+v", ex)
	}
	if !strings.Contains(ex.Desc, "限时 60 分钟") {
		t.Fatalf("考试描述应含限时,实际 %q", ex.Desc)
	}
	// 作业与考试落在同一页,跳转地址相同
	if ex.URL != hw.URL {
		t.Fatalf("共享课的作业与考试应在同一页:%s vs %s", hw.URL, ex.URL)
	}
}

// 数据来源标识:乐学任务 zy 是纯数字,智慧树带前缀。
func TestPlatformOf(t *testing.T) {
	cases := map[string]string{
		"22532":             platformEclass,
		"zhs:ai:100000001":  platformZhs,
		"zhs:share:1000001": platformZhs,
	}
	for zy, want := range cases {
		if got := platformOf(zy); got != want {
			t.Fatalf("zy=%q 应判定为 %s,实际 %s", zy, want, got)
		}
	}
}

// 报告行要带上数据来源,速查页据此打标签与筛选。
func TestBuildReportPlatform(t *testing.T) {
	now := time.Now().In(TZ)
	end := now.Add(5 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	items := []Item{
		{Zy: "22532", Kind: "homework", ID: 1, Title: "乐学作业", End: end, SC: -1},
		{Zy: "zhs:share:1000001", Kind: kindAIHomework, ID: 2, Title: "共享课作业",
			End: end, Status: "未完成", URL: "https://example/task"},
		{Zy: "zhs:ai:100000003", Kind: kindAITopic, ID: 3, Title: "话题讨论",
			End: end, Status: "未完成", URL: "https://example/task2"},
	}
	courses := []Course{
		{ZyID: "22532", Title: "乐学课程"},
		{ZyID: "zhs:share:1000001", Title: "示例学分课"},
		{ZyID: "zhs:ai:100000003", Title: "示例智慧课"},
	}
	rep := buildReport(courses, items, nil, 3, nil)
	got := map[string]string{}
	for _, r := range rep.Rows {
		got[r.Title] = r.Platform
	}
	if got["乐学作业"] != platformEclass {
		t.Fatalf("乐学任务来源应为 %s,实际 %q", platformEclass, got["乐学作业"])
	}
	for _, title := range []string{"共享课作业", "话题讨论"} {
		if got[title] != platformZhs {
			t.Fatalf("%s 来源应为 %s,实际 %q", title, platformZhs, got[title])
		}
	}
}

// 网关被风控/未预热时会回智慧树登录页 HTML(HTTP 200),必须与「JSON 格式变了」区分开。
func TestLooksLikeHTML(t *testing.T) {
	for _, body := range []string{"\n\n<!DOCTYPE html>\n<html>", "<html></html>", "  <script>"} {
		if !looksLikeHTML(body) {
			t.Fatalf("%q 应判定为 HTML", body)
		}
	}
	for _, body := range []string{`{"rt":[]}`, "", "  \n{\"a\":1}"} {
		if looksLikeHTML(body) {
			t.Fatalf("%q 不应判定为 HTML", body)
		}
	}
}

// 未登录:401/403 或重定向到登录页(30x)。
func TestZhsAPIResultAuthFailed(t *testing.T) {
	cases := map[int]bool{200: false, 302: true, 401: true, 403: true, 500: false}
	for status, want := range cases {
		if got := (zhsAPIResult{Status: status}).authFailed(); got != want {
			t.Fatalf("HTTP %d 判定应为 %v,实际 %v", status, want, got)
		}
	}
}
