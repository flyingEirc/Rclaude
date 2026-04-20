package safepath

import (
	"errors"
	"path"
	"path/filepath"
	"strings"
)

// 错误集合：调用方应使用 errors.Is 比较。

var (

	// ErrEmptyPath 表示传入了空字符串路径。

	ErrEmptyPath = errors.New("safepath: empty path")

	// ErrAbsoluteRel 表示相对路径参数实际是绝对路径。

	ErrAbsoluteRel = errors.New("safepath: relative path must not be absolute")

	// ErrEscape 表示路径越出 base 范围。

	ErrEscape = errors.New("safepath: path escapes base")

	// ErrBaseNotAbs 表示 base 必须是绝对路径但不是。

	ErrBaseNotAbs = errors.New("safepath: base must be absolute")
)

// Clean 把任意输入路径规范化为以 forward slash 表示的相对路径，

// 不含 "." / ".." 段、不含 leading 或 trailing slash。

// 支持 Windows 反斜杠输入。空字符串返回 ErrEmptyPath。

// 输入若解析后逃出根（出现顶层 ".."）返回 ErrEscape。

func Clean(p string) (string, error) {
	if p == "" {
		return "", ErrEmptyPath
	}

	// 统一为 forward slash 后用 path.Clean。

	p = ToSlash(p)

	cleaned := path.Clean(p)

	// 顶层 ".." 等价于越界。

	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrEscape
	}

	// 去掉 leading slash 让结果保持相对。

	cleaned = strings.TrimPrefix(cleaned, "/")

	// path.Clean(".") 表示空目录，这里规范化为空串。

	if cleaned == "." {
		return "", nil
	}

	return cleaned, nil
}

// Join 把相对路径 rel 安全地拼接到绝对路径 base 之下，

// 结果保证位于 base 内（不含符号链接解析）。

// base 必须是绝对路径；rel 经 Clean 后不得越界。

// 返回的绝对路径使用宿主机平台分隔符。

func Join(base, rel string) (string, error) {
	if base == "" {
		return "", ErrEmptyPath
	}

	if !filepath.IsAbs(base) {
		return "", ErrBaseNotAbs
	}

	// 拒绝把绝对路径当 rel 传入（包括 Unix 与 Windows 形式）。

	if isAbsLike(rel) {
		return "", ErrAbsoluteRel
	}

	cleanedRel, err := Clean(rel)
	if err != nil {
		return "", err
	}

	// 空 rel 等价于 base 自身。

	if cleanedRel == "" {
		return filepath.Clean(base), nil
	}

	joined := filepath.Join(base, FromSlash(cleanedRel))

	// 二次防御：复检结果仍位于 base 内。

	within, err := IsWithin(base, joined)
	if err != nil {
		return "", err
	}

	if !within {
		return "", ErrEscape
	}

	return joined, nil
}

// IsWithin 判断 target 是否落在 base 之下（含 base 自身）。

// 不解析符号链接；调用方需自行处理 symlink 场景。

// base 与 target 必须均为绝对路径。

func IsWithin(base, target string) (bool, error) {
	if base == "" || target == "" {
		return false, ErrEmptyPath
	}

	if !filepath.IsAbs(base) || !filepath.IsAbs(target) {
		return false, ErrBaseNotAbs
	}

	cleanBase := filepath.Clean(base)

	cleanTarget := filepath.Clean(target)

	if cleanBase == cleanTarget {
		return true, nil
	}

	// 用 filepath.Rel 检测前缀关系，避免字符串前缀误判。

	rel, err := filepath.Rel(cleanBase, cleanTarget)
	if err != nil {
		return false, nil //nolint:nilerr // Rel 失败一般是跨卷，视为不在 base 下。
	}

	rel = filepath.ToSlash(rel)

	if rel == ".." || strings.HasPrefix(rel, "../") {
		return false, nil
	}

	return true, nil
}

// ToSlash 把任意平台分隔符的路径转为 forward slash 表示。

func ToSlash(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

// FromSlash 把 forward slash 路径转为宿主机平台分隔符表示。

func FromSlash(p string) string {
	return filepath.FromSlash(p)
}

// isAbsLike 同时识别 Unix 与 Windows 形式的绝对路径，

// 不依赖宿主机 OS。这样在 Linux 上跑时也能拒绝 "C:\..." 输入。

func isAbsLike(p string) bool {
	if p == "" {
		return false
	}

	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
		return true
	}

	if len(p) >= 3 && isDriveLetter(p[0]) && p[1] == ':' && (p[2] == '/' || p[2] == '\\') {
		return true
	}

	return false
}

func isDriveLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
