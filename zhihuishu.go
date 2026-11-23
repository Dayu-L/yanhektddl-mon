package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------- 智慧树(在线学堂)
//
// 覆盖范围:学生首页「智慧课程」卡片下的作业测试 / 考试 / 话题讨论。
// 接口形态与实测结论见 docs/zhihuishu-api.md,要点:
//
//	· 登录态就是 CAS Cookie(域 .zhihuishu.com),没有 token;页面内
//	  fetch 带上 credentials 即可,无需模拟登录。
//	· 所有 /gateway/t/* 要求参数 secretStr = base64(AES-128-CBC-PKCS7(JSON))。
//	  学生首页网关(onlineservice-api)的 key/iv 是编译进前端的常量,
//	  在 Go 侧直接算(见 aesSecret / zhsSiteAESKey)。
//	· 智慧课程任务接口(kg-run-student)的签名由页面主世界的
//	  window.yxyz(payload, await window.labc(6)) 现算(RSA 包住会话密钥),
//	  无法离线复现,因此必须在智慧课程域内的页面里执行 JS(chromedp 的
//	  Runtime.evaluate 默认就在主世界,可直接拿到这两个全局函数)。
//
// 本文件只包含后端抓取逻辑;共享课(共享学分课)的作业/考试另行接入。

const (
	// 学生首页:智慧课程列表从这里取(网关是 onlineservice-api)
	zhsHomeURL = "https://onlineweb.zhihuishu.com/onlinestuh5"
	// 智慧课程页面来源:任务接口的签名 SDK 只在该域的页面里可用
	zhsAICourseOrigin = "https://ai-smart-course-student-pro.zhihuishu.com"
	// 课程列表接口(返回 courseId / classId / linkUrl 等)
	zhsAICourseAPI = "https://onlineservice-api.zhihuishu.com/gateway/t/v1/student/queryStudentAICourseList"
	// 任务列表接口:taskType 1=作业测试 2=考试 3=话题讨论
	//              status  0=未完成/进行中 1=已完成/已结束
	zhsAITaskAPI = "https://kg-run-student.zhihuishu.com/student/gateway/t/task/taskList"

	// 学生首页网关的 AES 密钥:前端 bundle 里的常量。
	// 平台换包后若所有接口都报「aes加密参数异常」,先回来核对这两个值。
	zhsSiteAESKey = "7q9oko0vqb3la20r"
	zhsSiteAESIv  = "1g3qqdh4jvbskb9x"
)

// 智慧课程任务类型(接口的 taskType 字段)
const (
	zhsTaskHomework = 1
	zhsTaskExam     = 2
	zhsTaskTopic    = 3
)

// Item.Kind 取值。与延河课堂的 homework/exam 分开,便于报告里区分来源,
// 中文名见 report.go 的 kindNames。
const (
	kindAIHomework = "ai_homework"
	kindAIExam     = "ai_exam"
	kindAITopic    = "ai_topic"
)

// zhsZYPrefix 智慧树课程在快照里的 zy 前缀。
// 延河课堂用数字 zhiyuan_id,这里加前缀避免两套课程 id 撞车(忽略清单的
// key 是 "zy#任务id",撞车会导致误忽略)。
const zhsZYPrefix = "zhs:ai:"

// errZhsNotLoggedIn 智慧树未登录(或签名 SDK 未加载):调用方跳过本平台,不影响延河。
var errZhsNotLoggedIn = errors.New("智慧树未登录")

// ---------------------------------------------------------------- 签名参数

// aesSecret 按智慧树网关的要求加密请求参数:
// base64(AES-128-CBC(PKCS7(JSON(payload)))),key/iv 见 zhsSiteAESKey/IV。
//
// 校验方式(写实现时请用单测锁住):aesSecret("{}", ...) 必须等于
// "yaVfYIJIdqrHDXsDL3vsYw==",该值取自站点真实请求。
func aesSecret(payload, key, iv string) (string, error) {
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return "", fmt.Errorf("AES 密钥无效:%w", err)
	}
	if len(iv) != block.BlockSize() {
		return "", fmt.Errorf("iv 应为 %d 字节,实际 %d", block.BlockSize(), len(iv))
	}
	pt := []byte(payload)
	pad := block.BlockSize() - len(pt)%block.BlockSize()
	for i := 0; i < pad; i++ {
		pt = append(pt, byte(pad))
	}
	ct := make([]byte, len(pt))
	cipher.NewCBCEncrypter(block, []byte(iv)).CryptBlocks(ct, pt)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// ---------------------------------------------------------------- 数据结构

