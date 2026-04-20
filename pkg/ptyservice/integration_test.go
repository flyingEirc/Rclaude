package ptyservice_test

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/internal/inmemtest"
	"flyingEirc/Rclaude/pkg/ptyhost"
)

func TestAttach_IntegrationHappyPath(t *testing.T) {
	host := inmemtest.NewPTYHost()
	spawner := inmemtest.NewPTYSpawner(host)
	harness := inmemtest.NewPTYHarness(t, inmemtest.PTYHarnessOptions{
		UserID:  "alice",
		Spawner: spawner,
	})
	harness.ConnectDaemon(context.Background())

	client, _ := harness.NewPTYClient()
	ctx, cancel := context.WithTimeout(harness.ClientContext(context.Background()), 5*time.Second)
	defer cancel()

	stream, err := client.Attach(ctx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(attachFrame()))

	first, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, first.GetAttached())
	assert.Equal(t, "alice-pty-1", first.GetAttached().GetSessionId())
	assert.Equal(t, filepath.Join(harness.WorkspaceRoot, harness.UserID), first.GetAttached().GetCwd())

	requests := spawner.Requests()
	require.Len(t, requests, 1)
	assert.Equal(t, filepath.Join(harness.WorkspaceRoot, harness.UserID), requests[0].Cwd)
	assert.Equal(t, ptyhost.WindowSize{Cols: 80, Rows: 24}, requests[0].InitSize)
	assert.Contains(t, requests[0].Env, "TERM=xterm-256color")

	require.NoError(t, stream.Send(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Stdin{Stdin: []byte("hello\n")},
	}))
	require.Eventually(t, func() bool {
		return host.StdinString() == "hello\n"
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, stream.Send(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Resize{Resize: &remotefsv1.Resize{Cols: 120, Rows: 50}},
	}))
	require.Eventually(t, func() bool {
		return host.LastResize() == (ptyhost.WindowSize{Cols: 120, Rows: 50})
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, host.EmitStdout([]byte("world\n")))

	stdoutFrame, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, []byte("world\n"), stdoutFrame.GetStdout())

	require.NoError(t, stream.Send(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Detach{Detach: &remotefsv1.Detach{}},
	}))

	exitedFrame, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, exitedFrame.GetExited())
	assert.EqualValues(t, 0, exitedFrame.GetExited().GetCode())
	assert.Equal(t, 1, host.ShutdownCalls())
}

func TestAttach_IntegrationSessionBusyReleasedAfterPeerEOF(t *testing.T) {
	host1 := inmemtest.NewPTYHost()
	host2 := inmemtest.NewPTYHost()
	spawner := inmemtest.NewPTYSpawner(host1, host2)
	harness := inmemtest.NewPTYHarness(t, inmemtest.PTYHarnessOptions{
		UserID:  "alice",
		Spawner: spawner,
	})
	harness.ConnectDaemon(context.Background())

	client1, _ := harness.NewPTYClient()
	ctx1, cancel1 := context.WithTimeout(harness.ClientContext(context.Background()), 5*time.Second)
	defer cancel1()

	stream1, err := client1.Attach(ctx1)
	require.NoError(t, err)
	require.NoError(t, stream1.Send(attachFrame()))
	first, err := stream1.Recv()
	require.NoError(t, err)
	require.NotNil(t, first.GetAttached())

	client2, _ := harness.NewPTYClient()
	ctx2, cancel2 := context.WithTimeout(harness.ClientContext(context.Background()), 5*time.Second)
	defer cancel2()

	stream2, err := client2.Attach(ctx2)
	require.NoError(t, err)
	require.NoError(t, stream2.Send(attachFrame()))

	busyFrame, err := stream2.Recv()
	require.NoError(t, err)
	require.NotNil(t, busyFrame.GetError())
	assert.Equal(t, remotefsv1.Error_KIND_SESSION_BUSY, busyFrame.GetError().GetKind())

	_, err = stream2.Recv()
	assert.ErrorIs(t, err, io.EOF)

	require.NoError(t, stream1.CloseSend())
	_, err = stream1.Recv()
	assert.ErrorIs(t, err, io.EOF)
	require.Eventually(t, func() bool {
		return host1.ShutdownCalls() == 1
	}, time.Second, 10*time.Millisecond)

	client3, _ := harness.NewPTYClient()
	ctx3, cancel3 := context.WithTimeout(harness.ClientContext(context.Background()), 5*time.Second)
	defer cancel3()

	stream3, err := client3.Attach(ctx3)
	require.NoError(t, err)
	require.NoError(t, stream3.Send(attachFrame()))

	retryFrame, err := stream3.Recv()
	require.NoError(t, err)
	require.NotNil(t, retryFrame.GetAttached())
	assert.Equal(t, "alice-pty-2", retryFrame.GetAttached().GetSessionId())

	require.NoError(t, stream3.Send(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Detach{Detach: &remotefsv1.Detach{}},
	}))
	_, err = stream3.Recv()
	require.NoError(t, err)

	requests := spawner.Requests()
	require.Len(t, requests, 2)
}

func TestAttach_IntegrationOversizeStdinReturnsProtocolError(t *testing.T) {
	host := inmemtest.NewPTYHost()
	spawner := inmemtest.NewPTYSpawner(host)
	harness := inmemtest.NewPTYHarness(t, inmemtest.PTYHarnessOptions{
		UserID:   "alice",
		Spawner:  spawner,
		FrameMax: 8,
	})
	harness.ConnectDaemon(context.Background())

	client, _ := harness.NewPTYClient()
	ctx, cancel := context.WithTimeout(harness.ClientContext(context.Background()), 5*time.Second)
	defer cancel()

	stream, err := client.Attach(ctx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(attachFrame()))

	first, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, first.GetAttached())

	require.NoError(t, stream.Send(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Stdin{Stdin: []byte("123456789")},
	}))

	errFrame, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, errFrame.GetError())
	assert.Equal(t, remotefsv1.Error_KIND_PROTOCOL, errFrame.GetError().GetKind())
	assert.Empty(t, host.StdinString())
	require.Eventually(t, func() bool {
		return host.ShutdownCalls() == 1
	}, time.Second, 10*time.Millisecond)

	_, err = stream.Recv()
	assert.ErrorIs(t, err, io.EOF)
}
