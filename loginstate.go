package main

// 数据源登录态:最近一次「已知」结论(登录向导 / 单源登录 / 手动刷新 / 每日巡检 /
// 保温 ping 都会更新),落盘 login_status.json。设置页显示这份状态,并提供两个动作:
//   · 检查登录状态:headless 起浏览器逐个探测三路会话(不弹窗口、不跳页面);
//   · 单源登录:对查出未登录的那一路,单独开一个可见窗口等用户登录。
//
// 记录刻意只存布尔 + 时间 + 原因,不存任何账号/会话内容。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
	loginCheckBusy  atomic.Bool     // 探测三路会话进行中
	loginSourceBusy atomic.Bool     // 单源登录窗口等待中
	loginSourceKey  string          // 正在登录的数据源(仅 loginStatusMu 下读写)
	loginMsg        string          // 给设置页的提示(仅 loginStatusMu 下读写)
	loginMsgAt      string          // 提示产生的时间,过旧的提示页面会忽略
)

func loginStatusPath() string { return filepath.Join(base, "login_status.json") }

func sourceName(key string) string {
	switch key {
	case sourceEclass:
		return "乐学"
	case sourceAita:
		return "延河课堂"
	case sourceZhs:
		return "智慧树"
	}
	return key
}

func isKnownSource(key string) bool {
	switch key {
	case sourceEclass, sourceAita, sourceZhs:
		return true
	}
	return false
}

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

func setLoginMessage(msg string) {
	loginStatusMu.Lock()
	loginMsg = msg
	loginMsgAt = time.Now().In(TZ).Format(time.RFC3339)
	loginStatusMu.Unlock()
}

// recordSource 记下一次结论(ok + 原因)。写盘失败只记日志:
// 登录态只是展示用,不能因为它影响抓取主流程。
func recordSource(key string, ok bool, why string) {
	if !isKnownSource(key) {
		return
	}
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

// loginStatusSnapshot 设置页用:三路最近结论 + 三个动作的进行状态 + 提示。
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
		"source_login":     loginSourceBusy.Load(),
		"source_login_key": loginSourceKey,
		"message":          loginMsg,
		"message_at":       loginMsgAt,
	}
}

// startLoginCheck 异步探测三路会话;已有检查/登录在进行时返回 false。
func startLoginCheck() bool {
	if loginSourceBusy.Load() {
		setLoginMessage("正在登录某个平台,等它结束再检查")
		return false
	}
	if loginBusy() || refreshRunning() {
		setLoginMessage("正在抓取数据或登录中,等它结束再检查")
		return false
	}
	if !loginCheckBusy.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		defer loginCheckBusy.Store(false)
		setLoginMessage(checkLoginSources())
	}()
	return true
}

// startSourceLogin 异步为单个数据源开可见窗口等登录。
// 返回 (是否已启动, 拒绝原因)。
func startSourceLogin(key string) (bool, string) {
	if !isKnownSource(key) {
		return false, "未知的数据源"
	}
	if loginCheckBusy.Load() {
		return false, "正在检查登录状态,请等检查结束"
	}
	if loginBusy() || refreshRunning() {
		return false, "正在抓取数据或登录中,等它结束再试"
	}
	if !loginSourceBusy.CompareAndSwap(false, true) {
		return false, "已有登录窗口在等待,请先完成或关掉它"
	}
	loginStatusMu.Lock()
	loginSourceKey = key
	loginMsg = "正在打开「" + sourceName(key) + "」登录窗口…"
	loginMsgAt = time.Now().In(TZ).Format(time.RFC3339)
	loginStatusMu.Unlock()

	go func() {
		needRefresh := false
		func() {
			defer func() {
				loginStatusMu.Lock()
				loginSourceKey = ""
				loginStatusMu.Unlock()
				loginSourceBusy.Store(false)
			}()
			needRefresh = runSourceLogin(key)
		}()
		if needRefresh {
			// 浏览器已关、锁已放:用正常的后台刷新把新数据取回来
			runRefresh(refreshDays)
		}
	}()
	return true, ""
}

