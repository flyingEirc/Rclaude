package audit

import (
	"context"
	"time"
)

// Record describes one remote file operation executed on the local workspace.
type Record struct {
	// Time is when the operation was handled locally.
	Time time.Time
	// RequestID is the remote request identifier, if any.
	RequestID string
	// Operation is the operation kind: read, stat, list_dir, write, delete,
	// mkdir, rename, truncate or unknown.
	Operation string
	// Path is the workspace-relative path the operation targeted.
	Path string
	// Target is the rename destination path; empty for other operations.
	Target string
	// Bytes is the payload size: bytes returned for read, bytes received for
	// write, the requested size for truncate, and zero otherwise.
	Bytes int64
	// Success reports whether the operation succeeded.
	Success bool
	// Error holds the failure message when Success is false.
	Error string
}

// Store persists audit records.
type Store interface {
	// Save writes one record. Implementations must be safe for use from a
	// single writer goroutine.
	Save(ctx context.Context, rec *Record) error
	// Close releases the underlying resources.
	Close() error
}
