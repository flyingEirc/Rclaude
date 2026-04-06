package fstree_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/fstree"
)

// fi 是构造 *FileInfo 的简便 helper。
func fi(p string, isDir bool, size int64) *remotefsv1.FileInfo {
	return &remotefsv1.FileInfo{
		Path:    p,
		Size:    size,
		IsDir:   isDir,
		ModTime: 1700000000,
		Mode:    0o644,
	}
}

func TestNewHasRoot(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	got, ok := tr.Lookup("")
	require.True(t, ok)
	require.NotNil(t, got)
	assert.True(t, got.GetIsDir())
	assert.Equal(t, "", got.GetPath())
}

func TestInsertNilReturnsError(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	err := tr.Insert(nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, fstree.ErrNilInfo))
}

func TestInsertFileAndLookup(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("a/b/c.txt", false, 42)))

	got, ok := tr.Lookup("a/b/c.txt")
	require.True(t, ok)
	assert.False(t, got.GetIsDir())
	assert.Equal(t, int64(42), got.GetSize())

	// 父目录被自动补齐为占位目录。
	parent, ok := tr.Lookup("a/b")
	require.True(t, ok)
	assert.True(t, parent.GetIsDir())

	grand, ok := tr.Lookup("a")
	require.True(t, ok)
	assert.True(t, grand.GetIsDir())
}

func TestInsertUpdatesExisting(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("doc.md", false, 10)))
	require.NoError(t, tr.Insert(fi("doc.md", false, 99)))

	got, ok := tr.Lookup("doc.md")
	require.True(t, ok)
	assert.Equal(t, int64(99), got.GetSize())
}

func TestInsertNormalizesPath(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	// 反斜杠 + 重复斜杠 + ./ 都应被规范化为 a/b.txt。
	require.NoError(t, tr.Insert(fi("a\\b.txt", false, 1)))

	got, ok := tr.Lookup("a/b.txt")
	require.True(t, ok)
	assert.Equal(t, "a/b.txt", got.GetPath())
}

func TestInsertRootIsNoop(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	// path == "" 不应覆盖根。
	require.NoError(t, tr.Insert(fi("", true, 0)))
	root, ok := tr.Lookup("")
	require.True(t, ok)
	assert.True(t, root.GetIsDir())
}

func TestDeleteFile(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("dir/x.go", false, 1)))
	require.NoError(t, tr.Insert(fi("dir/y.go", false, 1)))

	tr.Delete("dir/x.go")

	_, ok := tr.Lookup("dir/x.go")
	assert.False(t, ok)

	// 兄弟节点保留。
	_, ok = tr.Lookup("dir/y.go")
	assert.True(t, ok)

	// 父目录保留。
	_, ok = tr.Lookup("dir")
	assert.True(t, ok)
}

func TestDeleteDirectoryRecursive(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("a/b/c/leaf.txt", false, 1)))
	require.NoError(t, tr.Insert(fi("a/b/c/other.txt", false, 1)))
	require.NoError(t, tr.Insert(fi("a/sibling.txt", false, 1)))

	tr.Delete("a/b")

	for _, p := range []string{"a/b", "a/b/c", "a/b/c/leaf.txt", "a/b/c/other.txt"} {
		_, ok := tr.Lookup(p)
		assert.Falsef(t, ok, "expected %q to be deleted", p)
	}

	// 兄弟分支不受影响。
	_, ok := tr.Lookup("a/sibling.txt")
	assert.True(t, ok)
	_, ok = tr.Lookup("a")
	assert.True(t, ok)
}

func TestDeleteRootIsNoop(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("a.txt", false, 1)))
	tr.Delete("")
	tr.Delete("/")

	_, ok := tr.Lookup("")
	assert.True(t, ok)
	_, ok = tr.Lookup("a.txt")
	assert.True(t, ok)
}

func TestDeleteMissingPathIsNoop(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	tr.Delete("nope/never")
	// 不应 panic、根仍在。
	_, ok := tr.Lookup("")
	assert.True(t, ok)
}

func TestLookupMiss(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	got, ok := tr.Lookup("missing.txt")
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestLookupReturnsCopy(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("a.txt", false, 5)))

	got, ok := tr.Lookup("a.txt")
	require.True(t, ok)

	// 修改返回值不应影响内部存储。
	got.Size = 9999
	again, ok := tr.Lookup("a.txt")
	require.True(t, ok)
	assert.Equal(t, int64(5), again.GetSize())
}

