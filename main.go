package main

import (
	_ "embed"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// dashboard.html 打入 exe,无需单独分发
//
//go:embed web/dashboard.html
var dashboardHTML string

// 版本号:默认值与 installer.iss/build-installer.ps1 保持一致,
// 官方构建经 -ldflags "-X main.version=<版本>" 注入
var version = "0.3.2"

// preprocessArgs 把裸的 --from-json 补成 --from-json=(即“使用默认快照路径”),
// 以保持与旧版 `--from-json [路径]` 兼容。
func preprocessArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--from-json" &&
			(i+1 == len(args) || strings.HasPrefix(args[i+1], "-")) {
			out = append(out, "--from-json=")
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func flagSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func main() {
	// 尽早声明 DPI 感知:托盘右键菜单是原生窗口,晚于窗口创建再声明就无效了
	enableHiDPI()

	days := flag.Int("days", 3, "“临期”阈值天数(默认 3)")
	watch := flag.Int("watch", 0, "循环监控:每 N 分钟重新抓取一次(Ctrl+C 停止)")
	notifyFlag := flag.Bool("notify", false, "发现临期/漏交任务时弹 Windows 通知")
	visible := flag.Bool("visible", false, "以可见窗口运行浏览器(调试用)")
	fetch := flag.Bool("fetch", false, "单次抓取并生成报告")
	fromJSON := flag.String("from-json", "", "不联网,用数据快照生成报告(可选指定路径)")
	installFlag := flag.Bool("install-task", false, "注册每天 9:00 巡检计划任务")
	uninstallFlag := flag.Bool("uninstall-task", false, "删除巡检计划任务")
	showVersion := flag.Bool("version", false, "显示版本号")
	quitFlag := flag.Bool("quit", false, "通知运行中的桌面服务退出")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "延河课堂/乐学 DDL 监控(无参数 = 桌面模式:本地服务 + 速查窗口)")
		flag.PrintDefaults()
	}
	flag.CommandLine.Parse(preprocessArgs(os.Args[1:]))

	// ---- 显示版本 ----
	if *showVersion {
		utf8Console()
		fmt.Printf("延河课堂 DDL v%s\n", version)
		return
	}

	// ---- 退出运行中的服务 ----
	if *quitFlag {
		utf8Console()
		if !postQuit() {
			fmt.Println("没有发现运行中的服务(8765-8769 均无响应)")
			os.Exit(1)
		}
		fmt.Println("已通知服务退出")
		return
	}

	// ---- 卸载计划任务 ----
	if *uninstallFlag {
		utf8Console()
		if err := uninstallTask(); err != nil {
			fmt.Printf("删除失败:%v\n", err)
			os.Exit(1)
		}
		fmt.Println("已删除每日巡检计划任务")
		return
	}

	// ---- 注册计划任务 ----
	if *installFlag {
		utf8Console()
		if err := installTask(); err != nil {
			fmt.Printf("注册失败:%v\n", err)
			os.Exit(1)
		}
		fmt.Println("已注册每日 09:00 巡检任务(包含 Windows 通知提醒)")
		return
	}

	// ---- 用快照离线生成报告 ----
	if flagSet("from-json") {
		utf8Console()
		path := *fromJSON
		if path == "" {
			path = dataJSON
		}
		if !reportFromSnapshot(path, *days) {
			os.Exit(1) // 存在紧急事项,退出码便于脚本联动
		}
		return
	}

	// ---- 循环监控 ----
	if *watch > 0 {
		utf8Console()
		fmt.Printf("循环监控已启动:每 %d 分钟检查一次,Ctrl+C 退出。\n", *watch)
		for {
			fetchOnce(*days, *visible, *notifyFlag)
			time.Sleep(time.Duration(*watch) * time.Minute)
		}
	}

	// ---- 单次抓取(--fetch/--notify/--visible/显式 --days) ----
	if *fetch || *notifyFlag || *visible || flagSet("days") {
		if *notifyFlag { // 计划任务静默运行,不留控制台黑窗
			freeConsole()
		} else {
			utf8Console()
		}
		if !fetchOnce(*days, *visible, *notifyFlag) {
			os.Exit(1)
		}
		return
	}

	// ---- 桌面模式(双击 exe) ----
	freeConsole()
	initFileLog()
	desktopMode()
}

// fetchOnce 完整抓取一次并生成报告与通知;返回是否有临期/漏交事项
func fetchOnce(days int, visible, withNotify bool) bool {
	snap, err := collectSnapshot(days, visible)
	if err != nil {
		fmt.Printf("[!] 抓取失败:%v\n", err)
		return false
	}
	submitted := snap.SubmittedExams
	if submitted == nil {
		submitted = map[string][]interface{}{}
	}
	rep := buildReport(snap.Courses, snap.Items, submitted, days)
	writeReports(rep, days)
	printSummary(rep, days)

	if withNotify && hasUrgent(rep) {
		var lines []string
		for i, r := range append(rep.Bucket.Missed, rep.Bucket.Urgent...) {
			if i >= 5 {
				break
			}
			lines = append(lines, fmt.Sprintf("%s | %s 截止 %s",
				r.Course, r.Title, r.End.Format("01-02 15:04")))
		}
		notify("延河课堂 DDL 提醒", strings.Join(lines, "\n"))
	}
	return hasUrgent(rep)
}

// reportFromSnapshot 不打开浏览器,直接用已保存的数据快照生成报告
func reportFromSnapshot(path string, days int) bool {
	snap, err := loadSnapshotFrom(path)
	if err != nil {
		fmt.Printf("[错误] 读取快照失败:%v\n", err)
		return false
	}
	submitted := snap.SubmittedExams
	if submitted == nil {
		submitted = map[string][]interface{}{}
	}
	rep := buildReport(snap.Courses, snap.Items, submitted, days)
	writeReports(rep, days)
	printSummary(rep, days)
	return !hasUrgent(rep)
}
