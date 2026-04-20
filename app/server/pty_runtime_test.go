package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/config"
	"flyingEirc/Rclaude/pkg/session"
)

func TestNewPTYServiceBuildsFromServerConfig(t *testing.T) {
	t.Parallel()

	manager := session.NewManager()
	current := manager.NewSession("alice")
	require.NoError(t, current.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{}},
	}))
	_, err := manager.Register(current)
	require.NoError(t, err)

	svc, err := newPTYService(&config.ServerConfig{
		PTY: config.PTYConfig{
			Binary:                  testBinaryPath(t),
			WorkspaceRoot:           testWorkspaceRoot(),
			EnvPassthrough:          []string{"TERM", "PATH"},
			FrameMaxBytes:           64 * 1024,
			GracefulShutdownTimeout: time.Second,
			RateLimit: config.PTYRateLimitConfig{
				AttachQPS:   1,
				AttachBurst: 1,
				StdinBPS:    1024,
				StdinBurst:  512,
			},
		},
	}, manager)
	require.NoError(t, err)
	require.NotNil(t, svc)
}

func TestPTYRegistryReportsBusyWithoutLeakingOldToken(t *testing.T) {
	t.Parallel()

	manager := session.NewManager()
	registry := ptyRegistry{manager: manager}

	first, ok, err := registry.RegisterPTY("alice")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotEmpty(t, first)

	second, ok, err := registry.RegisterPTY("alice")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, second)

	registry.UnregisterPTY("alice", "wrong")

	third, ok, err := registry.RegisterPTY("alice")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, third)

	registry.UnregisterPTY("alice", first)

	afterRelease, ok, err := registry.RegisterPTY("alice")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.NotEmpty(t, afterRelease)
	assert.NotEqual(t, first, afterRelease)
}

func TestAttachLimiterStoreIsPerUser(t *testing.T) {
	t.Parallel()

	store := newAttachLimiterStore(1, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	start := time.Now()
	require.NoError(t, store.Wait(ctx, "alice"))
	require.NoError(t, store.Wait(ctx, "bob"))

	assert.Less(t, time.Since(start), 100*time.Millisecond)
}

func testBinaryPath(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	require.NoError(t, err)
	return path
}

func testWorkspaceRoot() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.TempDir(), "rclaude-app-server-pty")
	}
	return filepath.Join(string(filepath.Separator), "tmp", "rclaude-app-server-pty")
}
