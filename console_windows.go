//go:build windows

package main

import (
	"fmt"
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

// killStaleBrowsers 清理上次异常退出后残留的抓取浏览器(占着 profile 锁会导致启动失败)。
// 只杀命令行里携带本程序 browser_profile 路径的进程,不影响用户日常使用的 Edge/Chrome。
func killStaleBrowsers() {
	ps := fmt.Sprintf(
		`Get-CimInstance Win32_Process -Filter "Name='msedge.exe' OR Name='chrome.exe'" |`+
			` Where-Object { $_.CommandLine -like '*%s*' } |`+
			` ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }`,
		profileDir)
	cmd := exec.Command("powershell", "-NoProfile", "-Command", ps)
	_ = cmd.Run() // 查不到就是空操作,失败也不阻塞抓取
}
