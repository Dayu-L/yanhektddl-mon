package main

import (
	"fmt"
	"os"
	"os/exec"
)

// installTask 按当前设置注册每日巡检计划任务(schtasks,仅当前用户,无需管理员)
func installTask() error {
	return installTaskAt(currentSettings().CheckTime)
}

// installTaskAt 在指定时间(HH:MM)注册每日巡检计划任务。
func installTaskAt(hhmm string) error {
	if !validCheckTime(hhmm) {
		return fmt.Errorf("检测时间格式无效:%s", hhmm)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	tr := fmt.Sprintf(`"%s" --fetch --days 3 --notify`, exe)
	out, err := exec.Command("schtasks", "/Create", "/F",
		"/SC", "DAILY", "/ST", hhmm,
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
