package main

import "testing"

// 保温 ping 冒烟测试:真实 headless Edge 走完整链路
// (启动 -> 导航乐学首页 -> 调 /api/todos -> 关闭)。
// 已登录机器上应返回 ok=true;未登录机器上 needLogin=true 也算通过
// (会话与 CAS 均失效属预期,交给登录向导处理);两种环境都能验证
// ping 本身不 panic、浏览器干净退出。go test -run TestPingEclass
func TestPingEclass(t *testing.T) {
	ok, needLogin := pingEclass()
	switch {
	case ok:
		t.Logf("会话活跃,保温 ping 成功")
	case needLogin:
		t.Logf("未登录/会话已彻底失效(本机属预期范围),ping 探测链路正常")
	default:
		t.Fatalf("ping 出现偶发故障(浏览器启动/导航失败),请查看日志")
	}
}
