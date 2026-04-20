package syncer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/internal/testutil"
	"flyingEirc/Rclaude/pkg/config"
	"flyingEirc/Rclaude/pkg/logx"
)

func TestRun_NilConfig(t *testing.T) {
	err := Run(context.Background(), RunOptions{})
	assert.ErrorIs(t, err, ErrNilConfig)
}

func TestRun_InvalidConfig(t *testing.T) {
	cfg := &config.DaemonConfig{}
	err := Run(context.Background(), RunOptions{Config: cfg})
	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrEmptyServerAddress)
}

func TestRun_InvalidSensitivePatterns(t *testing.T) {
	cfg := testDaemonConfig(t.TempDir(), "passthrough:///phase6b", "token")
	cfg.Workspace.SensitivePatterns = []string{"["}

	err := Run(context.Background(), RunOptions{Config: cfg})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidSensitivePattern)
}

func TestRun_InitialTreeRequestsWatchAndHeartbeat(t *testing.T) {
	root := testutil.NewTempWorkspace(t, map[string]string{
		"README.md":      "hello",
		"nested/":        "",
		"nested/app.txt": "world",
	})
	server := testutil.NewRecordingServer()
	dialer := testutil.NewBufconnServer(t, server)

	restore := overrideDaemonTimings(t, 40*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond)
	defer restore()

	var logBuf bytes.Buffer
	logger := logx.New(logx.Options{
		Level:  slog.LevelDebug,
		Format: logx.FormatText,
		Output: &logBuf,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, RunOptions{
			Config: testDaemonConfig(root, "passthrough:///phase2", "token"),
			Logger: logger,
			Dialer: dialer,
		})
	}()

	waitForReady(t, server.WaitReady())

	treeMsg := waitForRecordedMessage(t, server, func(msg *remotefsv1.DaemonMessage) bool {
		return msg.GetFileTree() != nil
	})
	treePaths := collectTreePaths(treeMsg.GetFileTree().GetFiles())
	assert.ElementsMatch(t, []string{"README.md", "nested", "nested/app.txt"}, treePaths)

	require.NoError(t, server.SendRequest(&remotefsv1.ServerMessage{
		Msg: &remotefsv1.ServerMessage_Request{
			Request: &remotefsv1.FileRequest{
				RequestId: "read-1",
				Operation: &remotefsv1.FileRequest_Read{
					Read: &remotefsv1.ReadFileReq{Path: "README.md"},
				},
			},
		},
	}))
	readResp := waitForRecordedMessage(t, server, func(msg *remotefsv1.DaemonMessage) bool {
		resp := msg.GetResponse()
		return resp != nil && resp.GetRequestId() == "read-1"
	}).GetResponse()
	require.True(t, readResp.GetSuccess(), readResp.GetError())
	assert.Equal(t, "hello", string(readResp.GetContent()))

	require.NoError(t, server.SendRequest(&remotefsv1.ServerMessage{
		Msg: &remotefsv1.ServerMessage_Request{
			Request: &remotefsv1.FileRequest{
				RequestId: "stat-1",
				Operation: &remotefsv1.FileRequest_Stat{
					Stat: &remotefsv1.StatReq{Path: "nested/app.txt"},
				},
			},
		},
	}))
	statResp := waitForRecordedMessage(t, server, func(msg *remotefsv1.DaemonMessage) bool {
		resp := msg.GetResponse()
		return resp != nil && resp.GetRequestId() == "stat-1"
	}).GetResponse()
	require.True(t, statResp.GetSuccess(), statResp.GetError())
	assert.Equal(t, "nested/app.txt", statResp.GetInfo().GetPath())

	require.NoError(t, server.SendRequest(&remotefsv1.ServerMessage{
		Msg: &remotefsv1.ServerMessage_Request{
			Request: &remotefsv1.FileRequest{
				RequestId: "list-1",
				Operation: &remotefsv1.FileRequest_ListDir{
					ListDir: &remotefsv1.ListDirReq{Path: "nested"},
				},
			},
		},
	}))
	listResp := waitForRecordedMessage(t, server, func(msg *remotefsv1.DaemonMessage) bool {
		resp := msg.GetResponse()
		return resp != nil && resp.GetRequestId() == "list-1"
	}).GetResponse()
	require.True(t, listResp.GetSuccess(), listResp.GetError())
	require.Len(t, listResp.GetEntries().GetFiles(), 1)
	assert.Equal(t, "nested/app.txt", listResp.GetEntries().GetFiles()[0].GetPath())

	require.NoError(t, os.WriteFile(root+"/created.txt", []byte("new"), 0o600))
	changeMsg := waitForRecordedMessage(t, server, func(msg *remotefsv1.DaemonMessage) bool {
		change := msg.GetChange()
		return change != nil && change.GetFile().GetPath() == "created.txt"
	})
	assert.Contains(t,
		[]remotefsv1.ChangeType{
			remotefsv1.ChangeType_CHANGE_TYPE_CREATE,
			remotefsv1.ChangeType_CHANGE_TYPE_MODIFY,
		},
		changeMsg.GetChange().GetType(),
	)

	heartbeatMsg := waitForRecordedMessage(t, server, func(msg *remotefsv1.DaemonMessage) bool {
		return msg.GetHeartbeat() != nil
	})
	assert.NotZero(t, heartbeatMsg.GetHeartbeat().GetTimestamp())

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestRun_SensitivePathsAreFiltered(t *testing.T) {
	root := testutil.NewTempWorkspace(t, map[string]string{
		".env":               "secret",
		"visible.txt":        "hello",
		".ssh/":              "",
		".ssh/id_ed25519":    "private",
		"secrets/":           "",
		"secrets/value.yaml": "classified",
	})
	server := testutil.NewRecordingServer()
	dialer := testutil.NewBufconnServer(t, server)

	restore := overrideDaemonTimings(t, 40*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond)
	defer restore()

	cfg := testDaemonConfig(root, "passthrough:///phase6b", "token")
	cfg.Workspace.SensitivePatterns = []string{"secrets/**"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, RunOptions{
			Config: cfg,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Dialer: dialer,
		})
	}()

	waitForReady(t, server.WaitReady())

	treeMsg := waitForRecordedMessage(t, server, func(msg *remotefsv1.DaemonMessage) bool {
		return msg.GetFileTree() != nil
	})
	assert.ElementsMatch(t, []string{".ssh", "visible.txt"}, collectTreePaths(treeMsg.GetFileTree().GetFiles()))

	require.NoError(t, server.SendRequest(&remotefsv1.ServerMessage{
		Msg: &remotefsv1.ServerMessage_Request{
			Request: &remotefsv1.FileRequest{
				RequestId: "list-1",
				Operation: &remotefsv1.FileRequest_ListDir{
					ListDir: &remotefsv1.ListDirReq{Path: ""},
				},
			},
		},
	}))
	listResp := waitForRecordedMessage(t, server, func(msg *remotefsv1.DaemonMessage) bool {
		resp := msg.GetResponse()
		return resp != nil && resp.GetRequestId() == "list-1"
	}).GetResponse()
	require.True(t, listResp.GetSuccess(), listResp.GetError())
	assert.ElementsMatch(t, []string{".ssh", "visible.txt"}, collectTreePaths(listResp.GetEntries().GetFiles()))

	require.NoError(t, server.SendRequest(&remotefsv1.ServerMessage{
		Msg: &remotefsv1.ServerMessage_Request{
			Request: &remotefsv1.FileRequest{
				RequestId: "read-1",
				Operation: &remotefsv1.FileRequest_Read{
					Read: &remotefsv1.ReadFileReq{Path: ".env"},
				},
			},
		},
	}))
	readResp := waitForRecordedMessage(t, server, func(msg *remotefsv1.DaemonMessage) bool {
		resp := msg.GetResponse()
		return resp != nil && resp.GetRequestId() == "read-1"
	}).GetResponse()
	assert.False(t, readResp.GetSuccess())
	assertNotExistError(t, readResp.GetError())

	require.NoError(t, server.SendRequest(&remotefsv1.ServerMessage{
		Msg: &remotefsv1.ServerMessage_Request{
			Request: &remotefsv1.FileRequest{
				RequestId: "write-1",
				Operation: &remotefsv1.FileRequest_Write{
					Write: &remotefsv1.WriteFileReq{Path: ".env", Content: []byte("override")},
				},
			},
		},
	}))
	writeResp := waitForRecordedMessage(t, server, func(msg *remotefsv1.DaemonMessage) bool {
		resp := msg.GetResponse()
		return resp != nil && resp.GetRequestId() == "write-1"
	}).GetResponse()
	assert.False(t, writeResp.GetSuccess())
	assert.Contains(t, writeResp.GetError(), "permission denied")

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestRun_ReadRateLimitDelaysResponse(t *testing.T) {
	root := testutil.NewTempWorkspace(t, map[string]string{
		"big.txt": string(bytes.Repeat([]byte("a"), 1500)),
	})
	server := testutil.NewRecordingServer()
	dialer := testutil.NewBufconnServer(t, server)

	restore := overrideDaemonTimings(t, time.Hour, 10*time.Millisecond, 20*time.Millisecond)
	defer restore()

	cfg := testDaemonConfig(root, "passthrough:///rate-read", "token")
	cfg.RateLimit.ReadBytesPerSec = 1000

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, RunOptions{
			Config: cfg,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Dialer: dialer,
		})
	}()

	waitForReady(t, server.WaitReady())
	waitForRecordedMessage(t, server, func(msg *remotefsv1.DaemonMessage) bool {
		return msg.GetFileTree() != nil
	})

	start := time.Now()
	require.NoError(t, server.SendRequest(&remotefsv1.ServerMessage{
		Msg: &remotefsv1.ServerMessage_Request{
			Request: &remotefsv1.FileRequest{
				RequestId: "read-rate",
				Operation: &remotefsv1.FileRequest_Read{
					Read: &remotefsv1.ReadFileReq{Path: "big.txt"},
				},
			},
		},
	}))
	readResp := waitForRecordedMessage(t, server, func(msg *remotefsv1.DaemonMessage) bool {
		resp := msg.GetResponse()
		return resp != nil && resp.GetRequestId() == "read-rate"
	}).GetResponse()
	elapsed := time.Since(start)

	require.True(t, readResp.GetSuccess(), readResp.GetError())
	assert.Len(t, readResp.GetContent(), 1500)
	assert.GreaterOrEqual(t, elapsed, 400*time.Millisecond)

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestRun_WriteRateLimitDelaysResponse(t *testing.T) {
	root := testutil.NewTempWorkspace(t, map[string]string{})
	server := testutil.NewRecordingServer()
	dialer := testutil.NewBufconnServer(t, server)

	restore := overrideDaemonTimings(t, time.Hour, 10*time.Millisecond, 20*time.Millisecond)
	defer restore()

	cfg := testDaemonConfig(root, "passthrough:///rate-write", "token")
	cfg.RateLimit.WriteBytesPerSec = 1000

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, RunOptions{
			Config: cfg,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Dialer: dialer,
		})
	}()

	waitForReady(t, server.WaitReady())
	waitForRecordedMessage(t, server, func(msg *remotefsv1.DaemonMessage) bool {
		return msg.GetFileTree() != nil
	})

	start := time.Now()
	require.NoError(t, server.SendRequest(&remotefsv1.ServerMessage{
		Msg: &remotefsv1.ServerMessage_Request{
			Request: &remotefsv1.FileRequest{
				RequestId: "write-rate",
				Operation: &remotefsv1.FileRequest_Write{
					Write: &remotefsv1.WriteFileReq{
						Path:    "big.txt",
						Content: bytes.Repeat([]byte("b"), 1500),
					},
				},
			},
		},
	}))
	writeResp := waitForRecordedMessage(t, server, func(msg *remotefsv1.DaemonMessage) bool {
		resp := msg.GetResponse()
		return resp != nil && resp.GetRequestId() == "write-rate"
	}).GetResponse()
	elapsed := time.Since(start)

	require.True(t, writeResp.GetSuccess(), writeResp.GetError())
	assert.GreaterOrEqual(t, elapsed, 400*time.Millisecond)

	//nolint:gosec // test reads from a temp workspace fixture
	data, err := os.ReadFile(filepath.Join(root, "big.txt"))
	require.NoError(t, err)
	assert.Len(t, data, 1500)

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestRun_CancelWhileRateLimitedReadUnblocks(t *testing.T) {
	root := testutil.NewTempWorkspace(t, map[string]string{
		"slow.txt": "ab",
	})
	server := testutil.NewRecordingServer()
	dialer := testutil.NewBufconnServer(t, server)

	restore := overrideDaemonTimings(t, time.Hour, 10*time.Millisecond, 20*time.Millisecond)
	defer restore()

	cfg := testDaemonConfig(root, "passthrough:///rate-cancel", "token")
	cfg.RateLimit.ReadBytesPerSec = 1

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, RunOptions{
			Config: cfg,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Dialer: dialer,
		})
	}()

	waitForReady(t, server.WaitReady())
	waitForRecordedMessage(t, server, func(msg *remotefsv1.DaemonMessage) bool {
		return msg.GetFileTree() != nil
	})

	require.NoError(t, server.SendRequest(&remotefsv1.ServerMessage{
		Msg: &remotefsv1.ServerMessage_Request{
			Request: &remotefsv1.FileRequest{
				RequestId: "cancel-rate",
				Operation: &remotefsv1.FileRequest_Read{
					Read: &remotefsv1.ReadFileReq{Path: "slow.txt"},
				},
			},
		},
	}))

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not return promptly after cancellation during rate-limited read")
	}
}