// checkLoginSources 真起一次 headless Edge,依次探测乐学 / 延河课堂 / 智慧树。
// 全程不开可见窗口、不打断用户:返回给设置页的提示(空 = 正常完成)。
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

// runSourceLogin 单源登录:开可见窗口 -> 等用户登录 -> 记结论。
// 返回是否需要接着跑一次后台刷新(乐学在函数内就抓完了,可选源交给刷新)。
func runSourceLogin(key string) (needRefresh bool) {
	if !profileMu.TryLock() {
		setLoginMessage("正在抓取数据,等它结束再点登录")
		return false
	}
	defer profileMu.Unlock()

	setLoginMessage("已弹出 Edge 窗口,请在窗口中完成「" + sourceName(key) + "」登录")
	s, err := openBrowser(true)
	if err != nil {
		recordSource(key, false, "浏览器启动失败:"+err.Error())
		setLoginMessage("浏览器启动失败:" + err.Error())
		return false
	}
	defer s.close()

	var ok bool
	switch key {
	case sourceEclass:
		ok = waitLoginEclass(s, loginWait)
	case sourceAita:
		ok = waitLoginAita(s, aitaLoginTO)
	case sourceZhs:
		ok = waitLoginZhihuishu(s, zhsLoginTO)
	}

	if !ok {
		recordSource(key, false, "等待登录超时,可以再点一次登录")
		setLoginMessage("没有等到「" + sourceName(key) + "」登录完成,已放弃本次登录")
		return false
	}
	recordSource(key, true, "")
	logger.Printf("[登录] %s 登录成功(单源)", sourceName(key))

	if key == sourceEclass {
		// 乐学是必需数据源:登录后立刻抓一次(与刷新同一条路径)
		setLoginRequired(false)
		deep := needDeepScan()
		res, cerr := tryCollect(s, deep)
		if cerr != nil {
			logger.Printf("[登录] 登录成功但抓取失败:%v", cerr)
			setLoginMessage("乐学登录成功,但抓取失败:" + cerr.Error())
			return false
		}
		snap, serr := saveCollected(res, deep)
		if serr != nil {
			logger.Printf("[登录] 保存快照失败:%v", serr)
			setLoginMessage("乐学登录成功,但保存数据失败:" + serr.Error())
			return false
		}
		writeReportsFromSnapshot(snap, refreshDays)
		syncAutoCheck()
		setLoginMessage("乐学登录成功,已抓取 " + strconv.Itoa(len(snap.Courses)) + " 门课、" +
			strconv.Itoa(len(snap.Items)) + " 条任务")
		return false
	}

	if key == sourceZhs {
		// 立刻数一次智慧课程,给用户一个准信(免得"登录成功但什么都没有")
		if n, cerr := zhsCourseCount(s); cerr == nil {
			setLoginMessage("「智慧树」登录成功,检测到 " + strconv.Itoa(n) + " 门智慧课程,正在刷新数据…")
		} else {
			logger.Printf("[登录] 智慧树课程列表读取失败:%v", cerr)
			setLoginMessage("「智慧树」登录成功,正在刷新数据…")
		}
	} else {
		setLoginMessage("「" + sourceName(key) + "」登录成功,正在刷新数据…")
	}
	return true
}

// zhsCourseCount 登录成功后数一次智慧课程,仅用于给设置页一句准信。
func zhsCourseCount(s *browserSession) (int, error) {
	if err := s.navigate(zhsAICourseOrigin+"/", 0); err != nil {
		return 0, err
	}
	secret, err := zhsSecret()
	if err != nil {
		return 0, err
	}
	courses, err := evalZhsAICourses(s, secret)
	if err != nil {
		return 0, err
	}
	return len(courses), nil
}
