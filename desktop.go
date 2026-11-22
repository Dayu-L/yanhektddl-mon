package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// 速查页与登录页的 <title>(--app 窗口的窗口标题就是页面标题)
var dashboardTitles = []string{
	"延河课堂 DDL",
	"登录乐学 · 延河课堂 DDL",
}

// 设置页的窗口标题
const settingsTitle = "设置 · 延河课堂 DDL"

// allAppTitles 退出时要一并关闭的全部应用窗口(速查 + 登录 + 设置)
func allAppTitles() []string {
	out := append([]string{}, dashboardTitles...)
	return append(out, settingsTitle)
}

// 新建窗口防抖:快速连点「打开窗口」时不至于开出多个
var (
	openMu           sync.Mutex
	lastOpen         time.Time
	lastSettingsOpen time.Time
)

// openWindow 显示速查窗口:已打开则切到前台,没打开才新建。
// (系统没装 Edge 时退回默认浏览器)
func openWindow(port int) {
	// 已有窗口:幂等前置,无需防抖
	if focusExistingAppWindow(dashboardTitles...) {
		logger.Printf(">> 速查窗口已存在,已切换到前台")
		applyWindowIconSoon(dashboardTitles...)
		return
	}

	openMu.Lock()
	if time.Since(lastOpen) < 2*time.Second {
		openMu.Unlock()
		return // 刚发起过新建(窗口可能还没出来),忽略本次
	}
	lastOpen = time.Now()
	openMu.Unlock()

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	if bin := findBrowserBin(); bin != "" {
		// --app 用应用窗口(无地址栏),不影响日常浏览器
		_ = exec.Command(bin, "--app="+url, "--window-size=1180,880").Start()
		logger.Printf(">> 已打开速查窗口")
		applyWindowIconSoon(dashboardTitles...)
		return
	}
	_ = exec.Command("cmd", "/c", "start", url).Start()
	logger.Printf(">> 已用默认浏览器打开速查页")
}

// openSettingsWindow 打开设置页(独立 --app 窗口);已打开则前置。
func openSettingsWindow(port int) {
	if focusExistingAppWindow(settingsTitle) {
		logger.Printf(">> 设置窗口已存在,已切换到前台")
		applyWindowIconSoon(settingsTitle)
		return
	}
	openMu.Lock()
	if time.Since(lastSettingsOpen) < 2*time.Second {
		openMu.Unlock()
		return
	}
	lastSettingsOpen = time.Now()
	openMu.Unlock()

	url := fmt.Sprintf("http://127.0.0.1:%d/settings", port)
	if bin := findBrowserBin(); bin != "" {
		_ = exec.Command(bin, "--app="+url, "--window-size=760,780").Start()
		logger.Printf(">> 已打开设置窗口")
		applyWindowIconSoon(settingsTitle)
		return
	}
	_ = exec.Command("cmd", "/c", "start", url).Start()
	logger.Printf(">> 已用默认浏览器打开设置页")
}

// shutdownAndExit 统一退出路径:先把速查窗口一并关掉,再退出进程。
//
// 不关窗口的话,托盘「退出」后屏幕上会留一个页面点不动的 Edge 窗口
// (背后服务已死)。注意窗口是复用用户日常 Edge 进程的,所以只能
// 按窗口发 WM_CLOSE,不能杀进程。
func shutdownAndExit() {
	if n := closeAppWindows(allAppTitles()...); n > 0 {
		logger.Printf(">> 已关闭 %d 个速查窗口", n)
		time.Sleep(400 * time.Millisecond) // 给 WM_CLOSE 一点送达/关闭时间
	}
	logger.Printf(">> 进程退出")
	os.Exit(0)
}

// probeOurs 8765 上运行的是不是我们自己的服务(防止把别的程序误认成自己)
func probeOurs() bool {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/status", portFirst))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var st struct {
		Running bool `json:"running"`
	}
	return json.NewDecoder(resp.Body).Decode(&st) == nil
}

