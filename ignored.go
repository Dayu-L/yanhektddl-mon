package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ---------------------------------------------------------------- 忽略清单
// 用户在速查页标记“不再提醒”的任务,保存在程序目录 ignored.json
// (不可写时回退 %LOCALAPPDATA%\YanhektDDL),不上传。
// key 形如 "zy课程号#任务id",平台侧稳定,跨多次抓取生效。

const ignoreKeySep = "#"

func ignoreKey(zy, id string) string {
	return zy + ignoreKeySep + id
}

func splitIgnoreKey(key string) (zy, id string) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == ignoreKeySep[0] {
			return key[:i], key[i+1:]
		}
	}
	return "", key
}

// ignoredFile 磁盘格式:忽略 key 列表(保持插入顺序,便于人工检查)
type ignoredFile struct {
	Ignored []string `json:"ignored"`
}

var ignoredMu sync.Mutex

// altIgnoredPath 程序目录不可写时的回退位置(与 settings 一致)。
func altIgnoredPath() string {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return ""
	}
	return filepath.Join(local, "YanhektDDL", "ignored.json")
}

// loadIgnored 读取忽略清单。文件很小且调用频率低(每次渲染/报告一次),
// 直接读盘不做缓存,避免“改了文件界面不知道”的一致性问题。
// 文件损坏时按空清单处理(用户最多损失一次点击)。
func loadIgnored() map[string]bool {
	ignoredMu.Lock()
	defer ignoredMu.Unlock()
	out := map[string]bool{}
	for _, p := range []string{filepath.Join(base, "ignored.json"), altIgnoredPath()} {
		if p == "" {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var f ignoredFile
		if json.Unmarshal(b, &f) != nil {
			continue
		}
		for _, k := range f.Ignored {
			if k != "" {
				out[k] = true
			}
		}
		break // 只读第一个存在的文件(与 settings 的加载策略一致)
	}
	return out
}

// setIgnored 增删一个忽略标记并落盘。文件不存在视为空清单。
func setIgnored(key string, ignored bool) error {
	if key == "" {
		return errors.New("忽略标记不能为空")
	}
	if len(key) > 200 {
		return errors.New("忽略标记过长")
	}
	ignoredMu.Lock()
	defer ignoredMu.Unlock()

	for _, p := range []string{filepath.Join(base, "ignored.json"), altIgnoredPath()} {
		if p == "" {
			continue
		}
		f := ignoredFile{}
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &f) // 损坏文件按空清单重建
		}
		list := f.Ignored
		if ignored {
			found := false
			for _, k := range list {
				if k == key {
					found = true
					break
				}
			}
			if !found {
				list = append(list, key)
			}
		} else {
			out := list[:0]
			for _, k := range list {
				if k != key {
					out = append(out, k)
				}
			}
			list = out
		}
		f.Ignored = list
		b, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			return err
		}
		b = append(b, '\n')
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			continue
		}
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, b, 0o644); err != nil {
			continue
		}
		if err := os.Rename(tmp, p); err != nil {
			os.Remove(tmp)
			continue
		}
		return nil
	}
	return fmt.Errorf("没有可用的忽略清单文件位置")
}
