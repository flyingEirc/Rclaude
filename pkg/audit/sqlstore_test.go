package audit_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/audit"
)

func sqliteDSN(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "audit.db")
}

func TestOpenSQL_SQLiteSaveAndQuery(t *testing.T) {
	t.Parallel()
	dsn := sqliteDSN(t)
	store, err := audit.OpenSQL(context.Background(), audit.SQLOptions{
		Driver: "sqlite",
		DSN:    dsn,
	})
	require.NoError(t, err)

	rec := &audit.Record{
		Time:      time.UnixMilli(1751600000000),
		RequestID: "req-1",
		Operation: "write",
		Path:      "dir/a.txt",
		Bytes:     5,
		Success:   true,
	}
	require.NoError(t, store.Save(context.Background(), rec))
	require.NoError(t, store.Save(context.Background(), &audit.Record{
		Time:      time.UnixMilli(1751600001000),
		RequestID: "req-2",
		Operation: "rename",
		Path:      "dir/a.txt",
		Target:    "dir/b.txt",
		Success:   false,
		Error:     "boom",
	}))
	require.NoError(t, store.Close())

	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	rows, err := db.Query(
		"SELECT created_at_ms, request_id, operation, path, target, bytes, success, error" +
			" FROM file_audit_log ORDER BY id")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	type row struct {
		createdAtMS int64
		requestID   string
		operation   string
		path        string
		target      string
		bytes       int64
		success     int
		errMsg      string
	}
	var got []row
	for rows.Next() {
		var r row
		require.NoError(t, rows.Scan(
			&r.createdAtMS, &r.requestID, &r.operation,
			&r.path, &r.target, &r.bytes, &r.success, &r.errMsg))
		got = append(got, r)
	}
	require.NoError(t, rows.Err())
	require.Len(t, got, 2)

	assert.Equal(t, row{
		createdAtMS: 1751600000000,
		requestID:   "req-1",
		operation:   "write",
		path:        "dir/a.txt",
		bytes:       5,
		success:     1,
	}, got[0])
	assert.Equal(t, row{
		createdAtMS: 1751600001000,
		requestID:   "req-2",
		operation:   "rename",
		path:        "dir/a.txt",
		target:      "dir/b.txt",
		success:     0,
		errMsg:      "boom",
	}, got[1])
}

func TestOpenSQL_DriverAliases(t *testing.T) {
	t.Parallel()
	for _, driver := range []string{"sqlite", "sqlite3", "SQLite"} {
		store, err := audit.OpenSQL(context.Background(), audit.SQLOptions{
			Driver: driver,
			DSN:    sqliteDSN(t),
		})
		require.NoError(t, err, driver)
		require.NoError(t, store.Close())
	}
}

func TestOpenSQL_CustomTable(t *testing.T) {
	t.Parallel()
	dsn := sqliteDSN(t)
	store, err := audit.OpenSQL(context.Background(), audit.SQLOptions{
		Driver: "sqlite",
		DSN:    dsn,
		Table:  "my_audit",
	})
	require.NoError(t, err)
	require.NoError(t, store.Save(context.Background(), &audit.Record{
		Time:      time.UnixMilli(1),
		Operation: "read",
		Path:      "x",
		Success:   true,
	}))
	require.NoError(t, store.Close())

	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM my_audit").Scan(&count))
	assert.Equal(t, 1, count)
}

func TestOpenSQL_ReopenExistingTable(t *testing.T) {
	t.Parallel()
	dsn := sqliteDSN(t)
	for range 2 {
		store, err := audit.OpenSQL(context.Background(), audit.SQLOptions{
			Driver: "sqlite",
			DSN:    dsn,
		})
		require.NoError(t, err)
		require.NoError(t, store.Close())
	}
}

func TestOpenSQL_UnsupportedDriver(t *testing.T) {
	t.Parallel()
	_, err := audit.OpenSQL(context.Background(), audit.SQLOptions{
		Driver: "oracle",
		DSN:    "whatever",
	})
	require.ErrorIs(t, err, audit.ErrUnsupportedDriver)
}

func TestOpenSQL_InvalidTable(t *testing.T) {
	t.Parallel()
	_, err := audit.OpenSQL(context.Background(), audit.SQLOptions{
		Driver: "sqlite",
		DSN:    sqliteDSN(t),
		Table:  "bad-name; DROP TABLE x",
	})
	require.ErrorIs(t, err, audit.ErrInvalidTable)
}
