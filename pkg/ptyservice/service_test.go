package ptyservice_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/auth"
	"flyingEirc/Rclaude/pkg/config"
	"flyingEirc/Rclaude/pkg/ptyhost"
	"flyingEirc/Rclaude/pkg/ptyservice"
)

func TestAttach_FirstFrameMustBeAttach(t *testing.T) {
	t.Parallel()

	stream := newFakeStream(auth.WithUserID(context.Background(), "alice"))
	stream.pushClient(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Resize{Resize: &remotefsv1.Resize{Cols: 80, Rows: 24}},
	})

	svc := newService(t, fakeRegistry{daemonOnline: true})
	require.NoError(t, svc.Attach(stream))

	frames := stream.serverFrames()
	require.Len(t, frames, 1)
	assert.Equal(t, remotefsv1.Error_KIND_PROTOCOL, frames[0].GetError().GetKind())
}

func TestAttach_DaemonOfflineReturnsApplicationError(t *testing.T) {
	t.Parallel()

	stream := newFakeStream(auth.WithUserID(context.Background(), "alice"))
	stream.pushClient(attachFrame())

	svc := newService(t, fakeRegistry{})
	require.NoError(t, svc.Attach(stream))

	frames := stream.serverFrames()
	require.Len(t, frames, 1)
	assert.Equal(t, remotefsv1.Error_KIND_DAEMON_NOT_CONNECTED, frames[0].GetError().GetKind())
}

func TestAttach_HappyPathForwardsIOAndExit(t *testing.T) {
	t.Parallel()

	stream := newFakeStream(auth.WithUserID(context.Background(), "alice"))
	stream.pushClient(attachFrame())
	stream.pushClient(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Stdin{Stdin: []byte("hello\n")},
	})
	stream.pushClient(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Resize{Resize: &remotefsv1.Resize{Cols: 120, Rows: 50}},
	})
	stream.pushClient(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Detach{Detach: &remotefsv1.Detach{}},
	})

	host := newFakeHost()
	host.stdout.WriteString("world\n")
	go func() {
		time.Sleep(20 * time.Millisecond)
		host.finish(ptyhost.ExitInfo{Code: 0})
	}()

	registry := fakeRegistry{daemonOnline: true, sessionID: "pty-1"}
	svc := newService(t, registry, withSpawner(fakeSpawner{host: host}))

	require.NoError(t, svc.Attach(stream))

	frames := stream.serverFrames()
	require.Len(t, frames, 3)
	assert.Equal(t, "pty-1", frames[0].GetAttached().GetSessionId())
	assert.Equal(t, []byte("world\n"), frames[1].GetStdout())
	assert.Equal(t, int32(0), frames[2].GetExited().GetCode())
	assert.Equal(t, "hello\n", host.stdin.String())
	assert.Equal(t, ptyhost.WindowSize{Cols: 120, Rows: 50}, host.lastResize)
	assert.True(t, host.shutdownCalled)
}

func TestAttach_TooLargeStdinTriggersProtocolError(t *testing.T) {
	t.Parallel()

	stream := newFakeStream(auth.WithUserID(context.Background(), "alice"))
	stream.pushClient(attachFrame())
	stream.pushClient(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Stdin{Stdin: bytes.Repeat([]byte("a"), 17)},
	})

	host := newFakeHost()

	svc := newService(t, fakeRegistry{daemonOnline: true}, withSpawner(fakeSpawner{host: host}), withFrameMax(16))
	require.NoError(t, svc.Attach(stream))

	frames := stream.serverFrames()
	require.Len(t, frames, 2)
	assert.NotNil(t, frames[0].GetAttached())
	assert.Equal(t, remotefsv1.Error_KIND_PROTOCOL, frames[1].GetError().GetKind())
	assert.True(t, host.shutdownCalled)
}

func TestAttach_AttachRateLimitedReleasesPTYSlot(t *testing.T) {
	t.Parallel()

	stream := newFakeStream(auth.WithUserID(context.Background(), "alice"))
	stream.pushClient(attachFrame())

	registry := &trackingRegistry{daemonOnline: true, sessionID: "pty-1"}
	svc := newService(
		t,
		registry,
		withAttachLimit(fakeAttachLimiter{err: errors.New("attach limited")}),
	)

	require.NoError(t, svc.Attach(stream))

	frames := stream.serverFrames()
	require.Len(t, frames, 1)
	assert.Equal(t, remotefsv1.Error_KIND_RATE_LIMITED, frames[0].GetError().GetKind())
	assert.Equal(t, []string{"alice:pty-1"}, registry.unregistered)
}

