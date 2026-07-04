package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/config"
)

func TestLoadDaemonLogFileSettings(t *testing.T) {
	t.Parallel()
	body := `
server:
  address: "example.com:9000"
workspace:
  path: ` + escapeYAML(absWorkspace()) + `
log:
  level: "debug"
  format: "json"
  dir: "logs/daemon"
  max_size_mb: 10
  max_backups: 5
  max_age_days: 30
`
	path := writeYAML(t, "daemon.yaml", body)
	cfg, err := config.LoadDaemon(path)
	require.NoError(t, err)
	assert.Equal(t, "debug", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)
	assert.Equal(t, "logs/daemon", cfg.Log.Dir)
	assert.Equal(t, 10, cfg.Log.MaxSizeMB)
	assert.Equal(t, 5, cfg.Log.MaxBackups)
	assert.Equal(t, 30, cfg.Log.MaxAgeDays)
}

func TestLoadDaemonLogDefaultsEmpty(t *testing.T) {
	t.Parallel()
	body := `
server:
  address: "example.com:9000"
workspace:
  path: ` + escapeYAML(absWorkspace()) + `
`
	path := writeYAML(t, "daemon.yaml", body)
	cfg, err := config.LoadDaemon(path)
	require.NoError(t, err)
	// 空值交由 logx 兜底：目录用 DefaultDir，轮转参数用默认值。
	assert.Empty(t, cfg.Log.Dir)
	assert.Zero(t, cfg.Log.MaxSizeMB)
}
