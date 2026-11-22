package main

import (
	"embed"
	"image"
	"image/png"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/energye/systray"
)

// 托盘图标:复用安装包/exe 同一个 app.ico(34KB),嵌入后无需外部文件
//
//go:embed assets/app.ico
var trayIcon []byte

// 按 DPI 预渲染的托盘专用 PNG(16..64,8 档),运行时按 SM_CXSMICON 精确选档,
// 避免 energye/systray 固定按 SM_CXICON 加载后由 Explorer 拉伸导致的发糊
//
//go:embed assets/tray/*.png
var trayIconFS embed.FS

// trayIconSet 懒加载:档位尺寸 -> 解码后的 RGBA 图像
var trayIconSet map[int]*image.NRGBA

func loadTrayIconSet() map[int]*image.NRGBA {
	if trayIconSet != nil {
		return trayIconSet
	}
	set := map[int]*image.NRGBA{}
	entries, err := trayIconFS.ReadDir("assets/tray")
	if err == nil {
		for _, e := range entries {
			name := e.Name()
			if !strings.HasPrefix(name, "tray_") {
				continue
			}
			size, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "tray_"), ".png"))
			if err != nil || size <= 0 {
				continue
			}
			f, err := trayIconFS.Open("assets/tray/" + name)
			if err != nil {
				continue
			}
			img, err := png.Decode(f)
			f.Close()
			if err != nil {
				continue
			}
			if m, ok := img.(*image.NRGBA); ok {
				set[size] = m
			}
		}
	}
	trayIconSet = set
	return set
}

// pickTrayIcon 选出目标尺寸的托盘图像:优先精确档位;没有精确档(罕见
// 缩放比例)时取最近的一档做盒式重采样兜底。
func pickTrayIcon(target int) *image.NRGBA {
	set := loadTrayIconSet()
	if len(set) == 0 {
		return nil
	}
	if m, ok := set[target]; ok {
		return m
	}
	// 就近找一档:先找不小于目标的,否则取最大的一档
	var chosen int
	for size := range set {
		if chosen == 0 || abs(size-target) < abs(chosen-target) {
			chosen = size
		}
	}
	return resizeBox(set[chosen], target)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// resizeBox 盒式平均重采样(纯 Go,无第三方依赖)。缩略图场景
// 质量足够;仅在目标尺寸不在预渲染档位里时才会走到这里。
func resizeBox(src *image.NRGBA, size int) *image.NRGBA {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	if w == 0 || h == 0 || size <= 0 {
		return src
	}
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	px, py := float64(w)/float64(size), float64(h)/float64(size)
	for y := 0; y < size; y++ {
		y0, y1 := int(float64(y)*py), int(float64(y+1)*py)
		if y1 <= y0 {
			y1 = y0 + 1
		}
		if y1 > h {
			y1 = h
		}
		for x := 0; x < size; x++ {
			x0, x1 := int(float64(x)*px), int(float64(x+1)*px)
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if x1 > w {
				x1 = w
			}
			var r, g, b, a, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					o := yy*src.Stride + xx*4
					r += uint64(src.Pix[o])
					g += uint64(src.Pix[o+1])
					b += uint64(src.Pix[o+2])
					a += uint64(src.Pix[o+3])
					n++
				}
			}
			o := y*dst.Stride + x*4
			dst.Pix[o] = uint8(r / n)
			dst.Pix[o+1] = uint8(g / n)
			dst.Pix[o+2] = uint8(b / n)
			dst.Pix[o+3] = uint8(a / n)
		}
	}
	return dst
}

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
		fixTrayIcon() // Windows:按 DPI 精确档位替换库默认加载的图标
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
