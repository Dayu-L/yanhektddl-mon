package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

var errNotLoggedIn = errors.New("未登录")

const (
	navSettle  = 2 * time.Second  // 页面跳转后的稳定等待(与旧版一致)
	eclassWait = 3 * time.Second  // eclass SSO 跳转等待
	actionTO   = 60 * time.Second // 单次导航/脚本执行上限
	bootTO     = 30 * time.Second // 浏览器启动(管道模式之外的握手)上限
	pipeBootTO = 12 * time.Second // 管道模式首次尝试上限:旧版浏览器不认 Windows 管道参数,尽快回退
)

// preferWebSocket 记录本次运行是否已回退到 WebSocket 传输。
//
// chromedp 0.24 在 Windows 默认用管道(--remote-debugging-io-pipes),这个
// 开关只有较新的 Chromium 才支持;旧版 Edge/Chrome、被组策略管控或
// 以管理员身份运行时,管道可能直接失败(“chrome failed to start”且
// stderr 为空)。一旦管道失败就改走 WebSocket(--remote-debugging-port=0),
// 这是 Chromium 十几年都稳定的方式,后续启动不再重试管道。
var preferWebSocket atomic.Bool

// ---------------------------------------------------------------- 浏览器会话

// browserCandidates 按优先级列出系统里可能存在的 Edge / Chrome。
// 用环境变量拼路径,兼容装在非 C 盘、以及“仅当前用户”安装(Chrome 默认
// 就是装到 %LOCALAPPDATA%,不少同学的机器上 Program Files 里没有)。
func browserCandidates() []string {
	var out []string
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LOCALAPPDATA"} {
		root := os.Getenv(env)
		if root == "" {
			continue
		}
		out = append(out,
			filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"))
	}
	return append(out,
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	)
}