func TestRun_RetriesUntilStreamOpens(t *testing.T) {
	root := testutil.NewTempWorkspace(t, map[string]string{
		"main.go": "package main",
	})
	server := testutil.NewRecordingServer()
	baseDialer := testutil.NewBufconnServer(t, server)

	restore := overrideDaemonTimings(t, time.Hour, 10*time.Millisecond, 20*time.Millisecond)
	defer restore()

	var attempts atomic.Int32
	dialer := func(ctx context.Context, address string) (net.Conn, error) {
		n := attempts.Add(1)
		if n < 3 {
			return nil, errors.New("dialer: temporary failure")
		}
		return baseDialer(ctx, address)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, RunOptions{
			Config: testDaemonConfig(root, "passthrough:///retry", "token"),
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Dialer: dialer,
		})
	}()

	waitForReady(t, server.WaitReady())
	waitForRecordedMessage(t, server, func(msg *remotefsv1.DaemonMessage) bool {
		return msg.GetFileTree() != nil
	})
	assert.GreaterOrEqual(t, attempts.Load(), int32(3))

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func testDaemonConfig(root, address, token string) *config.DaemonConfig {
	return &config.DaemonConfig{
		Server: config.ServerEndpoint{
			Address: address,
			Token:   token,
		},
		Workspace: config.Workspace{
			Path: root,
		},
		PTY: config.DaemonPTYConfig{
			FrameMaxBytes: config.DefaultPTYFrameMaxBytes,
		},
	}
}

