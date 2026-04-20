package inmemtest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/internal/testutil"
	"flyingEirc/Rclaude/pkg/auth"
	"flyingEirc/Rclaude/pkg/config"
	"flyingEirc/Rclaude/pkg/ptyhost"
	"flyingEirc/Rclaude/pkg/ptyservice"
	"flyingEirc/Rclaude/pkg/session"
)

const defaultPTYHarnessTimeout = 2 * time.Second

var ErrNoQueuedPTYHost = errors.New("inmemtest: no queued pty host")

// PTYHarnessOptions describes one authenticated in-memory gRPC runtime for PTY
// integration tests.
type PTYHarnessOptions struct {
	UserID      string
	ClientToken string
	DaemonToken string

	WorkspaceRoot string
	Spawner       ptyservice.Spawner
	AttachLimit   ptyservice.AttachLimiter
	InputLimit    ptyservice.InputLimiter
	FrameMax      int64
	GracefulStop  time.Duration
}

// PTYHarness wires RemoteFS and RemotePTY through one authenticated bufconn
// gRPC server so tests can drive daemon and user clients against the same
// session manager.
type PTYHarness struct {
	t testing.TB

	Manager       *session.Manager
	Session       *session.Service
	PTY           *ptyservice.Service
	Server        *testutil.GRPCBufconnServer
	WorkspaceRoot string
	UserID        string
	ClientToken   string
	DaemonToken   string
}

// PTYDaemon keeps one real RemoteFS.Connect stream open for PTY tests.
type PTYDaemon struct {
	t testing.TB

	conn   *grpc.ClientConn
	stream grpc.BidiStreamingClient[remotefsv1.DaemonMessage, remotefsv1.ServerMessage]
	cancel context.CancelFunc

	recvErrCh <-chan error
	once      sync.Once
}

// PTYHost is a controllable ptyservice.Host stub.
type PTYHost struct {
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter

	stdinMu sync.Mutex
	stdin   bytes.Buffer

	stateMu       sync.Mutex
	lastResize    ptyhost.WindowSize
	shutdownCalls int
	waitInfo      ptyhost.ExitInfo
	waitErr       error

	waitCh    chan struct{}
	closeOnce sync.Once
}

// PTYSpawner records spawn requests and returns queued hosts in order.
type PTYSpawner struct {
	mu       sync.Mutex
	hosts    []*PTYHost
	requests []ptyhost.SpawnReq
	err      error
}

func NewPTYHarness(t testing.TB, opts PTYHarnessOptions) *PTYHarness {
	t.Helper()

	userID := opts.UserID
	if userID == "" {
		userID = "alice"
	}
	clientToken := opts.ClientToken
	if clientToken == "" {
		clientToken = "tok-client-" + userID
	}
	daemonToken := opts.DaemonToken
	if daemonToken == "" {
		daemonToken = "tok-daemon-" + userID
	}
	require.NotNil(t, opts.Spawner)

	workspaceRoot := opts.WorkspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = t.TempDir()
	}
	require.NoError(t, os.MkdirAll(filepath.Join(workspaceRoot, userID), 0o750))

	manager := session.NewManager()
	sessionService, err := session.NewService(manager)
	require.NoError(t, err)

	binary, err := os.Executable()
	require.NoError(t, err)

	ptyService, err := ptyservice.New(ptyservice.Config{
		Registry:     managerPTYRegistry{manager: manager},
		Spawner:      opts.Spawner,
		AttachLimit:  opts.AttachLimit,
		InputLimit:   opts.InputLimit,
		Binary:       binary,
		Workspace:    workspaceRoot,
		EnvWhitelist: append([]string(nil), config.DefaultPTYEnvPassthrough...),
		FrameMax:     opts.FrameMax,
		GracefulStop: opts.GracefulStop,
	})
	require.NoError(t, err)

	server := testutil.NewGRPCBufconnServer(t, testutil.GRPCBufconnOptions{
		Verifier: auth.NewStaticVerifier(map[string]string{
			clientToken: userID,
			daemonToken: userID,
		}),
		RemoteFS:  sessionService,
		RemotePTY: ptyService,
	})

	return &PTYHarness{
		t:             t,
		Manager:       manager,
		Session:       sessionService,
		PTY:           ptyService,
		Server:        server,
		WorkspaceRoot: workspaceRoot,
		UserID:        userID,
		ClientToken:   clientToken,
		DaemonToken:   daemonToken,
	}
}