func TestListRoot(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("a.txt", false, 1)))
	require.NoError(t, tr.Insert(fi("b/c.txt", false, 1)))

	items, ok := tr.List("")
	require.True(t, ok)
	require.Len(t, items, 2)

	names := map[string]bool{}
	for _, it := range items {
		names[it.GetPath()] = true
	}
	assert.True(t, names["a.txt"])
	assert.True(t, names["b"])
}

func TestListSubDir(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("dir/x.go", false, 1)))
	require.NoError(t, tr.Insert(fi("dir/y.go", false, 1)))
	require.NoError(t, tr.Insert(fi("other/z.go", false, 1)))

	items, ok := tr.List("dir")
	require.True(t, ok)
	require.Len(t, items, 2)
}

func TestListEmptyDir(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("emptydir", true, 0)))

	items, ok := tr.List("emptydir")
	require.True(t, ok)
	assert.Empty(t, items)
}

func TestListMissingPath(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	items, ok := tr.List("does/not/exist")
	assert.False(t, ok)
	assert.Nil(t, items)
}

func TestListOnFileReturnsFalse(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("file.txt", false, 1)))

	items, ok := tr.List("file.txt")
	assert.False(t, ok)
	assert.Nil(t, items)
}

func TestApplyNilReturnsError(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	err := tr.Apply(nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, fstree.ErrNilChange))
}

func TestApplyCreate(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	err := tr.Apply(&remotefsv1.FileChange{
		Type: remotefsv1.ChangeType_CHANGE_TYPE_CREATE,
		File: fi("new.txt", false, 1),
	})
	require.NoError(t, err)

	_, ok := tr.Lookup("new.txt")
	assert.True(t, ok)
}

func TestApplyModify(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("doc.md", false, 1)))

	err := tr.Apply(&remotefsv1.FileChange{
		Type: remotefsv1.ChangeType_CHANGE_TYPE_MODIFY,
		File: fi("doc.md", false, 88),
	})
	require.NoError(t, err)

	got, ok := tr.Lookup("doc.md")
	require.True(t, ok)
	assert.Equal(t, int64(88), got.GetSize())
}

func TestApplyDelete(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("gone.txt", false, 1)))

	err := tr.Apply(&remotefsv1.FileChange{
		Type: remotefsv1.ChangeType_CHANGE_TYPE_DELETE,
		File: fi("gone.txt", false, 0),
	})
	require.NoError(t, err)

	_, ok := tr.Lookup("gone.txt")
	assert.False(t, ok)
}

func TestApplyRename(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("old.txt", false, 7)))

	err := tr.Apply(&remotefsv1.FileChange{
		Type:    remotefsv1.ChangeType_CHANGE_TYPE_RENAME,
		OldPath: "old.txt",
		File:    fi("new.txt", false, 7),
	})
	require.NoError(t, err)

	_, ok := tr.Lookup("old.txt")
	assert.False(t, ok)

	got, ok := tr.Lookup("new.txt")
	require.True(t, ok)
	assert.Equal(t, int64(7), got.GetSize())
}

func TestApplyUnspecifiedReturnsError(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	err := tr.Apply(&remotefsv1.FileChange{
		Type: remotefsv1.ChangeType_CHANGE_TYPE_UNSPECIFIED,
		File: fi("x.txt", false, 1),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, fstree.ErrUnknownChangeType))
}

func TestSnapshotCount(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("a/b.txt", false, 1)))
	require.NoError(t, tr.Insert(fi("c.txt", false, 1)))

	// snapshot 包含 a (auto) + a/b.txt + c.txt = 3 个非根节点。
	snap := tr.Snapshot()
	assert.Len(t, snap, 3)
}

func TestSnapshotIsolated(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("a.txt", false, 5)))

	snap := tr.Snapshot()
	require.Len(t, snap, 1)

	// 修改 snapshot 内容不应影响后续 Lookup 结果。
	snap[0].Size = 9999
	got, ok := tr.Lookup("a.txt")
	require.True(t, ok)
	assert.Equal(t, int64(5), got.GetSize())
}

// TestConcurrentMixed 用 1000 个 goroutine 混合读写以验证不会 panic / deadlock。
// Windows 本地无 -race（见 测试错误.md E001），Linux/CI 仍保留 race 覆盖。
func TestConcurrentMixed(t *testing.T) {
	t.Parallel()
	tr := fstree.New()

	const n = 1000
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			path := "concurrent/file"
			switch i % 5 {
			case 0:
				//nolint:errcheck,gosec // concurrent insert may race; only verifying no panic/deadlock
				tr.Insert(fi(path, false, int64(i)))
			case 1:
				tr.Lookup(path)
			case 2:
				tr.List("concurrent")
			case 3:
				tr.Delete(path)
			case 4:
				tr.Snapshot()
			}
		}(i)
	}
	wg.Wait()
}
