package syncer

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

func TestScan_EmptyRoot(t *testing.T) {
	root := t.TempDir()
	got, err := Scan(ScanOptions{Root: root})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestScan_FilesAndDirs(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.txt"), "hello")
	mustWriteFile(t, filepath.Join(root, "dir", "b.txt"), "world")

	got, err := Scan(ScanOptions{Root: root})
	require.NoError(t, err)

	paths := collectScanPaths(got)
	assert.ElementsMatch(t, []string{"a.txt", "dir", "dir/b.txt"}, paths)
}

func TestScan_PathsUseForwardSlash(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "d", "e", "f.txt"), "")

	got, err := Scan(ScanOptions{Root: root})
	require.NoError(t, err)
	for _, f := range got {
		assert.NotContains(t, f.GetPath(), "\\",
			"path %q should be forward-slash", f.GetPath())
	}
}

func TestScan_ExcludeBasenamePattern(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "keep.txt"), "")
	mustWriteFile(t, filepath.Join(root, "node_modules", "pkg", "x.js"), "")
	mustWriteFile(t, filepath.Join(root, "sub", "node_modules", "y.js"), "")

	got, err := Scan(ScanOptions{
		Root:     root,
		Excludes: []string{"node_modules"},
	})
	require.NoError(t, err)

	paths := collectScanPaths(got)
	assert.Contains(t, paths, "keep.txt")
	for _, p := range paths {
		assert.NotContains(t, p, "node_modules",
			"path %q should have been excluded", p)
	}
}

func TestScan_ExcludeGlobPattern(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.exe"), "")
	mustWriteFile(t, filepath.Join(root, "src", "b.exe"), "")
	mustWriteFile(t, filepath.Join(root, "src", "b.go"), "")

	got, err := Scan(ScanOptions{
		Root:     root,
		Excludes: []string{"*.exe"},
	})
	require.NoError(t, err)

	paths := collectScanPaths(got)
	assert.NotContains(t, paths, "a.exe")
	assert.NotContains(t, paths, "src/b.exe")
	assert.Contains(t, paths, "src/b.go")
}

func TestScan_SensitiveBuiltinsAndCustomPatterns(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, ".env"), "topsecret")
	mustWriteFile(t, filepath.Join(root, ".ssh", "id_ed25519"), "secret-key")
	mustWriteFile(t, filepath.Join(root, "secrets", "api.txt"), "classified")
	mustWriteFile(t, filepath.Join(root, "visible.txt"), "ok")

	filter, err := NewSensitiveFilter([]string{"secrets/**"})
	require.NoError(t, err)

	got, err := Scan(ScanOptions{
		Root:            root,
		SensitiveFilter: filter,
	})
	require.NoError(t, err)

	paths := collectScanPaths(got)
	assert.Contains(t, paths, "visible.txt")
	assert.NotContains(t, paths, ".env")
	assert.NotContains(t, paths, ".ssh/id_ed25519")
	assert.NotContains(t, paths, "secrets")
	assert.NotContains(t, paths, "secrets/api.txt")
}

func TestScan_ExcludeRootedPattern(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "build", "out.bin"), "")
	mustWriteFile(t, filepath.Join(root, "src", "build", "out.bin"), "")

	got, err := Scan(ScanOptions{
		Root:     root,
		Excludes: []string{"build/**"},
	})
	require.NoError(t, err)

	paths := collectScanPaths(got)
	// 根一级的 build 目录本身未被模式匹配（build 与 build/** 不相等），但其内容被过滤。
	assert.NotContains(t, paths, "build/out.bin")
	// 子目录下同名 build 不受 rooted 模式影响。
	assert.Contains(t, paths, "src/build")
	assert.Contains(t, paths, "src/build/out.bin")
}

func TestScan_NonAbsRoot(t *testing.T) {
	_, err := Scan(ScanOptions{Root: "relative/path"})
	assert.ErrorIs(t, err, ErrRootNotAbsolute)
}

func TestScan_RootIsFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "only.txt")
	mustWriteFile(t, file, "")
	_, err := Scan(ScanOptions{Root: file})
	assert.ErrorIs(t, err, ErrRootNotDirectory)
}

func TestScan_RootMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := Scan(ScanOptions{Root: missing})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrRootNotAbsolute)
}

func TestScan_Mode(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	mustWriteFile(t, file, "")
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Chmod(file, 0o600)) //nolint:gosec // 测试写权限
	}

	got, err := Scan(ScanOptions{Root: root})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.False(t, got[0].GetIsDir())
	assert.Equal(t, int64(0), got[0].GetSize())
	if runtime.GOOS != "windows" {
		assert.Equal(t, uint32(0o600), got[0].GetMode())
	}
}

// mustWriteFile 写文件并自动建立父目录，仅供本文件的测试使用。
func mustWriteFile(t *testing.T, p, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
}

// collectScanPaths 把 FileInfo 列表映射成 path 切片，供 ElementsMatch。
func collectScanPaths(files []*remotefsv1.FileInfo) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.GetPath())
	}
	return out
}