func TestAttach_StdinRateLimitedReturnsApplicationError(t *testing.T) {
	t.Parallel()

	stream := newFakeStream(auth.WithUserID(context.Background(), "alice"))
	stream.pushClient(attachFrame())
	stream.pushClient(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Stdin{Stdin: []byte("hello\n")},
	})

	host := newFakeHost()
	svc := newService(
		t,
		fakeRegistry{daemonOnline: true},
		withSpawner(fakeSpawner{host: host}),
		withInputLimit(fakeInputLimiter{err: errors.New("stdin limited")}),
	)

	require.NoError(t, svc.Attach(stream))

	frames := stream.serverFrames()
	require.Len(t, frames, 2)
	assert.NotNil(t, frames[0].GetAttached())
	assert.Equal(t, remotefsv1.Error_KIND_RATE_LIMITED, frames[1].GetError().GetKind())
	assert.True(t, host.shutdownCalled)
	assert.Empty(t, host.stdin.String())
}

func newService(t *testing.T, registry ptyservice.Registry, opts ...serviceOption) *ptyservice.Service {
	t.Helper()

	cfg := ptyservice.Config{
		Registry:     registry,
		Spawner:      fakeSpawner{host: newFakeHost()},
		Binary:       testBinary(t),
		Workspace:    testWorkspaceRoot(),
		EnvWhitelist: append([]string(nil), config.DefaultPTYEnvPassthrough...),
		FrameMax:     64,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	svc, err := ptyservice.New(cfg)
	require.NoError(t, err)
	return svc
}

type serviceOption func(*ptyservice.Config)

func withSpawner(spawner fakeSpawner) serviceOption {
	return func(cfg *ptyservice.Config) {
		cfg.Spawner = spawner
	}
}

func withFrameMax(n int64) serviceOption {
	return func(cfg *ptyservice.Config) {
		cfg.FrameMax = n
	}
}

func withAttachLimit(limiter ptyservice.AttachLimiter) serviceOption {
	return func(cfg *ptyservice.Config) {
		cfg.AttachLimit = limiter
	}
}

func withInputLimit(limiter ptyservice.InputLimiter) serviceOption {
	return func(cfg *ptyservice.Config) {
		cfg.InputLimit = limiter
	}
}

func attachFrame() *remotefsv1.ClientFrame {
	return &remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Attach{
			Attach: &remotefsv1.AttachReq{
				InitialSize: &remotefsv1.Resize{Cols: 80, Rows: 24},
				Term:        "xterm-256color",
			},
		},
	}
}

func testBinary(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	require.NoError(t, err)
	return path
}

func testWorkspaceRoot() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.TempDir(), "rclaude-pty")
	}
	return filepath.Join(string(filepath.Separator), "tmp", "rclaude-pty")
}

type fakeRegistry struct {
	daemonOnline bool
	sessionID    string
	busy         bool
}

func (r fakeRegistry) LookupDaemon(_ string) bool {
	return r.daemonOnline
}

func (r fakeRegistry) RegisterPTY(_ string) (string, bool, error) {
	if r.busy {
		return "", false, nil
	}
	sessionID := r.sessionID
	if sessionID == "" {
		sessionID = "pty-test"
	}
	return sessionID, true, nil
}

func (r fakeRegistry) UnregisterPTY(_, _ string) {}

type trackingRegistry struct {
	daemonOnline bool
	sessionID    string
	unregistered []string
}

func (r *trackingRegistry) LookupDaemon(_ string) bool {
	return r.daemonOnline
}

func (r *trackingRegistry) RegisterPTY(_ string) (string, bool, error) {
	sessionID := r.sessionID
	if sessionID == "" {
		sessionID = "pty-test"
	}
	return sessionID, true, nil
}

