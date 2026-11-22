package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestUIDataDump 仅供前端视觉验收:把 /api/data 的真实载荷导出为
// .taste-skill/uidata.json,配合 scripts/make-preview.mjs 生成静态预览页。
// 日常测试不需要快照,没有数据时自动跳过。
func TestUIDataDump(t *testing.T) {
	if _, err := loadSnapshot(); err != nil {
		t.Skipf("本地没有快照,跳过 UI 数据导出:%v", err)
	}
	data, err := apiData()
	if err != nil {
		t.Fatalf("apiData 失败:%v", err)
	}
	j, err := marshalNoEscape(data)
	if err != nil {
		t.Fatalf("序列化失败:%v", err)
	}
	dir := ".taste-skill"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建目录失败:%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uidata.json"), j, 0o644); err != nil {
		t.Fatalf("写入失败:%v", err)
	}
	t.Logf("已导出 .taste-skill/uidata.json")
}
