//go:build windows

package main

import (
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
