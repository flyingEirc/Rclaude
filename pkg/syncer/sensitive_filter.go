package syncer

import (
	"errors"
	"fmt"
	pathpkg "path"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"flyingEirc/Rclaude/pkg/safepath"
)

var ErrInvalidSensitivePattern = errors.New("syncer: invalid sensitive pattern")

var defaultSensitivePatterns = []string{
	".env",
	".env.*",
	"*.pem",
	"*.key",
	"*.p12",
	"*.pfx",
	"*.crt",
	"*.cer",
	"*.p8",
	"id_rsa",
	"id_dsa",
	"id_ecdsa",
	"id_ed25519",
	"*_secret",
	"*_secret.*",
}

type SensitiveFilter struct {
	patterns []string
}

func NewSensitiveFilter(extra []string) (*SensitiveFilter, error) {
	patterns := make([]string, 0, len(defaultSensitivePatterns)+len(extra))
	patterns = append(patterns, defaultSensitivePatterns...)

	for _, raw := range extra {
		pattern := normalizePattern(raw)
		if pattern == "" || !doublestar.ValidatePattern(pattern) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidSensitivePattern, raw)
		}
		patterns = append(patterns, pattern)
	}

	return &SensitiveFilter{patterns: patterns}, nil
}

func (f *SensitiveFilter) Match(relPath string) bool {
	if f == nil || len(f.patterns) == 0 {
		return false
	}

	relPath = normalizeRelativePath(relPath)
	if relPath == "" {
		return false
	}

	for _, candidate := range pathCandidates(relPath) {
		for _, pattern := range f.patterns {
			if matchSensitivePattern(candidate, pattern) {
				return true
			}
		}
	}

	return false
}

func normalizePattern(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	return strings.ReplaceAll(pattern, "\\", "/")
}

func normalizeRelativePath(relPath string) string {
	relPath = safepath.ToSlash(relPath)
	if relPath == "" || relPath == "." || relPath == "/" {
		return ""
	}

	cleaned := pathpkg.Clean(relPath)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func pathCandidates(relPath string) []string {
	out := []string{relPath}
	for parent := pathpkg.Dir(relPath); parent != "." && parent != "/" && parent != relPath; parent = pathpkg.Dir(parent) {
		out = append(out, parent)
	}
	return out
}

func matchSensitivePattern(candidate, pattern string) bool {
	target := candidate
	if !strings.Contains(pattern, "/") {
		target = pathpkg.Base(candidate)
	}
	if doublestar.MatchUnvalidated(pattern, target) {
		return true
	}
	if strings.Contains(pattern, "/") && strings.HasSuffix(pattern, "/**") {
		return candidate == strings.TrimSuffix(pattern, "/**")
	}
	return false
}
