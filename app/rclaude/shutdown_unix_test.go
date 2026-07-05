//go:build unix

package main

import (
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/logx"
)

// TestWatchShutdownSignalsCancelsOnSIGHUP proves that closing the terminal
// (SIGHUP) drives a graceful shutdown instead of killing the process: the
// watcher must invoke cancel so the daemon and PTY can wind down.
func TestWatchShutdownSignalsCancelsOnSIGHUP(t *testing.T) {
	var canceled atomic.Bool
	done := make(chan struct{})
	stop := watchShutdownSignals(logx.Nop(), func() {
		canceled.Store(true)
		close(done)
	})
	defer stop()

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGHUP))

	select {
	case <-done:
		require.True(t, canceled.Load())
	case <-time.After(2 * time.Second):
		t.Fatal("SIGHUP did not trigger a graceful cancel")
	}
}
