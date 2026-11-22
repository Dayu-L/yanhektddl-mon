package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// settings.html 打入 exe:设置页(自动巡检、关闭窗口行为)
//
//go:embed web/settings.html
var settingsHTML string

const (
	closeBehaviorTray = "tray" // 关闭窗口 -> 服务留在托盘
	closeBehaviorExit = "exit" // 关闭窗口 -> 退出程序
	defaultCheckTime  = "09:00"
)

// Settings 用户设置,保存在程序目录的 settings.json(不可写时回退到
// %LOCALAPPDATA%\YanhektDDL),不上传。
type Settings struct {
	AutoCheck     bool   `json:"auto_check"`     // 是否启用每日自动检测(Windows 计划任务)
	CheckTime     string `json:"check_time"`     // "HH:MM",24 小时制
	CloseBehavior string `json:"close_behavior"` // tray / exit
}

var (
	settingsMu   sync.Mutex
	settingsData *Settings
	settingsOnce sync.Once
)

// checkTimeRE 24 小时制 HH:MM
var checkTimeRE = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

func validCheckTime(s string) bool { return checkTimeRE.MatchString(s) }

// altSettingsPath 程序目录不可写时的设置文件回退位置。
func altSettingsPath() string {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return ""
	}
	return filepath.Join(local, "YanhektDDL", "settings.json")
}

// defaultSettings 首次运行(没有 settings.json)时的默认值:
// 沿用安装时是否已注册巡检任务,关闭窗口沿用“留在托盘”。
func defaultSettings() Settings {
	return Settings{
		AutoCheck:     taskExists(),
		CheckTime:     defaultCheckTime,
		CloseBehavior: closeBehaviorTray,
	}
}

func normalizeSettings(s Settings) (Settings, error) {
	if !validCheckTime(s.CheckTime) {
		return s, errors.New("检测时间格式无效,应为 HH:MM")
	}
	switch s.CloseBehavior {
	case closeBehaviorTray, closeBehaviorExit:
	default:
		return s, errors.New("关闭窗口行为无效")
	}
	return s, nil
}

// currentSettings 读取当前设置(进程内缓存;首次从磁盘加载)。
func currentSettings() Settings {
	settingsOnce.Do(func() {
		s := defaultSettings()
		paths := []string{settingsJSON, altSettingsPath()}
		for _, p := range paths {
			if p == "" {
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			var got Settings
			if json.Unmarshal(b, &got) != nil {
				continue
			}
			if got.CheckTime == "" {
				got.CheckTime = defaultCheckTime
			}
			if got.CloseBehavior == "" {
				got.CloseBehavior = closeBehaviorTray
			}
			if n, err := normalizeSettings(got); err == nil {
				s = n
			}
			break
		}
		settingsMu.Lock()
		settingsData = &s
		settingsMu.Unlock()
	})
	settingsMu.Lock()
	defer settingsMu.Unlock()
	return *settingsData
}

// writeSettings 原子写入(先写临时文件再改名);程序目录不可写时回退到
// %LOCALAPPDATA%\YanhektDDL,保证便携版放在只读目录也能保存设置。
func writeSettings(s Settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	paths := []string{settingsJSON, altSettingsPath()}
	var lastErr error
	for _, p := range paths {
		if p == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			lastErr = err
			continue
		}
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, b, 0o644); err != nil {
			lastErr = err
			continue
		}
		if err := os.Rename(tmp, p); err != nil {
			os.Remove(tmp)
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的设置文件位置")
	}
	return lastErr
}

// saveSettings 校验并应用设置:先确保计划任务与设置一致,再落盘。
// 计划任务创建失败时不保存,避免界面显示“已开启”但实际不会巡检。
func saveSettings(in Settings) error {
	s, err := normalizeSettings(in)
	if err != nil {
		return err
	}
	if s.AutoCheck {
		if err := installTaskAt(s.CheckTime); err != nil {
			return err
		}
	} else if taskExists() {
		if err := uninstallTask(); err != nil {
			return err
		}
	}
	if err := writeSettings(s); err != nil {
		return err
	}
	settingsMu.Lock()
	settingsData = &s
	settingsMu.Unlock()
	return nil
}

// syncAutoCheck 启动时让计划任务向设置看齐:开启则补建缺失的任务,
// 关闭则删除已存在的任务。用户手工删掉任务后,下次启动会按设置恢复。
func syncAutoCheck() {
	s := currentSettings()
	if s.AutoCheck {
		if !taskExists() {
			if err := installTaskAt(s.CheckTime); err != nil {
				logger.Printf("[设置] 注册自动巡检任务失败:%v", err)
			} else {
				logger.Printf("[设置] 已按设置注册每天 %s 的自动巡检", s.CheckTime)
			}
		}
		return
	}
	if taskExists() {
		if err := uninstallTask(); err != nil {
			logger.Printf("[设置] 删除自动巡检任务失败:%v", err)
		} else {
			logger.Printf("[设置] 自动巡检已关闭,已删除计划任务")
		}
	}
}
