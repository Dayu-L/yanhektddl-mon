package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

var errNotLoggedIn = errors.New("未登录")

const (
	navSettle  = 2 * time.Second  // 页面跳转后的稳定等待(与旧版一致)
	eclassWait = 3 * time.Second  // eclass SSO 跳转等待
	actionTO   = 60 * time.Second // 单次导航/脚本执行上限
)

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

// browserSession 一个受控的 Edge/Chrome 实例(独立 profile,不影响日常浏览器)。
//
// 库选型说明:chromedp 纯 CDP 实现,浏览器直接作为本进程子进程启动,
// 不向 %TEMP% 或其他位置释放任何辅助可执行文件,避免杀软误报
// (前一代 rod 默认释放 leakless.exe,Windows Defender 屡次误判为木马)。
type browserSession struct {
	allocCtx    context.Context
	cancelAlloc context.CancelFunc
	tab         context.Context
	cancelTab   context.CancelFunc
}

// openBrowser 启动持久化浏览器;visible=true 时显示窗口(供人工登录)。
// 启动失败("chrome failed to start",典型原因是上次强杀宿主遗留的孤儿 Edge
// 占着 profile 锁)时自动清锁重试,让登录向导具备自愈能力。
func openBrowser(visible bool) (*browserSession, error) {
	var s *browserSession
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		killStaleBrowsers() // 清掉强杀宿主后遗留的孤儿 Edge(占着 profile 锁)
		s, err = tryOpenBrowser(visible)
		if err == nil {
			return s, nil
		}
		if attempt == 3 {
			break
		}
		logger.Printf("[浏览器] 第 %d 次启动失败:%v;清理残留后重试", attempt, err)
		time.Sleep(1500 * time.Millisecond)
	}
	return nil, err
}

func tryOpenBrowser(visible bool) (*browserSession, error) {
	bin := findBrowserBin()
	if bin == "" {
		return nil, errors.New("未找到 Microsoft Edge / Chrome,无法启动抓取浏览器")
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(bin),
		chromedp.UserDataDir(profileDir),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		// 注意:不要加 --disable-blink-features=AutomationControlled 之类的
		// “反检测”标志:Edge 会弹“不受支持的命令行标志”警告条,且该标志
		// 在杀软眼里是恶意软件常用特征。抓取走的是已登录会话内的普通
		// fetch,平台并不检查 navigator.webdriver,无需伪装。
	)
	if visible {
		// 移除 headless / enable-automation 等,窗口行为与手动打开一致
		opts = append(opts, chromedp.VisibleWindow)
	} else {
		opts = append(opts, chromedp.Flag("enable-automation", false))
	}

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	tab, cancelTab := chromedp.NewContext(alloc)
	// 第一次 Run 才真正拉起浏览器;注意不要带超时的 ctx 来做这件事
	if err := chromedp.Do(tab); err != nil {
		cancelTab()
		cancelAlloc()
		return nil, err
	}
	return &browserSession{allocCtx: alloc, cancelAlloc: cancelAlloc,
		tab: tab, cancelTab: cancelTab}, nil
}

func (s *browserSession) close() {
	s.cancelTab()
	s.cancelAlloc() // 浏览器随本会话退出而终止
}

// navigate 跳转并等待页面稳定(对应旧版 goto + sleep)
func (s *browserSession) navigate(url string, settle time.Duration) error {
	nctx, cancel := context.WithTimeout(s.tab, actionTO)
	defer cancel()
	if err := chromedp.Do(nctx, chromedp.Navigate(url)); err != nil {
		return err
	}
	time.Sleep(settle)
	return nil
}

// eval 在当前页面执行 JS 表达式,返回其结果字符串(配合 EvalAwaitPromise 支持异步)
func (s *browserSession) eval(jsExpr string) (string, error) {
	nctx, cancel := context.WithTimeout(s.tab, actionTO)
	defer cancel()
	return chromedp.Run(nctx, chromedp.Evaluate[string](jsExpr, chromedp.EvalAwaitPromise))
}

