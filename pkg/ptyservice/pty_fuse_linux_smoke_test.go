//go:build linux

package ptyservice_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/internal/inmemtest"
	"flyingEirc/Rclaude/pkg/auth"
	"flyingEirc/Rclaude/pkg/config"
	"flyingEirc/Rclaude/pkg/fusefs"
	"flyingEirc/Rclaude/pkg/ptyclient"
	"flyingEirc/Rclaude/pkg/ptyhost"
	"flyingEirc/Rclaude/pkg/ptyservice"
	"flyingEirc/Rclaude/pkg/session"
	"flyingEirc/Rclaude/pkg/transport"
)

func TestAttachOverGRPC_LinuxSmoke_PTYReadsDaemonBackedFUSEFile(t *testing.T) {
	daemonRoot := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(daemonRoot, "README.md"),
		[]byte("hello from daemon workspace\n"),
		0o600,
	))

	harness := inmemtest.NewHarness(t)
	defer harness.Cleanup()

	user := harness.AddUser(inmemtest.UserOptions{UserID: "alice", DaemonRoot: daemonRoot})

	mountpoint := t.TempDir()
	mountCtx, mountCancel := context.WithCancel(context.Background())
	defer mountCancel()

	mounted := mountFUSEOrSkip(t, mountCtx, fusefs.Options{
		Mountpoint: mountpoint,
		Manager:    harness.Manager,
	})
	defer func() {
		assert.NoError(t, mounted.Close())
	}()

	svc := newRealPTYService(t, harness.Manager, mountpoint)
	dialer := newPTYDialer(t, harness.Manager, svc)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := transport.Dial(ctx, transport.DialOptions{
		Address: "passthrough:///bufnet",
		Dialer:  dialer,
	})
	require.NoError(t, err)
	defer func() { assert.NoError(t, conn.Close()) }()

	stream, err := remotefsv1.NewRemotePTYClient(conn).Attach(auth.NewOutgoingContext(ctx, "tok-alice"))
	require.NoError(t, err)

	var stdout bytes.Buffer
	client := ptyclient.New(ptyclient.Config{
		Stream: stream,
		Stdin: io.NopCloser(strings.NewReader(
			"printf '__RCLAUDE_INTEGRATED_PTY__\\n'\n" +
				"pwd\n" +
				"cat README.md\n" +
				"exit 7\n",
		)),
		Stdout: &stdout,
		Attach: ptyclient.AttachParams{
			InitialSize: ptyclient.WindowSize{Cols: 80, Rows: 24},
			Term:        "xterm-256color",
		},
		FrameMax: int(config.DefaultPTYFrameMaxBytes),
	})

	result := client.Run(ctx)
	require.NoError(t, result.Err)
	require.Nil(t, result.ServerError)
	assert.Equal(t, int32(7), result.Code)

	out := stdout.String()
	assert.Contains(t, out, "__RCLAUDE_INTEGRATED_PTY__")
	assert.Contains(t, out, filepath.Join(mountpoint, user.UserID))
	assert.Contains(t, out, "hello from daemon workspace")
	assert.Equal(t, inmemtest.RequestSnapshot{Kind: "read", Path: "README.md"}, user.LastRequest())
}

func newRealPTYService(t *testing.T, manager *session.Manager, workspaceRoot string) *ptyservice.Service {
	t.Helper()

	svc, err := ptyservice.New(ptyservice.Config{
		Registry:     integratedRegistry{manager: manager},
		Spawner:      realPTYSpawner{},
		Binary:       "/bin/sh",
		Workspace:    workspaceRoot,
		EnvWhitelist: append([]string(nil), config.DefaultPTYEnvPassthrough...),
		FrameMax:     config.DefaultPTYFrameMaxBytes,
	})
	require.NoError(t, err)
	return svc
}

type realPTYSpawner struct{}

func (realPTYSpawner) Spawn(req ptyhost.SpawnReq) (ptyservice.Host, error) {
	return ptyhost.Spawn(req)
}

func mountFUSEOrSkip(t *testing.T, ctx context.Context, opts fusefs.Options) fusefs.Mounted {
	t.Helper()

	probeFUSEEnvironment(t)
	mounted, err := fusefs.Mount(ctx, opts)
	if err != nil && isFUSEEnvironmentBlocker(err) {
		t.Skipf("skip integrated PTY/FUSE smoke test: %v", err)
	}
	require.NoError(t, err)
	return mounted
}

func probeFUSEEnvironment(t *testing.T) {
	t.Helper()

	file, err := os.OpenFile("/dev/fuse", os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
		t.Skip("skip integrated PTY/FUSE smoke test: /dev/fuse not available")
	}
	if err != nil && isFUSEEnvironmentBlocker(err) {
		t.Skipf("skip integrated PTY/FUSE smoke test: %v", err)
	}
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

func isFUSEEnvironmentBlocker(err error) bool {
	if err == nil {
		return false
	}
	if hasFUSEErrno(err) {
		return true
	}

	lower := strings.ToLower(err.Error())
	return containsAny(lower,
		"operation not permitted",
		"permission denied",
		"device not found",
		"no such file or directory",
		"not implemented",
	)
}

func hasFUSEErrno(err error) bool {
	targets := []error{
		syscall.EPERM,
		syscall.EACCES,
		syscall.ENODEV,
		syscall.ENOENT,
		syscall.ENOSYS,
	}
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func containsAny(text string, patterns ...string) bool {
	for _, pattern := range patterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}
