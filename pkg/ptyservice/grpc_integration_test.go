package ptyservice_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/internal/testutil"
	"flyingEirc/Rclaude/pkg/auth"
	"flyingEirc/Rclaude/pkg/config"
	"flyingEirc/Rclaude/pkg/ptyhost"
	"flyingEirc/Rclaude/pkg/ptyservice"
	"flyingEirc/Rclaude/pkg/session"
	"flyingEirc/Rclaude/pkg/transport"
)

func TestAttachOverGRPC_HappyPath(t *testing.T) {
	t.Parallel()

	manager := newLiveManager(t, "alice")
	host := newFakeHost()
	host.stdout.WriteString("hello-from-host\n")
	go func() {
		time.Sleep(20 * time.Millisecond)
		host.finish(ptyhost.ExitInfo{Code: 0})
	}()

	svc := newIntegratedPTYService(t, manager, fakeSpawner{host: host})
	dialer := newPTYDialer(t, manager, svc)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, err := transport.Dial(ctx, transport.DialOptions{
		Address: "passthrough:///bufnet",
		Dialer:  dialer,
	})
	require.NoError(t, err)
	defer func() { assert.NoError(t, conn.Close()) }()

	stream, err := remotefsv1.NewRemotePTYClient(conn).Attach(auth.NewOutgoingContext(ctx, "tok-alice"))
	require.NoError(t, err)

	require.NoError(t, stream.Send(attachFrame()))
	require.NoError(t, stream.Send(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Stdin{Stdin: []byte("ping\n")},
	}))
	require.NoError(t, stream.Send(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Resize{Resize: &remotefsv1.Resize{Cols: 132, Rows: 48}},
	}))
	require.NoError(t, stream.Send(&remotefsv1.ClientFrame{
		Payload: &remotefsv1.ClientFrame_Detach{Detach: &remotefsv1.Detach{}},
	}))

	first, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, first.GetAttached())
	assert.Equal(t, "alice-pty-1", first.GetAttached().GetSessionId())

	second, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, []byte("hello-from-host\n"), second.GetStdout())

	third, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, third.GetExited())
	assert.Equal(t, int32(0), third.GetExited().GetCode())

	assert.Equal(t, "ping\n", host.stdin.String())
	assert.Equal(t, ptyhost.WindowSize{Cols: 132, Rows: 48}, host.lastResize)
	assert.True(t, host.shutdownCalled)
}

func TestAttachOverGRPC_DaemonOffline(t *testing.T) {
	t.Parallel()

	manager := session.NewManager()
	svc := newIntegratedPTYService(t, manager, fakeSpawner{host: newFakeHost()})
	dialer := newPTYDialer(t, manager, svc)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, err := transport.Dial(ctx, transport.DialOptions{
		Address: "passthrough:///bufnet",
		Dialer:  dialer,
	})
	require.NoError(t, err)
	defer func() { assert.NoError(t, conn.Close()) }()

	stream, err := remotefsv1.NewRemotePTYClient(conn).Attach(auth.NewOutgoingContext(ctx, "tok-alice"))
	require.NoError(t, err)

	require.NoError(t, stream.Send(attachFrame()))

	frame, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, frame.GetError())
	assert.Equal(t, remotefsv1.Error_KIND_DAEMON_NOT_CONNECTED, frame.GetError().GetKind())
}

func TestAttachOverGRPC_SessionBusy(t *testing.T) {
	t.Parallel()

	manager := newLiveManager(t, "alice")
	_, err := manager.RegisterPTY("alice")
	require.NoError(t, err)

	svc := newIntegratedPTYService(t, manager, fakeSpawner{host: newFakeHost()})
	dialer := newPTYDialer(t, manager, svc)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, err := transport.Dial(ctx, transport.DialOptions{
		Address: "passthrough:///bufnet",
		Dialer:  dialer,
	})
	require.NoError(t, err)
	defer func() { assert.NoError(t, conn.Close()) }()

	stream, err := remotefsv1.NewRemotePTYClient(conn).Attach(auth.NewOutgoingContext(ctx, "tok-alice"))
	require.NoError(t, err)

	require.NoError(t, stream.Send(attachFrame()))

	frame, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, frame.GetError())
	assert.Equal(t, remotefsv1.Error_KIND_SESSION_BUSY, frame.GetError().GetKind())
}

func newIntegratedPTYService(
	t *testing.T,
	manager *session.Manager,
	spawner fakeSpawner,
) *ptyservice.Service {
	t.Helper()

	svc, err := ptyservice.New(ptyservice.Config{
		Registry:     integratedRegistry{manager: manager},
		Spawner:      spawner,
		Binary:       testBinary(t),
		Workspace:    testWorkspaceRoot(),
		EnvWhitelist: append([]string(nil), config.DefaultPTYEnvPassthrough...),
		FrameMax:     64,
	})
	require.NoError(t, err)
	return svc
}

func newPTYDialer(
	t *testing.T,
	manager *session.Manager,
	pty remotefsv1.RemotePTYServer,
) func(context.Context, string) (net.Conn, error) {
	t.Helper()
	dialer := testutil.NewBufconnServerWithServices(t, testutil.BufconnServices{
		Verifier:  auth.NewStaticVerifier(map[string]string{"tok-alice": "alice"}),
		RemoteFS:  mustRemoteFSService(t, manager),
		RemotePTY: pty,
	})
	return dialer
}

func mustRemoteFSService(t *testing.T, manager *session.Manager) remotefsv1.RemoteFSServer {
	t.Helper()
	svc, err := session.NewService(manager)
	require.NoError(t, err)
	return svc
}

func newLiveManager(t *testing.T, userID string) *session.Manager {
	t.Helper()

	manager := session.NewManager()
	current := manager.NewSession(userID)
	require.NoError(t, current.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{}},
	}))
	_, err := manager.Register(current)
	require.NoError(t, err)
	return manager
}

type integratedRegistry struct {
	manager *session.Manager
}

func (r integratedRegistry) LookupDaemon(userID string) bool {
	_, ok := r.manager.LookupDaemon(userID)
	return ok
}

func (r integratedRegistry) RegisterPTY(userID string) (string, bool, error) {
	sessionID, err := r.manager.RegisterPTY(userID)
	if err == nil {
		return sessionID, true, nil
	}
	if errors.Is(err, session.ErrPTYBusy) {
		return "", false, nil
	}
	return "", false, err
}

func (r integratedRegistry) UnregisterPTY(userID string, sessionID string) {
	_ = r.manager.UnregisterPTY(userID, sessionID)
}
