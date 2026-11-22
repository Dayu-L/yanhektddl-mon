//go:build !windows

package main

// 非 Windows 平台不做窗口复用与关闭
func focusExistingAppWindow(titles ...string) bool { return false }

func closeAppWindows(titles ...string) int { return 0 }
