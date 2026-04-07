//go:build linux

package fusefs

import (
	"context"
	"errors"
	"fmt"
	pathpkg "path"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/session"
)

type mountedFS struct {
	server *fuse.Server
	once   sync.Once
}

func Mount(ctx context.Context, opts Options) (Mounted, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	opts = defaultedOptions(opts)

	root := &rootNode{manager: opts.Manager}

	server, err := fs.Mount(opts.Mountpoint, root, &fs.Options{
		MountOptions: fuse.MountOptions{
			FsName:             "rclaude",
			Name:               "rclaude",
			DisableXAttrs:      true,
			DisableReadDirPlus: true,
		},
		EntryTimeout:    durationPtr(opts.EntryTimeout),
		AttrTimeout:     durationPtr(opts.AttrTimeout),
		NegativeTimeout: durationPtr(opts.NegativeTimeout),
	})
	if err != nil {
		return nil, fmt.Errorf("fusefs: mount %q: %w", opts.Mountpoint, err)
	}
	if err := server.WaitMount(); err != nil {
		if cleanupErr := server.Unmount(); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("unmount after failed mount: %w", cleanupErr))
		}
		return nil, fmt.Errorf("fusefs: wait mount %q: %w", opts.Mountpoint, err)
	}

	mounted := &mountedFS{server: server}
	go func() {
		<-ctx.Done()
		if err := mounted.Close(); err != nil {
			return
		}
	}()

	return mounted, nil
}

func (m *mountedFS) Close() error {
	var err error
	m.once.Do(func() {
		err = m.server.Unmount()
	})
	return err
}

type rootNode struct {
	fs.Inode
	manager *session.Manager
}

type workspaceNode struct {
	fs.Inode
	manager *session.Manager
	userID  string
	relPath string
}

var (
	_ = (fs.NodeGetattrer)((*rootNode)(nil))
	_ = (fs.NodeLookuper)((*rootNode)(nil))
	_ = (fs.NodeReaddirer)((*rootNode)(nil))
	_ = (fs.NodeGetattrer)((*workspaceNode)(nil))
	_ = (fs.NodeLookuper)((*workspaceNode)(nil))
	_ = (fs.NodeReaddirer)((*workspaceNode)(nil))
	_ = (fs.NodeOpener)((*workspaceNode)(nil))
	_ = (fs.NodeReader)((*workspaceNode)(nil))
	_ = (fs.NodeWriter)((*workspaceNode)(nil))
	_ = (fs.NodeCreater)((*workspaceNode)(nil))
	_ = (fs.NodeMkdirer)((*workspaceNode)(nil))
	_ = (fs.NodeUnlinker)((*workspaceNode)(nil))
	_ = (fs.NodeRmdirer)((*workspaceNode)(nil))
	_ = (fs.NodeRenamer)((*workspaceNode)(nil))
	_ = (fs.NodeSetattrer)((*workspaceNode)(nil))
)

func (n *rootNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFDIR | 0o555
	out.Mtime = uint64(time.Now().Unix())
	return 0
}

func (n *rootNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	info, err := lookupInfo(n.manager, name, "")
	if err != nil {
		return nil, errnoFromError(err)
	}
	fillEntryOut(out, info)
	child := &workspaceNode{manager: n.manager, userID: name, relPath: ""}
	return n.NewInode(ctx, child, fs.StableAttr{Mode: stableMode(info)}), 0
}

func (n *rootNode) Readdir(_ context.Context) (fs.DirStream, syscall.Errno) {
	userIDs := n.manager.UserIDs()
	entries := make([]fuse.DirEntry, 0, len(userIDs))
	for _, userID := range userIDs {
		entries = append(entries, fuse.DirEntry{Name: userID, Mode: syscall.S_IFDIR})
	}
	return fs.NewListDirStream(entries), 0
}

func (n *workspaceNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	info, err := lookupInfo(n.manager, n.userID, n.relPath)
	if err != nil {
		return errnoFromError(err)
	}
	fillAttrOut(out, info)
	return 0
}

