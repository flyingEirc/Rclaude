package syncer

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/audit"
)

type captureAuditor struct {
	mu      sync.Mutex
	records []*audit.Record
}

func (c *captureAuditor) Record(rec *audit.Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, rec)
}

func (c *captureAuditor) last(t *testing.T) *audit.Record {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	require.NotEmpty(t, c.records)
	return c.records[len(c.records)-1]
}

func auditedHandle(t *testing.T, root string, req *remotefsv1.FileRequest) *audit.Record {
	t.Helper()
	auditor := &captureAuditor{}
	callHandle(req, HandleOptions{Root: root, Auditor: auditor})
	return auditor.last(t)
}

func TestHandle_Audit_Write(t *testing.T) {
	root := t.TempDir()
	rec := auditedHandle(t, root, &remotefsv1.FileRequest{
		RequestId: "req-w",
		Operation: &remotefsv1.FileRequest_Write{Write: &remotefsv1.WriteFileReq{
			Path:    "a.txt",
			Content: []byte("hello"),
		}},
	})

	assert.Equal(t, "write", rec.Operation)
	assert.Equal(t, "a.txt", rec.Path)
	assert.Equal(t, "req-w", rec.RequestID)
	assert.EqualValues(t, 5, rec.Bytes)
	assert.True(t, rec.Success)
	assert.Empty(t, rec.Error)
	assert.False(t, rec.Time.IsZero())
}

func TestHandle_Audit_Read(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o600))

	rec := auditedHandle(t, root, &remotefsv1.FileRequest{
		RequestId: "req-r",
		Operation: &remotefsv1.FileRequest_Read{Read: &remotefsv1.ReadFileReq{Path: "a.txt"}},
	})

	assert.Equal(t, "read", rec.Operation)
	assert.Equal(t, "a.txt", rec.Path)
	assert.EqualValues(t, 5, rec.Bytes)
	assert.True(t, rec.Success)
}

func TestHandle_Audit_ReadFailure(t *testing.T) {
	rec := auditedHandle(t, t.TempDir(), &remotefsv1.FileRequest{
		RequestId: "req-miss",
		Operation: &remotefsv1.FileRequest_Read{Read: &remotefsv1.ReadFileReq{Path: "missing.txt"}},
	})

	assert.Equal(t, "read", rec.Operation)
	assert.Equal(t, "missing.txt", rec.Path)
	assert.Zero(t, rec.Bytes)
	assert.False(t, rec.Success)
	assert.NotEmpty(t, rec.Error)
}

func TestHandle_Audit_Rename(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "old.txt"), []byte("x"), 0o600))

	rec := auditedHandle(t, root, &remotefsv1.FileRequest{
		RequestId: "req-mv",
		Operation: &remotefsv1.FileRequest_Rename{Rename: &remotefsv1.RenameReq{
			OldPath: "old.txt",
			NewPath: "new.txt",
		}},
	})

	assert.Equal(t, "rename", rec.Operation)
	assert.Equal(t, "old.txt", rec.Path)
	assert.Equal(t, "new.txt", rec.Target)
	assert.True(t, rec.Success)
}

func TestHandle_Audit_OtherOperations(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "f.txt"), []byte("abcdef"), 0o600))

	tests := []struct {
		name    string
		req     *remotefsv1.FileRequest
		op      string
		path    string
		bytes   int64
		success bool
	}{
		{
			name: "stat",
			req: &remotefsv1.FileRequest{Operation: &remotefsv1.FileRequest_Stat{
				Stat: &remotefsv1.StatReq{Path: "f.txt"},
			}},
			op: "stat", path: "f.txt", success: true,
		},
		{
			name: "list_dir",
			req: &remotefsv1.FileRequest{Operation: &remotefsv1.FileRequest_ListDir{
				ListDir: &remotefsv1.ListDirReq{Path: "."},
			}},
			op: "list_dir", path: ".", success: true,
		},
		{
			name: "mkdir",
			req: &remotefsv1.FileRequest{Operation: &remotefsv1.FileRequest_Mkdir{
				Mkdir: &remotefsv1.MkdirReq{Path: "sub"},
			}},
			op: "mkdir", path: "sub", success: true,
		},
		{
			name: "truncate",
			req: &remotefsv1.FileRequest{Operation: &remotefsv1.FileRequest_Truncate{
				Truncate: &remotefsv1.TruncateReq{Path: "f.txt", Size: 3},
			}},
			op: "truncate", path: "f.txt", bytes: 3, success: true,
		},
		{
			name: "delete",
			req: &remotefsv1.FileRequest{Operation: &remotefsv1.FileRequest_Delete{
				Delete: &remotefsv1.DeleteReq{Path: "f.txt"},
			}},
			op: "delete", path: "f.txt", success: true,
		},
		{
			name:    "unknown",
			req:     &remotefsv1.FileRequest{},
			op:      "unknown",
			success: false,
		},
	}
	for _, tt := range tests {
		rec := auditedHandle(t, root, tt.req)
		assert.Equal(t, tt.op, rec.Operation, tt.name)
		assert.Equal(t, tt.path, rec.Path, tt.name)
		assert.Equal(t, tt.bytes, rec.Bytes, tt.name)
		assert.Equal(t, tt.success, rec.Success, tt.name)
	}
}

func TestHandle_NoAuditorIsSafe(t *testing.T) {
	resp := callHandle(&remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Mkdir{Mkdir: &remotefsv1.MkdirReq{Path: "sub"}},
	}, HandleOptions{Root: t.TempDir()})
	assert.True(t, resp.GetSuccess())
}
