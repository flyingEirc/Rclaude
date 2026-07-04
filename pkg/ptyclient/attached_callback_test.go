package ptyclient_test

import (
	"bytes"
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/ptyclient"
)

func TestClientOnAttachedFiresAfterHandshake(t *testing.T) {
	stream := newFakeStream()
	var attached atomic.Int32

	client := ptyclient.New(ptyclient.Config{
		Stream:     stream,
		Stdin:      io.NopCloser(bytes.NewReader(nil)),
		Stdout:     io.Discard,
		OnAttached: func() { attached.Add(1) },
	})

	go func() {
		stream.pushAttached("sess-1", "/workspace/alice")
		time.Sleep(20 * time.Millisecond)
		stream.pushExited(0, 0)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := client.Run(ctx)
	require.NoError(t, result.Err)
	require.Equal(t, int32(1), attached.Load())
}

func TestClientOnAttachedSkippedOnServerError(t *testing.T) {
	stream := newFakeStream()
	var attached atomic.Int32

	client := ptyclient.New(ptyclient.Config{
		Stream:     stream,
		Stdin:      io.NopCloser(bytes.NewReader(nil)),
		Stdout:     io.Discard,
		OnAttached: func() { attached.Add(1) },
	})

	go stream.pushError(remotefsv1.Error_KIND_DAEMON_NOT_CONNECTED, "daemon offline")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := client.Run(ctx)
	require.NotNil(t, result.ServerError)
	require.Equal(t, int32(0), attached.Load())
}
