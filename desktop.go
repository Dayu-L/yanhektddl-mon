package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"time"
)

// openWindow 用 Edge 应用窗口打开速查页(系统没装 Edge 时退回默认浏览器)
func openWindow(port int) {
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	if bin := findBrowserBin(); bin != "" {
		// --app 用应用窗口(无地址栏),不影响日常浏览器
		_ = exec.Command(bin, "--app="+url, "--window-size=1180,880").Start()
		return
	}
	_ = exec.Command("cmd", "/c", "start", url).Start()
}

// probeOurs 8765 上运行的是不是我们自己的服务(防止把别的程序误认成自己)
func probeOurs() bool {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/status", portFirst))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var st struct {
		Running bool `json:"running"`
	}
	return json.NewDecoder(resp.Body).Decode(&st) == nil
}

// desktopMode 双击 exe 的默认模式:本地服务 + Edge 速查窗口。
// 重复启动时只新开窗口,不会起第二个服务。
func desktopMode() {
	// 是自家服务在跑:只开窗口
	if probeOurs() {
		openWindow(portFirst)
		logger.Printf(">> 服务已在运行,已打开窗口")
		return
	}

	// 端口被其他程序占用时自动换端口(8765-8769),避免“双击没反应”
	ln, port := listenAvailable()
	if ln == nil {
		msg := "端口 8765-8769 均被占用,服务无法启动。详见 server.log"
		logger.Printf(">> %s", msg)
		msgBox(msg, "延河课堂 DDL")
		return
	}
	if port != portFirst {
		logger.Printf(">> 8765 被其他程序占用,已改用端口 %d", port)
	}

	srv := &http.Server{Handler: newMux()}
	go func() {
		if err := srv.Serve(ln); err != nil {
			logger.Printf(">> 服务异常退出:%v", err)
		}
	}()
	time.Sleep(300 * time.Millisecond)

	// 首次运行还没有数据 -> 窗口会自动落到 /login 登录向导页,
	// 由用户点[开始登录]再进入可见浏览器流程(不自动弹窗,避免吓到新手)
	openWindow(port)
	logger.Printf(">> 延河课堂 DDL 服务已启动:http://127.0.0.1:%d/", port)
	logger.Printf(">> 关闭进程后,双击 exe 可随时重新启动(进程名 %s)", exeName)

	// 常驻服务:直到进程被结束
	select {}
}

func listenAvailable() (net.Listener, int) {
	for port := portFirst; port <= portLast; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return ln, port
		}
	}
	return nil, 0
}