func findBrowserBin() string {
	seen := map[string]bool{}
	for _, p := range browserCandidates() {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// ensureProfileDir 确保浏览器配置目录存在且可写。
// 便携版若被放进 Program Files 等只读位置,Edge 无法建 profile,
// 会以“chrome failed to start”退出;此时自动改用 %LOCALAPPDATA% 下的目录。
func ensureProfileDir() error {
	if err := os.MkdirAll(profileDir, 0o755); err == nil {
		probe := filepath.Join(profileDir, ".write-test")
		if os.WriteFile(probe, []byte("ok"), 0o644) == nil {
			os.Remove(probe)
			return nil
		}
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return fmt.Errorf("配置目录不可写:%s", profileDir)
	}
	alt := filepath.Join(local, "YanhektDDL", "browser_profile")
	if err := os.MkdirAll(alt, 0o755); err != nil {
		return fmt.Errorf("配置目录不可写:%s", profileDir)
	}
	logger.Printf("[浏览器] 配置目录 %s 不可写,改用 %s", profileDir, alt)
	profileDir = alt
	return nil
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
// 占着 profile 锁,或刚关掉的 headless 实例还没释放锁)时自动清锁重试。
// 慢机器/杀软扫描 profile 目录时首次退出可能滞后数秒,故退避重试总计约 10 秒。
func openBrowser(visible bool) (*browserSession, error) {
	var s *browserSession
	var err error
	retryDelays := []time.Duration{2 * time.Second, 3 * time.Second, 5 * time.Second}
	for attempt := 1; ; attempt++ {
		killStaleBrowsers() // 清掉强杀宿主后遗留的孤儿 Edge(占着 profile 锁)
		// 第 1 次走管道(不占端口);失败后本次运行固定走 WebSocket。
		ws := preferWebSocket.Load() || attempt > 1
		timeout := bootTO
		if !ws {
			timeout = pipeBootTO
		}
		s, err = tryOpenBrowser(visible, ws, timeout)
		if err == nil {
			if ws && !preferWebSocket.Swap(true) {
				logger.Printf("[浏览器] 管道传输不可用,已切换到 WebSocket 传输")
			}
			return s, nil
		}
		if attempt > len(retryDelays) {
			return nil, err
		}
		delay := retryDelays[attempt-1]
		logger.Printf("[浏览器] 第 %d 次启动失败:%v;%.0f 秒后清理残留并重试",
			attempt, err, delay.Seconds())
		time.Sleep(delay)
	}
}

func tryOpenBrowser(visible, ws bool, timeout time.Duration) (*browserSession, error) {
	bin := findBrowserBin()
	if bin == "" {
		return nil, errors.New("未找到 Microsoft Edge / Chrome,无法启动抓取浏览器(请安装 Edge 后重试)")
	}
	if err := ensureProfileDir(); err != nil {
		return nil, err
	}
	transport := "管道"
	if ws {
		transport = "WebSocket"
	}
	logger.Printf("[浏览器] 启动 %s(可见=%v,%s,配置=%s)", bin, visible, transport, profileDir)
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
	if strings.EqualFold(filepath.Base(bin), "msedge.exe") {
		// Edge 133+ 已知 bug(EdgeWebDriver#189、playwright-dotnet#3155):
		// 新路径/新环境下首次启动会被 Windows 程序兼容性助手(PCA)盯上,
		// Edge 随即自我重启一次(给新进程追加 --edge-skip-compat-layer-relaunch
		// 防止循环),原进程立刻退出。chromedp 只看到子进程死掉且 stderr 为空,
		// 报 "chrome failed to start",重启出来的 Edge 则变成停在 about:blank
		// 的孤儿窗口,谁也控制不了。主动带上该标志即可跳过这次重启。
		// 仅对 msedge 加:Chrome 不认识这个标志,会弹警告条。
		opts = append(opts, chromedp.Flag("edge-skip-compat-layer-relaunch", true))
	}
	if ws {
		// WebSocket 传输:兼容老版 Chromium。端口固定随机并只绑本机。
		opts = append(opts,
			remote.WebSocket,
			chromedp.Flag("remote-debugging-port", "0"),
			chromedp.Flag("remote-debugging-address", "127.0.0.1"),
		)
	}
	if visible {
		// 移除 headless / enable-automation 等,窗口行为与手动打开一致
		opts = append(opts, chromedp.VisibleWindow)
	} else {
		opts = append(opts, chromedp.Flag("enable-automation", false))
	}

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	tab, cancelTab := chromedp.NewContext(alloc)
	// 第一次 Run 才真正拉起浏览器。加超时:某些被管控的机器会“进程活着但
	// 不响应 CDP”,没有超时的话登录页会一直转圈。
	done := make(chan error, 1)
	go func() { done <- chromedp.Do(tab) }()
	select {
	case err := <-done:
		if err != nil {
			cancelTab()
			cancelAlloc()
			return nil, fmt.Errorf("启动 %s 失败(%s,配置目录 %s):%w",
				filepath.Base(bin), transport, profileDir, err)
		}
	case <-time.After(timeout):
		cancelTab()
		cancelAlloc()
		return nil, fmt.Errorf("启动 %s 超时(%s 内未响应,%s,配置目录 %s)",
			filepath.Base(bin), timeout, transport, profileDir)
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
	return collectSnapshotMode(visible, false)
}

// collectSnapshotForLogin 登录向导专用:直接以可见窗口进入“等用户登录”流程。
//
// 不再先起 headless 探测登录态再关掉重开:Edge 退出是异步的,重开的可见
// 实例会撞上尚未释放的 profile 锁,浏览器静默移交后立刻退出,最终报
// "chrome failed to start"(stderr 为空)。慢机器/杀软实时扫描时这个
// 竞态窗口会放大,于是“开发机正常、别人电脑秒失败”。
func collectSnapshotForLogin() (*Snapshot, error) {
	return collectSnapshotMode(true, true)
}

func collectSnapshotMode(visible, forceLogin bool) (*Snapshot, error) {
	s, err := openBrowser(visible)
	if err != nil {
		return nil, err
	}
	// 用闭包读取最终的 s:中途换窗口后只关掉最后那个会话。
	defer func() {
		if s != nil {
			s.close()
		}
	}()

	if forceLogin {
		if !waitLogin(s, loginWait) {
			return nil, errors.New("等待登录超时,未获取到有效会话")
		}
		res, err := tryCollect(s)
		if err == errNotLoggedIn {
			return nil, errors.New("登录后仍未获取到有效会话,请检查账号")
		}
		if err != nil {
			return nil, err
		}
		return saveCollected(res)
	}

	res, err := tryCollect(s)
	if err == errNotLoggedIn && !visible { // 需要人工登录 -> 换可见窗口
		s.close()
		s = nil
		// 留足时间让 Edge 释放 profile 锁;再加上 openBrowser 内部约 10 秒
		// 退避重试,慢机器也能自愈。
		time.Sleep(2 * time.Second)
		s, err = openBrowser(true)
		if err != nil {
			return nil, err
		}
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
	return saveCollected(res)
}

// saveCollected 把抓取结果落盘成快照,供报告与速查页共用。
func saveCollected(res *collectResult) (*Snapshot, error) {
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
