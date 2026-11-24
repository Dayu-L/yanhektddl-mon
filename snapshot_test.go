package main

import (
	"path/filepath"
	"testing"
	"time"
)

// 快路径刷新(/api/todos)只覆盖近期待办。这里验证:
// 浅扫覆盖快照时能把上一轮的其余项(已完成/已过期/远期任务)带回来,
// 而本轮有更新结论的项以本轮为准。
func TestSaveCollectedCarriesOverHistory(t *testing.T) {
	oldBase, oldData, oldProfile := base, dataJSON, profileDir
	dir := t.TempDir()
	base = dir
	dataJSON = filepath.Join(dir, "ddl_data.json")
	profileDir = filepath.Join(dir, "browser_profile")
	loginStatusMu.Lock()
	loginStatusData = nil
	loginStatusMu.Unlock()
	t.Cleanup(func() {
		base, dataJSON, profileDir = oldBase, oldData, oldProfile
		loginStatusMu.Lock()
		loginStatusData = nil
		loginStatusMu.Unlock()
	})

	now := time.Now().In(TZ)
	past := now.Add(-72 * time.Hour).Format(endLayout)
	future := now.Add(72 * time.Hour).Format(endLayout)

	// 上一轮(深扫)快照:已完成 / 已截止未交 / 还没到期的待办(本轮会消失)
	prev := &Snapshot{
		FetchedAt: now.Add(-24 * time.Hour).Format(time.RFC3339),
		DeepAt:    now.Add(-24 * time.Hour).Format(time.RFC3339),
		Items: []Item{
			{Zy: "zy1", Kind: kindHomework, ID: "1", Title: "已完成的作业", End: past, Status: statusDone},
			{Zy: "zy1", Kind: kindHomework, ID: "2", Title: "漏交的作业", End: past, Status: statusUndone},
			{Zy: "zy1", Kind: kindHomework, ID: "3", Title: "消失的待办", End: future, Status: statusUndone},
		},
	}
	if err := saveSnapshot(prev); err != nil {
		t.Fatalf("写上一轮快照失败:%v", err)
	}

	// 本轮快路径:只剩一条新的待办
	res := &collectResult{Courses: []Course{{ID: 1, Title: "课", ZyID: "zy1"}}, Items: []Item{
		{Zy: "zy1", Kind: kindHomework, ID: "4", Title: "新的作业", End: future, SC: 0},
		// 与上一轮同 id:本轮说已完成,不能被上一轮的"未完成"覆盖
		{Zy: "zy1", Kind: kindHomework, ID: "2", Title: "漏交的作业", End: past, Status: statusDone},
	}}
	snap, err := saveCollected(res, false)
	if err != nil {
		t.Fatalf("saveCollected 失败:%v", err)
	}

	got := map[string]string{}
	for _, it := range snap.Items {
		got[it.Title] = it.Status
	}
	if _, ok := got["新的作业"]; !ok {
		t.Fatalf("本轮新项丢了:%+v", got)
	}
	if s, ok := got["已完成的作业"]; !ok || s != statusDone {
		t.Fatalf("已完成的历史项应保留,实际 %q(存在=%v)", s, ok)
	}
	if s, ok := got["漏交的作业"]; !ok || s != statusDone {
		t.Fatalf("本轮有更新结论时应以本轮为准,实际 %q(存在=%v)", s, ok)
	}
	if s, ok := got["消失的待办"]; !ok || s != statusUndone {
		t.Fatalf("快路径没返回的远期待办应沿用上一轮结论(不能被清掉):%+v", got)
	}
	if snap.DeepAt != prev.DeepAt {
		t.Fatalf("快路径不应更新 deepAt,实际 %q", snap.DeepAt)
	}

	// 深度扫描是完整数据,不该再合并旧的
	deepSnap, err := saveCollected(&collectResult{Items: []Item{
		{Zy: "zy1", Kind: kindHomework, ID: "5", Title: "深扫项", End: future, Status: statusDone},
	}}, true)
	if err != nil {
		t.Fatalf("深扫 saveCollected 失败:%v", err)
	}
	if len(deepSnap.Items) != 1 || deepSnap.Items[0].Title != "深扫项" {
		t.Fatalf("深扫应整体覆盖,实际 %+v", deepSnap.Items)
	}
	if deepSnap.DeepAt == prev.DeepAt {
		t.Fatalf("深扫应更新 deepAt")
	}
}
