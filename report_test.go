package main

import (
	"testing"
	"time"
)

// 忽略清单:被忽略且未完成的任务应移出临期/漏交分组、不再触发通知,
// 单独归入 Ignored 组;已完成任务的“忽略”标记不影响其进已完成组。
func TestBuildReportIgnored(t *testing.T) {
	now := time.Now().In(TZ)
	items := []Item{
		{Zy: "zy1", Kind: "homework", ID: 101, Title: "临期作业",
			End: now.Add(2 * time.Hour).Format(time.RFC3339), SC: 0},
		{Zy: "zy1", Kind: "homework", ID: 102, Title: "过期作业",
			End: now.Add(-24 * time.Hour).Format(time.RFC3339), SC: 0},
		{Zy: "zy1", Kind: "homework", ID: 103, Title: "本周作业",
			End: now.Add(5 * 24 * time.Hour).Format(time.RFC3339), SC: 0},
		{Zy: "zy1", Kind: "homework", ID: 104, Title: "已完成作业",
			End: now.Add(2 * time.Hour).Format(time.RFC3339), SC: 3},
	}
	courses := []Course{{ZyID: "zy1", Title: "数据结构"}}

	ignored := map[string]bool{
		ignoreKey("zy1", "101"): true, // 忽略临期
		ignoreKey("zy1", "102"): true, // 忽略过期
		ignoreKey("zy1", "104"): true, // 已完成 + 忽略(应照常进已完成组)
	}
	rep := buildReport(courses, items, nil, 3, ignored)

	if len(rep.Bucket.Urgent) != 0 || len(rep.Bucket.Missed) != 0 {
		t.Fatalf("被忽略的临期/漏交任务不应进入相应分组(urgent=%d missed=%d)",
			len(rep.Bucket.Urgent), len(rep.Bucket.Missed))
	}
	if hasUrgent(rep) {
		t.Fatalf("忽略后不应触发临期通知")
	}
	if len(rep.Bucket.Ignored) != 2 {
		t.Fatalf("Ignored 组应有 2 项(已完成的不算),实际 %d", len(rep.Bucket.Ignored))
	}
	if len(rep.Bucket.Week) != 1 {
		t.Fatalf("普通任务应正常进入 Week 组,实际 %d", len(rep.Bucket.Week))
	}
	if len(rep.Bucket.Done) != 1 {
		t.Fatalf("已完成的被忽略任务应照常进 Done 组,实际 %d", len(rep.Bucket.Done))
	}

	// 对照:不带忽略清单
	rep2 := buildReport(courses, items, nil, 3, nil)
	if len(rep2.Bucket.Urgent) != 1 || len(rep2.Bucket.Missed) != 1 {
		t.Fatalf("未忽略时临期/漏交各应 1 项(urgent=%d missed=%d)",
			len(rep2.Bucket.Urgent), len(rep2.Bucket.Missed))
	}
	if !hasUrgent(rep2) {
		t.Fatalf("未忽略时应触发临期通知")
	}
}
