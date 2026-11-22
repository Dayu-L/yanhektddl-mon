package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// server.log 新建时应带 UTF-8 BOM(让 Windows 编辑器正确识别编码),
// 再次以追加模式打开时不得重复写入 BOM。
func TestInitFileLogBOM(t *testing.T) {
	dir := t.TempDir()
	oldBase, oldLogger := base, logger
	defer func() { base, logger = oldBase, oldLogger }()

	closeCurLog := func() {
		if f, ok := logger.Writer().(*os.File); ok {
			f.Close()
		}
	}

	// 第一次:新建文件
	base = dir
	initFileLog()
	logger.Print("第一行")
	closeCurLog()
	if b := logFileForRead(t, dir); !strings.HasPrefix(string(b), "\xEF\xBB\xBF") {
		t.Fatalf("新日志缺少 UTF-8 BOM")
	}

	// 重启场景:文件已有内容,initFileLog 不得再插入 BOM
	initFileLog()
	logger.Print("第二行")
	closeCurLog()
	b := logFileForRead(t, dir)
	if strings.Count(string(b), "\xEF\xBB\xBF") != 1 {
		t.Fatalf("BOM 数量异常(应为 1)")
	}
	if !strings.Contains(string(b), "第二行") {
		t.Fatalf("追加内容丢失")
	}
}

func logFileForRead(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatalf("读取日志失败:%v", err)
	}
	return b
}