func (h *PTYHarness) ClientContext(ctx context.Context) context.Context {
	return auth.NewOutgoingContext(baseContext(ctx), h.ClientToken)
}

func (h *PTYHarness) DaemonContext(ctx context.Context) context.Context {
	return auth.NewOutgoingContext(baseContext(ctx), h.DaemonToken)
}

func (h *PTYHarness) NewClientConn() *grpc.ClientConn {
	h.t.Helper()

	conn, err := h.Server.NewClientConn()
	require.NoError(h.t, err)
	h.t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			h.t.Logf("close PTY client connection: %v", err)
		}
	})
	return conn
}

func (h *PTYHarness) NewPTYClient() (remotefsv1.RemotePTYClient, *grpc.ClientConn) {
	h.t.Helper()

	conn := h.NewClientConn()
	return remotefsv1.NewRemotePTYClient(conn), conn
}

func (h *PTYHarness) ConnectDaemon(ctx context.Context, files ...*remotefsv1.FileInfo) *PTYDaemon {
	h.t.Helper()

	daemonCtx, cancel := context.WithCancel(baseContext(ctx))
	conn := h.NewClientConn()
	stream, err := remotefsv1.NewRemoteFSClient(conn).Connect(h.DaemonContext(daemonCtx))
	require.NoError(h.t, err)
	require.NoError(h.t, stream.Send(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{
			FileTree: &remotefsv1.FileTree{Files: files},
		},
	}))

	recvErrCh := make(chan error, 1)
	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				recvErrCh <- err
				return
			}
		}
	}()

	daemon := &PTYDaemon{
		t:         h.t,
		conn:      conn,
		stream:    stream,
		cancel:    cancel,
		recvErrCh: recvErrCh,
	}
	h.t.Cleanup(daemon.Cleanup)

	require.Eventually(h.t, func() bool {
		_, ok := h.Manager.LookupDaemon(h.UserID)
		return ok
	}, defaultPTYHarnessTimeout, 10*time.Millisecond)

	return daemon
}

func (d *PTYDaemon) Send(msg *remotefsv1.DaemonMessage) error {
	if d == nil || d.stream == nil {
		return net.ErrClosed
	}
	return d.stream.Send(msg)
}

func (d *PTYDaemon) Cleanup() {
	if d == nil {
		return
	}

	d.once.Do(func() {
		d.closeSend()
		d.cancelContext()
		d.closeConn()
		d.waitRecv()
	})
}

func (d *PTYDaemon) closeSend() {
	if d.stream == nil {
		return
	}
	if err := d.stream.CloseSend(); err != nil {
		d.logf("close PTY daemon send stream: %v", err)
	}
}

func (d *PTYDaemon) cancelContext() {
	if d.cancel != nil {
		d.cancel()
	}
}

func (d *PTYDaemon) closeConn() {
	if d.conn == nil {
		return
	}
	if err := d.conn.Close(); err != nil {
		d.logf("close PTY daemon connection: %v", err)
	}
}

func (d *PTYDaemon) waitRecv() {
	if d.recvErrCh != nil {
		<-d.recvErrCh
	}
}

func (d *PTYDaemon) logf(format string, args ...any) {
	if d.t != nil {
		d.t.Logf(format, args...)
	}
}

func NewPTYHost() *PTYHost {
	stdoutR, stdoutW := io.Pipe()
	return &PTYHost{
		stdoutR: stdoutR,
		stdoutW: stdoutW,
		waitCh:  make(chan struct{}),
	}
}

