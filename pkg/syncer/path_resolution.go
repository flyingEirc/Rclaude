package syncer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"flyingEirc/Rclaude/pkg/safepath"
)

const maxSymlinkResolveDepth = 40

type pathResolutionOptions struct {
	allowMissingLeaf   bool
	followFinalSymlink bool
}

var errSensitiveDescendant = errors.New("syncer: sensitive descendant")

func blocksSensitivePath(
	root string,
	relPath string,
	filter *SensitiveFilter,
	opts pathResolutionOptions,
) (bool, error) {
	if isSensitivePath(filter, relPath) {
		return true, nil
	}

	targetRel, ok, err := resolveActionTargetRel(root, relPath, opts)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !ok {
		return false, nil
	}
	return isSensitivePath(filter, targetRel), nil
}

func resolveActionTargetRel(root, relPath string, opts pathResolutionOptions) (string, bool, error) {
	abs, err := resolveWorkspacePath(root, relPath)
	if err != nil {
		return "", false, err
	}

	resolvedAbs, err := resolveActionTargetAbs(abs, opts)
	if err != nil {
		return "", false, err
	}

	within, err := safepath.IsWithin(root, resolvedAbs)
	if err != nil {
		return "", false, err
	}
	if !within {
		return "", false, nil
	}

	targetRel, err := filepath.Rel(root, resolvedAbs)
	if err != nil {
		return "", false, err
	}
	return normalizeRelativePath(targetRel), true, nil
}

func resolveActionTargetAbs(abs string, opts pathResolutionOptions) (string, error) {
	current := filepath.Clean(abs)

	for depth := 0; depth < maxSymlinkResolveDepth; depth++ {
		resolved, next, restarted, err := resolveActionTargetPass(current, opts)
		if err != nil {
			return "", err
		}
		if !restarted {
			return resolved, nil
		}
		current = next
	}

	return "", fmt.Errorf("syncer: resolve %q: too many symlinks", abs)
}

func resolveActionTargetPass(
	current string,
	opts pathResolutionOptions,
) (string, string, bool, error) {
	prefix, parts := splitAbsPath(current)
	resolved := prefix

	if len(parts) == 0 {
		return resolved, "", false, nil
	}

	for i, part := range parts {
		next := filepath.Join(resolved, part)
		info, missingLeaf, err := lstatActionPath(next, i == len(parts)-1, opts.allowMissingLeaf)
		if err != nil {
			return "", "", false, err
		}
		if missingLeaf {
			return next, "", false, nil
		}
		if !shouldFollowSymlink(info, i, len(parts), opts.followFinalSymlink) {
			resolved = next
			continue
		}

		target, err := expandSymlinkTarget(next, parts[i+1:])
		if err != nil {
			return "", "", false, err
		}
		return "", filepath.Clean(target), true, nil
	}

	return resolved, "", false, nil
}

func lstatActionPath(path string, isLeaf, allowMissingLeaf bool) (fs.FileInfo, bool, error) {
	info, err := os.Lstat(path)
	if err == nil {
		return info, false, nil
	}
	if allowMissingLeaf && isLeaf && errors.Is(err, fs.ErrNotExist) {
		return nil, true, nil
	}
	return nil, false, err
}

func shouldFollowSymlink(info fs.FileInfo, idx, total int, followFinal bool) bool {
	return info.Mode()&os.ModeSymlink != 0 && (followFinal || idx < total-1)
}

func expandSymlinkTarget(path string, remaining []string) (string, error) {
	target, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	if len(remaining) == 0 {
		return target, nil
	}
	return filepath.Join(target, filepath.Join(remaining...)), nil
}

func splitAbsPath(abs string) (string, []string) {
	cleaned := filepath.Clean(abs)
	volume := filepath.VolumeName(cleaned)
	rest := strings.TrimPrefix(cleaned, volume)
	rest = strings.TrimPrefix(rest, string(filepath.Separator))

	prefix := volume + string(filepath.Separator)
	if prefix == "" {
		prefix = string(filepath.Separator)
	}
	if rest == "" {
		return prefix, nil
	}
	return prefix, strings.Split(rest, string(filepath.Separator))
}

func hasSensitiveDescendant(root, relPath string, filter *SensitiveFilter) (bool, error) {
	if filter == nil {
		return false, nil
	}

	abs, err := resolveWorkspacePath(root, relPath)
	if err != nil {
		return false, err
	}

	info, err := os.Lstat(abs)
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, nil
	}

	walkErr := filepath.WalkDir(abs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == abs {
			return nil
		}

		descendant, err := filepath.Rel(abs, path)
		if err != nil {
			return err
		}
		descendantRel := joinRelPath(relPath, filepath.ToSlash(descendant))
		if isSensitivePath(filter, descendantRel) {
			return errSensitiveDescendant
		}
		return nil
	})
	if errors.Is(walkErr, errSensitiveDescendant) {
		return true, nil
	}
	return false, walkErr
}