// bringToFront 激活标签页(登录时把窗口带前台)
func (s *browserSession) bringToFront() {
	_, _ = chromedp.Run(s.tab, func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		_, err := cdp.Call(ctx, t, page.BringToFront, cdp.Empty{})
		return chromedp.Void{}, err
	})
}

// resetTab 标签页被用户关掉后重建
func (s *browserSession) resetTab() error {
	s.cancelTab()
	s.tab, s.cancelTab = chromedp.NewContext(s.allocCtx)
	return chromedp.Do(s.tab)
}

// ---------------------------------------------------------------- 登录与抓取

// waitLogin 弹出可见窗口等待用户完成登录(含验证码),轮询直到会话生效。
func waitLogin(s *browserSession, timeout time.Duration) bool {
	_ = s.navigate(portalURL, navSettle)
	s.bringToFront()
	fmt.Println()
	fmt.Println("============================================================")
	fmt.Println(">>> 需要登录延河课堂:请在刚弹出的 Edge 窗口中输入 <<<")
	fmt.Println("    账号 + 密码 + 图形验证码,然后点击[登录]")
	fmt.Printf("    (最长等待 %d 分钟,登录成功后会自动继续)\n", int(timeout.Minutes()))
	fmt.Println("============================================================")
	fmt.Println()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := s.eval("(" + jsCheckLogin + ")()")
		if err != nil { // 页面可能被用户关闭 -> 自动重开
			if rerr := s.resetTab(); rerr == nil {
				_ = s.navigate(portalURL, navSettle)
				s.bringToFront()
			}
		} else {
			var chk struct {
				Ok bool `json:"ok"`
			}
			if json.Unmarshal([]byte(out), &chk) == nil && chk.Ok {
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
func tryCollect(s *browserSession) (*collectResult, error) {
	// ---- 阶段 1:延河课堂(课程列表 + 课程关闭时间) ----
	if err := s.navigate(portalURL, navSettle); err != nil {
		return nil, err
	}
	var chk struct {
		Ok bool `json:"ok"`
	}
	if out, err := s.eval("(" + jsCheckLogin + ")()"); err != nil {
		return nil, err
	} else if json.Unmarshal([]byte(out), &chk) != nil || !chk.Ok {
		return nil, errNotLoggedIn
	}

	out, err := s.eval("(" + jsFetchCourses + ")()")
	if err != nil {
		return nil, err
	}
	var coursesRes struct {
		Total   int      `json:"total"`
		Courses []Course `json:"courses"`
		Error   string   `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &coursesRes); err != nil {
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
		if err := s.navigate(eclassBase+"/course/"+zyIDs[0]+"/content", eclassWait); err != nil {
			return nil, err
		}
		zyJSON, _ := json.Marshal(zyIDs)
		out, err = s.eval("(" + jsCollectEclass + ")(" + string(zyJSON) + ")")
		if err != nil {
			return nil, err
		}
		var ec struct {
			Items          []Item                   `json:"items"`
			SubmittedExams map[string][]interface{} `json:"submittedExams"`
		}
		if err := json.Unmarshal([]byte(out), &ec); err != nil {
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
	// visible 仅供 --fetch --visible 调试:直接以可见窗口抓取
	s, err := openBrowser(visible)
	if err != nil {
		return nil, err
	}
	defer s.close()

	res, err := tryCollect(s)
	if err == errNotLoggedIn && !visible { // 需要人工登录 -> 换可见窗口
		s.close()
		// headless Edge 的终止是异步的:立即重开可见窗口会撞上 profile 锁,
		// Edge 静默移交后瞬间退出 -> "chrome failed to start"
		time.Sleep(time.Second)
		s, err = openBrowser(true)
		if err != nil {
			return nil, err
		}
		defer s.close()
		if !waitLogin(s, loginWait) {
			return nil, errors.New("等待登录超时,未获取到有效会话")
		}
		res, err = tryCollect(s)
		if err == errNotLoggedIn {
			return nil, errors.New("登录后仍未获取到有效会话,请检查账号")
		}
	}
	if err != nil {
		return nil, err
	}

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

// ---------------------------------------------------------------- 快照读写

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
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}