func (h *PTYHost) Stdin() io.Writer {
	return stdinRecorder{host: h}
}

func (h *PTYHost) Stdout() io.Reader {
	return h.stdoutR
}

func (h *PTYHost) Resize(size ptyhost.WindowSize) error {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	h.lastResize = size
	return nil
}

func (h *PTYHost) Shutdown(_ bool) error {
	h.stateMu.Lock()
	h.shutdownCalls++
	h.stateMu.Unlock()
	h.Finish(ptyhost.ExitInfo{}, nil)
	return nil
}

func (h *PTYHost) Wait(ctx context.Context) (ptyhost.ExitInfo, error) {
	select {
	case <-ctx.Done():
		return ptyhost.ExitInfo{}, ctx.Err()
	case <-h.waitCh:
		h.stateMu.Lock()
		defer h.stateMu.Unlock()
		return h.waitInfo, h.waitErr
	}
}

func (h *PTYHost) EmitStdout(data []byte) error {
	if h == nil {
		return net.ErrClosed
	}
	_, err := h.stdoutW.Write(data)
	return err
}

func (h *PTYHost) Finish(info ptyhost.ExitInfo, err error) {
	if h == nil {
		return
	}

	h.closeOnce.Do(func() {
		h.stateMu.Lock()
		h.waitInfo = info
		h.waitErr = err
		h.stateMu.Unlock()
		if err := h.stdoutW.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			h.stateMu.Lock()
			h.waitErr = errors.Join(h.waitErr, err)
			h.stateMu.Unlock()
		}
		close(h.waitCh)
	})
}

func (h *PTYHost) StdinString() string {
	h.stdinMu.Lock()
	defer h.stdinMu.Unlock()
	return h.stdin.String()
}

func (h *PTYHost) LastResize() ptyhost.WindowSize {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	return h.lastResize
}

func (h *PTYHost) ShutdownCalls() int {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	return h.shutdownCalls
}

func NewPTYSpawner(hosts ...*PTYHost) *PTYSpawner {
	return &PTYSpawner{hosts: append([]*PTYHost(nil), hosts...)}
}

func (s *PTYSpawner) Enqueue(host *PTYHost) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hosts = append(s.hosts, host)
}

func (s *PTYSpawner) SetError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *PTYSpawner) Spawn(req ptyhost.SpawnReq) (ptyservice.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.requests = append(s.requests, req)
	if s.err != nil {
		return nil, s.err
	}
	if len(s.hosts) == 0 {
		return nil, ErrNoQueuedPTYHost
	}

	host := s.hosts[0]
	s.hosts = s.hosts[1:]
	return host, nil
}

func (s *PTYSpawner) Requests() []ptyhost.SpawnReq {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]ptyhost.SpawnReq, len(s.requests))
	copy(out, s.requests)
	return out
}

type managerPTYRegistry struct {
	manager *session.Manager
}

func (r managerPTYRegistry) LookupDaemon(userID string) bool {
	if r.manager == nil {
		return false
	}
	_, ok := r.manager.LookupDaemon(userID)
	return ok
}

func (r managerPTYRegistry) RegisterPTY(userID string) (string, bool, error) {
	if r.manager == nil {
		return "", false, session.ErrNilManager
	}
	sessionID, err := r.manager.RegisterPTY(userID)
	if errors.Is(err, session.ErrPTYBusy) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return sessionID, true, nil
}

func (r managerPTYRegistry) UnregisterPTY(userID string, sessionID string) {
	if r.manager == nil {
		return
	}
	_ = r.manager.UnregisterPTY(userID, sessionID)
}

type stdinRecorder struct {
	host *PTYHost
}

func (w stdinRecorder) Write(p []byte) (int, error) {
	if w.host == nil {
		return 0, net.ErrClosed
	}
	w.host.stdinMu.Lock()
	defer w.host.stdinMu.Unlock()
	return w.host.stdin.Write(p)
}

func baseContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
