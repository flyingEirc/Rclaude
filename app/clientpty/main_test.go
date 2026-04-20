package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/config"
	"flyingEirc/Rclaude/pkg/ptyclient"
)

type fakeTerminal struct {
	tty        bool
	prepareErr error
	session    terminalSession
}

func (f fakeTerminal) IsTerminal(int) bool {
	return f.tty
}

func (f fakeTerminal) Prepare(context.Context, int, int) (terminalSession, error) {
	if f.prepareErr != nil {
		return terminalSession{}, f.prepareErr
	}
	return f.session, nil
}

type fakeStream struct {
	sent     chan *remotefsv1.ClientFrame
	incoming chan *remotefsv1.ServerFrame
	closed   chan struct{}

	closeOnce sync.Once
}

type spyReadCloser struct {
	reader io.Reader
	closed bool
}

func (s *spyReadCloser) Read(p []byte) (int, error) {
	return s.reader.Read(p)
}

func (s *spyReadCloser) Close() error {
	s.closed = true
	return nil
}

func newFakeStream() *fakeStream {
	return &fakeStream{
		sent:     make(chan *remotefsv1.ClientFrame, 32),
		incoming: make(chan *remotefsv1.ServerFrame, 32),
		closed:   make(chan struct{}),
	}
}

func (f *fakeStream) Send(frame *remotefsv1.ClientFrame) error {
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

func TestRunCommandReusesDaemonConfigAndBridgesPTY(t *testing.T) {
	stream := newFakeStream()
	stdin := &spyReadCloser{reader: bytes.NewBufferString("hello from cli")}
	var stdout bytes.Buffer

	var gotAddress string
	var gotToken string
	restoreCalled := false

	deps := commandDeps{
		stdin:  stdin,
		stdout: &stdout,
		loadConfig: func(string) (loadedConfig, error) {
			return loadedConfig{
				Address:  "example.com:9326",
				Token:    "tok-auth",
				FrameMax: 32,
			}, nil
		},
		terminal: fakeTerminal{
			tty: true,
			session: terminalSession{
				InitialSize: ptyclient.WindowSize{Cols: 120, Rows: 40},
				Resizes:     closedResizeCh(),
				Restore: func() error {
					restoreCalled = true
					return nil
				},
			},
		},
		dialPTY: func(_ context.Context, cfg dialConfig) (ptyclient.Stream, io.Closer, error) {
			gotAddress = cfg.Address
			gotToken = cfg.Token
			return stream, io.NopCloser(bytes.NewReader(nil)), nil
		},
		stdinFD:  0,
		stdoutFD: 1,
		termName: "screen-256color",
	}

	go func() {
		stream.pushAttached("sess-1", "/workspace/demo")
		stream.pushStdout([]byte("remote-ready\n"))
		time.Sleep(20 * time.Millisecond)
		stream.pushExited(0, 0)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := runCommand(ctx, deps, "daemon.yaml")
	require.NoError(t, err)

	assert.Equal(t, "example.com:9326", gotAddress)
	assert.Equal(t, "tok-auth", gotToken)
	assert.Contains(t, stdout.String(), "remote-ready\n")
	assert.True(t, restoreCalled)
	assert.False(t, stdin.closed)

	frames := drainSentFrames(stream.sent)
	require.NotEmpty(t, frames)
	require.NotNil(t, frames[0].GetAttach())
	assert.Equal(t, "screen-256color", frames[0].GetAttach().GetTerm())
	assert.Equal(t, uint32(120), frames[0].GetAttach().GetInitialSize().GetCols())
	assert.Equal(t, uint32(40), frames[0].GetAttach().GetInitialSize().GetRows())
	assert.Contains(t, sentStdinPayloads(frames), []byte("hello from cli"))
}

func TestRunCommandMapsServerErrorToExitStatus(t *testing.T) {
	stream := newFakeStream()

	deps := commandDeps{
		stdin:  io.NopCloser(bytes.NewReader(nil)),
		stdout: io.Discard,
		loadConfig: func(string) (loadedConfig, error) {
			return loadedConfig{Address: "example.com:9326", Token: "tok-auth", FrameMax: 64}, nil
		},
		terminal: fakeTerminal{
			tty: true,
			session: terminalSession{
				InitialSize: ptyclient.WindowSize{Cols: 80, Rows: 24},
				Resizes:     closedResizeCh(),
				Restore:     func() error { return nil },
			},
		},
		dialPTY: func(context.Context, dialConfig) (ptyclient.Stream, io.Closer, error) {
			return stream, io.NopCloser(bytes.NewReader(nil)), nil
		},
		stdinFD:  0,
		stdoutFD: 1,
		termName: "xterm-256color",
	}

	go stream.pushError(remotefsv1.Error_KIND_DAEMON_NOT_CONNECTED, "daemon offline")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := runCommand(ctx, deps, "daemon.yaml")
	require.Error(t, err)

	var exitErr *exitStatus
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 2, exitErr.code)
	assert.Equal(t, "daemon offline", exitErr.message)
}

func TestRunCommandMapsRateLimitedServerErrorToExitStatus(t *testing.T) {
	stream := newFakeStream()

	deps := commandDeps{
		stdin:  io.NopCloser(bytes.NewReader(nil)),
		stdout: io.Discard,
		loadConfig: func(string) (loadedConfig, error) {
			return loadedConfig{Address: "example.com:9326", Token: "tok-auth", FrameMax: 64}, nil
		},
		terminal: fakeTerminal{
			tty: true,
			session: terminalSession{
				InitialSize: ptyclient.WindowSize{Cols: 80, Rows: 24},
				Resizes:     closedResizeCh(),
				Restore:     func() error { return nil },
			},
		},
		dialPTY: func(context.Context, dialConfig) (ptyclient.Stream, io.Closer, error) {
			return stream, io.NopCloser(bytes.NewReader(nil)), nil
		},
		stdinFD:  0,
		stdoutFD: 1,
		termName: "xterm-256color",
	}

	go func() {
		stream.pushAttached("sess-1", "/workspace/demo")
		stream.pushError(remotefsv1.Error_KIND_RATE_LIMITED, "stdin limited: burst exceeded")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := runCommand(ctx, deps, "daemon.yaml")
	require.Error(t, err)

	var exitErr *exitStatus
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 5, exitErr.code)
	assert.Equal(t, "stdin limited: burst exceeded", exitErr.message)
}

func TestLoadClientConfigUsesServerToken(t *testing.T) {
	t.Run("use server token", func(t *testing.T) {
		path := writeDaemonConfig(t, `
server:
  address: "example.com:9326"
  token: "server-token"
workspace:
  path: `+yamlPath(absWorkspace())+`
`)

		cfg, err := loadClientConfigFromDaemon(path)
		require.NoError(t, err)
		assert.Equal(t, "example.com:9326", cfg.Address)
		assert.Equal(t, "server-token", cfg.Token)
		assert.Equal(t, int(config.DefaultPTYFrameMaxBytes), cfg.FrameMax)
	})

	t.Run("use daemon pty frame max", func(t *testing.T) {
		path := writeDaemonConfig(t, `
server:
  address: "example.com:9326"
  token: "server-token"
workspace:
  path: `+yamlPath(absWorkspace())+`
pty:
  frame_max_bytes: 32768
`)

		cfg, err := loadClientConfigFromDaemon(path)
		require.NoError(t, err)
		assert.Equal(t, 32768, cfg.FrameMax)
	})

	t.Run("reject missing token", func(t *testing.T) {
		path := writeDaemonConfig(t, `
server:
  address: "example.com:9326"
workspace:
  path: `+yamlPath(absWorkspace())+`
`)

		_, err := loadClientConfigFromDaemon(path)
		require.Error(t, err)
		assert.ErrorIs(t, err, errEmptyServerToken)
	})
}

func writeDaemonConfig(t *testing.T, body string) string {
	t.Helper()

	dir := t.TempDir()
	path := dir + string(os.PathSeparator) + "daemon.yaml"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func absWorkspace() string {
	if runtime.GOOS == "windows" {
		return `C:\workspace\demo`
	}
	return "/workspace/demo"
}

func yamlPath(path string) string {
	return `'` + strings.ReplaceAll(path, `'`, `''`) + `'`
}

func closedResizeCh() <-chan ptyclient.WindowSize {
	ch := make(chan ptyclient.WindowSize)
	close(ch)
	return ch
}

func drainSentFrames(ch <-chan *remotefsv1.ClientFrame) []*remotefsv1.ClientFrame {
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

func sentStdinPayloads(frames []*remotefsv1.ClientFrame) [][]byte {
	payloads := make([][]byte, 0, len(frames))
	for _, frame := range frames {
		if data := frame.GetStdin(); len(data) > 0 {
			payloads = append(payloads, data)
		}
	}
	return payloads
}
