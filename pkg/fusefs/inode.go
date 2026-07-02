package fusefs

import (
	"sync"
	"syscall"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

const firstWorkspaceInode uint64 = 2

type inodeAllocator struct {
	mu     sync.Mutex
	next   uint64
	byPath map[inodeKey]uint64
}

type inodeKey struct {
	userID  string
	relPath string
	mode    uint32
}

type stableAttr struct {
	Mode uint32
	Ino  uint64
	Gen  uint64
}

func newInodeAllocator() *inodeAllocator {
	return &inodeAllocator{
		next:   firstWorkspaceInode,
		byPath: make(map[inodeKey]uint64),
	}
}

func (a *inodeAllocator) stableAttr(userID, relPath string, info *remotefsv1.FileInfo) stableAttr {
	mode := stableMode(info)
	attr := stableAttr{Mode: mode}
	if a == nil {
		return attr
	}

	key := inodeKey{userID: userID, relPath: relPath, mode: mode}

	a.mu.Lock()
	defer a.mu.Unlock()

	ino, ok := a.byPath[key]
	if !ok {
		ino = a.next
		a.next++
		a.byPath[key] = ino
	}
	attr.Ino = ino
	return attr
}

func stableMode(info *remotefsv1.FileInfo) uint32 {
	if info != nil && info.GetIsDir() {
		return syscall.S_IFDIR
	}
	return syscall.S_IFREG
}
