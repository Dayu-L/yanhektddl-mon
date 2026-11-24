package main

// 数据源登录态:最近一次「已知」结论(登录向导 / 手动刷新 / 每日巡检 / 保温 ping
// 都会更新),落盘 login_status.json。设置页显示这份状态,点「检查登录状态」
// 时再真起一次 headless Edge 逐个探测三路会话并写回。
//
// 记录刻意只存布尔 + 时间 + 原因,不存任何账号/会话内容。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	sourceEclass = "eclass" // 乐学 zy-eclass(必需)
	sourceAita   = "aita"   // 延河课堂(可选:课程关闭时间)
	sourceZhs    = "zhs"    // 智慧树在线学堂(可选:共享课 / 智慧课程)
)

type sourceState struct {
	OK        bool   `json:"ok"`
	CheckedAt string `json:"checked_at"`    // 最近一次已知结论的时间(RFC3339,北京时间)
	Why       string `json:"why,omitempty"` // 未登录/异常时的说明
}

type loginStatusDoc struct {
	Sources map[string]sourceState `json:"sources"`
}

var (
	loginStatusMu   sync.Mutex
	loginStatusData *loginStatusDoc // 懒加载缓存(base 变化只在测试里发生)
	loginCheckBusy  atomic.Bool     // 探测进行中(设置页据此显示转圈)
	loginCheckMsg   string          // 上一次检查留下的提示(仅 loginStatusMu 下读写)
	loginCheckMsgAt string          // 提示产生的时间,过旧的提示页面会忽略
)

func loginStatusPath() string { return filepath.Join(base, "login_status.json") }

// loadLoginStatusLocked 读盘一次并缓存;文件缺失/损坏都按"没有记录"处理。
func loadLoginStatusLocked() *loginStatusDoc {
	if loginStatusData != nil {
		return loginStatusData
	}
	doc := &loginStatusDoc{Sources: map[string]sourceState{}}
	if b, err := os.ReadFile(loginStatusPath()); err == nil {
		var d loginStatusDoc
		if json.Unmarshal(b, &d) == nil && d.Sources != nil {
			doc = &d
		}
	}
	loginStatusData = doc
	return doc
}

func saveLoginStatusLocked() error {
	b, err := json.MarshalIndent(loginStatusData, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(loginStatusPath(), append(b, '\n'), 0o644); err != nil {
		return err
	}
	return nil
}

// recordSource 记下一次结论(ok + 原因)。写盘失败只记日志:
// 登录态只是展示用,不能因为它影响抓取主流程。
func recordSource(key string, ok bool, why string) {
	loginStatusMu.Lock()
	defer loginStatusMu.Unlock()
	doc := loadLoginStatusLocked()
	doc.Sources[key] = sourceState{
		OK:        ok,
		CheckedAt: time.Now().In(TZ).Format(time.RFC3339),
		Why:       why,
	}
	if err := saveLoginStatusLocked(); err != nil {
		logger.Printf("[登录状态] 写入失败:%v", err)
	}
}

// loginStatusSnapshot 设置页用:三路最近结论 + 是否正在检查 + 上次检查的提示。
func loginStatusSnapshot() map[string]interface{} {
	loginStatusMu.Lock()
	defer loginStatusMu.Unlock()
	doc := loadLoginStatusLocked()
	src := make(map[string]sourceState, len(doc.Sources))
	for k, v := range doc.Sources {
		src[k] = v
	}
	return map[string]interface{}{
		"sources":          src,
		"checking":         loginCheckBusy.Load(),
		"check_message":    loginCheckMsg,
		"check_message_at": loginCheckMsgAt,
	}
}

// startLoginCheck 异步跑一次会话探测;已有检查在进行时返回 false。
func startLoginCheck() bool {
	if !loginCheckBusy.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		defer loginCheckBusy.Store(false)
		msg := checkLoginSources()
		loginStatusMu.Lock()
		loginCheckMsg = msg
		loginCheckMsgAt = time.Now().In(TZ).Format(time.RFC3339)
		loginStatusMu.Unlock()
	}()
	return true
}

// checkLoginSources 真起一次 headless Edge,依次探测乐学 / 延河课堂 / 智慧树。
// 返回给设置页的提示(空 = 正常完成)。
func checkLoginSources() string {
	// 与刷新/登录向导/保温 ping 抢同一个 profile:抢不到就直接告诉用户,
	// 不让 HTTP 请求线程干等几分钟。
	if !profileMu.TryLock() {
		return "正在抓取数据或登录中,等它结束再检查"
	}
	defer profileMu.Unlock()

	s, err := openBrowser(false)
	if err != nil {
		for _, k := range []string{sourceEclass, sourceAita, sourceZhs} {
			recordSource(k, false, "浏览器启动失败")
		}
		return "浏览器启动失败:" + err.Error()
	}
	defer s.close()

	// ---- 乐学(必需)----
	if err := s.navigate(eclassHome, 0); err != nil {
		recordSource(sourceEclass, false, "导航失败(网络或浏览器异常)")
	} else if ok, needLogin := probeEclass(s); ok {
		recordSource(sourceEclass, true, "")
		setLoginRequired(false)
	} else if needLogin {
		recordSource(sourceEclass, false, "会话已失效,需要重新登录")
		setLoginRequired(true)
	} else {
		recordSource(sourceEclass, false, "探测失败(网络或接口异常)")
	}

	// ---- 延河课堂(可选)----
	if probeAita(s) {
		recordSource(sourceAita, true, "")
	} else {
		recordSource(sourceAita, false, "未登录或会话已失效,课程关闭时间沿用上次快照")
	}

	// ---- 智慧树(可选)----
	ok, why := probeZhs(s)
	recordSource(sourceZhs, ok, why)

	logger.Printf("[检查] 会话探测完成")
	return ""
}