// desktopMode 双击 exe 的默认模式:本地服务 + 托盘 + Edge 速查窗口。
// 严格单实例:已有实例在运行时,本进程只负责把已有窗口叫出来,然后退出。
func desktopMode() {
	// 单实例互斥量(内核级原子,无端口探测的竞态)
	if !acquireSingleInstance() {
		logger.Printf(">> 已有实例在运行")
		if wakeExisting() {
			logger.Printf(">> 已打开运行中实例的窗口")
			return
		}
		// 已有实例但叫不动(服务卡死等):提示用户从托盘退出后重试
		msg := "延河课堂 DDL 已在运行(见系统托盘图标)。\n可从托盘右键「退出」后再重新启动。"
		logger.Printf(">> %s", msg)
		msgBox(msg, "延河课堂 DDL")
		return
	}
	// 互斥量建失败时的兜底:仍是自家服务在跑则只开窗口
	if probeOurs() {
		openWindow(portFirst)
		logger.Printf(">> 服务已在运行,已打开窗口")
		return
	}

	// 端口被其他程序占用时自动换端口(8765-8769),避免“双击没反应”
	ln, port := listenAvailable()
	if ln == nil {
		msg := "端口 8765-8769 均被占用,服务无法启动。详见 server.log"
		logger.Printf(">> %s", msg)
		msgBox(msg, "延河课堂 DDL")
		return
	}
	if port != portFirst {
		logger.Printf(">> 8765 被其他程序占用,已改用端口 %d", port)
	}

	srv := &http.Server{Handler: newMux(port)}
	go func() {
		if err := srv.Serve(ln); err != nil {
			logger.Printf(">> 服务异常退出:%v", err)
		}
	}()
	time.Sleep(300 * time.Millisecond)

	// 按设置同步自动巡检计划任务(开启则补建,关闭则删除)
	syncAutoCheck()

	// 首次运行还没有数据 -> 窗口会自动落到 /login 登录向导页,
	// 由用户点[开始登录]再进入可见浏览器流程(不自动弹窗,避免吓到新手)
	openWindow(port)
	logger.Printf(">> 延河课堂 DDL 服务已启动:http://127.0.0.1:%d/", port)
	logger.Printf(">> 托盘菜单:打开窗口 / 设置 / 退出(关闭窗口行为可在设置中修改)")

	// 窗口存活看门狗:设置里选了“关闭即退出”时,所有窗口关闭后自动收尾;
	// “最小化到托盘”则不退出,由托盘负责后续入口。
	startLivenessWatchdog(false)

	// 会话保温:常驻期间每 10 分钟轻量 ping 一次 /api/todos,
	// 避免平台会话空闲过期后刷新/巡检弹登录窗
	startKeepAlive()

	// 常驻托盘:右键菜单 打开窗口 / 设置 / 退出。
	// 若托盘不可用则自动退回 watchdog 兜底(见 runTray)。
	runTray(func() { openWindow(port) }, func() { openSettingsWindow(port) })
}

// wakeExisting 要求已运行的实例把窗口打开(第二个实例被拦下时用)。
// 端口可能在 8765-8769 之间,逐个尝试。
func wakeExisting() bool {
	client := &http.Client{Timeout: 2 * time.Second}
	for port := portFirst; port <= portLast; port++ {
		resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/api/open", port), "", nil)
		if err == nil {
			resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		}
	}
	return false
}

// postQuit 通知 8765-8769 上的本程序服务优雅退出(--quit)
func postQuit() bool {
	client := &http.Client{Timeout: 2 * time.Second}
	for port := portFirst; port <= portLast; port++ {
		resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/api/quit", port), "", nil)
		if err == nil {
			resp.Body.Close()
			return true
		}
	}
	return false
}

func listenAvailable() (net.Listener, int) {
	for port := portFirst; port <= portLast; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return ln, port
		}
	}
	return nil, 0
}
