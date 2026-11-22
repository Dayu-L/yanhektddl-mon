//go:build !windows

package main

// 非 Windows 平台无需处理(Win32 DPI API 仅 Windows 有)
func enableHiDPI() {}
