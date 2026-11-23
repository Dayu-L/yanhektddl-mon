package main

import (
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// ---------------------------------------------------------------- 智慧树 · 共享课
//
// 共享课(在线学堂的学分课,学生首页「共享课」卡片)与智慧课程是两套接口,
// 密钥与参数形态都不同,详见 docs/zhihuishu-api.md §3.1:
//
//	· 课程列表:onlineservice-api,secretStr 用 zhsSiteAESKey
//	· 作业/考试:studentexam-api / taurusexam-api,secretStr 用 zhsShareAESKey
//	· 作业考试页深链:需要把 recruitId 用 AES-128-ECB(zhsECBKey) 编码再 base64,
//	  这一点是实测反推出来的(站点点击「作业考试」时生成的 URL 就是它)
//
// 请求一律「Go 侧算好 secretStr、页面只负责带 Cookie 发」,因此这里只拼
// payload 与解析响应,不碰任何浏览器细节。

const (
	zhsShareCourseAPI   = "https://onlineservice-api.zhihuishu.com/gateway/t/v1/student/course/share/queryShareCourseInfo"
	zhsShareHomeworkAPI = "https://studentexam-api.zhihuishu.com/studentExam/gateway/t/v1/student/getStudentHomework"
	zhsShareExamAPI     = "https://taurusexam-api.zhihuishu.com/taurusExam/gateway/t/v1/student/getStudentFinalExam"

	// 作业/考试网关节点的 AES 密钥(前端常量)
	zhsShareAESKey = "onbfhdyvz8x7otrp"
	// 共享课作业考试页把 recruitId 编码成 base64 用的固定 key(AES-128-ECB/PKCS7)
	zhsECBKey = "86cc793c53dc851b"
	// 共享课作业考试页(hash 路由,recruitId 为上面编出来的串)
	zhsShareExamPageURL = "https://onlineexamh5new.zhihuishu.com/stuExamWeb.html#/webExamList?recruitId="

	// zhsShareZYPrefix 共享课在快照里的 zy 前缀(与智慧课程 zhs:ai: 区分)
	zhsShareZYPrefix = "zhs:share:"
)

// 作业/考试接口的 flag:1=未提交 2=已提交
const (
	zhsFlagPending  = 1
	zhsFlagFinished = 2
)

// aesSecretECB 与 aesSecret 同源,但用 ECB 模式(无 IV)。
// 共享课作业考试页给 recruitId 生成的编码就是它。
func aesSecretECB(payload, key string) (string, error) {
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return "", fmt.Errorf("AES 密钥无效:%w", err)
	}
	pt := []byte(payload)
	pad := block.BlockSize() - len(pt)%block.BlockSize()
	for i := 0; i < pad; i++ {
		pt = append(pt, byte(pad))
	}
	ct := make([]byte, len(pt))
	for i := 0; i < len(pt); i += block.BlockSize() {
		block.Encrypt(ct[i:i+block.BlockSize()], pt[i:i+block.BlockSize()])
	}
	return base64.StdEncoding.EncodeToString(ct), nil
}

// ---------------------------------------------------------------- 响应结构

type zhsShareCourse struct {
	CourseID      flexID `json:"courseId"`
	RecruitID     flexID `json:"recruitId"`
	CourseName    string `json:"courseName"`
	TeacherName   string `json:"teacherName"`
	SchoolName    string `json:"schoolName"`
	Progress      string `json:"progress"`
	CourseEndTime flexID `json:"courseEndTime"` // 毫秒时间戳(暂未用于报告,见 zhsShareCourseToCourse)
}

// zhsHomework 共享课作业(getStudentHomework 的 studentHomeworkList 元素)
type zhsHomework struct {
	ID         flexID `json:"id"`
	ExamID     flexID `json:"examId"`
	ExamName   string `json:"examName"`
	Chapter    string `json:"cpOrderNumber"` // 章节名
	StartTime  string `json:"startTime"`
	EndDate    string `json:"endDate"`
	State      int    `json:"state"`
	Score      flexID `json:"score"`
	TotalScore flexID `json:"totalScore"`
	ProblemNum int    `json:"problemNum"`
	CourseName string `json:"courseName"`
	Teacher    string `json:"examUserName"`
}

