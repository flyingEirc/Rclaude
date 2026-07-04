package syncer

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/internal/testutil"
	"flyingEirc/Rclaude/pkg/logx"
)

func TestRun_StartupFailFastReturnsFirstError(t *testing.T) {
	root := testutil.NewTempWorkspace(t, map[string]string{
		"main.go": "package main",
	})

	var attempts atomic.Int32
	dialErr := errors.New("dialer: refused")
	dialer := func(context.Context, string) (net.Conn, error) {
		attempts.Add(1)
		return nil, dialErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := Run(ctx, RunOptions{
		Config:          testDaemonConfig(root, "passthrough:///failfast", "token"),
		Logger:          logx.Nop(),
		Dialer:          dialer,
		StartupFailFast: true,
	})
	require.Error(t, err)
	require.NoError(t, ctx.Err(), "Run must fail fast, not wait for ctx timeout")
	// gRPC 懒拨号会把底层错误封进 status 字符串，无法 errors.Is 匹配原始错误。
	assert.ErrorContains(t, err, "dialer: refused")
	assert.GreaterOrEqual(t, attempts.Load(), int32(1))
}

func TestRun_OnReadyFiresOnceOnFirstSession(t *testing.T) {
	root := testutil.NewTempWorkspace(t, map[string]string{
		"main.go": "package main",
	})
	server := testutil.NewRecordingServer()
	dialer := testutil.NewBufconnServer(t, server)

	restore := overrideDaemonTimings(t, time.Hour, 10*time.Millisecond, 20*time.Millisecond)
	defer restore()

	var readyCount atomic.Int32

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, RunOptions{
			Config:          testDaemonConfig(root, "passthrough:///onready", "token"),
			Logger:          logx.Nop(),
			Dialer:          dialer,
			OnReady:         func() { readyCount.Add(1) },
			StartupFailFast: true,
		})
	}()

	waitForReady(t, server.WaitReady())
	waitForRecordedMessage(t, server, func(msg *remotefsv1.DaemonMessage) bool {
		return msg.GetFileTree() != nil
	})
	assert.Equal(t, int32(1), readyCount.Load())

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}
