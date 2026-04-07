package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/config"
)

func writeYAML(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func absWorkspace() string {
	if runtime.GOOS == "windows" {
		return `C:\workspace\alice`
	}
	return "/workspace/alice"
}

func absMountpoint() string {
	if runtime.GOOS == "windows" {
		return `C:\mnt\rclaude`
	}
	return "/mnt/rclaude"
}

func TestLoadDaemonOK(t *testing.T) {
	t.Parallel()
	body := `
server:
  address: "example.com:9000"
  token: "tk"
workspace:
  path: ` + escapeYAML(absWorkspace()) + `
  exclude: [".git", "node_modules"]
log:
  level: "info"
  format: "json"
`
	path := writeYAML(t, "daemon.yaml", body)
	cfg, err := config.LoadDaemon(path)
	require.NoError(t, err)
	assert.Equal(t, "example.com:9000", cfg.Server.Address)
	assert.Equal(t, "tk", cfg.Server.Token)
	assert.Equal(t, absWorkspace(), cfg.Workspace.Path)
	assert.Equal(t, []string{".git", "node_modules"}, cfg.Workspace.Exclude)
	assert.Equal(t, "info", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)
}

func TestLoadDaemonMissingAddress(t *testing.T) {
	t.Parallel()
	body := `
workspace:
  path: ` + escapeYAML(absWorkspace()) + `
`
	path := writeYAML(t, "daemon.yaml", body)
	_, err := config.LoadDaemon(path)
	require.Error(t, err)
	assert.True(t, errors.Is(err, config.ErrEmptyServerAddress))
}

func TestLoadDaemonRelativeWorkspace(t *testing.T) {
	t.Parallel()
	body := `
server:
  address: "example.com:9000"
workspace:
  path: "relative/path"
`
	path := writeYAML(t, "daemon.yaml", body)
	_, err := config.LoadDaemon(path)
	require.Error(t, err)
	assert.True(t, errors.Is(err, config.ErrWorkspacePathNotAbs))
}

func TestLoadServerOK(t *testing.T) {
	t.Parallel()
	body := `
listen: ":9000"
auth:
  tokens:
    "tok-alice": "alice"
    "tok-bob": "bob"
fuse:
  mountpoint: ` + escapeYAML(absMountpoint()) + `
cache:
  max_bytes: 268435456
log:
  level: "warn"
  format: "text"
`
	path := writeYAML(t, "server.yaml", body)
	cfg, err := config.LoadServer(path)
	require.NoError(t, err)
	assert.Equal(t, ":9000", cfg.Listen)
	assert.Len(t, cfg.Auth.Tokens, 2)
	assert.Equal(t, "alice", cfg.Auth.Tokens["tok-alice"])
	assert.Equal(t, absMountpoint(), cfg.FUSE.Mountpoint)
	assert.Equal(t, int64(268435456), cfg.Cache.MaxBytes)
	assert.Equal(t, "warn", cfg.Log.Level)
	assert.Equal(t, config.DefaultOfflineReadOnlyTTL, cfg.OfflineReadOnlyTTL)
}

func TestLoadServerMissingListen(t *testing.T) {
	t.Parallel()
	body := `
auth:
  tokens: { "t": "u" }
fuse:
  mountpoint: ` + escapeYAML(absMountpoint()) + `
`
	path := writeYAML(t, "server.yaml", body)
	_, err := config.LoadServer(path)
	require.Error(t, err)
	assert.True(t, errors.Is(err, config.ErrEmptyListen))
}

func TestLoadServerEmptyTokens(t *testing.T) {
	t.Parallel()
	body := `
listen: ":9000"
auth:
  tokens: {}
fuse:
  mountpoint: ` + escapeYAML(absMountpoint()) + `
`
	path := writeYAML(t, "server.yaml", body)
	_, err := config.LoadServer(path)
	require.Error(t, err)
	assert.True(t, errors.Is(err, config.ErrEmptyTokens))
}

func TestLoadServerRelativeMount(t *testing.T) {
	t.Parallel()
	body := `
listen: ":9000"
auth:
  tokens: { "t": "u" }
fuse:
  mountpoint: "rel/path"
`
	path := writeYAML(t, "server.yaml", body)
	_, err := config.LoadServer(path)
	require.Error(t, err)
	assert.True(t, errors.Is(err, config.ErrMountpointNotAbs))
}

func TestLoadServerExplicitOfflineReadonlyTTL(t *testing.T) {
	t.Parallel()
	body := `
listen: ":9000"
auth:
  tokens: { "t": "u" }
fuse:
  mountpoint: ` + escapeYAML(absMountpoint()) + `
offline_readonly_ttl: 0s
`
	path := writeYAML(t, "server.yaml", body)
	cfg, err := config.LoadServer(path)
	require.NoError(t, err)
	assert.Zero(t, cfg.OfflineReadOnlyTTL)
}

func TestLoadDaemonEnvOverride(t *testing.T) {
	body := `
server:
  address: "fallback:1"
workspace:
  path: ` + escapeYAML(absWorkspace()) + `
`
	path := writeYAML(t, "daemon.yaml", body)
	t.Setenv("RCLAUDE_SERVER_ADDRESS", "envhost:9999")
	cfg, err := config.LoadDaemon(path)
	require.NoError(t, err)
	assert.Equal(t, "envhost:9999", cfg.Server.Address)
}

func TestLoadDaemonFileMissing(t *testing.T) {
	t.Parallel()
	_, err := config.LoadDaemon(filepath.Join(t.TempDir(), "nope.yaml"))
	require.Error(t, err)
}

// escapeYAML 把可能含反斜杠的 Windows 路径包成 YAML 双引号字符串。
func escapeYAML(p string) string {
	// YAML 双引号串支持转义反斜杠，最简单的方式是把 \ 翻倍。
	out := make([]byte, 0, len(p)+4)
	out = append(out, '"')
	for i := range len(p) {
		c := p[i]
		if c == '\\' {
			out = append(out, '\\', '\\')
			continue
		}
		out = append(out, c)
	}
	out = append(out, '"')
	return string(out)
}
