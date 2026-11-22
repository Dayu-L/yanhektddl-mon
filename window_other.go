//go:build !windows

package main

// 非 Windows 平台不做窗口复用(总是新开窗口)
func focusExistingAppWindow(titles ...string) bool { return false }
