package syncer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/safepath"
)

var (
	ErrRootNotAbsolute  = errors.New("syncer: scan root must be absolute")
	ErrRootNotDirectory = errors.New("syncer: scan root is not a directory")
)

type ScanOptions struct {
	Root     string
	Excludes []string
}

func Scan(opts ScanOptions) ([]*remotefsv1.FileInfo, error) {
	if err := validateScanRoot(opts.Root); err != nil {
		return nil, err
	}

	var out []*remotefsv1.FileInfo
	if err := filepath.WalkDir(opts.Root, scanWalkFn(opts, &out)); err != nil {
		return nil, fmt.Errorf("syncer: walk %q: %w", opts.Root, err)
	}
	return out, nil
}

func validateScanRoot(root string) error {
	if !filepath.IsAbs(root) {
		return ErrRootNotAbsolute
	}

	rootInfo, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("syncer: stat root %q: %w", root, err)
	}
	if !rootInfo.IsDir() {
		return ErrRootNotDirectory
	}
	return nil
}

func scanWalkFn(opts ScanOptions, out *[]*remotefsv1.FileInfo) fs.WalkDirFunc {
	return func(p string, d fs.DirEntry, walkErr error) error {
		entry, skipDir := scanEntry(opts.Root, p, d, walkErr, opts.Excludes)
		if skipDir {
			return filepath.SkipDir
		}
		if entry != nil {
			*out = append(*out, entry)
		}
		return nil
	}
}

func scanEntry(
	root string,
	p string,
	d fs.DirEntry,
	walkErr error,
	excludes []string,
) (*remotefsv1.FileInfo, bool) {
	if walkErr != nil || p == root {
		return nil, false
	}

	rel, ok := scanRelativePath(root, p)
	if !ok {
		return nil, false
	}
	if matchExclude(rel, excludes) {
		return nil, d.IsDir()
	}

	info, err := d.Info()
	if err != nil {
		return nil, false
	}
	return dirEntryToFileInfo(rel, info), false
}

func scanRelativePath(root, p string) (string, bool) {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return "", false
	}
	return safepath.ToSlash(rel), true
}

func dirEntryToFileInfo(rel string, info fs.FileInfo) *remotefsv1.FileInfo {
	return &remotefsv1.FileInfo{
		Path:    rel,
		Size:    info.Size(),
		ModTime: info.ModTime().Unix(),
		IsDir:   info.IsDir(),
		Mode:    uint32(info.Mode().Perm()),
	}
}

func matchExclude(rel string, patterns []string) bool {
	base := path.Base(rel)
	for _, pattern := range patterns {
		target := rel
		if !strings.Contains(pattern, "/") {
			target = base
		}
		ok, err := doublestar.Match(pattern, target)
		if err == nil && ok {
			return true
		}
	}
	return false
}
