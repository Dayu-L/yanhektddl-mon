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
	actionTO    = 60 * time.Second  // 单次导航/脚本执行上限
	bootTO      = 30 * time.Second  // 浏览器启动(管道模式之外的握手)上限
	pipeBootTO  = 12 * time.Second  // 管道模式首次尝试上限:旧版浏览器不认 Windows 管道参数,尽快回退
	todosWaitTO = 15 * time.Second  // eclass 待办轮询窗口:覆盖会话过期后的 CAS 静默续期重定向链
	aitaLoginTO = 90 * time.Second  // 登录向导中延河课堂(可选步骤)的等待上限
	zhsLoginTO  = 120 * time.Second // 登录向导中智慧树(可选数据源)的等待上限:微信扫码/手机号登录预留时间
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

// pollProbe 每 3 秒在当前页面执行一次探测 JS,直到 ok 或超时。
// 页面被用户关闭时自动重建标签页并重新导航。
func pollProbe(s *browserSession, probeJS, reloadURL string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	fails := 0
	for time.Now().Before(deadline) {
		out, err := s.eval("(" + probeJS + ")()")
		if err != nil {
			// 单次 eval 失败多半是页面正在跳转(登录页重定向会换掉执行上下文)。
			// 这时候重开页面会把用户刚输入的账号密码刷掉,所以连续失败够久
			// (约 12 秒)才当成"窗口被关掉"去重开。
			fails++
			if fails >= 4 {
				if rerr := s.resetTab(); rerr == nil {
					_ = s.navigate(reloadURL, 0)
					s.bringToFront()
				}
				fails = 0
			}
		} else {
			fails = 0
			var chk struct {
				Ok bool `json:"ok"`
			}
			if json.Unmarshal([]byte(out), &chk) == nil && chk.Ok {
				return true
			}
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

// waitLoginEclass 登录向导第 1 步(必须):弹统一身份认证页,等待 eclass 会话生效。
// 会话由 zy-eclass 的持久 cookie(约 24 小时)与北理工 CAS 会话共同维持,
// 失效后经此页重新认证一次即可,支持 i北理 扫码 / 通行密钥 / 短信验证码。
func waitLoginEclass(s *browserSession, timeout time.Duration) bool {
	_ = s.navigate(eclassHome, 0)
	s.bringToFront()
	fmt.Println()
	fmt.Println("============================================================")
	fmt.Println(">>> 需要登录乐学 zy-eclass:请在 Edge 窗口完成 <<<")
	fmt.Println("    北理工统一身份认证(支持 i北理 扫码 / 通行密钥 / 短信)")
	fmt.Printf("    (最长等待 %d 分钟,登录成功后会自动继续)\n", int(timeout.Minutes()))
	fmt.Println("============================================================")
	fmt.Println()
	return pollProbe(s, jsTodosReady, eclassHome, timeout)
}

// waitLoginAita 登录向导第 2 步(可选):登录延河课堂以更新课程关闭时间。
// 不输入可直接等待超时,程序会沿用上次快照的课程数据。
func waitLoginAita(s *browserSession, timeout time.Duration) bool {
	_ = s.navigate(portalURL, 0)
	s.bringToFront()
	fmt.Println()
	fmt.Println("------------------------------------------------------------")
	fmt.Println(">>> 可选:登录延河课堂,更新课程关闭时间 <<<")
	fmt.Println("    账号 + 密码 + 图形验证码;不输入则沿用上次数据,")
	fmt.Printf("    %d 秒后自动继续\n", int(timeout.Seconds()))
	fmt.Println("------------------------------------------------------------")
	fmt.Println()
	return pollProbe(s, jsCheckLogin, portalURL, timeout)
}

// waitLoginZhihuishu 登录向导第 3 步(可选):登录智慧树在线学堂。
// 该平台用微信扫码/手机号登录,登录态与乐学的 CAS 互不相干,
// 所以单独开一次窗口轮询;超时则跳过该可选数据源(智慧课程)。
// 轮询只认用户信息(jsZhsLoginReady):课程列表为空不等于没登录,
// 拿列表判会把「还没输完密码」的窗口提前关掉。
func waitLoginZhihuishu(s *browserSession, timeout time.Duration) bool {
	_ = s.navigate(zhsHomeURL, 0)
	s.bringToFront()
	fmt.Println()
	fmt.Println("------------------------------------------------------------")
	fmt.Println(">>> 可选:登录智慧树在线学堂,抓取智慧课程 <<<")
	fmt.Println("    可扫微信 / 手机号登录;只读取本人的作业测试、考试、话题讨论。")
	fmt.Printf("    %d 秒后自动继续(不登录则跳过该数据源)\n", int(timeout.Seconds()))
	fmt.Println("------------------------------------------------------------")
	fmt.Println()
	return pollProbe(s, jsZhsLoginReady, zhsHomeURL, timeout)
}

// ensureZhsLogin 给可选数据源「智慧树」一次登录机会:已登录则直接返回,
// 未登录才弹提示;探测异常(网络/接口改动)不打扰用户,静默返回 false,
// 后面阶段 4 会再记一次具体原因。
func ensureZhsLogin(s *browserSession) bool {
	ok, perr := zhsProbe(s)
	if ok {
		fmt.Println(">> 智慧树:已登录")
		return true
	}
	if perr != nil {
		logger.Printf("[智慧树] 登录探测异常:%v", perr)
		return false
	}
	return waitLoginZhihuishu(s, zhsLoginTO)
}

type collectResult struct {
	Courses    []Course
	Items      []Item
	Submitted  map[string][]interface{}
	Deep       bool
	AitaOK     bool   // 延河课堂课程列表本次是否取到(可选数据源)
	ZhsSkipped bool   // 可选数据源「智慧树」本次被跳过(未登录/接口异常)
	ZhsWhy     string // 跳过的原因,给设置页的登录状态用
}

// fetchAitaCourses 延河课堂课程列表(含课程关闭时间 EndAt)。
// 该平台会话很短且不走 CAS 续期,所以只作为"尽力而为"的数据源:
// ok=false 时调用方沿用上次快照的课程,核心待办抓取不受影响。
func fetchAitaCourses(s *browserSession) ([]Course, bool) {
	if err := s.navigate(portalURL, 0); err != nil {
		return nil, false
	}
	// 导航刚完成 JS 上下文可能未就绪,重试 3 次
	for attempt := 0; attempt < 3; attempt++ {
		out, err := s.eval("(" + jsFetchCourses + ")()")
		if err == nil {
			var res struct {
				Total   int      `json:"total"`
				Courses []Course `json:"courses"`
				Error   string   `json:"error"`
			}
			if json.Unmarshal([]byte(out), &res) == nil {
				if res.Error == "" && res.Total > 0 {
					for i := range res.Courses {
						res.Courses[i].Platform = platformAita
					}
					return res.Courses, true
				}
				return nil, false // HTTP 401 等明确失败,重试无意义
			}
		}
		time.Sleep(700 * time.Millisecond)
	}
	return nil, false
}

// probeAita 探测延河课堂会话(可选数据源):只看课程接口是否放行,不抓课程。
// 导航刚回来时 JS 上下文可能未就绪,重试几次;拿不到明确结果按未登录处理。
func probeAita(s *browserSession) bool {
	if err := s.navigate(portalURL, 0); err != nil {
		return false
	}
	for attempt := 0; attempt < 3; attempt++ {
		out, err := s.eval("(" + jsCheckLogin + ")()")
		if err == nil {
			var res struct {
				OK bool `json:"ok"`
			}
			if json.Unmarshal([]byte(out), &res) == nil {
				return res.OK
			}
		}
		time.Sleep(700 * time.Millisecond)
	}
	return false
}

// tryCollect 四阶段:
//   阶段 1  延河课堂课程列表 —— 尽力而为,失败沿用缓存
//   阶段 2  eclass 待办(快路径,/api/todos)—— 核心数据,必须成功
//   阶段 3  深度扫描(可选)—— 逐课程并行扫,补齐已完成/已过期/问卷
//   阶段 4  智慧树(共享课 + 智慧课程)的作业/考试/话题讨论 —— 增量数据源,失败只记日志
//
// 未登录 -> errNotLoggedIn;成功 -> 结构化结果。
func tryCollect(s *browserSession, deep bool) (*collectResult, error) {
	// ---- 阶段 1:延河课堂 ----
	courses, aitaOK := fetchAitaCourses(s)
	if !aitaOK {
		if snap, err := loadSnapshot(); err == nil && len(snap.Courses) > 0 {
			courses = snap.Courses
			fmt.Printf(">> 延河课堂会话失效,课程/关闭时间沿用上次快照(%d 门课)\n", len(courses))
		}
	} else {
		fmt.Printf(">> 延河课堂:共 %d 门课\n", len(courses))
	}

	// ---- 阶段 2:eclass 待办(快路径) ----
	if err := s.navigate(eclassHome, 0); err != nil {
		return nil, err
	}
	var todos struct {
		OK      bool   `json:"ok"`
		Items   []Item `json:"items"`
		Courses []struct {
			ZyID  string `json:"zyId"`
			Title string `json:"title"`
		} `json:"courses"`
	}
	// 会话过期时此导航触发 CAS 静默续期(重定向链),轮询窗口要盖住它
	ready := false
	deadline := time.Now().Add(todosWaitTO)
	for time.Now().Before(deadline) {
		out, err := s.eval("(" + jsFetchTodos + ")()")
		if err == nil && json.Unmarshal([]byte(out), &todos) == nil && todos.OK {
			ready = true
			break
		}
		time.Sleep(700 * time.Millisecond)
	}
	if !ready {
		return nil, errNotLoggedIn
	}
	// 待办里可能出现快照没有的新课程(半途选课):补进课程列表
	have := map[string]bool{}
	for _, c := range courses {
		have[c.ZyID] = true
	}
	for _, c := range todos.Courses {
		if c.ZyID != "" && !have[c.ZyID] {
			courses = append(courses, Course{Title: c.Title, ZyID: c.ZyID})
		}
	}
	items := todos.Items
	submitted := map[string][]interface{}{}
	fmt.Printf(">> 乐学待办(快路径):未提交任务 %d 条\n", len(items))

	// ---- 阶段 3:深度扫描 ----
	if deep {
		var zyIDs []string
		for _, c := range courses {
			if c.ZyID != "" {
				zyIDs = append(zyIDs, c.ZyID)
			}
		}
		if len(zyIDs) > 0 {
			zyJSON, _ := json.Marshal(zyIDs)
			out, err := s.eval("(" + jsCollectEclass + ")(" + string(zyJSON) + ")")
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
			if ec.Items != nil {
				items = ec.Items
			}
			if ec.SubmittedExams != nil {
				submitted = ec.SubmittedExams
			}
			fmt.Printf(">> 深度扫描:%d 门课,带截止时间的任务 %d 条\n",
				len(zyIDs), len(items))
		}
	}

	// ---- 阶段 4:智慧树(在线学堂)共享课 + 智慧课程 ----
	// 放在最后:它会把浏览器导航到智慧树域,不能影响上面的 eclass 抓取。
	// 该平台未登录/接口异常都只跳过,不阻死延河课堂的主流程。
	zhsSkipped := false
	zhsWhy := ""
	if ais, aiItems, zhsErr := collectZhihuishu(s, deep); zhsErr != nil {
		logZhsSkip(zhsErr)
		zhsSkipped = true
		zhsWhy = zhsErr.Error()
	} else if len(ais) > 0 {
		courses = append(courses, ais...)
		items = append(items, aiItems...)
		fmt.Printf(">> 智慧树:%d 门课,任务 %d 条\n", len(ais), len(aiItems))
	}
	return &collectResult{Courses: courses, Items: items,
		Submitted: submitted, Deep: deep, AitaOK: aitaOK,
		ZhsSkipped: zhsSkipped, ZhsWhy: zhsWhy}, nil
}

// ---------------------------------------------------------------- 主流程

// collectSnapshot 完整抓取一次并保存 ddl_data.json。
// autoLogin=false 用在无人值守的场景(计划任务 --notify):会话失效时只返回错误,
// 不弹登录窗——否则早上 9:00 会自己蹦出一个 Edge 窗口跳到统一身份认证页。
func collectSnapshot(days int, visible, forceDeep, autoLogin bool) (*Snapshot, error) {
	return collectSnapshotMode(visible, false, forceDeep, autoLogin)
}

// collectSnapshotBackground 后台刷新专用:会话失效时只返回 errNotLoggedIn,
// 不换成可见窗口等登录 —— 该不该登录交给设置页的单源登录决定,
// 否则用户点一下刷新就会有个 Edge 窗口自己跳到统一身份认证页。
func collectSnapshotBackground(days int) (*Snapshot, error) {
	return collectSnapshotMode(false, false, false, false)
}

// collectSnapshotForLogin 登录向导专用:直接以可见窗口进入“等用户登录”流程。
//
// 不再先起 headless 探测登录态再关掉重开:Edge 退出是异步的,重开的可见
// 实例会撞上尚未释放的 profile 锁,浏览器静默移交后立刻退出,最终报
// "chrome failed to start"(stderr 为空)。慢机器/杀软实时扫描时这个
// 竞态窗口会放大,于是“开发机正常、别人电脑秒失败”。
func collectSnapshotForLogin() (*Snapshot, error) {
	return collectSnapshotMode(true, true, true, true)
}

// needDeepScan 今天还没有深度扫描过 -> 需要一次。
// 深扫负责补齐快路径看不到的数据:已完成/已过期未提交/问卷/课程关闭时间。
func needDeepScan() bool {
	snap, err := loadSnapshot()
	if err != nil || snap.DeepAt == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, snap.DeepAt)
	if err != nil {
		return true
	}
	return t.In(TZ).Format("2006-01-02") != time.Now().In(TZ).Format("2006-01-02")
}

func collectSnapshotMode(visible, forceLogin, forceDeep, autoLogin bool) (*Snapshot, error) {
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
	deep := forceDeep || needDeepScan()

	if forceLogin {
		if !waitLoginEclass(s, loginWait) {
			return nil, errors.New("等待统一身份认证超时,未获取到有效会话")
		}
		if !waitLoginAita(s, aitaLoginTO) {
			fmt.Println(">> 已跳过延河课堂登录:课程关闭时间沿用上次快照")
		}
		if !ensureZhsLogin(s) {
			fmt.Println(">> 已跳过智慧树:本次不抓取智慧课程")
		}
		res, err := tryCollect(s, deep)
		if err == errNotLoggedIn {
			return nil, errors.New("登录后仍未获取到有效会话,请检查账号")
		}
		if err != nil {
			return nil, err
		}
		return saveCollected(res, deep)
	}

	res, err := tryCollect(s, deep)
	if err == errNotLoggedIn { // 需要人工登录:后台刷新不弹窗,只上报
		if !autoLogin {
			return nil, errNotLoggedIn
		}
		if !visible {
			s.close()
			s = nil
			// 留足时间让 Edge 释放 profile 锁;再加上 openBrowser 内部约 10 秒
			// 退避重试,慢机器也能自愈。
			time.Sleep(2 * time.Second)
			s, err = openBrowser(true)
			if err != nil {
				return nil, err
			}
		}
		if !waitLoginEclass(s, loginWait) {
			return nil, errors.New("等待统一身份认证超时,未获取到有效会话")
		}
		if !waitLoginAita(s, aitaLoginTO) {
			fmt.Println(">> 已跳过延河课堂登录:课程关闭时间沿用上次快照")
		}
		if !ensureZhsLogin(s) {
			fmt.Println(">> 已跳过智慧树:本次不抓取智慧课程")
		}
		res, err = tryCollect(s, deep)
		if err == errNotLoggedIn {
			return nil, errors.New("登录后仍未获取到有效会话,请检查账号")
		}
	}
	if err != nil {
		return nil, err
	}

	// 可选数据源「智慧树」的登录时机:
	//   · 无可见窗口(定时巡检/计划任务)没人能登录 —— 只在智慧树被跳过时提示一句;
	//   · 有可见窗口(--fetch --visible / 登录向导)且阶段 4 确实跳过了 —— 先探登录态,
	//     没登录才弹提示,登录成功后补抓这一路。
	// 探测本身会导航到学生首页,所以仅在「被跳过」时做,避免白跑一趟。
	if visible && res.ZhsSkipped {
		if ensureZhsLogin(s) {
			fmt.Println(">> 智慧树:登录成功,补抓共享课与智慧课程")
			cs, its, zerr := collectZhihuishu(s, deep)
			if zerr != nil {
				logZhsSkip(zerr)
				res.ZhsWhy = zerr.Error()
			} else {
				res.Courses = append(res.Courses, cs...)
				res.Items = append(res.Items, its...)
				res.ZhsSkipped = false // 已补抓成功,登录态按已登录记
				res.ZhsWhy = ""
				fmt.Printf(">> 智慧树:%d 门课,任务 %d 条\n", len(cs), len(its))
			}
		} else {
			fmt.Println(">> 已跳过智慧树:本次不抓取智慧课程")
		}
	} else if !visible && res.ZhsSkipped {
		fmt.Println(">> 提示:运行 --fetch --visible 可登录智慧树,补上智慧课程数据")
	}
	return saveCollected(res, deep)
}

// saveCollected 把抓取结果落盘成快照,供报告与速查页共用。
// 深度扫描时刷新 deepAt;快路径保留上次 deepAt(每日首轮判断依据)。
func saveCollected(res *collectResult, deep bool) (*Snapshot, error) {
	var deepAt string
	prev, _ := loadSnapshot() // 可能不存在(首次运行),下面按 nil 处理
	if prev != nil {
		deepAt = prev.DeepAt
	}
	if deep {
		deepAt = time.Now().In(TZ).Format(time.RFC3339)
	}
	// 出口归一:快照文件本身只保留一种格式(时间北京时间、id 字符串、
	// kind 规范值、status 显式),这样 --from-json 与外部脚本都不用再兼容。
	items := make([]Item, 0, len(res.Items))
	seen := make(map[string]bool, len(res.Items))
	for _, it := range res.Items {
		n := normalizeItem(it, res.Submitted)
		items = append(items, n)
		seen[itemKey(n)] = true
	}
	// 快路径(/api/todos)只会带回「还没做」的项:已完成、已过期、问卷这些
	// 只有深度扫描才拿得到。这里用上一轮快照把它们补回来,否则点一次
	// 「刷新数据」,速查页的已完成/已过期列表就整片消失。
	if !deep && prev != nil {
		if carried := carryOverItems(prev.Items, seen); len(carried) > 0 {
			items = append(items, carried...)
			logger.Printf("[快照] 保留上一轮 %d 条任务(快路径只覆盖近期待办)", len(carried))
		}
	}
	courses := make([]Course, len(res.Courses))
	for i, c := range res.Courses {
		courses[i] = normalizeCourse(c)
	}
	snap := &Snapshot{
		FetchedAt:      time.Now().In(TZ).Format(time.RFC3339),
		DeepAt:         deepAt,
		Courses:        courses,
		Items:          items,
		SubmittedExams: res.Submitted,
	}
	if err := saveSnapshot(snap); err != nil {
		return nil, err
	}
	// 顺带记下三路会话的最近结论:设置页显示登录状态时不必再跑一遍浏览器。
	// 走到这里乐学必然通了(/api/todos 成功),可选数据源按本次是否取到记。
	recordSource(sourceEclass, true, "")
	if res.AitaOK {
		recordSource(sourceAita, true, "")
	} else {
		recordSource(sourceAita, false, "会话失效,课程关闭时间沿用上次快照")
	}
	if res.ZhsSkipped {
		recordSource(sourceZhs, false, res.ZhsWhy)
	} else {
		recordSource(sourceZhs, true, "")
	}
	return snap, nil
}

// itemKey 任务的稳定标识,与速查页「忽略」用的 key 同源(zy#id)。
func itemKey(it Item) string { return it.Zy + "#" + idString(it.ID) }

// carryOverItems 保留上一轮快照里本轮没返回的项。
// 快路径(/api/todos)只覆盖近期待办:已完成、已过期、问卷以及远期任务都只有
// 深度扫描才拿得到(实测 61 条 vs 9 条)。所以缺项一律沿用上一轮结论,等下一次
// 深度扫描(每天首轮)再校准 —— 否则点一次「刷新数据」,远期任务和已完成列表
// 会整片消失。
func carryOverItems(prev []Item, seen map[string]bool) []Item {
	var out []Item
	for _, it := range prev {
		if seen[itemKey(it)] {
			continue // 本轮有更新结论,以本轮为准
		}
		out = append(out, it)
	}
	return out
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
