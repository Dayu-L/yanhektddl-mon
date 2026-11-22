//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

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
