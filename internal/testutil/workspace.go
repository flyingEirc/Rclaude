package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// NewTempWorkspace 在 tb.TempDir() 下按 layout 描述构造一个工作区并返回根目录绝对路径。
// layout 的 key 是相对 forward-slash 路径；以 "/" 结尾的 key 表示要建空目录，
// 否则 value 被写进对应文件，缺失的父目录会自动创建。
func NewTempWorkspace(tb testing.TB, layout map[string]string) string {
	tb.Helper()
	root := tb.TempDir()
	for rel, content := range layout {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(abs, 0o750); err != nil {
				tb.Fatalf("testutil: mkdir %q: %v", abs, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
			tb.Fatalf("testutil: mkdir parent %q: %v", filepath.Dir(abs), err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
			tb.Fatalf("testutil: write %q: %v", abs, err)
		}
	}
	return root
}