func (n *workspaceNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	nextPath := childPath(n.relPath, name)
	info, err := lookupInfo(n.manager, n.userID, nextPath)
	if err != nil {
		return nil, errnoFromError(err)
	}
	fillEntryOut(out, info)
	child := &workspaceNode{manager: n.manager, userID: n.userID, relPath: nextPath}
	return n.NewInode(ctx, child, fs.StableAttr{Mode: stableMode(info)}), 0
}

func (n *workspaceNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	infos, err := listInfos(n.manager, n.userID, n.relPath)
	if err != nil {
		return nil, errnoFromError(err)
	}
	startPrefetch(ctx, n.manager, n.userID, infos)
	sort.Slice(infos, func(i, j int) bool {
		return baseName(infos[i].GetPath()) < baseName(infos[j].GetPath())
	})

	entries := make([]fuse.DirEntry, 0, len(infos))
	for _, info := range infos {
		entries = append(entries, fuse.DirEntry{
			Name: baseName(info.GetPath()),
			Mode: stableMode(info),
		})
	}
	return fs.NewListDirStream(entries), 0
}

func (n *workspaceNode) Open(_ context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	info, err := lookupInfo(n.manager, n.userID, n.relPath)
	if err != nil {
		return nil, 0, errnoFromError(err)
	}
	if info.GetIsDir() {
		return nil, 0, syscall.EISDIR
	}
	return nil, 0, 0
}

func (n *workspaceNode) Read(ctx context.Context, _ fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	data, err := readChunk(ctx, n.manager, n.userID, n.relPath, off, len(dest))
	if err != nil {
		return nil, errnoFromError(err)
	}
	return fuse.ReadResultData(data), 0
}

func (n *workspaceNode) Write(ctx context.Context, _ fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	if err := writeChunk(ctx, n.manager, n.userID, n.relPath, off, data); err != nil {
		return 0, errnoFromError(err)
	}
	return uint32(len(data)), 0
}

func (n *workspaceNode) Create(
	ctx context.Context,
	name string,
	_ uint32,
	_ uint32,
	out *fuse.EntryOut,
) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	childRel := childPath(n.relPath, name)
	if err := createFile(ctx, n.manager, n.userID, childRel); err != nil {
		return nil, nil, 0, errnoFromError(err)
	}
	info, err := lookupInfo(n.manager, n.userID, childRel)
	if err != nil {
		return nil, nil, 0, errnoFromError(err)
	}
	fillEntryOut(out, info)
	child := &workspaceNode{manager: n.manager, userID: n.userID, relPath: childRel}
	inode := n.NewInode(ctx, child, fs.StableAttr{Mode: stableMode(info)})
	return inode, nil, 0, 0
}

func (n *workspaceNode) Mkdir(ctx context.Context, name string, _ uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	childRel := childPath(n.relPath, name)
	if err := mkdirAt(ctx, n.manager, n.userID, childRel, false); err != nil {
		return nil, errnoFromError(err)
	}
	info, err := lookupInfo(n.manager, n.userID, childRel)
	if err != nil {
		return nil, errnoFromError(err)
	}
	fillEntryOut(out, info)
	child := &workspaceNode{manager: n.manager, userID: n.userID, relPath: childRel}
	return n.NewInode(ctx, child, fs.StableAttr{Mode: stableMode(info)}), 0
}

func (n *workspaceNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if err := removePath(ctx, n.manager, n.userID, childPath(n.relPath, name)); err != nil {
		return errnoFromError(err)
	}
	return 0
}

func (n *workspaceNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	if err := removePath(ctx, n.manager, n.userID, childPath(n.relPath, name)); err != nil {
		return errnoFromError(err)
	}
	return 0
}

func (n *workspaceNode) Rename(
	ctx context.Context,
	oldName string,
	newParent fs.InodeEmbedder,
	newName string,
	_ uint32,
) syscall.Errno {
	target, ok := newParent.(*workspaceNode)
	if !ok {
		return syscall.EINVAL
	}
	if target.userID != n.userID {
		return syscall.EXDEV
	}

	oldRel := childPath(n.relPath, oldName)
	newRel := childPath(target.relPath, newName)
	if err := renamePath(ctx, n.manager, n.userID, oldRel, newRel); err != nil {
		return errnoFromError(err)
	}
	return 0
}

