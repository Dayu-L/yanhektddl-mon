package main

import (
	_ "embed"
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
func runTray(onOpen func()) {
	// onReady 由库在独立 goroutine 调用;onExit 在事件循环内调用
	ready := make(chan struct{})
	go func() {
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			// 托盘创建失败(极少数环境):退回"窗口全关自动退出",避免留下
			// 用户无法关闭的隐形服务
			logger.Printf(">> 托盘创建超时,改用窗口关闭自动退出")
			startLivenessWatchdog()
		}
	}()

	systray.Run(func() {
		close(ready)
		systray.SetIcon(trayIcon)
		systray.SetTitle("延河课堂 DDL")
		systray.SetTooltip("延河课堂 DDL · 左键打开速查窗口")

		mOpen := systray.AddMenuItem("打开窗口", "打开速查窗口")
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("退出", "停止后台服务并退出")

		mOpen.Click(func() { onOpen() })
		mQuit.Click(func() {
			logger.Printf(">> 用户从托盘退出")
			systray.Quit() // 触发 onExit
		})
		// 左键单击图标 = 打开窗口
		systray.SetOnClick(func(systray.IMenu) { onOpen() })

		logger.Printf(">> 托盘已就绪(右键菜单:打开窗口 / 退出)")
	}, func() {
		logger.Printf(">> 托盘已关闭")
		shutdownAndExit()
	})
}
