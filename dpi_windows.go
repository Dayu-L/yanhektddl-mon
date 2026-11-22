//go:build windows

package main

import "syscall"

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
