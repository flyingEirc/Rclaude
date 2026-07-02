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

	first := allocator.stableAttr("alice", "dir", &remotefsv1.FileInfo{IsDir: true})
	second := allocator.stableAttr("alice", "dir", &remotefsv1.FileInfo{IsDir: true})
	otherPath := allocator.stableAttr("alice", "other", &remotefsv1.FileInfo{IsDir: true})
	otherUser := allocator.stableAttr("bob", "dir", &remotefsv1.FileInfo{IsDir: true})

	assert.Equal(t, first, second)
	assert.NotZero(t, first.Ino)
	assert.Equal(t, uint32(syscall.S_IFDIR), first.Mode)
	assert.NotEqual(t, first.Ino, otherPath.Ino)
	assert.NotEqual(t, first.Ino, otherUser.Ino)
}
