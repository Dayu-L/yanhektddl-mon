package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// notify Windows 气泡通知(与 python notify 一致:PowerShell + NotifyIcon,失败忽略)
func notify(title, msg string) {
	ps := strings.Join([]string{
		"Add-Type -AssemblyName System.Windows.Forms, System.Drawing;",
		"$n = New-Object System.Windows.Forms.NotifyIcon;",
		"$n.Icon = [System.Drawing.SystemIcons]::Warning;",
		"$n.Visible = $true;",
		fmt.Sprintf("$n.ShowBalloonTip(15000, '%s', @'\n%s\n'@, "+
			"[System.Windows.Forms.ToolTipIcon]::Warning);", title, msg),
		"Start-Sleep -Seconds 16; $n.Dispose()",
	}, "")
	cmd := exec.Command("powershell", "-NoProfile", "-Command", ps)
	if err := cmd.Start(); err != nil {
		return
	}
	go func() { _ = cmd.Wait() }() // 不阻塞主流程
}
