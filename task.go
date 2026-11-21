package main

import (
	"fmt"
	"os"
	"os/exec"
)

// installTask 注册每天 09:00 的巡检计划任务(schtasks,仅当前用户,无需管理员)
func installTask() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	tr := fmt.Sprintf(`"%s" --fetch --days 3 --notify`, exe)
	out, err := exec.Command("schtasks", "/Create", "/F",
		"/SC", "DAILY", "/ST", "09:00",
		"/TN", taskName, "/TR", tr).CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks 失败:%v:%s", err, string(out))
	}
	return nil
}

// taskExists 检查巡检计划任务是否已注册
func taskExists() bool {
	return exec.Command("schtasks", "/Query", "/TN", taskName).Run() == nil
}

// uninstallTask 删除计划任务
func uninstallTask() error {
	out, err := exec.Command("schtasks", "/Delete", "/F", "/TN", taskName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks 失败:%v:%s", err, string(out))
	}
	return nil
}