func (r *trackingRegistry) UnregisterPTY(userID string, sessionID string) {
	r.unregistered = append(r.unregistered, userID+":"+sessionID)
}

type fakeAttachLimiter struct {
	err error
}

func (l fakeAttachLimiter) Wait(context.Context, string) error {
	return l.err
}

type fakeInputLimiter struct {
	err error
}

func (l fakeInputLimiter) Wait(context.Context, string, int) error {
	return l.err
}

type fakeSpawner struct {
	host *fakeHost
	err  error
}

func (s fakeSpawner) Spawn(_ ptyhost.SpawnReq) (ptyservice.Host, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.host, nil
}

type fakeHost struct {
	stdin  bytes.Buffer
	stdout bytes.Buffer

	mu             sync.Mutex
	lastResize     ptyhost.WindowSize
	shutdownCalled bool
	waitCh         chan struct{}
	waitInfo       ptyhost.ExitInfo
	waitErr        error
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		waitCh: make(chan struct{}),
	}
}

func (h *fakeHost) Stdin() io.Writer {
	return &h.stdin
}

func (h *fakeHost) Stdout() io.Reader {
	return bytes.NewReader(h.stdout.Bytes())
}

func (h *fakeHost) Resize(size ptyhost.WindowSize) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastResize = size
	return nil
}

func (h *fakeHost) Shutdown(_ bool) error {
	h.mu.Lock()
	h.shutdownCalled = true
	h.mu.Unlock()
	h.finish(ptyhost.ExitInfo{})
	return nil
}

func (h *fakeHost) Wait(ctx context.Context) (ptyhost.ExitInfo, error) {
	select {
	case <-ctx.Done():
		return ptyhost.ExitInfo{}, ctx.Err()
	case <-h.waitCh:
		return h.waitInfo, h.waitErr
	}
}

func (h *fakeHost) finish(info ptyhost.ExitInfo) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.waitInfo = info
	select {
	case <-h.waitCh:
	default:
		close(h.waitCh)
	}
}

type fakeStream struct {
	ctx context.Context

	clientFrames []*remotefsv1.ClientFrame
	serverSent   []*remotefsv1.ServerFrame

	mu sync.Mutex
}

func newFakeStream(ctx context.Context) *fakeStream {
	return &fakeStream{ctx: ctx}
}

func (s *fakeStream) pushClient(frame *remotefsv1.ClientFrame) {
	s.clientFrames = append(s.clientFrames, frame)
}

func (s *fakeStream) serverFrames() []*remotefsv1.ServerFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*remotefsv1.ServerFrame, 0, len(s.serverSent))
	for _, frame := range s.serverSent {
		cloned, ok := proto.Clone(frame).(*remotefsv1.ServerFrame)
		if !ok {
			panic("clone server frame")
		}
		out = append(out, cloned)
	}
	return out
}

func (s *fakeStream) Context() context.Context {
	return s.ctx
}

func (s *fakeStream) Send(frame *remotefsv1.ServerFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cloned, ok := proto.Clone(frame).(*remotefsv1.ServerFrame)
	if !ok {
		return errors.New("clone server frame")
	}
	s.serverSent = append(s.serverSent, cloned)
	return nil
}

func (s *fakeStream) Recv() (*remotefsv1.ClientFrame, error) {
	if len(s.clientFrames) == 0 {
		return nil, io.EOF
	}
	frame := s.clientFrames[0]
	s.clientFrames = s.clientFrames[1:]
	return frame, nil
}

func (s *fakeStream) SetHeader(metadata.MD) error { return nil }

func (s *fakeStream) SendHeader(metadata.MD) error { return nil }

func (s *fakeStream) SetTrailer(metadata.MD) {}

func (s *fakeStream) SendMsg(m any) error {
	frame, ok := m.(*remotefsv1.ServerFrame)
	if !ok {
		return errors.New("unexpected SendMsg type")
	}
	return s.Send(frame)
}

func (s *fakeStream) RecvMsg(m any) error {
	frame, ok := m.(*remotefsv1.ClientFrame)
	if !ok {
		return errors.New("unexpected RecvMsg type")
	}
	got, err := s.Recv()
	if err != nil {
		return err
	}
	proto.Reset(frame)
	proto.Merge(frame, got)
	return nil
}
