package syncer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"flyingEirc/Rclaude/pkg/safepath"
)

var (
	// ErrWorkspaceRootRequired indicates that RunOptions.WorkspaceRoot was not provided.
	ErrWorkspaceRootRequired = errors.New("syncer: workspace root is required")
	// ErrWorkspaceRootNotAbs indicates that the workspace root is not an absolute path.
	ErrWorkspaceRootNotAbs = errors.New("syncer: workspace root must be absolute")
	// ErrWorkspaceRootNotDir indicates that the workspace root does not exist or is not a directory.
	ErrWorkspaceRootNotDir = errors.New("syncer: workspace root must be an existing directory")
	// ErrWorkspaceNameUnsafe indicates that the project directory name cannot be
	// used as the server-side /workspace/{user_id}/{name} segment.
	ErrWorkspaceNameUnsafe = errors.New("syncer: project directory name is not a safe path segment " +
		"(no separators, control characters, \".\" or \"..\")")
	// ErrWorkspaceRootUnscoped indicates that the daemon was started from the
	// filesystem root or the user's home directory instead of a project root.
	ErrWorkspaceRootUnscoped = errors.New("syncer: refusing to share this directory; " +
		"start rclaude from your project root directory")
)

// ResolveWorkspaceRoot 把 daemon 进程的启动目录解析为工作区根。用户必须在项目
// 根目录启动：cwd 即工作区根，其目录名成为服务端 /workspace/{user_id}/{name}/
// 中的项目名。为避免误共享，拒绝文件系统根目录与用户家目录本身。
func ResolveWorkspaceRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("syncer: resolve working directory: %w", err)
	}
	if err := guardWorkspaceRoot(cwd); err != nil {
		return "", err
	}
	if _, err := ValidateWorkspaceRoot(cwd); err != nil {
		return "", err
	}
	return cwd, nil
}

// ValidateWorkspaceRoot 校验一个显式给定的工作区根（绝对路径、存在的目录、
// 目录名可作为服务端安全路径段），并返回派生的项目名。
func ValidateWorkspaceRoot(root string) (string, error) {
	if root == "" {
		return "", ErrWorkspaceRootRequired
	}
	if !filepath.IsAbs(root) {
		return "", ErrWorkspaceRootNotAbs
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrWorkspaceRootNotDir, root)
	}
	name, err := safepath.CleanSegment(filepath.Base(filepath.Clean(root)))
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrWorkspaceNameUnsafe, filepath.Base(filepath.Clean(root)))
	}
	return name, nil
}

// guardWorkspaceRoot 拒绝把整个文件系统或家目录当项目根共享出去。
func guardWorkspaceRoot(root string) error {
	cleaned := filepath.Clean(root)
	if cleaned == string(filepath.Separator) || cleaned == filepath.VolumeName(cleaned)+string(filepath.Separator) {
		return fmt.Errorf("%w (got filesystem root %q)", ErrWorkspaceRootUnscoped, root)
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == cleaned {
		return fmt.Errorf("%w (got home directory %q)", ErrWorkspaceRootUnscoped, root)
	}
	return nil
}
