package main

import (
	_ "embed"
	"sync"
)

// login.html 打入 exe:登录指引页(首次运行 / 登录失效时展示)
//
//go:embed login.html
var loginHTML string

// ---------------------------------------------------------------- 登录流程

type loginStatusT struct {
	mu      sync.Mutex
	running bool
	done    bool
	errMsg  string
	courses int
	items   int
}

var loginSt loginStatusT

func loginSnapshot() map[string]interface{} {
	loginSt.mu.Lock()
	defer loginSt.mu.Unlock()
	return map[string]interface{}{
		"running": loginSt.running,
		"done":    loginSt.done,
		"error":   loginSt.errMsg,
		"courses": loginSt.courses,
		"items":   loginSt.items,
	}
}

func loginBusy() bool {
	loginSt.mu.Lock()
	defer loginSt.mu.Unlock()
	return loginSt.running
}

// runLogin 弹出可见浏览器等待用户登录(含验证码),成功后抓取并保存快照。
// 与首次/失效重登共用同一入口:collectSnapshot 检测到未登录时会自动
// 切换到可见窗口并等待人工登录。
func runLogin() {
	loginSt.mu.Lock()
	if loginSt.running {
		loginSt.mu.Unlock()
		return
	}
	loginSt.running = true
	loginSt.done = false
	loginSt.errMsg = ""
	loginSt.mu.Unlock()

	logger.Printf("[登录] 开始登录流程…")
	snap, err := collectSnapshot(3, false)

	loginSt.mu.Lock()
	loginSt.running = false
	if err != nil {
		loginSt.errMsg = err.Error()
		loginSt.mu.Unlock()
		logger.Printf("[登录] 失败:%v", err)
		return
	}
	loginSt.done = true
	loginSt.courses = len(snap.Courses)
	loginSt.items = len(snap.Items)
	loginSt.mu.Unlock()
	logger.Printf("[登录] 成功:%d 门课 %d 条任务", len(snap.Courses), len(snap.Items))

	// 首次登录成功后顺带注册每日巡检(已存在则跳过)
	if !taskExists() {
		if err := installTask(); err == nil {
			logger.Printf("[登录] 已注册每日 9:00 巡检任务")
		}
	}
}
