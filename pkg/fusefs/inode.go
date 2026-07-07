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

// forget 释放某路径的稳定 inode 记录（同时清文件/目录两种 mode 变体），
// 由 Unlink/Rmdir/Rename(old) 在删除成功后调用，避免 byPath 随删除的路径无界增长。
// 非递归：Rmdir 仅作用于空目录、Unlink 作用于文件，均已完整；Rename 一棵非空目录时，
// 其子孙的旧路径条目仍会残留（属既有限制，量级有限）。
func (a *inodeAllocator) forget(userID, relPath string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.byPath, inodeKey{userID: userID, relPath: relPath, mode: syscall.S_IFREG})
	delete(a.byPath, inodeKey{userID: userID, relPath: relPath, mode: syscall.S_IFDIR})
}
