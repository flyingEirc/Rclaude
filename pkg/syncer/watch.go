package syncer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/logx"
	"flyingEirc/Rclaude/pkg/safepath"
)

// ErrNilEvents 表示 WatchOptions.Events 为 nil。
var ErrNilEvents = errors.New("syncer: watch events channel is nil")

// WatchOptions 配置 Watch 的行为。
type WatchOptions struct {
	// Root 是工作区根绝对路径，必须存在且为目录。
	Root string
	// Excludes 同 ScanOptions.Excludes，命中的路径不会上报事件。
	Excludes []string
	// Events 是 Watcher 输出事件的目标 channel，由调用方创建与关闭。
	// Watch 不会关闭该 channel；channel 满时会阻塞直到有消费者或 ctx 取消。
	Events chan<- *remotefsv1.FileChange
	// Logger 为 nil 时回退到 logx.FromContext(ctx)。
	Logger *slog.Logger
}

// Watch 递归监听 opts.Root 的文件系统变更并输出到 opts.Events。
// 阻塞直到 ctx.Done() 或 fsnotify 初始化失败。
func Watch(ctx context.Context, opts WatchOptions) error {
	if opts.Events == nil {
		return ErrNilEvents
	}
	if !filepath.IsAbs(opts.Root) {
		return ErrRootNotAbsolute
	}
	logger := opts.Logger
	if logger == nil {
		logger = logx.FromContext(ctx)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("syncer: new watcher: %w", err)
	}
	defer func() {
		if cerr := w.Close(); cerr != nil {
			logger.Warn("watcher close", "err", cerr)
		}
	}()

	if err := addTreeRecursively(w, opts.Root, opts.Excludes); err != nil {
		return fmt.Errorf("syncer: initial add: %w", err)
	}

	return runWatchLoop(ctx, w, opts, logger)
}

// runWatchLoop 是 Watch 内部的事件主循环，拆出单函数以控制复杂度。
func runWatchLoop(
	ctx context.Context,
	w *fsnotify.Watcher,
	opts WatchOptions,
	logger *slog.Logger,
) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			logger.Error("fsnotify error", "err", err)
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			handleWatchEvent(ctx, w, opts, ev, logger)
		}
	}
}

// addTreeRecursively 把 root 下所有子目录（含 root 本身）加入 watcher，
// 被 exclude 命中的目录跳过整棵子树。
func addTreeRecursively(w *fsnotify.Watcher, root string, excludes []string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if p != root {
			rel, err := filepath.Rel(root, p)
			if err == nil && matchExclude(safepath.ToSlash(rel), excludes) {
				return filepath.SkipDir
			}
		}
		if err := w.Add(p); err != nil {
			return fmt.Errorf("add %q: %w", p, err)
		}
		return nil
	})
}

// handleWatchEvent 把单个 fsnotify.Event 翻译成 FileChange 并尝试发送。
func handleWatchEvent(
	ctx context.Context,
	w *fsnotify.Watcher,
	opts WatchOptions,
	ev fsnotify.Event,
	logger *slog.Logger,
) {
	change, watchDir := watchChangeFromEvent(opts, ev)
	if change == nil {
		return
	}
	if watchDir {
		if addErr := w.Add(ev.Name); addErr != nil {
			logger.Warn("watch add new dir", "path", ev.Name, "err", addErr)
		}
	}
	select {
	case opts.Events <- change:
	case <-ctx.Done():
	}
}

func watchChangeFromEvent(
	opts WatchOptions,
	ev fsnotify.Event,
) (*remotefsv1.FileChange, bool) {
	rel, ok := watchRelativePath(opts.Root, ev.Name)
	if !ok || matchExclude(rel, opts.Excludes) {
		return nil, false
	}

	changeType := toChangeType(ev.Op)
	if changeType == remotefsv1.ChangeType_CHANGE_TYPE_UNSPECIFIED {
		return nil, false
	}

	info := lookupChangedInfo(opts.Root, rel, changeType)
	change := &remotefsv1.FileChange{Type: changeType, File: info}
	return change, changeType == remotefsv1.ChangeType_CHANGE_TYPE_CREATE && info.GetIsDir()
}

func watchRelativePath(root, name string) (string, bool) {
	rel, err := filepath.Rel(root, name)
	if err != nil {
		return "", false
	}
	rel = safepath.ToSlash(rel)
	if rel == "" || rel == "." {
		return "", false
	}
	return rel, true
}

// toChangeType 把 fsnotify.Op 映射到协议 ChangeType。
// 同一事件带多个位时，按 Create > Write > Remove > Rename 的优先级选取。
// 纯 Chmod 事件返回 UNSPECIFIED，由调用方丢弃。
func toChangeType(op fsnotify.Op) remotefsv1.ChangeType {
	switch {
	case op.Has(fsnotify.Create):
		return remotefsv1.ChangeType_CHANGE_TYPE_CREATE
	case op.Has(fsnotify.Write):
		return remotefsv1.ChangeType_CHANGE_TYPE_MODIFY
	case op.Has(fsnotify.Remove):
		return remotefsv1.ChangeType_CHANGE_TYPE_DELETE
	case op.Has(fsnotify.Rename):
		return remotefsv1.ChangeType_CHANGE_TYPE_DELETE
	default:
		return remotefsv1.ChangeType_CHANGE_TYPE_UNSPECIFIED
	}
}

// lookupChangedInfo 尝试 Lstat 变更路径，失败或 DELETE 时返回仅含 path 的占位 FileInfo。
func lookupChangedInfo(root, rel string, changeType remotefsv1.ChangeType) *remotefsv1.FileInfo {
	if changeType == remotefsv1.ChangeType_CHANGE_TYPE_DELETE {
		return &remotefsv1.FileInfo{Path: rel}
	}
	abs, err := safepath.Join(root, rel)
	if err != nil {
		return &remotefsv1.FileInfo{Path: rel}
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return &remotefsv1.FileInfo{Path: rel}
	}
	return &remotefsv1.FileInfo{
		Path:    rel,
		Size:    fi.Size(),
		ModTime: fi.ModTime().Unix(),
		IsDir:   fi.IsDir(),
		Mode:    uint32(fi.Mode().Perm()),
	}
}