func overrideDaemonTimings(
	t *testing.T,
	heartbeat time.Duration,
	initialDelay time.Duration,
	maxDelay time.Duration,
) func() {
	t.Helper()
	oldHeartbeat := daemonHeartbeatInterval
	oldInitial := daemonReconnectInitialDelay
	oldMax := daemonReconnectMaxDelay
	oldElapsed := daemonReconnectMaxElapsed
	daemonHeartbeatInterval = heartbeat
	daemonReconnectInitialDelay = initialDelay
	daemonReconnectMaxDelay = maxDelay
	daemonReconnectMaxElapsed = 0
	return func() {
		daemonHeartbeatInterval = oldHeartbeat
		daemonReconnectInitialDelay = oldInitial
		daemonReconnectMaxDelay = oldMax
		daemonReconnectMaxElapsed = oldElapsed
	}
}

func waitForReady(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("server never observed a daemon connection")
	}
}

func waitForRecordedMessage(
	t *testing.T,
	server *testutil.RecordingServer,
	predicate func(*remotefsv1.DaemonMessage) bool,
) *remotefsv1.DaemonMessage {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, msg := range server.Received() {
			if predicate(msg) {
				return msg
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for daemon message")
	return nil
}

func collectTreePaths(files []*remotefsv1.FileInfo) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		out = append(out, file.GetPath())
	}
	return out
}