// zhsExam 共享课考试(getStudentFinalExam 的 rt 元素)
type zhsExam struct {
	ID         flexID `json:"id"`
	ExamID     flexID `json:"examId"`
	ExamName   string `json:"examName"`
	StartTime  string `json:"startTime"`
	EndDate    string `json:"endDate"`
	SubmitTime string `json:"submitTime"`
	State      int    `json:"state"`
	LimitTime  int    `json:"limitTime"`
	ProblemNum int    `json:"problemNum"`
	CourseName string `json:"courseName"`
	Teacher    string `json:"examUserName"`
}

// ---------------------------------------------------------------- payload 构造
// 字段顺序与站点真实请求保持一致,便于和文档里的密文样本对照。

// zhsShareCoursePayload 课程列表:status 0=进行中 1=已完成。
func zhsShareCoursePayload(status int) string {
	return fmt.Sprintf(`{"status":%d,"pageNo":1,"pageSize":50}`, status)
}

// zhsShareHomeworkPayload 作业列表。注意 recruitId 是字符串、courseId 是数字。
func zhsShareHomeworkPayload(recruitID, courseID string, flag int) string {
	return fmt.Sprintf(`{"recruitId":%s,"courseId":%s,"flag":%d,"pageSize":100,"pageNum":0}`,
		strconv.Quote(recruitID), courseID, flag)
}

// zhsShareExamPayload 考试列表。同样 recruitId 字符串、courseId 数字,且不带分页参数。
func zhsShareExamPayload(recruitID, courseID string, flag int) string {
	return fmt.Sprintf(`{"courseId":%s,"recruitId":%s,"flag":%d}`,
		courseID, strconv.Quote(recruitID), flag)
}

// zhsShareRecruitCode 把数字 recruitId 编成作业考试页 URL 里用的 base64 串。
func zhsShareRecruitCode(recruitID string) (string, error) {
	return aesSecretECB(recruitID, zhsECBKey)
}

// zhsSharePageURL 共享课的作业/考试都落在同一页(按 recruitId 分区),
// 编码失败时退回学生首页,宁可少跳一步也不要给出坏链接。
func zhsSharePageURL(c zhsShareCourse) string {
	code, err := zhsShareRecruitCode(string(c.RecruitID))
	if err != nil {
		return zhsHomeURL
	}
	return zhsShareExamPageURL + url.QueryEscape(code)
}

// zhsShareCourseToCourse 共享课 -> 快照里的 Course。
// EndAt 留 0:报告里「课程开放截止时间」一栏目前只放延河课堂口径的数据,
// 智慧树课程(sharedEnd 是毫秒)等前端做好平台标识后再合并进去。
func zhsShareCourseToCourse(c zhsShareCourse) Course {
	// 共享课给的是毫秒时间戳;courseEndTime 即课程关闭时间
	return Course{
		Title:    c.CourseName,
		Teacher:  c.TeacherName,
		College:  c.SchoolName,
		ZyID:     zhsShareZYPrefix + string(c.CourseID),
		EndAt:    endAtFromMillis(string(c.CourseEndTime)),
		Platform: platformZhs,
	}
}

// zhsShareStatus 由 flag 得出完成状态。
func zhsShareStatus(flag int) string {
	if flag == zhsFlagFinished {
		return statusDone
	}
	return statusUndone
}

// zhsShareHomeworkItem / zhsShareExamItem 任务 -> Item。
func zhsShareHomeworkItem(c zhsShareCourse, h zhsHomework, flag int) Item {
	desc := strings.TrimSpace(strings.Join(nonEmpty(h.Chapter, h.Teacher), " · "))
	if h.ProblemNum > 0 {
		desc = strings.TrimSpace(desc + " · " + strconv.Itoa(h.ProblemNum) + " 题")
	}
	return Item{
		Zy:     zhsShareZYPrefix + string(c.CourseID),
		Kind:   kindAIHomework,
		ID:     string(h.ID),
		Title:  h.ExamName,
		End:    h.EndDate,
		SC:     -1,
		Desc:   desc,
		Status: zhsShareStatus(flag),
		URL:    zhsSharePageURL(c),
	}
}

