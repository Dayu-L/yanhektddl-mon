//go:build windows

package main

// Windows 平台适配层:控制台、DPI、单实例、窗口枚举/激活/关闭。
// 全部通过 syscall 直接调用 Win32 API,不依赖 CGO(gcc)。
// 非 Windows 的对应实现见 platform_other.go。

import (
	"image"
	"os"
	"os/exec"
	"syscall"
	"time"
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

// ---------------------------------------------------------------- 托盘图标按 DPI 精确渲染

// energye/systray 用 LoadImage(cx=0,cy=0,LR_DEFAULTSIZE) 加载托盘图标,
// 即固定按 SM_CXICON(本进程 Per-Monitor V2 感知,250% 缩放下 = 80px)取
// 最近帧再拉伸;Explorer 又把它缩到托盘物理槽位(40px),两次重采样导致
// 图标发糊。fixTrayIcon 按 SM_CXSMICON(托盘小图标的物理尺寸)精确选档,
// 自建 HICON 后用 Shell_NotifyIcon(NIM_MODIFY) 替换库加载的图标。
//
// 库内部约定:托盘隐藏窗口类名 "SystrayClass"、托盘项 ID = 100
// (见 energye/systray v1.0.3 systray_windows.go)。Explorer 重启时库会用
// 自己的图标重新 NIM_ADD,此修正只在进程启动时执行一次,可接受。

var (
	gdi32                = syscall.NewLazyDLL("gdi32.dll")
	shell32              = syscall.NewLazyDLL("shell32.dll")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	procCreateIconInd    = user32.NewProc("CreateIconIndirect")
	procCreateBitmap     = gdi32.NewProc("CreateBitmap")
	procCreateDIBSection = gdi32.NewProc("CreateDIBSection")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
	procShellNotifyIcon  = shell32.NewProc("Shell_NotifyIconW")
)

const (
	smCxSMIcon    = 49 // SM_CXSMICON
	smCySMIcon    = 50 // SM_CYSMICON
	smCxIcon      = 11 // SM_CXICON
	smCyIcon      = 12 // SM_CYICON
	systrayClass  = "SystrayClass"
	systrayItemID = 100
	nimModify     = 0x00000001
	nifIcon       = 0x00000002
	wmSetIcon     = 0x0080
	wmGetIcon     = 0x007F
	iconSmall     = 0
	iconBig       = 1
)

// iconInfo 对应 Win32 ICONINFO(注意 64 位下句柄按 8 字节对齐,中间有 4 字节填充)
type iconInfo struct {
	FIcon    uint32
	XHotspot uint32
	YHotspot uint32
	_        uint32
	HbmMask  uintptr
	HbmColor uintptr
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

// trayNotifyIconData 与 energye/systray 内部 notifyIconData 逐字段一致
// (即 Win32 NOTIFYICONDATAW 的 Vista+ 布局),保证 cbSize 与已注册项吻合。
type trayNotifyIconData struct {
	Size                       uint32
	Wnd                        uintptr
	ID, Flags, CallbackMessage uint32
	Icon                       uintptr
	Tip                        [128]uint16
	State, StateMask           uint32
	Info                       [256]uint16
	Timeout, Version           uint32
	InfoTitle                  [64]uint16
	InfoFlags                  uint32
	GUID                       [16]byte
	BalloonIcon                uintptr
}

var (
	trayEnumPID uint32
	trayEnumHit uintptr
)

func trayEnumProc(hwnd uintptr, _ uintptr) uintptr {
	var cls [64]uint16
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&cls[0])), 63)
	if n == 0 || syscall.UTF16ToString(cls[:n]) != systrayClass {
		return 1
	}
	var pid uint32
	procGetWindowThread.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == trayEnumPID {
		trayEnumHit = hwnd
		return 0 // 找到即停止枚举
	}
	return 1
}

// findSystrayWindow 找到本进程内 energye/systray 的隐藏托盘窗口
func findSystrayWindow() uintptr {
	trayEnumPID = uint32(syscall.Getpid())
	trayEnumHit = 0
	procEnumWindows.Call(syscall.NewCallback(trayEnumProc), 0)
	return trayEnumHit
}

// createTrayHICON 用 32bpp 顶朝下 DIB(直通 alpha)构造 HICON
func createTrayHICON(img *image.NRGBA) uintptr {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if w == 0 || h == 0 {
		return 0
	}
	bi := bitmapInfo{Header: bitmapInfoHeader{
		Size:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:    int32(w),
		Height:   -int32(h), // 负高 = 顶朝下,与 RGBA 行序一致
		Planes:   1,
		BitCount: 32,
	}}
	var bits unsafe.Pointer
	hColor, _, _ := procCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bi)),
		0 /* DIB_RGB_COLORS */, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hColor == 0 || bits == nil {
		return 0
	}
	// RGBA -> BGRA
	raw := unsafe.Slice((*byte)(bits), w*h*4)
	for i := 0; i < w*h; i++ {
		o := i * 4
		raw[o], raw[o+2] = img.Pix[o+2], img.Pix[o]
		raw[o+1] = img.Pix[o+1]
		raw[o+3] = img.Pix[o+3]
	}
	// 全零 1bpp 掩码:32bpp 色彩位图自带 alpha,掩码不参与绘制。
	// 注意 CreateBitmap 传 NULL 时内容未定义,必须显式给零缓冲,
	// 否则垃圾掩码位会遮掉正常像素。
	maskBytes := make([]byte, ((w+7)/8)*h)
	hMask, _, _ := procCreateBitmap.Call(uintptr(w), uintptr(h), 1, 1,
		uintptr(unsafe.Pointer(&maskBytes[0])))
	ii := iconInfo{FIcon: 1, HbmMask: hMask, HbmColor: hColor}
	hIcon, _, _ := procCreateIconInd.Call(uintptr(unsafe.Pointer(&ii)))
	// CreateIconIndirect 内部复制了位图,原始句柄即可释放
	procDeleteObject.Call(hMask)
	procDeleteObject.Call(hColor)
	return hIcon
}

