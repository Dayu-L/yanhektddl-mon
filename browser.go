package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

var errNotLoggedIn = errors.New("未登录")

// ---------------------------------------------------------------- 浏览器会话

func findBrowserBin() string {
	candidates := []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// launchBrowser 启动持久化 Edge/Chrome(独立 profile,不影响日常使用的浏览器)。
func launchBrowser(headless bool) (*rod.Browser, error) {
	bin := findBrowserBin()
	if bin == "" {
		return nil, errors.New("未找到 Microsoft Edge / Chrome,无法启动抓取浏览器")
	}
	u, err := launcher.New().
		Bin(bin).
		UserDataDir(profileDir).
		Headless(headless).
		Set("no-first-run").
		Set("no-default-browser-check").
		Set("disable-blink-features", "AutomationControlled").
		Launch()
	if err != nil {
		return nil, err
	}
	b := rod.New().ControlURL(u)
	if err := b.Connect(); err != nil {
		return nil, err
	}
	return b, nil
}

func firstPage(b *rod.Browser) *rod.Page {
	if pages, err := b.Pages(); err == nil && len(pages) > 0 {
		return pages[0]
	}
	p, err := b.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil
	}
	return p
}

// evalJSON 在页面里执行 JS,返回其 JSON.stringify 的字符串结果
func evalJSON(page *rod.Page, js string, args ...interface{}) (string, error) {
	res, err := page.Eval(js, args...)
	if err != nil {
		return "", err
	}
	return res.Value.Str(), nil
}

// ---------------------------------------------------------------- 登录与抓取

// waitLogin 弹出可见窗口等待用户完成登录(含验证码),轮询直到会话生效。
func waitLogin(b *rod.Browser, timeout time.Duration) bool {
	page := firstPage(b)
	if page == nil {
		return false
	}
	_ = page.Navigate(portalURL)
	_ = proto.PageBringToFront{}.Call(page)
	fmt.Println()
	fmt.Println("============================================================")
	fmt.Println(">>> 需要登录延河课堂:请在刚弹出的 Edge 窗口中输入 <<<")
	fmt.Println("    账号 + 密码 + 图形验证码,然后点击[登录]")
	fmt.Printf("    (最长等待 %d 分钟,登录成功后会自动继续)\n", int(timeout.Minutes()))
	fmt.Println("============================================================")
	fmt.Println()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s, err := evalJSON(page, jsCheckLogin)
		if err != nil { // 页面可能被用户关闭 -> 自动重开
			page = firstPage(b)
			if page != nil {
				_ = page.Navigate(portalURL)
				_ = proto.PageBringToFront{}.Call(page)
			}
		} else {
			var chk struct {
				Ok bool `json:"ok"`
			}
			if json.Unmarshal([]byte(s), &chk) == nil && chk.Ok {
				fmt.Println(">> 登录成功,会话已保存到 browser_profile (下次免登录)")
				return true
			}
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

type collectResult struct {
	Courses   []Course
	Items     []Item
	Submitted map[string][]interface{}
}

// tryCollect 阶段1(延河课堂课程)+ 阶段2(eclass 任务)。
// 未登录 -> errNotLoggedIn;成功 -> 结构化结果。
func tryCollect(b *rod.Browser) (*collectResult, error) {
	page := firstPage(b)
	if page == nil {
		return nil, errors.New("无法创建浏览器页面")
	}
	// ---- 阶段 1:延河课堂(课程列表 + 课程关闭时间) ----
	if err := page.Navigate(portalURL); err != nil {
		return nil, err
	}
	time.Sleep(2 * time.Second)

	s, err := evalJSON(page, jsCheckLogin)
	if err != nil {
		return nil, err
	}
	var chk struct {
		Ok bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(s), &chk); err != nil || !chk.Ok {
		return nil, errNotLoggedIn
	}

	s, err = evalJSON(page, jsFetchCourses)
	if err != nil {
		return nil, err
	}
	var coursesRes struct {
		Total   int      `json:"total"`
		Courses []Course `json:"courses"`
		Error   string   `json:"error"`
	}
	if err := json.Unmarshal([]byte(s), &coursesRes); err != nil {
		return nil, err
	}
	if coursesRes.Error != "" {
		return nil, fmt.Errorf("课程列表获取失败:%s", coursesRes.Error)
	}
	fmt.Printf(">> 延河课堂:共 %d 门课\n", coursesRes.Total)

	// ---- 阶段 2:eclass(作业 / 考试 / 问卷 + 提交状态) ----
	var zyIDs []string
	for _, c := range coursesRes.Courses {
		if c.ZyID != "" {
			zyIDs = append(zyIDs, c.ZyID)
		}
	}
	res := &collectResult{Courses: coursesRes.Courses,
		Items: []Item{}, Submitted: map[string][]interface{}{}}
	if len(zyIDs) > 0 {
		// 任意课程页触发 CAS->eclass SSO,自动建立 eclass 会话
		_ = page.Navigate(eclassBase + "/course/" + zyIDs[0] + "/content")
		time.Sleep(3 * time.Second)
		s, err = evalJSON(page, jsCollectEclass, zyIDs)
		if err != nil {
			return nil, err
		}
		var ec struct {
			Items          []Item                   `json:"items"`
			SubmittedExams map[string][]interface{} `json:"submittedExams"`
		}
		if err := json.Unmarshal([]byte(s), &ec); err != nil {
			return nil, err
		}
		res.Items = ec.Items
		if ec.SubmittedExams != nil {
			res.Submitted = ec.SubmittedExams
		}
		fmt.Printf(">> 乐学 eclass:已扫描 %d 门课,带截止时间的任务 %d 条\n",
			len(zyIDs), len(res.Items))
	}
	return res, nil
}

// ---------------------------------------------------------------- 主流程

// collectSnapshot 完整抓取一次并保存 ddl_data.json;需要时自动弹 Edge 登录窗。
func collectSnapshot(days int, visible bool) (*Snapshot, error) {
	b, err := launchBrowser(!visible)
	if err != nil {
		return nil, err
	}

	res, err := tryCollect(b)
	if err == errNotLoggedIn { // 需要人工登录 -> 换可见窗口
		_ = b.Close()
		b, err = launchBrowser(false)
		if err != nil {
			return nil, err
		}
		if !waitLogin(b, loginWait) {
			_ = b.Close()
			return nil, errors.New("等待登录超时,未获取到有效会话")
		}
		res, err = tryCollect(b)
		if err == errNotLoggedIn {
			_ = b.Close()
			return nil, errors.New("登录后仍未获取到有效会话,请检查账号")
		}
	}
	if err != nil {
		_ = b.Close()
		return nil, err
	}
	_ = b.Close()

	snap := &Snapshot{
		FetchedAt:      time.Now().In(TZ).Format(time.RFC3339),
		Courses:        res.Courses,
		Items:          res.Items,
		SubmittedExams: res.Submitted,
	}
	if err := saveSnapshot(snap); err != nil {
		return nil, err
	}
	return snap, nil
}

func saveSnapshot(snap *Snapshot) error {
	f, err := os.Create(dataJSON)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	return enc.Encode(snap)
}

func loadSnapshot() (*Snapshot, error) {
	return loadSnapshotFrom(dataJSON)
}

func loadSnapshotFrom(path string) (*Snapshot, error) {
	b, err := os.ReadFile(dataJSON)
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}
