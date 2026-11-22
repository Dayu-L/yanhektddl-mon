package main

import (
	_ "embed"
	"sync/atomic"
	"time"

	"github.com/energye/systray"
)

// 托盘图标:复用安装包/exe 同一个 app.ico(34KB),嵌入后无需外部文件
//
//go:embed assets/app.ico
var trayIcon []byte

// runTray 启动系统托盘并阻塞(桌面模式的主循环)。
//
// 托盘是为了给"无窗口的后台服务"一个可见、可关闭的入口:
//   - 右键菜单:打开窗口 / 退出
//   - 左键单击:打开窗口
//   - 关闭 Edge 速查窗口不再导致服务自杀(watchdog 由托盘兜底接替)
//
// 注意:energye/systray 的 SetIcon 会把图标字节按 md5 写到
// %TEMP%\systray_temp_icon_<hash>(仅一次、内容为 .ico 数据文件、从不执行),
// 这是该库的固有实现,不影响"不释放可执行文件"的约定。
// trayActive 托盘是否已经跑起来(用于退出时干净地移除图标)
var trayActive atomic.Bool

// quitFromWatchdog 请求退出程序。
//
// 托盘可用时走 systray.Quit:它会移除托盘图标并触发 onExit(内部调用
// shutdownAndExit);若托盘在极短时间内没响应,兜底直接退出,避免服务
// 变成关不掉的后台进程。
func quitFromWatchdog() {
	if trayActive.Load() {
		systray.Quit()
		time.Sleep(1500 * time.Millisecond)
	}
	shutdownAndExit()
}

func runTray(onOpen, onSettings func()) {
	// onReady 由库在独立 goroutine 调用;onExit 在事件循环内调用
	ready := make(chan struct{})
	go func() {
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			// 托盘创建失败(极少数环境):退回"窗口全关自动退出",避免留下
			// 用户无法关闭的隐形服务
			logger.Printf(">> 托盘创建超时,改用窗口关闭自动退出")
			startLivenessWatchdog(true)
		}
	}()

	systray.Run(func() {
		close(ready)
		trayActive.Store(true)
		systray.SetIcon(trayIcon)
		systray.SetTitle("延河课堂 DDL")
		systray.SetTooltip("延河课堂 DDL · 左键打开速查窗口")

		mOpen := systray.AddMenuItem("打开窗口", "打开速查窗口")
		mSettings := systray.AddMenuItem("设置…", "自动巡检与关闭窗口行为")
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("退出", "停止后台服务并退出")

		mOpen.Click(func() { onOpen() })
		mSettings.Click(func() { onSettings() })
		mQuit.Click(func() {
			logger.Printf(">> 用户从托盘退出")
			systray.Quit() // 触发 onExit
		})
		// 左键单击图标 = 打开窗口
		systray.SetOnClick(func(systray.IMenu) { onOpen() })

		logger.Printf(">> 托盘已就绪(右键菜单:打开窗口 / 设置 / 退出)")
	}, func() {
		logger.Printf(">> 托盘已关闭")
		shutdownAndExit()
	})
}