// flexID 兼容同一字段既可能是字符串也可能是数字(课程/班级/任务 id 都是这样),
// 响应里也可能出现 null。
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*f = ""
		return nil
	}
	*f = flexID(strings.Trim(s, `"`))
	return nil
}

// zhsCourse 智慧课程列表项(只保留用得上的字段,字段名与接口一致)
type zhsCourse struct {
	CourseID    flexID `json:"courseId"`
	ClassID     flexID `json:"classId"`
	CourseName  string `json:"courseName"`
	ClassName   string `json:"className"`
	TeacherName string `json:"teacherUserName"`
	SchoolName  string `json:"schoolName"`
	StartTime   string `json:"startTime"`
	EndTime     string `json:"endTime"`
	LinkURL     string `json:"linkUrl"`
}

// zhsTask 智慧课程任务(js.go 的 jsZhsAITasks 已把接口原始字段裁剪成这些)
type zhsTask struct {
	ID     flexID `json:"id"`
	Name   string `json:"name"`
	Desc   string `json:"desc"`
	Type   int    `json:"type"`
	Status int    `json:"status"`
	Start  string `json:"start"`
	End    string `json:"end"`
	ExamID flexID `json:"examId"`
	Reply  *bool  `json:"reply"` // 仅话题讨论:是否已回复
}

// ---------------------------------------------------------------- 映射(纯函数,便于单测)

// zhsCourseToCourse 课程 -> 快照里的 Course。EndAt 留 0:报告里「课程开放
// 截止时间」一栏按延河课堂口径展示,智慧树课程暂不混进去。
func zhsCourseToCourse(c zhsCourse) Course {
	return Course{
		Title:   c.CourseName,
		Teacher: c.TeacherName,
		College: c.SchoolName,
		ZyID:    zhsZYPrefix + string(c.CourseID),
	}
}

// zhsMapUID 从课程链接里取 mapUid(任务页深链需要它;取不到就不带该参数)
func zhsMapUID(linkURL string) string {
	u, err := url.Parse(strings.TrimSpace(linkURL))
	if err != nil {
		return ""
	}
	return u.Query().Get("mapUid")
}

// zhsTaskURL 任务的跳转地址:课程「任务·作业·考试」页 + 类型筛选。
func zhsTaskURL(c zhsCourse, taskType int) string {
	base := fmt.Sprintf("%s/singleCourse/taskAndExam/%s/%s",
		zhsAICourseOrigin, c.CourseID, c.ClassID)
	q := url.Values{}
	q.Set("taskType", strconv.Itoa(taskType))
	q.Set("activeKey", "TaskAndExam")
	if uid := zhsMapUID(c.LinkURL); uid != "" {
		q.Set("mapUid", uid)
	}
	return base + "?" + q.Encode()
}

// zhsItemFromTask 把一条任务映射成报告用的 Item。
//
// 完成状态判定:
//   - 作业/考试:查询侧 status(0=未完成 1=已完成)即结论,直接采信;
//   - 话题讨论:接口的 status 表示讨论本身「进行中/已结束」,与「我是否回复」
//     无关,所以用 taskDiscussionVO.isReply 判定;未回复的即使讨论已结束也算未完成
//     (这正是需要提醒的漏交)。
func zhsItemFromTask(c zhsCourse, t zhsTask, queryStatus int) Item {
	kind := kindAIHomework
	switch t.Type {
	case zhsTaskExam:
		kind = kindAIExam
	case zhsTaskTopic:
		kind = kindAITopic
	}
	done := queryStatus == 1
	if t.Type == zhsTaskTopic {
		done = t.Reply != nil && *t.Reply
	}
	status := "未完成"
	if done {
		status = "已完成"
	}
	desc := strings.TrimSpace(t.Desc)
	if t.Type == zhsTaskTopic {
		if t.Reply != nil && *t.Reply {
			desc = "已回复。" + desc
		} else {
			desc = "未回复。" + desc
		}
	}
	return Item{
		Zy:     zhsZYPrefix + string(c.CourseID),
		Kind:   kind,
		ID:     string(t.ID),
		Title:  t.Name,
		End:    t.End,
		SC:     -1,
		Desc:   desc,
		Status: status,
		URL:    zhsTaskURL(c, t.Type),
	}
}

