package ptyhost

import (
	"errors"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"flyingEirc/Rclaude/pkg/safepath"
)

var (
	ErrUnsafeUserID        = errors.New("ptyhost: user id contains unsafe characters or path elements")
	ErrUnsafeWorkspace     = errors.New("ptyhost: workspace name contains unsafe characters or path elements")
	ErrWorkspaceRootNotAbs = errors.New("ptyhost: workspace root must be an absolute path")
	ErrBinaryEmpty         = errors.New("ptyhost: binary name is empty")
	ErrBinaryNotFound      = errors.New("ptyhost: binary not found in PATH")
	ErrBinaryUnsafe        = errors.New("ptyhost: binary must be a bare program name or an absolute path")
)

// ResolveCwd returns the absolute working directory
// {workspaceRoot}/{userID}/{workspace} for one PTY session, with traversal
// protection on both segments. workspace is the daemon-reported project
// directory name.
func ResolveCwd(workspaceRoot, userID, workspace string) (string, error) {
	if workspaceRoot == "" {
		return "", ErrWorkspaceRootNotAbs
	}

	cleanedUserID, err := safepath.CleanSegment(userID)
	if err != nil {
		return "", ErrUnsafeUserID
	}
	cleanedWorkspace, err := safepath.CleanSegment(workspace)
	if err != nil {
		return "", ErrUnsafeWorkspace
	}

	cwd, err := safepath.Join(workspaceRoot, cleanedUserID+"/"+cleanedWorkspace)
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
	exact, patterns := compileEnvAllowlist(whitelist)

	out := make([]string, 0, len(source))
	for key, value := range source {
		if !envKeyAllowed(key, exact, patterns) {
			continue
		}
		out = append(out, key+"="+value)
	}

	if envKeyAllowed("TERM", exact, patterns) && isSafeTerm(clientTerm) {
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

func compileEnvAllowlist(whitelist []string) (map[string]struct{}, []string) {
	exact := make(map[string]struct{}, len(whitelist))
	patterns := make([]string, 0)

	for _, entry := range whitelist {
		key := strings.TrimSpace(entry)
		if key == "" {
			continue
		}
		if strings.ContainsAny(key, "*?[") {
			patterns = append(patterns, key)
			continue
		}
		exact[key] = struct{}{}
	}

	return exact, patterns
}

func envKeyAllowed(key string, exact map[string]struct{}, patterns []string) bool {
	if _, ok := exact[key]; ok {
		return true
	}

	for _, pattern := range patterns {
		matched, err := path.Match(pattern, key)
		if err == nil && matched {
			return true
		}
	}

	return false
}

// ResolveBinary returns an absolute path to the given binary name. Absolute
// paths are returned unchanged. Bare names are looked up via PATH. Relative
// paths containing separators are rejected: the name comes over the wire from
// the attach request, and LookPath would otherwise resolve it against the
// server process cwd.
func ResolveBinary(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", ErrBinaryEmpty
	}

	if isAbsoluteBinaryPath(trimmed) {
		return trimmed, nil
	}

	if strings.ContainsAny(trimmed, `/\`) {
		return "", ErrBinaryUnsafe
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
