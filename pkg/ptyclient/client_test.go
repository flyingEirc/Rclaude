package ptyclient_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/ptyclient"
)

var errConcurrentSend = errors.New("fakeStream: concurrent Send detected")

type fakeStream struct {
	sent     chan *remotefsv1.ClientFrame
	incoming chan *remotefsv1.ServerFrame
	closed   chan struct{}

	closeOnce sync.Once
	sendDelay time.Duration
	inFlight  atomic.Int32
}

func newFakeStream() *fakeStream {
	return &fakeStream{
		sent:     make(chan *remotefsv1.ClientFrame, 32),
		incoming: make(chan *remotefsv1.ServerFrame, 32),
		closed:   make(chan struct{}),
	}
}

func (f *fakeStream) Send(frame *remotefsv1.ClientFrame) error {
	if f.inFlight.Add(1) != 1 {
		f.inFlight.Add(-1)
		return errConcurrentSend
	}
	defer f.inFlight.Add(-1)

	if f.sendDelay > 0 {
		time.Sleep(f.sendDelay)
	}

	select {
	case <-f.closed:
		return io.EOF
	case f.sent <- frame:
		return nil
	}
}

func (f *fakeStream) Recv() (*remotefsv1.ServerFrame, error) {
	select {
	case <-f.closed:
		return nil, io.EOF
	case frame, ok := <-f.incoming:
		if !ok {
			return nil, io.EOF
		}
		return frame, nil
	}
}

func (f *fakeStream) CloseSend() error {
	f.closeOnce.Do(func() {
		close(f.closed)
	})
	return nil
}

func (f *fakeStream) pushAttached(sessionID, cwd string) {
	f.incoming <- &remotefsv1.ServerFrame{
		Payload: &remotefsv1.ServerFrame_Attached{
			Attached: &remotefsv1.Attached{SessionId: sessionID, Cwd: cwd},
		},
	}
}

func (f *fakeStream) pushStdout(out []byte) {
	f.incoming <- &remotefsv1.ServerFrame{
		Payload: &remotefsv1.ServerFrame_Stdout{Stdout: out},
	}
}

func (f *fakeStream) pushExited(code int32, signal uint32) {
	f.incoming <- &remotefsv1.ServerFrame{
		Payload: &remotefsv1.ServerFrame_Exited{
			Exited: &remotefsv1.Exited{Code: code, Signal: signal},
		},
	}
	close(f.incoming)
}

func (f *fakeStream) pushError(kind remotefsv1.Error_Kind, message string) {
	f.incoming <- &remotefsv1.ServerFrame{
		Payload: &remotefsv1.ServerFrame_Error{
			Error: &remotefsv1.Error{Kind: kind, Message: message},
		},
	}
	close(f.incoming)
}

