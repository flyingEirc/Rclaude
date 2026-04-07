package inmemtest

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/internal/testutil"
	"flyingEirc/Rclaude/pkg/session"
	"flyingEirc/Rclaude/pkg/syncer"
)

type Pair struct {
	Manager   *session.Manager
	Session   *session.Session
	UserID    string
	DaemonDir string
	Cleanup   func()

	stream    *testutil.MockConnectStream
	readCount atomic.Int64
}

func Start(t *testing.T, daemonRoot string) *Pair {
	t.Helper()

	daemonRoot = ensureDaemonRoot(t, daemonRoot)

	manager := session.NewManager(session.ManagerOptions{
		RequestTimeout: 5 * time.Second,
		CacheMaxBytes:  1 << 20,
	})
	userID := "u-test"
	current := manager.NewSession(userID)
	_, err := manager.Register(current)
	require.NoError(t, err)
	require.NoError(t, current.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{}},
	}))

	ctx, cancel := context.WithCancel(context.Background())
	stream := testutil.NewMockConnectStream(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- current.Serve(ctx, stream)
	}()

	pair := &Pair{
		Manager:   manager,
		Session:   current,
		UserID:    userID,
		DaemonDir: daemonRoot,
		stream:    stream,
	}
	done := startHandleLoop(ctx, pair, stream, syncer.HandleOptions{Root: daemonRoot})

	pair.Cleanup = func() {
		cancel()
		<-done
		<-errCh
		manager.Remove(current)
	}
	return pair
}

func (p *Pair) AbsPath(rel string) string {
	return filepath.Join(p.DaemonDir, rel)
}

func (p *Pair) PushChange(change *remotefsv1.FileChange) {
	if p == nil || p.stream == nil {
		return
	}
	p.stream.PushRecv(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_Change{Change: change},
	})
}

func (p *Pair) ReadRequestCount() int64 {
	if p == nil {
		return 0
	}
	return p.readCount.Load()
}

func ensureDaemonRoot(t *testing.T, daemonRoot string) string {
	t.Helper()

	if daemonRoot == "" {
		return t.TempDir()
	}
	require.NoError(t, os.MkdirAll(daemonRoot, 0o750))
	return daemonRoot
}

func startHandleLoop(
	ctx context.Context,
	pair *Pair,
	stream *testutil.MockConnectStream,
	handleOpts syncer.HandleOptions,
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msg, err := stream.AwaitSend(50 * time.Millisecond)
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					continue
				}
			}
			req := msg.GetRequest()
			if req == nil {
				continue
			}
			if req.GetRead() != nil {
				pair.readCount.Add(1)
			}
			resp := syncer.Handle(req, handleOpts)
			stream.PushRecv(&remotefsv1.DaemonMessage{
				Msg: &remotefsv1.DaemonMessage_Response{Response: resp},
			})
		}
	}()
	return done
}
