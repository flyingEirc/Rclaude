package ptyhost

import (
	"errors"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"flyingEirc/Rclaude/pkg/safepath"
)

var (
	ErrUnsafeUserID        = errors.New("ptyhost: user id contains unsafe characters or path elements")
	ErrWorkspaceRootNotAbs = errors.New("ptyhost: workspace root must be an absolute path")
	ErrBinaryEmpty         = errors.New("ptyhost: binary name is empty")
	ErrBinaryNotFound      = errors.New("ptyhost: binary not found in PATH")
)

// ResolveCwd returns the absolute working directory under the workspace root
// for the given user id, with traversal protection.
func ResolveCwd(workspaceRoot, userID string) (string, error) {
	if workspaceRoot == "" {
		return "", ErrWorkspaceRootNotAbs
	}

	trimmedUserID := strings.TrimSpace(userID)
	if trimmedUserID == "" || strings.ContainsAny(trimmedUserID, `/\`) {
		return "", ErrUnsafeUserID
	}

	cleanedUserID, err := safepath.Clean(trimmedUserID)
	if err != nil || cleanedUserID == "" || cleanedUserID != trimmedUserID {
		return "", ErrUnsafeUserID
	}

	cwd, err := safepath.Join(workspaceRoot, cleanedUserID)
	if err == nil {
		return cwd, nil
	}
	if errors.Is(err, safepath.ErrBaseNotAbs) || errors.Is(err, safepath.ErrEmptyPath) {
		return "", ErrWorkspaceRootNotAbs
	}

	return "", ErrUnsafeUserID
}

// BuildEnv returns a sorted KEY=VALUE slice for exec.Cmd.Env, restricted to
// the given whitelist drawn from source. clientTerm overrides TERM only when
// it contains a conservative terminal-name character set.
func BuildEnv(source map[string]string, whitelist []string, clientTerm string) []string {
	allow := make(map[string]struct{}, len(whitelist))
	for _, key := range whitelist {
		allow[key] = struct{}{}
	}

	out := make([]string, 0, len(allow))
	for key, value := range source {
		if _, ok := allow[key]; !ok {
			continue
		}
		out = append(out, key+"="+value)
	}

	if _, ok := allow["TERM"]; ok && isSafeTerm(clientTerm) {
		filtered := out[:0]
		for _, kv := range out {
			if strings.HasPrefix(kv, "TERM=") {
				continue
			}
			filtered = append(filtered, kv)
		}
		out = append(filtered, "TERM="+clientTerm)
	}

	sort.Strings(out)
	return out
}

// ResolveBinary returns an absolute path to the given binary name. Absolute
// paths are returned unchanged. Bare names are looked up via PATH.
func ResolveBinary(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", ErrBinaryEmpty
	}

	if isAbsoluteBinaryPath(trimmed) {
		return trimmed, nil
	}

	path, err := exec.LookPath(trimmed)
	if err != nil {
		return "", ErrBinaryNotFound
	}

	return path, nil
}

func isSafeTerm(term string) bool {
	if term == "" {
		return false
	}

	return strings.IndexFunc(term, func(r rune) bool {
		return !unicode.IsLetter(r) &&
			!unicode.IsDigit(r) &&
			!strings.ContainsRune("-_.", r)
	}) == -1
}

func isAbsoluteBinaryPath(binary string) bool {
	return filepath.IsAbs(binary) ||
		strings.HasPrefix(binary, `\`) ||
		hasWindowsDrivePrefix(binary)
}

func hasWindowsDrivePrefix(binary string) bool {
	return len(binary) >= 3 &&
		unicode.IsLetter(rune(binary[0])) &&
		binary[1] == ':' &&
		(binary[2] == '/' || binary[2] == '\\')
}