func TestClientHappyPath(t *testing.T) {
	stream := newFakeStream()
	stdin := io.NopCloser(bytes.NewBufferString("hello\n"))
	var stdout bytes.Buffer

	client := ptyclient.New(ptyclient.Config{
		Stream: stream,
		Stdin:  stdin,
		Stdout: &stdout,
		Attach: ptyclient.AttachParams{
			InitialSize: ptyclient.WindowSize{Cols: 80, Rows: 24},
			Term:        "xterm-256color",
		},
	})

	go func() {
		stream.pushAttached("sess-1", "/workspace/alice")
		stream.pushStdout([]byte("world\n"))
		time.Sleep(20 * time.Millisecond)
		stream.pushExited(0, 0)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := client.Run(ctx)
	require.NoError(t, result.Err)
	require.Nil(t, result.ServerError)
	require.Equal(t, int32(0), result.Code)
	require.Equal(t, uint32(0), result.Signal)
	require.Contains(t, stdout.String(), "world\n")

	frames := drainSent(stream.sent)
	require.NotEmpty(t, frames)
	require.NotNil(t, frames[0].GetAttach())
	require.Equal(t, "xterm-256color", frames[0].GetAttach().GetTerm())
	require.Equal(t, uint32(80), frames[0].GetAttach().GetInitialSize().GetCols())
	require.Contains(t, sentStdinPayloads(frames), []byte("hello\n"))
}

func TestClientServerErrorBeforeAttached(t *testing.T) {
	stream := newFakeStream()
	client := ptyclient.New(ptyclient.Config{
		Stream: stream,
		Stdin:  io.NopCloser(bytes.NewReader(nil)),
		Stdout: io.Discard,
	})

	go stream.pushError(remotefsv1.Error_KIND_DAEMON_NOT_CONNECTED, "daemon offline")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := client.Run(ctx)
	require.NoError(t, result.Err)
	require.NotNil(t, result.ServerError)
	require.Equal(t, remotefsv1.Error_KIND_DAEMON_NOT_CONNECTED, result.ServerError.GetKind())
	require.Equal(t, "daemon offline", result.ServerError.GetMessage())
}

func TestClientFirstFrameWrongType(t *testing.T) {
	stream := newFakeStream()
	client := ptyclient.New(ptyclient.Config{
		Stream: stream,
		Stdin:  io.NopCloser(bytes.NewReader(nil)),
		Stdout: io.Discard,
	})

	go func() {
		stream.pushStdout([]byte("rogue"))
		close(stream.incoming)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := client.Run(ctx)
	require.ErrorIs(t, result.Err, ptyclient.ErrFirstFrameNotAttached)
	require.Nil(t, result.ServerError)
}

func TestClientStartSessionHonorsContextCancellation(t *testing.T) {
	stream := newFakeStream()
	client := ptyclient.New(ptyclient.Config{
		Stream: stream,
		Stdin:  io.NopCloser(bytes.NewReader(nil)),
		Stdout: io.Discard,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	result := client.Run(ctx)
	require.ErrorIs(t, result.Err, context.DeadlineExceeded)
	require.Nil(t, result.ServerError)
	require.Less(t, time.Since(start), 500*time.Millisecond)
}

func TestClientResizeForwarded(t *testing.T) {
	stream := newFakeStream()
	resizes := make(chan ptyclient.WindowSize, 1)

	client := ptyclient.New(ptyclient.Config{
		Stream:  stream,
		Stdin:   io.NopCloser(bytes.NewReader(nil)),
		Stdout:  io.Discard,
		Resizes: resizes,
		Attach:  ptyclient.AttachParams{InitialSize: ptyclient.WindowSize{Cols: 80, Rows: 24}},
	})

	go func() {
		stream.pushAttached("sess-2", "/workspace/alice")
		time.Sleep(20 * time.Millisecond)
		resizes <- ptyclient.WindowSize{Cols: 132, Rows: 50}
		time.Sleep(20 * time.Millisecond)
		stream.pushExited(0, 0)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := client.Run(ctx)
	require.NoError(t, result.Err)
	require.Nil(t, result.ServerError)

	frames := drainSent(stream.sent)
	require.True(t, hasResize(frames, 132, 50))
}

func TestClientSerializesAllSenders(t *testing.T) {
	stream := newFakeStream()
	stream.sendDelay = 30 * time.Millisecond

	resizes := make(chan ptyclient.WindowSize, 1)
	resizes <- ptyclient.WindowSize{Cols: 120, Rows: 40}

	client := ptyclient.New(ptyclient.Config{
		Stream:  stream,
		Stdin:   io.NopCloser(bytes.NewBuffer(make([]byte, 1024))),
		Stdout:  io.Discard,
		Resizes: resizes,
		Attach:  ptyclient.AttachParams{InitialSize: ptyclient.WindowSize{Cols: 80, Rows: 24}},
	})

	go func() {
		stream.pushAttached("sess-3", "/workspace/alice")
		time.Sleep(120 * time.Millisecond)
		stream.pushExited(0, 0)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := client.Run(ctx)
	require.NoError(t, result.Err)
	require.Nil(t, result.ServerError)

	frames := drainSent(stream.sent)
	require.GreaterOrEqual(t, len(frames), 3)
	require.NotNil(t, frames[0].GetAttach())
	require.True(t, hasResize(frames, 120, 40))
	require.NotEmpty(t, sentStdinPayloads(frames))
}

func drainSent(ch <-chan *remotefsv1.ClientFrame) []*remotefsv1.ClientFrame {
	var frames []*remotefsv1.ClientFrame
	for {
		select {
		case frame := <-ch:
			if frame == nil {
				return frames
			}
			frames = append(frames, frame)
		default:
			return frames
		}
	}
}

func hasResize(frames []*remotefsv1.ClientFrame, cols, rows uint32) bool {
	for _, frame := range frames {
		resize := frame.GetResize()
		if resize == nil {
			continue
		}
		if resize.GetCols() == cols && resize.GetRows() == rows {
			return true
		}
	}
	return false
}

func sentStdinPayloads(frames []*remotefsv1.ClientFrame) [][]byte {
	var payloads [][]byte
	for _, frame := range frames {
		if data := frame.GetStdin(); data != nil {
			payloads = append(payloads, data)
		}
	}
	return payloads
}
