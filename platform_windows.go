//go:build windows

package main

// Windows 平台适配层:控制台、DPI、单实例、窗口枚举/激活/关闭。
// 全部通过 syscall 直接调用 Win32 API,不依赖 CGO(gcc)。
// 非 Windows 的对应实现见 platform_other.go。

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

var (
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	user32       = syscall.NewLazyDLL("user32.dll")
	procFreeCons = kernel32.NewProc("FreeConsole")
	procSetCP    = kernel32.NewProc("SetConsoleOutputCP")
	procMsgBox   = user32.NewProc("MessageBoxW")
)

// freeConsole 脱离控制台(由计划任务静默启动时避免黑窗常驻)
func freeConsole() {
	procFreeCons.Call()
}

// utf8Console 让中文输出不乱码(独立进程自己的控制台;无控制台时静默失败)
func utf8Console() {
	procSetCP.Call(uintptr(65001))
}

// msgBox 简易错误弹窗(无第三方依赖)
func msgBox(text, caption string) {
	t, _ := syscall.UTF16PtrFromString(text)
	c, _ := syscall.UTF16PtrFromString(caption)
	procMsgBox.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), 0x30)
}

// hiddenWindow 令子进程不弹控制台窗口(无论本进程是否已 FreeConsole)
func hiddenWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// killStaleBrowsers 清理上次异常退出后残留的抓取浏览器(占着 profile 锁会导致启动失败)。
// 只杀命令行里携带本程序 browser_profile 路径的进程,不影响用户日常使用的 Edge/Chrome。
//
// profile 路径通过环境变量传入,避免用户名含引号/方括号时 PowerShell
// 字符串拼接出错导致清理静默失效。
func killStaleBrowsers() {
	const ps = `Get-CimInstance Win32_Process -Filter "Name='msedge.exe' OR Name='chrome.exe'" |` +
		` Where-Object { $_.CommandLine -and $_.CommandLine.Contains($env:YANHEKT_PROFILE) } |` +
		` ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }`
	cmd := exec.Command("powershell", "-NoProfile", "-Command", ps)
	cmd.Env = append(os.Environ(), "YANHEKT_PROFILE="+profileDir)
	hiddenWindow(cmd)
	_ = cmd.Run() // 查不到就是空操作;PowerShell 不可用也不阻塞抓取
}

var (
	shcore = syscall.NewLazyDLL("shcore.dll")

	procSetDpiCtx    = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetDpiAware  = user32.NewProc("SetProcessDPIAware")
	procSetDpiAware2 = shcore.NewProc("SetProcessDpiAwareness")
)

// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 == (DPI_AWARENESS_CONTEXT)-4
const dpiCtxPerMonitorV2 = ^uintptr(3)

// enableHiDPI 把进程声明为 Per-Monitor DPI 感知。
//
// 必须在创建任何窗口之前调用:否则 Windows 会把本进程当成 DPI 无关程序,
// 把窗口/菜单先按 96 DPI 渲染成位图再拉伸,在高分屏上就出现"字体发糊"。
// 托盘右键菜单正是这种原生菜单(energye/systray 自身不做 DPI 处理)。
//
// 按可用性依次回退到旧 API,保证 Win7 也能跑:
//
//	SetProcessDpiAwarenessContext(PER_MONITOR_AWARE_V2)  Win10 1703+
//	SetProcessDpiAwareness(PER_MONITOR_DPI_AWARE)        Win8.1+
//	SetProcessDPIAware()                                 Vista+
func enableHiDPI() {
	if err := procSetDpiCtx.Find(); err == nil {
		if r, _, _ := procSetDpiCtx.Call(dpiCtxPerMonitorV2); r != 0 {
			return
		}
	}
	if err := procSetDpiAware2.Find(); err == nil {
		// 0=unaware, 1=system aware, 2=per-monitor aware
		if r, _, _ := procSetDpiAware2.Call(2); r == 0 {
			return
		}
	}
	procSetDpiAware.Call()
}

// 单实例互斥量名。用 Local\ 前缀:同一登录会话内唯一即可,
// 不需要跨会话(多用户各自跑一份互不干扰)。
const instanceMutex = `Local\YanhektDDLMonDesktop`

// mutexHandle 持有互斥量句柄。故意在整个进程生命周期内不关闭:
// 关闭即释放互斥量,单实例约束随之失效。
var mutexHandle uintptr

const errorAlreadyExists = 183 // ERROR_ALREADY_EXISTS

var procCreateMutex = kernel32.NewProc("CreateMutexW")

