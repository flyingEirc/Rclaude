//go:build unix

package ptyclient_test

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/ptyclient"
)

func TestSIGWINCHResizeEmitsOnSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := ptyclient.NewSIGWINCHResize(ctx, int(syscall.Stderr))

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive initial resize event")
	}

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGWINCH))

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive resize event after SIGWINCH")
	}
}