func zhsShareExamItem(c zhsShareCourse, e zhsExam, flag int) Item {
	desc := strings.TrimSpace(strings.Join(nonEmpty(e.Teacher), " · "))
	if e.ProblemNum > 0 {
		desc = strings.TrimSpace(desc + " · " + strconv.Itoa(e.ProblemNum) + " 题")
	}
	if e.LimitTime > 0 {
		desc = strings.TrimSpace(desc + " · 限时 " + strconv.Itoa(e.LimitTime) + " 分钟")
	}
	return Item{
		Zy:     zhsShareZYPrefix + string(c.CourseID),
		Kind:   kindAIExam,
		ID:     string(e.ID),
		Title:  e.ExamName,
		End:    e.EndDate,
		SC:     -1,
		Desc:   desc,
		Status: zhsShareStatus(flag),
		URL:    zhsSharePageURL(c),
	}
}

// nonEmpty 过滤空串,顺手去掉首尾空白。
func nonEmpty(ss ...string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ---------------------------------------------------------------- 响应解析

func parseZhsShareCourses(raw string) ([]zhsShareCourse, error) {
	var res struct {
		Code   int `json:"code"`
		Result struct {
			TotalCount int              `json:"totalCount"`
			Courses    []zhsShareCourse `json:"courseOpenDtos"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, fmt.Errorf("解析共享课列表失败:%w", err)
	}
	return res.Result.Courses, nil
}

func parseZhsHomework(raw string) ([]zhsHomework, error) {
	var res struct {
		Status string `json:"status"`
		RT     struct {
			List []zhsHomework `json:"studentHomeworkList"`
			Next int           `json:"next"`
		} `json:"rt"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, fmt.Errorf("解析作业列表失败:%w", err)
	}
	return res.RT.List, nil
}

func parseZhsExams(raw string) ([]zhsExam, error) {
	var res struct {
		Status string    `json:"status"`
		RT     []zhsExam `json:"rt"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, fmt.Errorf("解析考试列表失败:%w", err)
	}
	return res.RT, nil
}

// ---------------------------------------------------------------- 直接发 HTTP
//
// 共享课的三个网关都是纯 Cookie 鉴权、参数只要 secretStr,所以没必要绕页面
// fetch:直接从浏览器取 Cookie(CDP 返回的就是明文,含 HttpOnly),用 net/http
// 发请求。好处有三:
//   · 不受页面 CORS/Referer 策略影响 —— 实测在页面里发作业/考试请求会被拦成
//     "TypeError: Failed to fetch",而同一会话在 Go 里发就正常;
//   · 拿得到真实 HTTP 状态码与响应体,出问题能定位到是哪一层;
//   · 登录轮询不再依赖页面停在某个域名(用户可在登录页慢慢输密码)。

// zhsHTTP 一组带登录凭据的请求参数。
type zhsHTTP struct {
	client *http.Client
	cookie string
	ua     string
}

const zhsReferer = "https://onlineweb.zhihuishu.com/onlinestuh5"

// newZhsHTTP 从浏览器里取 .zhihuishu.com 的 Cookie 与 User-Agent。
func newZhsHTTP(s *browserSession) (*zhsHTTP, error) {
	nctx, cancel := context.WithTimeout(s.tab, actionTO)
	defer cancel()
	res, err := chromedp.Call(nctx, network.GetCookies, network.GetCookiesParams{})
	if err != nil {
		return nil, fmt.Errorf("读取智慧树 Cookie 失败:%w", err)
	}
	cookies := res.Cookies
	parts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if !strings.Contains(c.Domain, "zhihuishu.com") {
			continue
		}
		parts = append(parts, c.Name+"="+c.Value)
	}
	if len(parts) == 0 {
		return nil, errZhsNotLoggedIn
	}
	ua := ""
	if out, err := s.eval("navigator.userAgent"); err == nil {
		ua = strings.TrimSpace(out)
	}
	if ua == "" {
		ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
			"(KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0"
	}
	return &zhsHTTP{
		client: &http.Client{Timeout: 30 * time.Second},
		cookie: strings.Join(parts, "; "),
		ua:     ua,
	}, nil
}

// postForm 发一个 form-urlencoded 请求(secretStr 已在调用方算好)。
func (h *zhsHTTP) postForm(endpoint, secret string) (int, string, error) {
	body := url.Values{"secretStr": {secret}}.Encode()
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", h.cookie)
	req.Header.Set("User-Agent", h.ua)
	req.Header.Set("Referer", zhsReferer)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(raw), nil
}

// zhsAPIResult 一次接口调用的结果;Status 是 HTTP 状态码。
type zhsAPIResult struct {
	Status int
	Body   string
}

// authFailed 未登录时智慧树网关给 401/403,或把请求重定向到 CAS 登录页(30x)。
func (r zhsAPIResult) authFailed() bool {
	return r.Status == http.StatusUnauthorized ||
		r.Status == http.StatusForbidden ||
		(r.Status >= 300 && r.Status < 400)
}

// ---------------------------------------------------------------- 抓取流程

// collectZhsShare 抓共享课的作业与考试。
// 全程走 Go 侧 HTTP(见 newZhsHTTP),不依赖浏览器当前停在哪个页面。
func collectZhsShare(s *browserSession, deep bool) ([]Course, []Item, error) {
	h, err := newZhsHTTP(s)
	if err != nil {
		return nil, nil, err
	}

	// 1) 课程列表(进行中;深扫再补已完成 —— 已完成课程的作业一般也都交了)
	statuses := []int{0}
	if deep {
		statuses = append(statuses, 1)
	}
	var courses []zhsShareCourse
	for _, st := range statuses {
		secret, err := aesSecret(zhsShareCoursePayload(st), zhsSiteAESKey, zhsSiteAESIv)
		if err != nil {
			return nil, nil, err
		}
		res, err := h.postFormResult(zhsShareCourseAPI, secret)
		if err != nil {
			return nil, nil, err
		}
		if res.authFailed() {
			return nil, nil, fmt.Errorf("%w(HTTP %d)", errZhsNotLoggedIn, res.Status)
		}
		cs, perr := parseZhsShareCourses(res.Body)
		if perr != nil {
			return nil, nil, perr
		}
		courses = append(courses, cs...)
	}
	if len(courses) == 0 {
		return nil, nil, nil
	}

	// 2) 预热作业/考试网关,再重新取一次 Cookie
	zhsWarmUpExamHosts(s, courses[0])
	if h, err = newZhsHTTP(s); err != nil {
		return nil, nil, err
	}

	// 3) 逐课拉作业与考试:常态只拉待办侧,深扫再补已完成侧
	flags := []int{zhsFlagPending}
	if deep {
		flags = append(flags, zhsFlagFinished)
	}
	var outCourses []Course
	var outItems []Item
	seen := map[string]bool{}
	for _, c := range courses {
		courseID, recruitID := string(c.CourseID), string(c.RecruitID)
		if courseID == "" || recruitID == "" {
			continue
		}
		for _, flag := range flags {
			if list, err := h.fetchHomework(c, flag); err != nil {
				if errors.Is(err, errZhsNotLoggedIn) {
					return nil, nil, err
				}
				logger.Printf("[智慧树] 共享课作业 %s(flag=%d):%v", courseID, flag, err)
			} else {
				for _, hw := range list {
					it := zhsShareHomeworkItem(c, hw, flag)
					if k := ignoreKey(it.Zy, idString(it.ID)); !seen[k] {
						seen[k] = true
						outItems = append(outItems, it)
					}
				}
			}
			if list, err := h.fetchExams(c, flag); err != nil {
				if errors.Is(err, errZhsNotLoggedIn) {
					return nil, nil, err
				}
				logger.Printf("[智慧树] 共享课考试 %s(flag=%d):%v", courseID, flag, err)
			} else {
				for _, ex := range list {
					it := zhsShareExamItem(c, ex, flag)
					if k := ignoreKey(it.Zy, idString(it.ID)); !seen[k] {
						seen[k] = true
						outItems = append(outItems, it)
					}
				}
			}
		}
		outCourses = append(outCourses, zhsShareCourseToCourse(c))
	}
	return outCourses, outItems, nil
}

// zhsWarmUpExamHosts 预热作业/考试网关。
//
// studentexam-api / taurusexam-api 除了 .zhihuishu.com 的 CAS Cookie,还要各自域下
// 的会话 Cookie;这些只有真正打开过「作业考试」页(H5)才会下发。没预热时 POST
// 会被回成智慧树登录页的 HTML —— 这是实测踩到的坑,不是密钥或参数问题。
func zhsWarmUpExamHosts(s *browserSession, c zhsShareCourse) {
	if err := s.navigate(zhsSharePageURL(c), 0); err != nil {
		logger.Printf("[智慧树] 预热作业考试页失败:%v", err)
		return
	}
	// 页面加载后自己会打两次网关接口,顺便把 Cookie 种下来;给它几秒
	time.Sleep(4 * time.Second)
}

// looksLikeHTML 网关回登录页/风控页时是 HTML,解析 JSON 必然失败。
// 单独判出来,日志才说得清「是没登录/没预热」而不是「JSON 格式变了」。
func looksLikeHTML(body string) bool {
	trimmed := strings.TrimSpace(body)
	return strings.HasPrefix(trimmed, "<")
}

// fetchHomework / fetchExams 单次拉取,把「应答不是 200」也转成可读的错误。
func (h *zhsHTTP) fetchHomework(c zhsShareCourse, flag int) ([]zhsHomework, error) {
	secret, err := aesSecret(zhsShareHomeworkPayload(string(c.RecruitID), string(c.CourseID), flag), zhsShareAESKey, zhsSiteAESIv)
	if err != nil {
		return nil, err
	}
	res, err := h.postFormResult(zhsShareHomeworkAPI, secret)
	if err != nil {
		return nil, err
	}
	if res.authFailed() {
		return nil, fmt.Errorf("%w(HTTP %d)", errZhsNotLoggedIn, res.Status)
	}
	list, perr := parseZhsHomework(res.Body)
	if perr != nil {
		return nil, fmt.Errorf("HTTP %d:%w(响应前 160 字节:%q)", res.Status, perr, head(res.Body, 700))
	}
	return list, nil
}

func (h *zhsHTTP) fetchExams(c zhsShareCourse, flag int) ([]zhsExam, error) {
	secret, err := aesSecret(zhsShareExamPayload(string(c.RecruitID), string(c.CourseID), flag), zhsShareAESKey, zhsSiteAESIv)
	if err != nil {
		return nil, err
	}
	res, err := h.postFormResult(zhsShareExamAPI, secret)
	if err != nil {
		return nil, err
	}
	if res.authFailed() {
		return nil, fmt.Errorf("%w(HTTP %d)", errZhsNotLoggedIn, res.Status)
	}
	list, perr := parseZhsExams(res.Body)
	if perr != nil {
		return nil, fmt.Errorf("HTTP %d:%w(响应前 160 字节:%q)", res.Status, perr, head(res.Body, 700))
	}
	return list, nil
}

// head 取响应体前 n 个字节,用于把「其实拿到的是 HTML」这类问题写进日志。
func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// postFormResult 把 postForm 的 (status, body, err) 包成 zhsAPIResult。
func (h *zhsHTTP) postFormResult(endpoint, secret string) (zhsAPIResult, error) {
	status, body, err := h.postForm(endpoint, secret)
	if err != nil {
		return zhsAPIResult{}, err
	}
	return zhsAPIResult{Status: status, Body: body}, nil
}
