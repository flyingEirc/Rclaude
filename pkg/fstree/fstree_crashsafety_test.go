package fstree_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/fstree"
)

// TestInsertChildUnderExistingFileNoPanic 复现历史 Critical 缺陷：向一个已作为
// 文件存在的节点插入子节点，曾对 nil children map 赋值触发 panic。修复后应静默
// 把父节点升级为目录并正常挂载子项。
func TestInsertChildUnderExistingFileNoPanic(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("a", false, 10)))

	require.NotPanics(t, func() {
		require.NoError(t, tr.Insert(fi("a/b", false, 3)))
	})

	parent, ok := tr.Lookup("a")
	require.True(t, ok)
	assert.True(t, parent.GetIsDir(), "父节点应被升级为目录")

	child, ok := tr.Lookup("a/b")
	require.True(t, ok)
	assert.False(t, child.GetIsDir())

	items, ok := tr.List("a")
	require.True(t, ok)
	require.Len(t, items, 1)
	assert.Equal(t, "a/b", items[0].GetPath())
}

// TestReplaceTreeStyleUnorderedInsertNoPanic 模拟 session.replaceTree 逐条插入一个
// 乱序 / 不一致的 FileTree（先文件 a，再其子项 a/b），全程不得 panic。
func TestReplaceTreeStyleUnorderedInsertNoPanic(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	files := []*remotefsv1.FileInfo{
		fi("a", false, 1),
		fi("a/b", false, 2),
		fi("a/b/c", false, 3),
	}
	require.NotPanics(t, func() {
		for _, f := range files {
			require.NoError(t, tr.Insert(f))
		}
	})

	// 深层子项仍可寻址，且各级祖先均为目录。
	leaf, ok := tr.Lookup("a/b/c")
	require.True(t, ok)
	assert.False(t, leaf.GetIsDir())
	for _, dir := range []string{"a", "a/b"} {
		got, ok := tr.Lookup(dir)
		require.True(t, ok)
		assert.Truef(t, got.GetIsDir(), "%q 应为目录", dir)
	}
}

// TestApplyCreateChildUnderFileNoPanic 走 Apply(CREATE) 分支（daemon 变更事件在
// 陈旧树上到达）验证同一加固。
func TestApplyCreateChildUnderFileNoPanic(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("dir", false, 5)))

	require.NotPanics(t, func() {
		err := tr.Apply(&remotefsv1.FileChange{
			Type: remotefsv1.ChangeType_CHANGE_TYPE_CREATE,
			File: fi("dir/child.txt", false, 7),
		})
		require.NoError(t, err)
	})

	_, ok := tr.Lookup("dir/child.txt")
	assert.True(t, ok)
}

// TestDirOverwrittenByFileDropsSubtree 复现历史 High 缺陷：用同名文件覆盖目录后，
// 旧子树不得成为孤儿（Lookup / Snapshot 不应再命中），且 Delete 应彻底清除。
func TestDirOverwrittenByFileDropsSubtree(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("d", true, 0)))
	require.NoError(t, tr.Insert(fi("d/x", false, 1)))
	require.NoError(t, tr.Insert(fi("d/sub/y", false, 1)))

	// 用文件覆盖目录 d。
	require.NoError(t, tr.Insert(fi("d", false, 42)))

	d, ok := tr.Lookup("d")
	require.True(t, ok)
	assert.False(t, d.GetIsDir())
	assert.Equal(t, int64(42), d.GetSize())

	for _, ghost := range []string{"d/x", "d/sub", "d/sub/y"} {
		_, exists := tr.Lookup(ghost)
		assert.Falsef(t, exists, "%q 不应作为孤儿残留", ghost)
	}

	// Snapshot 只应含 d 一个非根节点。
	snap := tr.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "d", snap[0].GetPath())

	// List(d) 现在是文件，返回 false。
	items, ok := tr.List("d")
	assert.False(t, ok)
	assert.Nil(t, items)

	// Delete 后应无残留。
	tr.Delete("d")
	assert.Empty(t, tr.Snapshot())
}

// TestModifyDirKeepsChildren 反向保证：MODIFY 一个仍为目录的节点不应误删其子项。
func TestModifyDirKeepsChildren(t *testing.T) {
	t.Parallel()
	tr := fstree.New()
	require.NoError(t, tr.Insert(fi("d", true, 0)))
	require.NoError(t, tr.Insert(fi("d/x", false, 1)))

	// 再次以目录写入 d（例如 mtime 变化），子项应保留。
	require.NoError(t, tr.Insert(fi("d", true, 0)))

	_, ok := tr.Lookup("d/x")
	assert.True(t, ok, "目录被目录覆盖时子项应保留")
}
