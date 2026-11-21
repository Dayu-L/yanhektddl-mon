//go:build !windows

package main

// 非 Windows 平台的占位实现(便于交叉编译检查)

func freeConsole() {}

func utf8Console() {}

func msgBox(text, caption string) {
	println(caption + ": " + text)
}

func killStaleBrowsers() {}