// zhsWantedLists 本次要拉哪些 (taskType, status) 组合。
// 常态只拉待办侧;深扫再补「已完成/已结束」侧,与延河深扫的口径一致。
// 话题讨论只拉待办侧:已结束的讨论没有提交动作,是否完成由 isReply 判定。
func zhsWantedLists(deep bool) [][2]int {
	out := [][2]int{
		{zhsTaskHomework, 0},
		{zhsTaskExam, 0},
		{zhsTaskTopic, 0},
	}
	if deep {
		out = append(out, [2]int{zhsTaskHomework, 1}, [2]int{zhsTaskExam, 1})
	}
	return out
}

// ---------------------------------------------------------------- 抓取流程

// zhsAuthFailure 判断一次列表请求的失败是否属于「没有有效登录会话」:
// 未登录时网关直接返回 401;有时也会被重定向到 CAS 登录页,拿回 HTML(not-json)。
// 这两种都当未登录处理,好让日志给出「去登录一次」的提示而不是干巴巴的报错。
func zhsAuthFailure(why string) bool {
	return strings.Contains(why, "401") ||
		strings.Contains(why, "not-json") ||
		strings.Contains(why, "HTTP 302")
}

// zhsSecret 学生首页网关的 secretStr。payload 固定为空对象:本程序只用它
// 做「课程列表」和「登录态探测」两类请求。
func zhsSecret() (string, error) {
	return aesSecret("{}", zhsSiteAESKey, zhsSiteAESIv)
}

// zhsProbeExpr 拼一个零参的探测表达式给 pollProbe 轮询:
// 直接复用真实拨取路径(jsZhsAICourses),登录成功就能拿到列表。
func zhsProbeExpr(secret string) string {
	return "() => (" + jsZhsAICourses + ")(" + strconv.Quote(secret) + ")"
}

