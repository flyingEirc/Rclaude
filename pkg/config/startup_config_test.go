package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/config"
)

func TestLoadDaemonStartupDefaults(t *testing.T) {
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
	assert.Equal(t, config.DefaultStartupMaxRetries, cfg.Startup.MaxRetries)
	assert.Equal(t, config.DefaultStartupRetryDelay, cfg.Startup.RetryDelay)
}

func TestLoadDaemonStartupExplicit(t *testing.T) {
	t.Parallel()
	body := `
server:
  address: "example.com:9000"
workspace:
  path: ` + escapeYAML(absWorkspace()) + `
startup:
  max_retries: 5
  retry_delay: 250ms
`
	path := writeYAML(t, "daemon.yaml", body)
	cfg, err := config.LoadDaemon(path)
	require.NoError(t, err)
	assert.Equal(t, 5, cfg.Startup.MaxRetries)
	assert.Equal(t, 250*time.Millisecond, cfg.Startup.RetryDelay)
}

func TestLoadDaemonStartupZeroRetriesAllowed(t *testing.T) {
	t.Parallel()
	body := `
server:
  address: "example.com:9000"
workspace:
  path: ` + escapeYAML(absWorkspace()) + `
startup:
  max_retries: 0
  retry_delay: 0s
`
	path := writeYAML(t, "daemon.yaml", body)
	cfg, err := config.LoadDaemon(path)
	require.NoError(t, err)
	assert.Zero(t, cfg.Startup.MaxRetries)
	assert.Zero(t, cfg.Startup.RetryDelay)
}

func TestLoadDaemonStartupRejectsNegativeRetries(t *testing.T) {
	t.Parallel()
	body := `
server:
  address: "example.com:9000"
workspace:
  path: ` + escapeYAML(absWorkspace()) + `
startup:
  max_retries: -1
`
	path := writeYAML(t, "daemon.yaml", body)
	_, err := config.LoadDaemon(path)
	require.ErrorIs(t, err, config.ErrStartupRetriesNegative)
}

func TestLoadDaemonStartupRejectsNegativeDelay(t *testing.T) {
	t.Parallel()
	body := `
server:
  address: "example.com:9000"
workspace:
  path: ` + escapeYAML(absWorkspace()) + `
startup:
  retry_delay: -1s
`
	path := writeYAML(t, "daemon.yaml", body)
	_, err := config.LoadDaemon(path)
	require.ErrorIs(t, err, config.ErrStartupDelayNegative)
}
