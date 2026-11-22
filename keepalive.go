package main

// 会话保温:zy-eclass 的服务端会话空闲约 30 分钟就失效(比它的持久
// cookie 短得多),失效后靠导航触发 CAS 静默续期,而 CAS 会话同样会
// 过期——两头都凉了就只能人工重登,表现为"有时候刷新会突然弹登录窗"。
//
// 对策:桌面服务常驻期间,每 10 分钟用 headless 浏览器轻量 ping 一次
// /api/todos,让会话始终"温着"。ping 顺带完成两件修复工作:
//   - zy-eclass 会话过期但 CAS 还活着 -> 导航触发静默续期,ping 即自愈;
//   - 连 CAS 也失效 -> ping 探明这一点,提前把状态标成"需要重登",
//     而不是等到每日巡检/手动刷新时才失败。

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

const (
	keepAliveEvery = 10 * time.Minute // 保温周期:明显小于实测的 30 分钟会话空闲上限
)

// profileMu 串行化所有会占用 browser_profile 的流程(登录 / 刷新 / 保温 ping)。
// 同一 profile 同时只能有一个浏览器实例:openBrowser 的清锁逻辑会直接杀掉
// 命令行里带 profile 路径的 Edge,两个流程并发跑会互杀对方的浏览器。
var profileMu sync.Mutex

// keepAliveBusy 当前是否有保温 ping 在跑(退出看门狗用它决定要不要等)
var keepAliveBusy atomic.Bool

// startKeepAlive 启动会话保温循环(仅桌面模式调用)。
// 流程化让路原则:登录向导与手动刷新永远优先,ping 只在空闲期活动,
// 抢不到锁就整轮跳过,绝不排队等待。
func startKeepAlive() {
	go func() {
		tick := time.NewTicker(keepAliveEvery)
		defer tick.Stop()
		failStreak := 0
		for range tick.C {
			// 从未成功抓取过(还没走过登录向导):保温无从谈起
			if _, err := loadSnapshot(); err != nil {
				continue
			}
			// 登录/刷新进行中:让路
			if loginBusy() || refreshRunning() {
				continue
			}
			// 每日巡检(独立进程,同样占用 profile)前后的 3 分钟:
			// 互斥量管不到它,让 ping 避开,防止两边清锁逻辑互杀
			if nearCheckTime() {
				continue
			}
			// 会话已连续失效:降频到约 30 分钟探测一次,减少无谓的浏览器起停
			if failStreak >= 3 && failStreak%3 != 0 {
				continue
			}
			if !profileMu.TryLock() {
				continue
			}
			keepAliveBusy.Store(true)
			ok, needLogin := pingEclass()
			keepAliveBusy.Store(false)
			profileMu.Unlock()

			switch {
			case ok:
				if failStreak > 0 {
					logger.Printf("[保温] 会话已恢复活跃")
				}
				failStreak = 0
				setLoginRequired(false)
			case needLogin:
				failStreak++
				setLoginRequired(true)
				logger.Printf("[保温] 会话与统一身份认证均已失效,需要重新登录(已连续失效 %d 轮)", failStreak)
			default:
				failStreak++ // 浏览器/网络偶发故障,不改变登录状态
			}
		}
	}()
}

// pingEclass 轻量保温:headless 起浏览器 -> 打开乐学首页(若 zy-eclass
// 会话已失效,这次导航顺带触发 CAS 静默续期)-> 调一次 /api/todos。
// 返回 (会话活跃, 需要人工重登):needLogin=true 表示会话连 CAS 静默续期
// 都救不回来;ok=false 且 needLogin=false 则是浏览器/网络层偶发故障。
func pingEclass() (ok, needLogin bool) {
	s, err := openBrowser(false)
	if err != nil {
		logger.Printf("[保温] 浏览器启动失败(本轮跳过):%v", err)
		return false, false
	}
	defer s.close()

	if err := s.navigate(eclassHome, 0); err != nil {
		logger.Printf("[保温] 导航失败(本轮跳过):%v", err)
		return false, false
	}
	// 轮询窗口要盖住 CAS 静默续期的重定向链(与 tryCollect 阶段 2 相同)
	deadline := time.Now().Add(todosWaitTO)
	for time.Now().Before(deadline) {
		out, err := s.eval("(" + jsFetchTodos + ")()")
		if err == nil {
			var res struct {
				OK  bool   `json:"ok"`
				Why string `json:"why"`
			}
			if json.Unmarshal([]byte(out), &res) == nil {
				if res.OK {
					return true, false
				}
				// HTTP 401 / 返回登录页 HTML:会话确已失效且 CAS 续期没救回来
				if res.Why == "HTTP 401" || res.Why == "not-json" {
					return false, true
				}
			}
		}
		// eval 失败多为续期重定向链尚未走完,稍后重试
		time.Sleep(700 * time.Millisecond)
	}
	return false, true
}

// nearCheckTime 当前时刻是否落在每日巡检时间点附近(±3 分钟)。
// 巡检计划任务是独立进程,不经过本进程的 profileMu;若 ping 恰在巡检
// 期间启动,openBrowser 的清锁重试会杀掉巡检正在用的浏览器,导致当日
// 通知抓取失败。让 ping 主动避开这段时间。
func nearCheckTime() bool {
	s := currentSettings()
	if !s.AutoCheck {
		return false
	}
	t, err := time.Parse("15:04", s.CheckTime)
	if err != nil {
		return false
	}
	now := time.Now()
	chk := time.Date(now.Year(), now.Month(), now.Day(),
		t.Hour(), t.Minute(), 0, 0, now.Location())
	d := now.Sub(chk)
	if d < 0 {
		d = -d
	}
	return d <= 3*time.Minute
}
