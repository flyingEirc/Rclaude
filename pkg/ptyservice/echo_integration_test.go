package ptyservice_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/internal/inmemtest"
)

func attachFramePredictive() *remotefsv1.ClientFrame {
	return &remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Attach{
			Attach: &remotefsv1.AttachReq{
				InitialSize:    &remotefsv1.Resize{Cols: 80, Rows: 24},
				Term:           "xterm-256color",
				PredictiveEcho: true,
			},
		},
	}
}

// TestAttach_IntegrationEchoAckWatermark verifies the mosh-style echo ack:
// the server confirms the capability in Attached, holds each stdin write for
// echoTimeout, and only then acknowledges it — after the echo bytes.
func TestAttach_IntegrationEchoAckWatermark(t *testing.T) {
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
	require.NoError(t, stream.Send(attachFramePredictive()))

	first, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, first.GetAttached())
	require.True(t, first.GetAttached().GetEchoAck(), "server must confirm echo-ack capability")

	sentAt := time.Now()
	require.NoError(t, stream.Send(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Stdin{Stdin: []byte("ab")},
	}))
	require.Eventually(t, func() bool {
		return host.StdinString() == "ab"
	}, time.Second, time.Millisecond)
	require.NoError(t, host.EmitStdout([]byte("ab")))

	var sawStdout bool
	for {
		frame, recvErr := stream.Recv()
		require.NoError(t, recvErr)
		if frame.GetStdout() != nil {
			sawStdout = true
			continue
		}
		ack := frame.GetEchoAck()
		require.NotNil(t, ack, "unexpected frame before echo ack: %v", frame)
		elapsed := time.Since(sentAt)
		assert.True(t, sawStdout, "echo bytes must precede their ack in the stream")
		assert.EqualValues(t, 2, ack.GetOffset())
		assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond,
			"ack must be held for the echo timeout, got %v", elapsed)
		break
	}

	require.NoError(t, stream.Send(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Detach{Detach: &remotefsv1.Detach{}},
	}))
}

// TestAttach_IntegrationNoEchoAckWithoutOptIn pins the compatibility default:
// clients that do not request predictive echo see the old wire behavior.
func TestAttach_IntegrationNoEchoAckWithoutOptIn(t *testing.T) {
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
	require.False(t, first.GetAttached().GetEchoAck())

	require.NoError(t, stream.Send(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Stdin{Stdin: []byte("ab")},
	}))
	require.Eventually(t, func() bool {
		return host.StdinString() == "ab"
	}, time.Second, time.Millisecond)
	require.NoError(t, host.EmitStdout([]byte("ab")))

	frame, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, frame.GetStdout(), "plain sessions still stream stdout only")

	// No EchoAck may follow within a generous window.
	waitCtx, waitCancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer waitCancel()
	type recvResult struct {
		frame *remotefsv1.ServerFrame
		err   error
	}
	got := make(chan recvResult, 1)
	go func() {
		f, recvErr := stream.Recv()
		got <- recvResult{frame: f, err: recvErr}
	}()
	select {
	case <-waitCtx.Done():
		// Expected: silence.
	case result := <-got:
		require.NoError(t, result.err)
		require.Nil(t, result.frame.GetEchoAck(), "no echo ack without opt-in")
	}

	require.NoError(t, stream.Send(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Detach{Detach: &remotefsv1.Detach{}},
	}))
}
