//go:build linux

package fusefs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/internal/inmemtest"
)

const defaultFuseCommandTimeout = 30 * time.Second

func TestMount_LinuxSmoke(t *testing.T) {
	harness := inmemtest.NewHarness(t)
	defer harness.Cleanup()

	userA := harness.AddUser(inmemtest.UserOptions{UserID: "user-a"})
	harness.AddUser(inmemtest.UserOptions{UserID: "user-b"})

	require.NoError(t, createFile(context.Background(), harness.Manager, userA.UserID, "existing.txt"))
	require.NoError(t, writeChunk(context.Background(), harness.Manager, userA.UserID, "existing.txt", 0, []byte("hello")))

	mountpoint := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mounted := mountOrSkip(t, ctx, Options{
		Mountpoint: mountpoint,
		Manager:    harness.Manager,
	})
	defer func() {
		assert.NoError(t, mounted.Close())
	}()

	rootEntries, err := os.ReadDir(mountpoint)
	require.NoError(t, err)
	assert.Equal(t, []string{"user-a", "user-b"}, dirEntryNames(rootEntries))

	userEntries, err := os.ReadDir(filepath.Join(mountpoint, userA.UserID))
	require.NoError(t, err)
	assert.Equal(t, []string{userA.WorkspaceName}, dirEntryNames(userEntries),
		"user dir must list exactly the bootstrap workspace name")

	got, err := readMountedFile(mountpoint, userA.UserID, userA.WorkspaceName, "existing.txt")
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))

	createdPath := filepath.Join(mountpoint, userA.UserID, userA.WorkspaceName, "created.txt")
	require.NoError(t, os.WriteFile(createdPath, []byte("world"), 0o600))
	got, err = os.ReadFile(userA.AbsPath("created.txt"))
	require.NoError(t, err)
	assert.Equal(t, "world", string(got))

	renamedPath := filepath.Join(mountpoint, userA.UserID, userA.WorkspaceName, "renamed.txt")
	require.NoError(t, os.Rename(createdPath, renamedPath))
	_, err = os.Stat(userA.AbsPath("created.txt"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	got, err = os.ReadFile(userA.AbsPath("renamed.txt"))
	require.NoError(t, err)
	assert.Equal(t, "world", string(got))

	require.NoError(t, os.Remove(renamedPath))
	_, err = os.Stat(userA.AbsPath("renamed.txt"))
	assert.ErrorIs(t, err, os.ErrNotExist)

	rootEntries, err = os.ReadDir(mountpoint)
	require.NoError(t, err)
	assert.Equal(t, []string{"user-a", "user-b"}, dirEntryNames(rootEntries))
}

func TestMount_LinuxSmoke_CwdAndGoList(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("skip Go cwd smoke test: %v", err)
	}

	daemonRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(daemonRoot, "go.mod"), []byte("module example.com/rclaude-smoke\n\ngo 1.25\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(daemonRoot, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600))

	harness := inmemtest.NewHarness(t)
	defer harness.Cleanup()

	user := harness.AddUser(inmemtest.UserOptions{UserID: "user-a", DaemonRoot: daemonRoot})

	mountpoint := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mounted := mountOrSkip(t, ctx, Options{
		Mountpoint: mountpoint,
		Manager:    harness.Manager,
	})
	defer func() {
		assert.NoError(t, mounted.Close())
	}()

	workspace := filepath.Join(mountpoint, user.UserID, user.WorkspaceName)
	cmdCtx, cmdCancel := context.WithTimeout(context.Background(), defaultFuseCommandTimeout)
	defer cmdCancel()

	pwdCmd := exec.CommandContext(cmdCtx, "/bin/pwd")
	pwdCmd.Dir = workspace
	pwdOut, err := pwdCmd.CombinedOutput()
	require.NoErrorf(t, err, "pwd output:\n%s", string(pwdOut))
	assert.Equal(t, workspace, strings.TrimSpace(string(pwdOut)))

	cmd := exec.CommandContext(cmdCtx, "go", "list", "./...")
	cmd.Dir = workspace
	cmd.Env = append(os.Environ(),
		"GOCACHE="+t.TempDir(),
		"GOMODCACHE="+t.TempDir(),
		"GOPATH="+t.TempDir(),
		"GOWORK=off",
	)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "command output:\n%s", string(out))
	assert.Contains(t, string(out), "example.com/rclaude-smoke")
}

func mountOrSkip(t *testing.T, ctx context.Context, opts Options) Mounted {
	t.Helper()

	probeFuseEnvironment(t)

	mounted, err := Mount(ctx, opts)
	if err != nil && isFuseEnvironmentBlocker(err) {
		t.Skipf("skip FUSE smoke test: %v", err)
	}
	require.NoError(t, err)
	return mounted
}

func probeFuseEnvironment(t *testing.T) {
	t.Helper()

	file, err := os.OpenFile("/dev/fuse", os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
		t.Skip("skip FUSE smoke test: /dev/fuse not available")
	}
	if err != nil && isFuseEnvironmentBlocker(err) {
		t.Skipf("skip FUSE smoke test: %v", err)
	}
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

func isFuseEnvironmentBlocker(err error) bool {
	if err == nil {
		return false
	}
	if hasFuseErrno(err) {
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

func dirEntryNames(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}

func hasFuseErrno(err error) bool {
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

func readMountedFile(mountpoint, userID, workspace, relPath string) ([]byte, error) {
	//nolint:gosec // test paths are built from t.TempDir and fixed test user ids
	return os.ReadFile(filepath.Join(mountpoint, userID, workspace, relPath))
}
