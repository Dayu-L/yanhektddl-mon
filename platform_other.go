//go:build !windows

package main

// 非 Windows 平台的占位实现(便于交叉编译检查)。
// 程序主要面向 Windows,这里只需保证能编译。

import "os/exec"

// 非 Windows 平台的占位实现(便于交叉编译检查)

func freeConsole() {}

func utf8Console() {}

func msgBox(text, caption string) {
	println(caption + ": " + text)
}

func killStaleBrowsers() {}

func hiddenWindow(cmd *exec.Cmd) {}

// 非 Windows 平台无需处理(Win32 DPI API 仅 Windows 有)
func enableHiDPI() {}

// 非 Windows 平台不做单实例限制(本程序主要面向 Windows)
func acquireSingleInstance() bool { return true }

// 非 Windows 平台不做窗口复用与关闭
func focusExistingAppWindow(titles ...string) bool { return false }

func closeAppWindows(titles ...string) int { return 0 }

// 非 Windows 平台无 energye/systray 托盘,无需替换图标
func fixTrayIcon() {}

// 非 Windows 平台没有 Edge 应用窗口图标问题
func applyWindowIconSoon(titles ...string) {}
