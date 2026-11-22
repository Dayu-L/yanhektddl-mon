//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// 单实例互斥量名。用 Local\ 前缀:同一登录会话内唯一即可,
// 不需要跨会话(多用户各自跑一份互不干扰)。
const instanceMutex = `Local\YanhektDDLMonDesktop`

// mutexHandle 持有互斥量句柄。故意在整个进程生命周期内不关闭:
// 关闭即释放互斥量,单实例约束随之失效。
var mutexHandle uintptr

const errorAlreadyExists = 183 // ERROR_ALREADY_EXISTS

var procCreateMutex = kernel32.NewProc("CreateMutexW")

// acquireSingleInstance 尝试成为唯一的桌面实例。
//
// 返回 false 表示已有实例正在运行,调用方应唤醒它并退出。
// 互斥量由内核保证原子性,不存在"两个进程同时探测不到对方"的竞态
// (端口探测有这种竞态)。
func acquireSingleInstance() bool {
	name, err := syscall.UTF16PtrFromString(instanceMutex)
	if err != nil {
		return true // 名称异常时不阻止启动
	}
	h, _, callErr := procCreateMutex.Call(0, 1 /* bInitialOwner */, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return true // 创建失败也不阻止启动,退化为旧的端口探测
	}
	if errno, ok := callErr.(syscall.Errno); ok && errno == errorAlreadyExists {
		syscall.CloseHandle(syscall.Handle(h))
		return false
	}
	mutexHandle = h
	return true
}