// acquireSingleInstance 尝试成为唯一的桌面实例。
//
// 返回 false 表示已有实例正在运行,调用方应唤醒它并退出。
// 互斥量由内核保证原子性,不存在"两个进程同时探测不到对方"的竞态
// (端口探测有这种竞态)。
func acquireSingleInstance() bool {
	name, err := syscall.UTF16PtrFromString(instanceMutex)
	if err != nil {
		return true // 名称异常时不阻止启动
	}
	h, _, callErr := procCreateMutex.Call(0, 1 /* bInitialOwner */, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return true // 创建失败也不阻止启动,退化为旧的端口探测
	}
	if errno, ok := callErr.(syscall.Errno); ok && errno == errorAlreadyExists {
		syscall.CloseHandle(syscall.Handle(h))
		return false
	}
	mutexHandle = h
	return true
}

// 窗口查找/激活用的 Win32 例程。
// 目的:重复打开速查窗口时,如果窗口已经存在,就把它切到前台,
// 而不是再新开一个 Edge 应用窗口。

var (
	procEnumWindows     = user32.NewProc("EnumWindows")
	procGetWindowTextW  = user32.NewProc("GetWindowTextW")
	procGetClassNameW   = user32.NewProc("GetClassNameW")
	procIsIconic        = user32.NewProc("IsIconic")
	procShowWindow      = user32.NewProc("ShowWindow")
	procSetForeground   = user32.NewProc("SetForegroundWindow")
	procBringWindowTop  = user32.NewProc("BringWindowToTop")
	procGetForeground   = user32.NewProc("GetForegroundWindow")
	procGetWindowThread = user32.NewProc("GetWindowThreadProcessId")
	procGetCurrentTID   = kernel32.NewProc("GetCurrentThreadId")
	procAttachThreadIn  = user32.NewProc("AttachThreadInput")
	procPostMessage     = user32.NewProc("PostMessageW")
)

const (
	swRestore = 9                    // SW_RESTORE
	wmClose   = 0x0010               // WM_CLOSE
	chromeApp = "Chrome_WidgetWin_1" // Edge/Chrome 顶层窗口类名
)

// 枚举时的临时状态(EnumWindows 回调不能带自定义参数,只能用包级变量)
var (
	enumTitles []string
	enumHits   []uintptr
)

func enumWindowsProc(hwnd uintptr, _ uintptr) uintptr {
	// 1) 类名必须是 Chromium 窗口,排除托盘隐藏窗口等
	var cls [256]uint16
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&cls[0])), 255)
	if n == 0 || syscall.UTF16ToString(cls[:n]) != chromeApp {
		return 1 // 继续枚举
	}
	// 2) 标题必须与页面 <title> 完全一致。--app 窗口没有浏览器界面,
	//    标题就是页面标题;而日常 Edge 标签页的标题会带 " - 个人 - Microsoft Edge"
	var buf [512]uint16
	m, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), 511)
	if m == 0 {
		return 1
	}
	title := syscall.UTF16ToString(buf[:m])
	for _, want := range enumTitles {
		if title == want {
			enumHits = append(enumHits, hwnd)
			break
		}
	}
	return 1 // 继续枚举:可能存在多个遗留窗口
}

// findAppWindows 返回所有标题匹配的速查/登录应用窗口
func findAppWindows(titles ...string) []uintptr {
	enumTitles = titles
	enumHits = enumHits[:0]
	procEnumWindows.Call(syscall.NewCallback(enumWindowsProc), 0)
	return append([]uintptr(nil), enumHits...)
}

// closeAppWindows 关闭速查/登录窗口。
//
// 这里发 WM_CLOSE 而不是杀进程:--app 窗口复用用户日常的 Edge 进程,
// 杀进程会连带关掉用户所有标签页;WM_CLOSE 只让目标窗口自己退出。
func closeAppWindows(titles ...string) int {
	n := 0
	for _, hwnd := range findAppWindows(titles...) {
		if r, _, _ := procPostMessage.Call(hwnd, wmClose, 0, 0); r != 0 {
			n++
		}
	}
	return n
}

// focusExistingAppWindow 在已打开的窗口里寻找标题匹配的应用窗口,
// 找到就还原(若最小化)并切到前台,返回 true。
func focusExistingAppWindow(titles ...string) bool {
	found := findAppWindows(titles...)
	if len(found) == 0 {
		return false
	}
	hwnd := found[0]

	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		procShowWindow.Call(hwnd, swRestore)
	}
	procBringWindowTop.Call(hwnd)
	procSetForeground.Call(hwnd)

	// 前台锁(Windows 禁止后台进程随意抢焦点):若上一步没生效,
	// 临时把本线程挂到当前前台线程上再设置一次。
	if fg, _, _ := procGetForeground.Call(); fg != hwnd {
		var fgPID uint32
		fgTID, _, _ := procGetWindowThread.Call(fg, uintptr(unsafe.Pointer(&fgPID)))
		myTID, _, _ := procGetCurrentTID.Call()
		if fgTID != 0 && fgTID != myTID {
			procAttachThreadIn.Call(myTID, fgTID, 1)
			procSetForeground.Call(hwnd)
			procAttachThreadIn.Call(myTID, fgTID, 0)
		}
	}
	return true
}
