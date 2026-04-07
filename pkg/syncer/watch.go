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

var ErrNilEvents = errors.New("syncer: watch events channel is nil")

type WatchOptions struct {
	Root            string
	Excludes        []string
	SensitiveFilter *SensitiveFilter
	Events          chan<- *remotefsv1.FileChange
	Logger          *slog.Logger
	SelfWrites      *selfWriteFilter
}

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

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("syncer: new watcher: %w", err)
	}
	defer func() {
		if closeErr := watcher.Close(); closeErr != nil {
			logger.Warn("watcher close", "err", closeErr)
		}
	}()

	if err := addTreeRecursively(watcher, opts.Root, opts.Excludes, opts.SensitiveFilter); err != nil {
		return fmt.Errorf("syncer: initial add: %w", err)
	}

	return runWatchLoop(ctx, watcher, opts, logger)
}

func runWatchLoop(ctx context.Context, watcher *fsnotify.Watcher, opts WatchOptions, logger *slog.Logger) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			logger.Error("fsnotify error", "err", err)
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			handleWatchEvent(ctx, watcher, opts, ev, logger)
		}
	}
}

func addTreeRecursively(
	watcher *fsnotify.Watcher,
	root string,
	excludes []string,
	filter *SensitiveFilter,
) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		skipDir, err := shouldSkipWatchedDir(root, path, excludes, filter)
		if err != nil {
			return err
		}
		if skipDir {
			return filepath.SkipDir
		}
		if err := watcher.Add(path); err != nil {
			return fmt.Errorf("add %q: %w", path, err)
		}
		return nil
	})
}

func shouldSkipWatchedDir(
	root string,
	path string,
	excludes []string,
	filter *SensitiveFilter,
) (bool, error) {
	if path == root {
		return false, nil
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false, nil
	}
	rel = safepath.ToSlash(rel)

	if filter != nil && filter.Match(rel) {
		return true, nil
	}
	return matchExclude(rel, excludes), nil
}

func handleWatchEvent(
	ctx context.Context,
	watcher *fsnotify.Watcher,
	opts WatchOptions,
	ev fsnotify.Event,
	logger *slog.Logger,
) {
	change, watchDir := watchChangeFromEvent(opts, ev)
	if change == nil {
		return
	}
	if watchDir {
		if err := watcher.Add(ev.Name); err != nil {
			logger.Warn("watch add new dir", "path", ev.Name, "err", err)
		}
	}

	select {
	case opts.Events <- change:
	case <-ctx.Done():
	}
}

func watchChangeFromEvent(opts WatchOptions, ev fsnotify.Event) (*remotefsv1.FileChange, bool) {
	rel, ok := watchRelativePath(opts.Root, ev.Name)
	if !ok || matchExclude(rel, opts.Excludes) {
		return nil, false
	}
	if opts.SensitiveFilter != nil && opts.SensitiveFilter.Match(rel) {
		return nil, false
	}
	if opts.SelfWrites != nil && opts.SelfWrites.ShouldSuppress(rel) {
		return nil, false
	}

	changeType := toChangeType(ev.Op)
	if changeType == remotefsv1.ChangeType_CHANGE_TYPE_UNSPECIFIED {
		return nil, false
	}

	info := lookupChangedInfo(opts.Root, rel, changeType)
	change := &remotefsv1.FileChange{
		Type: changeType,
		File: info,
	}
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