// zhsProbe 探测智慧树登录态:导航到学生首页 + 试调一次课程列表。
// 返回 (已登录, 错误);errZhsNotLoggedIn 归为「确实没登录」,
// 其余错误当异常(网络/接口改动),不要误导用户去登录。
func zhsProbe(s *browserSession, secret string) (bool, error) {
	if err := s.navigate(zhsHomeURL, 0); err != nil {
		return false, err
	}
	if _, err := evalZhsAICourses(s, secret); err != nil {
		if errors.Is(err, errZhsNotLoggedIn) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// evalZhsAICourses 在学生首页里取智慧课程列表。
// secretStr 由 Go 侧算好传进去;页面 JS 只负责带 Cookie 发请求。
func evalZhsAICourses(s *browserSession, secret string) ([]zhsCourse, error) {
	arg, _ := json.Marshal(secret)
	var lastWhy string
	// 导航刚回来时 JS 上下文可能还没就绪,重试几次;明确失败(如未登录)则直接返回
	for attempt := 0; attempt < 3; attempt++ {
		out, err := s.eval("(" + jsZhsAICourses + ")(" + string(arg) + ")")
		if err == nil {
			var res struct {
				OK      bool        `json:"ok"`
				Why     string      `json:"why"`
				Courses []zhsCourse `json:"courses"`
			}
			if json.Unmarshal([]byte(out), &res) == nil {
				if res.OK {
					return res.Courses, nil
				}
				if zhsAuthFailure(res.Why) {
					return nil, fmt.Errorf("%w(%s)", errZhsNotLoggedIn, res.Why)
				}
				if res.Why != "" {
					lastWhy = res.Why
					return nil, fmt.Errorf("智慧树课程列表:%s", res.Why)
				}
			}
		}
		time.Sleep(700 * time.Millisecond)
	}
	if lastWhy != "" {
		return nil, fmt.Errorf("智慧树课程列表:%s", lastWhy)
	}
	return nil, errZhsNotLoggedIn
}

// evalZhsAITasks 在智慧课程页面里拉一门课的任务列表。
// 依赖页面主世界的 window.labc / window.yxyz(SDK 未加载时返回 ok=false)。
func evalZhsAITasks(s *browserSession, courseID string, lists [][2]int) (map[string][]zhsTask, error) {
	courseArg, _ := json.Marshal(courseID)
	listArg, _ := json.Marshal(lists)
	var lastWhy string
	for attempt := 0; attempt < 3; attempt++ {
		out, err := s.eval("(" + jsZhsAITasks + ")(" + string(courseArg) + "," + string(listArg) + ")")
		if err == nil {
			var res struct {
				OK    bool                 `json:"ok"`
				Why   string               `json:"why"`
				Lists map[string][]zhsTask `json:"lists"`
				Errs  map[string]string    `json:"errs"`
			}
			if json.Unmarshal([]byte(out), &res) == nil {
				if res.OK {
					for k, msg := range res.Errs {
						logger.Printf("[智慧树] 任务列表 %s 失败:%s", k, msg)
					}
					return res.Lists, nil
				}
				if res.Why != "" {
					lastWhy = res.Why
					return nil, fmt.Errorf("%s", res.Why)
				}
			}
		}
		time.Sleep(700 * time.Millisecond)
	}
	if lastWhy != "" {
		return nil, errors.New(lastWhy)
	}
	return nil, errZhsNotLoggedIn
}

// collectZhihuishu 抓取智慧课程(作业测试 / 考试 / 话题讨论)。
//
// 全程「尽力而为」:未登录、SDK 缺失、接口报错都只返回错误,由调用方记日志
// 后跳过 —— 智慧树是增量数据源,不能因为它让延河课堂的主流程失败。
// 注意:本函数会把浏览器导航到智慧树域,应在延河课堂各阶段之后调用。
func collectZhihuishu(s *browserSession, deep bool) ([]Course, []Item, error) {
	secret, err := zhsSecret()
	if err != nil {
		return nil, nil, err
	}
	if err := s.navigate(zhsHomeURL, 0); err != nil {
		return nil, nil, fmt.Errorf("打开学生首页失败:%w", err)
	}
	courses, err := evalZhsAICourses(s, secret)
	if err != nil {
		return nil, nil, err
	}
	if len(courses) == 0 {
		return nil, nil, nil
	}

	// 任务接口的签名 SDK 只在智慧课程域可用:借第一门课的深链进入该域,
	// 之后所有课程都用同一个页面调接口(courseId 是参数,不需要逐课导航)。
	jump := zhsAICourseOrigin + "/"
	if u := strings.TrimSpace(courses[0].LinkURL); u != "" {
		jump = u
	}
	if err := s.navigate(jump, 0); err != nil {
		return nil, nil, fmt.Errorf("打开智慧课程页失败:%w", err)
	}

	lists := zhsWantedLists(deep)
	var outCourses []Course
	var outItems []Item
	seen := map[string]bool{} // "zy#任务id" -> 已收录(深扫时两侧可能重合)
	for _, c := range courses {
		if c.CourseID == "" {
			continue
		}
		got, err := evalZhsAITasks(s, string(c.CourseID), lists)
		if err != nil {
			// 单门课失败不影响其它课程
			logger.Printf("[智慧树] 课程 %s 任务抓取失败:%v", c.CourseID, err)
			continue
		}
		for _, l := range lists {
			for _, t := range got[fmt.Sprintf("%d:%d", l[0], l[1])] {
				it := zhsItemFromTask(c, t, l[1])
				key := ignoreKey(it.Zy, idString(it.ID))
				if seen[key] {
					continue
				}
				seen[key] = true
				outItems = append(outItems, it)
			}
		}
		outCourses = append(outCourses, zhsCourseToCourse(c))
	}
	return outCourses, outItems, nil
}

// logZhsSkip 统一的「跳过智慧树」日志。登录提示由调用方根据场景决定是否打印
// (可见窗口才能弹登录,定时巡检没人能操作就不啰喗)。
func logZhsSkip(err error) {
	if errors.Is(err, errZhsNotLoggedIn) {
		logger.Printf("[智慧树] 未登录或会话失效(%v),本次不抓取智慧课程", err)
		return
	}
	logger.Printf("[智慧树] 已跳过:%v", err)
}
