package audit_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/audit"
)

type fakeStore struct {
	mu      sync.Mutex
	records []*audit.Record
	closed  bool
	saveErr error
}

func (f *fakeStore) Save(_ context.Context, rec *audit.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.records = append(f.records, rec)
	return nil
}

func (f *fakeStore) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeStore) snapshot() ([]*audit.Record, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*audit.Record(nil), f.records...), f.closed
}

func TestRecorder_DrainsQueueOnClose(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	rec := audit.NewRecorder(store, 16, nil)

	for i := range 5 {
		rec.Record(&audit.Record{Operation: "write", Path: "p", Bytes: int64(i)})
	}
	require.NoError(t, rec.Close())

	records, closed := store.snapshot()
	assert.Len(t, records, 5)
	assert.True(t, closed)
}

func TestRecorder_RecordAfterCloseIsNoop(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	rec := audit.NewRecorder(store, 16, nil)
	require.NoError(t, rec.Close())

	rec.Record(&audit.Record{Operation: "read", Path: "p"})
	records, _ := store.snapshot()
	assert.Empty(t, records)
}

func TestRecorder_DoubleCloseIsSafe(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	rec := audit.NewRecorder(store, 16, nil)
	require.NoError(t, rec.Close())
	require.NoError(t, rec.Close())
}

func TestRecorder_SaveErrorDoesNotStopWorker(t *testing.T) {
	t.Parallel()
	store := &fakeStore{saveErr: errors.New("db down")}
	rec := audit.NewRecorder(store, 16, nil)

	rec.Record(&audit.Record{Operation: "write", Path: "p"})
	// The worker must survive a failing save and still terminate cleanly.
	errCh := make(chan error, 1)
	go func() {
		errCh <- rec.Close()
	}()
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("recorder did not shut down after save error")
	}
}

func TestRecorder_NilRecordIgnored(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	rec := audit.NewRecorder(store, 16, nil)
	rec.Record(nil)
	require.NoError(t, rec.Close())
	records, _ := store.snapshot()
	assert.Empty(t, records)
}
