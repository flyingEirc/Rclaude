package ptyhost_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/ptyhost"
)

func TestResolveCwd_BasicUserScope(t *testing.T) {
	t.Parallel()

	got, err := ptyhost.ResolveCwd(absWorkspaceRoot(), "alice")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(absWorkspaceRoot(), "alice"), got)
}

func TestResolveCwd_RejectsTraversalUserID(t *testing.T) {
	t.Parallel()

	_, err := ptyhost.ResolveCwd(absWorkspaceRoot(), "../etc")
	require.Error(t, err)
	assert.ErrorIs(t, err, ptyhost.ErrUnsafeUserID)
}

func TestResolveCwd_RejectsNestedUserID(t *testing.T) {
	t.Parallel()

	_, err := ptyhost.ResolveCwd(absWorkspaceRoot(), "alice/bob")
	require.Error(t, err)
	assert.ErrorIs(t, err, ptyhost.ErrUnsafeUserID)
}

func TestResolveCwd_RejectsNormalizedTraversalUserID(t *testing.T) {
	t.Parallel()

	testCases := []string{
		"alice/../bob",
		`alice\..\bob`,
	}

	for _, userID := range testCases {
		_, err := ptyhost.ResolveCwd(absWorkspaceRoot(), userID)
		require.Error(t, err)
		assert.ErrorIs(t, err, ptyhost.ErrUnsafeUserID)
	}
}

func TestResolveCwd_RejectsEmpty(t *testing.T) {
	t.Parallel()

	_, err := ptyhost.ResolveCwd(absWorkspaceRoot(), "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ptyhost.ErrUnsafeUserID)

	_, err = ptyhost.ResolveCwd("", "alice")
	require.Error(t, err)
	assert.ErrorIs(t, err, ptyhost.ErrWorkspaceRootNotAbs)
}

func TestResolveCwd_RejectsRelativeRoot(t *testing.T) {
	t.Parallel()

	_, err := ptyhost.ResolveCwd("workspace", "alice")
	require.Error(t, err)
	assert.ErrorIs(t, err, ptyhost.ErrWorkspaceRootNotAbs)
}

func TestBuildEnv_WhitelistOnly(t *testing.T) {
	t.Parallel()

	source := map[string]string{
		"TERM":           "xterm-256color",
		"LANG":           "en_US.UTF-8",
		"PATH":           "/usr/bin:/bin",
		"AWS_SECRET_KEY": "leakme",
		"HOME":           "/root",
	}
	whitelist := []string{"TERM", "LANG", "LC_ALL", "LC_CTYPE", "PATH"}

	got := ptyhost.BuildEnv(source, whitelist, "")
	assert.Equal(t, []string{
		"LANG=en_US.UTF-8",
		"PATH=/usr/bin:/bin",
		"TERM=xterm-256color",
	}, got)
}

func TestBuildEnv_ClientTermOverride(t *testing.T) {
	t.Parallel()

	got := ptyhost.BuildEnv(map[string]string{"TERM": "dumb"}, []string{"TERM"}, "xterm-256color")
	assert.Equal(t, []string{"TERM=xterm-256color"}, got)
}

func TestBuildEnv_RejectsBadClientTerm(t *testing.T) {
	t.Parallel()

	got := ptyhost.BuildEnv(map[string]string{"TERM": "dumb"}, []string{"TERM"}, "weird term")
	assert.Equal(t, []string{"TERM=dumb"}, got)
}

func TestResolveBinary_AbsolutePath(t *testing.T) {
	t.Parallel()

	got, err := ptyhost.ResolveBinary(absBinaryPath())
	require.NoError(t, err)
	assert.Equal(t, absBinaryPath(), got)
}

func TestResolveBinary_NameLookup(t *testing.T) {
	t.Parallel()

	got, err := ptyhost.ResolveBinary("go")
	require.NoError(t, err)
	assert.NotEmpty(t, got)
	assert.True(t, filepath.IsAbs(got), "expected absolute path, got %q", got)
}

func TestResolveBinary_NotFound(t *testing.T) {
	t.Parallel()

	_, err := ptyhost.ResolveBinary("definitely-not-a-real-binary-zzz")
	require.Error(t, err)
	assert.ErrorIs(t, err, ptyhost.ErrBinaryNotFound)
}

func TestResolveBinary_RejectsEmpty(t *testing.T) {
	t.Parallel()

	_, err := ptyhost.ResolveBinary("")
	require.Error(t, err)
	assert.ErrorIs(t, err, ptyhost.ErrBinaryEmpty)
}

func absWorkspaceRoot() string {
	if runtime.GOOS == "windows" {
		return `C:\workspace`
	}

	return "/workspace"
}

func absBinaryPath() string {
	if runtime.GOOS == "windows" {
		return `C:\Program Files\Claude\claude.exe`
	}

	return "/usr/local/bin/claude"
}
