package fusefs

import (
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

func TestInodeAllocator_StableAttrIsStableAndUniquePerPath(t *testing.T) {
	t.Parallel()

	allocator := newInodeAllocator()

	first := allocator.stableAttr("alice", "proj", "dir", &remotefsv1.FileInfo{IsDir: true})
	second := allocator.stableAttr("alice", "proj", "dir", &remotefsv1.FileInfo{IsDir: true})
	otherPath := allocator.stableAttr("alice", "proj", "other", &remotefsv1.FileInfo{IsDir: true})
	otherUser := allocator.stableAttr("bob", "proj", "dir", &remotefsv1.FileInfo{IsDir: true})
	otherWorkspace := allocator.stableAttr("alice", "proj2", "dir", &remotefsv1.FileInfo{IsDir: true})

	assert.Equal(t, first, second)
	assert.NotZero(t, first.Ino)
	assert.Equal(t, uint32(syscall.S_IFDIR), first.Mode)
	assert.NotEqual(t, first.Ino, otherPath.Ino)
	assert.NotEqual(t, first.Ino, otherUser.Ino)
	assert.NotEqual(t, first.Ino, otherWorkspace.Ino, "同名相对路径在不同项目下必须是不同 inode")
}
