//go:build !windows

package main

// 非 Windows 平台不做单实例限制(本程序主要面向 Windows)
func acquireSingleInstance() bool { return true }