func (n *workspaceNode) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if in.Valid&fuse.FATTR_SIZE != 0 {
		if err := truncatePath(ctx, n.manager, n.userID, n.relPath, int64(in.Size)); err != nil {
			return errnoFromError(err)
		}
	}
	return n.Getattr(ctx, fh, out)
}

func fillEntryOut(out *fuse.EntryOut, info *remotefsv1.FileInfo) {
	fillAttr(&out.Attr, info)
}

func fillAttrOut(out *fuse.AttrOut, info *remotefsv1.FileInfo) {
	fillAttr(&out.Attr, info)
}

func fillAttr(out *fuse.Attr, info *remotefsv1.FileInfo) {
	now := time.Now()
	modTime := time.Unix(info.GetModTime(), 0)
	if info.GetModTime() == 0 {
		modTime = now
	}
	out.Mode = visibleMode(info)
	out.Size = uint64(maxInt64(info.GetSize(), 0))
	out.Nlink = 1
	out.Mtime = uint64(modTime.Unix())
	out.Ctime = uint64(modTime.Unix())
	out.Atime = uint64(now.Unix())
}

func durationPtr(v time.Duration) *time.Duration {
	if v <= 0 {
		return nil
	}
	return &v
}

func maxInt64(v, floor int64) int64 {
	if v < floor {
		return floor
	}
	return v
}

func childPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

func baseName(relPath string) string {
	return pathpkg.Base(relPath)
}

func stableMode(info *remotefsv1.FileInfo) uint32 {
	if info != nil && info.GetIsDir() {
		return syscall.S_IFDIR
	}
	return syscall.S_IFREG
}

func visibleMode(info *remotefsv1.FileInfo) uint32 {
	if info == nil {
		return syscall.S_IFREG | 0o444
	}
	perms := info.GetMode()
	if perms == 0 {
		if info.GetIsDir() {
			perms = 0o555
		} else {
			perms = 0o444
		}
	}
	return stableMode(info) | perms
}

type errnoRule struct {
	errno syscall.Errno
	match func(error) bool
}

var errnoRules = []errnoRule{
	{errno: syscall.EINTR, match: func(err error) bool { return errors.Is(err, context.Canceled) }},
	{errno: syscall.ETIMEDOUT, match: func(err error) bool { return isAnyError(err, ErrRequestTimeout, context.DeadlineExceeded) }},
	{errno: syscall.EIO, match: func(err error) bool { return isAnyError(err, ErrSessionOffline, ErrSessionFailed) }},
	{errno: syscall.ENOENT, match: func(err error) bool { return errors.Is(err, ErrPathNotFound) }},
	{errno: syscall.EACCES, match: func(err error) bool { return errors.Is(err, ErrPermissionDenied) }},
	{errno: syscall.EEXIST, match: func(err error) bool { return errors.Is(err, ErrAlreadyExists) }},
	{errno: syscall.ENOTDIR, match: func(err error) bool { return errors.Is(err, ErrNotDirectory) }},
	{errno: syscall.EISDIR, match: func(err error) bool { return errors.Is(err, ErrIsDirectory) }},
	{errno: syscall.ENOTEMPTY, match: func(err error) bool { return errors.Is(err, ErrDirectoryNotEmpty) }},
	{errno: syscall.EXDEV, match: func(err error) bool { return errors.Is(err, ErrCrossDevice) }},
	{errno: syscall.EINVAL, match: func(err error) bool { return errors.Is(err, ErrInvalidArgument) }},
}

func errnoFromError(err error) syscall.Errno {
	if err == nil {
		return 0
	}

	for _, rule := range errnoRules {
		if rule.match(err) {
			return rule.errno
		}
	}
	return syscall.EIO
}

func isAnyError(err error, targets ...error) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
