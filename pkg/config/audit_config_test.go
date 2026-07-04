package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/config"
)

func daemonYAMLWithAudit(auditBody string) string {
	return `
server:
  address: "example.com:9000"
workspace:
  path: ` + escapeYAML(absWorkspace()) + `
audit:
` + auditBody
}

func TestLoadDaemonAuditDefaults(t *testing.T) {
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
	assert.False(t, cfg.Audit.Enabled)
	assert.Equal(t, config.DefaultAuditTable, cfg.Audit.Table)
	assert.Equal(t, config.DefaultAuditQueueSize, cfg.Audit.QueueSize)
}

func TestLoadDaemonAuditEnabled(t *testing.T) {
	t.Parallel()
	body := daemonYAMLWithAudit(`  enabled: true
  driver: "sqlite"
  dsn: "audit.db"
  table: "ops_audit"
  queue_size: 32
`)
	path := writeYAML(t, "daemon.yaml", body)
	cfg, err := config.LoadDaemon(path)
	require.NoError(t, err)
	assert.True(t, cfg.Audit.Enabled)
	assert.Equal(t, "sqlite", cfg.Audit.Driver)
	assert.Equal(t, "audit.db", cfg.Audit.DSN)
	assert.Equal(t, "ops_audit", cfg.Audit.Table)
	assert.Equal(t, 32, cfg.Audit.QueueSize)
}

func TestLoadDaemonAuditValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		err  error
	}{
		{
			name: "unsupported driver",
			body: daemonYAMLWithAudit(`  enabled: true
  driver: "oracle"
  dsn: "x"
`),
			err: config.ErrAuditDriverInvalid,
		},
		{
			name: "missing dsn",
			body: daemonYAMLWithAudit(`  enabled: true
  driver: "mysql"
`),
			err: config.ErrAuditDSNRequired,
		},
		{
			name: "invalid table",
			body: daemonYAMLWithAudit(`  enabled: true
  driver: "postgres"
  dsn: "postgres://localhost/db"
  table: "bad-table!"
`),
			err: config.ErrAuditTableInvalid,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeYAML(t, "daemon.yaml", tt.body)
			_, err := config.LoadDaemon(path)
			require.ErrorIs(t, err, tt.err)
		})
	}
}

func TestLoadDaemonAuditDisabledSkipsValidation(t *testing.T) {
	t.Parallel()
	body := daemonYAMLWithAudit(`  enabled: false
  driver: "oracle"
`)
	path := writeYAML(t, "daemon.yaml", body)
	cfg, err := config.LoadDaemon(path)
	require.NoError(t, err)
	assert.False(t, cfg.Audit.Enabled)
}

func TestIsSupportedAuditDriver(t *testing.T) {
	t.Parallel()
	for _, driver := range []string{"sqlite", "sqlite3", "mysql", "postgres", "postgresql", "pgsql", " SQLite "} {
		assert.True(t, config.IsSupportedAuditDriver(driver), driver)
	}
	for _, driver := range []string{"", "oracle", "mssql"} {
		assert.False(t, config.IsSupportedAuditDriver(driver), driver)
	}
}