// fixTrayIcon 在托盘就绪后调用:按当前 DPI 把库加载的默认图标替换为
// 精确尺寸渲染版。失败只记日志,保持库的原图标兜底;任何异常都被
// recover 拦下,绝不允许把桌面服务带崩。
func fixTrayIcon() {
	defer func() {
		if r := recover(); r != nil {
			logger.Printf("[托盘] 图标修正异常:%v(沿用默认图标)", r)
		}
	}()
	cx, _, _ := procGetSystemMetrics.Call(smCxSMIcon)
	cy, _, _ := procGetSystemMetrics.Call(smCySMIcon)
	if cx == 0 || cy == 0 {
		return
	}
	img := pickTrayIcon(int(cx))
	if img == nil {
		return
	}
	hIcon := createTrayHICON(img)
	if hIcon == 0 {
		logger.Printf("[托盘] 精确图标创建失败,沿用默认图标")
		return
	}
	hwnd := findSystrayWindow()
	if hwnd == 0 {
		logger.Printf("[托盘] 未找到托盘窗口,沿用默认图标")
		return
	}
	nid := trayNotifyIconData{
		Wnd:   hwnd,
		ID:    systrayItemID,
		Flags: nifIcon,
		Icon:  hIcon,
	}
	nid.Size = uint32(unsafe.Sizeof(nid))
	if r, _, _ := procShellNotifyIcon.Call(nimModify,
		uintptr(unsafe.Pointer(&nid))); r == 0 {
		logger.Printf("[托盘] 图标替换失败(Shell_NotifyIcon),沿用默认图标")
		return
	}
	logger.Printf("[托盘] 图标已按当前缩放替换为 %dpx 精确渲染版", cx)
}

// ---------------------------------------------------------------- 速查窗口任务栏图标

// Edge --app 窗口的任务栏按钮图标取自站点 favicon:Chromium 只拿约 24px
// 的帧光栅化到 40px(本机 250% 缩放),看起来发糊,而 <link rel=icon> 声明
// 大尺寸 PNG 也不会改变 --app 窗口图标。这里在窗口出现后主动 WM_SETICON
// 换成按 SM_CXSMICON/SM_CXICON 精确预渲染的 HICON(跨进程实测有效且
// 像素级精确)。Chromium 可能在 favicon 更新时覆盖,故短时间内补设几次。

var (
	procSendMessageW = user32.NewProc("SendMessageW")
	appIconCache     = map[int]uintptr{} // 尺寸 -> HICON(进程内复用,不释放)
)

// appWindowIcon 取(并缓存)指定物理尺寸的窗口图标
func appWindowIcon(size int) uintptr {
	if h, ok := appIconCache[size]; ok {
		return h
	}
	h := createTrayHICON(pickTrayIcon(size))
	if h != 0 {
		appIconCache[size] = h
	}
	return h
}

// setAppWindowIcon 给窗口设置大小图标各一次
func setAppWindowIcon(hwnd uintptr) {
	cx, _, _ := procGetSystemMetrics.Call(smCxSMIcon)
	big, _, _ := procGetSystemMetrics.Call(smCxIcon)
	if small := appWindowIcon(int(cx)); small != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, iconSmall, small)
	}
	if b := appWindowIcon(int(big)); b != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, iconBig, b)
	}
}

// applyWindowIconSoon 异步等待应用窗口出现并设置精确图标。
// favicon 晚于页面加载生效,可能在窗口出现后再覆盖图标,因此补设两次;
// 窗口一直没出现(启动失败等)则静静结束。
func applyWindowIconSoon(titles ...string) {
	go func() {
		deadline := time.Now().Add(20 * time.Second)
		var wins []uintptr
		for time.Now().Before(deadline) {
			if wins = findAppWindows(titles...); len(wins) > 0 {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if len(wins) == 0 {
			return
		}
		for _, hwnd := range wins {
			setAppWindowIcon(hwnd)
		}
		// favicon 晚于页面加载生效,可能覆盖图标,补设两次
		for _, delay := range []time.Duration{2 * time.Second, 4 * time.Second} {
			time.Sleep(delay)
			for _, hwnd := range wins {
				setAppWindowIcon(hwnd)
			}
		}
	}()
}

// ---------------------------------------------------------------- 窗口查找/激活

// 重复打开速查窗口时,如果窗口已经存在,就把它切到前台,
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
