package syncer

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/audit"
)

// TestHandle_AuditEndToEndSQLite drives the full local audit chain:
// Handle -> Recorder queue -> SQLStore -> sqlite file.
func TestHandle_AuditEndToEndSQLite(t *testing.T) {
	root := t.TempDir()
	dsn := filepath.Join(t.TempDir(), "audit.db")

	store, err := audit.OpenSQL(context.Background(), audit.SQLOptions{
		Driver: "sqlite",
		DSN:    dsn,
	})
	require.NoError(t, err)
	recorder := audit.NewRecorder(store, 16, nil)

	opts := HandleOptions{Root: root, Auditor: recorder}
	writeResp := callHandle(&remotefsv1.FileRequest{
		RequestId: "req-1",
		Operation: &remotefsv1.FileRequest_Write{Write: &remotefsv1.WriteFileReq{
			Path:    "audited.txt",
			Content: []byte("hello"),
		}},
	}, opts)
	require.True(t, writeResp.GetSuccess(), writeResp.GetError())

	readResp := callHandle(&remotefsv1.FileRequest{
		RequestId: "req-2",
		Operation: &remotefsv1.FileRequest_Read{Read: &remotefsv1.ReadFileReq{
			Path: "audited.txt",
		}},
	}, opts)
	require.True(t, readResp.GetSuccess(), readResp.GetError())

	// Close drains the async queue before the store is closed.
	require.NoError(t, recorder.Close())

	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	rows, err := db.Query(
		"SELECT request_id, operation, path, bytes, success FROM file_audit_log ORDER BY id")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	type row struct {
		requestID string
		operation string
		path      string
		bytes     int64
		success   int
	}
	var got []row
	for rows.Next() {
		var r row
		require.NoError(t, rows.Scan(&r.requestID, &r.operation, &r.path, &r.bytes, &r.success))
		got = append(got, r)
	}
	require.NoError(t, rows.Err())

	require.Len(t, got, 2)
	assert.Equal(t, row{requestID: "req-1", operation: "write", path: "audited.txt", bytes: 5, success: 1}, got[0])
	assert.Equal(t, row{requestID: "req-2", operation: "read", path: "audited.txt", bytes: 5, success: 1}, got[1])
}
