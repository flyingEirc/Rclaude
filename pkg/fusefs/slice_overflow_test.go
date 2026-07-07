package fusefs

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

// TestSliceReadContentOverflowSafe 保证超大 offset/size 不会因 offset+length 整数回绕
// 导致越界 panic，且返回结果正确截断。
func TestSliceReadContentOverflowSafe(t *testing.T) {
	t.Parallel()
	data := []byte("hello world")

	// size 接近 MaxInt64：offset+int64(size) 会回绕为负，历史上会触发切片越界。
	require.NotPanics(t, func() {
		got := sliceReadContent(data, 6, math.MaxInt32)
		assert.Equal(t, []byte("world"), got)
	})

	// 常规截断仍正确。
	assert.Equal(t, []byte("hel"), sliceReadContent(data, 0, 3))
	// offset 越界返回空。
	assert.Empty(t, sliceReadContent(data, 100, 10))
	// size<=0 读到末尾。
	assert.Equal(t, []byte("world"), sliceReadContent(data, 6, 0))
}

// TestForgetReleasesInode 保证 forget 回收指定路径的 inode 记录（文件/目录两种变体），
// 避免删除路径后 byPath 无界增长。
func TestForgetReleasesInode(t *testing.T) {
	t.Parallel()
	a := newInodeAllocator()

	file := a.stableAttr("alice", "d/x", &remotefsv1.FileInfo{IsDir: false})
	dir := a.stableAttr("alice", "d", &remotefsv1.FileInfo{IsDir: true})
	require.NotZero(t, file.Ino)
	require.NotZero(t, dir.Ino)
	require.Len(t, a.byPath, 2)

	a.forget("alice", "d/x")
	assert.Len(t, a.byPath, 1, "文件路径的 inode 记录应被回收")

	a.forget("alice", "d")
	assert.Empty(t, a.byPath, "目录路径的 inode 记录应被回收")

	// forget 幂等：重复调用与不存在路径均安全。
	require.NotPanics(t, func() {
		a.forget("alice", "d")
		a.forget("bob", "never")
	})
}
