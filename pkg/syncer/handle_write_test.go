package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/ratelimit"
)

func newWriteOpts(t *testing.T) (HandleOptions, writeDeps, string) {
	t.Helper()

	root := t.TempDir()
	filter, err := NewSensitiveFilter(nil)
	require.NoError(t, err)
	opts := HandleOptions{
		Root:            root,
		Locker:          newPathLocker(),
		SelfWrites:      newSelfWriteFilter(2 * time.Second),
		SensitiveFilter: filter,
	}
	return opts, depsFromOptions(opts), root
}

func TestHandleWrite_CreateAndOverwriteAtOffset(t *testing.T) {
	t.Parallel()

	opts, deps, root := newWriteOpts(t)
	resp := handleWrite(context.Background(), "r1", &remotefsv1.WriteFileReq{
		Path:    "a.txt",
		Content: []byte("hello"),
		Mode:    0o644,
	}, opts, deps)
	require.True(t, resp.GetSuccess(), resp.GetError())
	assert.Equal(t, "a.txt", resp.GetInfo().GetPath())

	resp = handleWrite(context.Background(), "r2", &remotefsv1.WriteFileReq{
		Path:    "a.txt",
		Content: []byte("X"),
		Offset:  1,
	}, opts, deps)
	require.True(t, resp.GetSuccess(), resp.GetError())

	//nolint:gosec // test reads from a tempdir fixture
	data, err := os.ReadFile(filepath.Join(root, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hXllo", string(data))
	assert.True(t, opts.SelfWrites.ShouldSuppress("a.txt"))
}

func TestHandleWrite_Append(t *testing.T) {
	t.Parallel()

	opts, deps, root := newWriteOpts(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi"), 0o600))

	resp := handleWrite(context.Background(), "r1", &remotefsv1.WriteFileReq{
		Path:    "a.txt",
		Content: []byte("!"),
		Append:  true,
	}, opts, deps)
	require.True(t, resp.GetSuccess(), resp.GetError())

	//nolint:gosec // test reads from a tempdir fixture
	data, err := os.ReadFile(filepath.Join(root, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hi!", string(data))
}

func TestHandleWrite_InvalidOffset(t *testing.T) {
	t.Parallel()

	opts, deps, _ := newWriteOpts(t)
	resp := handleWrite(context.Background(), "r1", &remotefsv1.WriteFileReq{
		Path:   "a.txt",
		Offset: -1,
	}, opts, deps)
	assert.False(t, resp.GetSuccess())
	assert.Contains(t, resp.GetError(), "invalid argument")
}

func TestHandleMkdirDeleteRenameTruncate(t *testing.T) {
	t.Parallel()

	opts, deps, root := newWriteOpts(t)

	require.True(t, handleMkdir("m1", &remotefsv1.MkdirReq{Path: "dir"}, opts, deps).GetSuccess())
	require.True(t, handleWrite(context.Background(), "w1", &remotefsv1.WriteFileReq{
		Path:    "dir/file.txt",
		Content: []byte("hello"),
	}, opts, deps).GetSuccess())

	renameResp := handleRename("rn1", &remotefsv1.RenameReq{
		OldPath: "dir/file.txt",
		NewPath: "dir/renamed.txt",
	}, opts, deps)
	require.True(t, renameResp.GetSuccess(), renameResp.GetError())
	assert.Equal(t, "dir/renamed.txt", renameResp.GetInfo().GetPath())

	truncateResp := handleTruncate("t1", &remotefsv1.TruncateReq{
		Path: "dir/renamed.txt",
		Size: 2,
	}, opts, deps)
	require.True(t, truncateResp.GetSuccess(), truncateResp.GetError())
	assert.EqualValues(t, 2, truncateResp.GetInfo().GetSize())

	//nolint:gosec // test reads from a tempdir fixture
	data, err := os.ReadFile(filepath.Join(root, "dir", "renamed.txt"))
	require.NoError(t, err)
	assert.Equal(t, "he", string(data))

	deleteResp := handleDelete("d1", &remotefsv1.DeleteReq{Path: "dir/renamed.txt"}, opts, deps)
	require.True(t, deleteResp.GetSuccess(), deleteResp.GetError())

	_, err = os.Stat(filepath.Join(root, "dir", "renamed.txt"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestHandleWrite_RespectsRateLimit(t *testing.T) {
	t.Parallel()

	opts, deps, root := newWriteOpts(t)
	opts.WriteLimiter = ratelimit.NewBytesPerSecond(1000)

	start := time.Now()
	resp := handleWrite(context.Background(), "w1", &remotefsv1.WriteFileReq{
		Path:    "a.txt",
		Content: make([]byte, 1500),
	}, opts, deps)
	elapsed := time.Since(start)

	require.True(t, resp.GetSuccess(), resp.GetError())
	assert.GreaterOrEqual(t, elapsed, 400*time.Millisecond)

	_, err := os.Stat(filepath.Join(root, "a.txt"))
	require.NoError(t, err)
}

func TestHandleWrite_CanceledWhileRateLimited(t *testing.T) {
	t.Parallel()

	opts, deps, root := newWriteOpts(t)
	opts.WriteLimiter = ratelimit.NewBytesPerSecond(1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resp := handleWrite(ctx, "w1", &remotefsv1.WriteFileReq{
		Path:    "a.txt",
		Content: []byte("ab"),
	}, opts, deps)
	assert.False(t, resp.GetSuccess())
	assert.Contains(t, resp.GetError(), "context canceled")

	_, err := os.Stat(filepath.Join(root, "a.txt"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestHandleWrite_CreateEmptyContentSkipsRateLimit(t *testing.T) {
	t.Parallel()

	opts, deps, root := newWriteOpts(t)
	opts.WriteLimiter = ratelimit.NewBytesPerSecond(1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resp := handleWrite(ctx, "w1", &remotefsv1.WriteFileReq{
		Path: "a.txt",
		Mode: 0o644,
	}, opts, deps)
	require.True(t, resp.GetSuccess(), resp.GetError())

	_, err := os.Stat(filepath.Join(root, "a.txt"))
	require.NoError(t, err)
}

func TestHandleRename_UnsafePath(t *testing.T) {
	t.Parallel()

	opts, deps, _ := newWriteOpts(t)
	resp := handleRename("rn1", &remotefsv1.RenameReq{
		OldPath: "../a",
		NewPath: "b",
	}, opts, deps)
	assert.False(t, resp.GetSuccess())
	assert.Contains(t, resp.GetError(), "unsafe path")
}

func TestHandleTruncate_InvalidArgument(t *testing.T) {
	t.Parallel()

	opts, deps, _ := newWriteOpts(t)
	resp := handleTruncate("t1", &remotefsv1.TruncateReq{
		Path: "a.txt",
		Size: -1,
	}, opts, deps)
	assert.False(t, resp.GetSuccess())
	assert.Contains(t, resp.GetError(), "invalid argument")
}

func TestHandleWriteOps_DenySensitivePaths(t *testing.T) {
	t.Parallel()

	opts, deps, root := newWriteOpts(t)
	filter, err := NewSensitiveFilter([]string{"secrets/**"})
	require.NoError(t, err)
	opts.SensitiveFilter = filter
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".ssh"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".ssh", "id_ed25519"), []byte("secret"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "plain.txt"), []byte("plain"), 0o600))

	tests := []struct {
		name string
		resp *remotefsv1.FileResponse
	}{
		{
			name: "write sensitive file",
			resp: handleWrite(context.Background(), "w1", &remotefsv1.WriteFileReq{Path: ".env", Content: []byte("x")}, opts, deps),
		},
		{
			name: "mkdir sensitive dir",
			resp: handleMkdir("m1", &remotefsv1.MkdirReq{Path: "secrets"}, opts, deps),
		},
		{
			name: "delete sensitive file",
			resp: handleDelete("d1", &remotefsv1.DeleteReq{Path: ".ssh/id_ed25519"}, opts, deps),
		},
		{
			name: "rename into sensitive target",
			resp: handleRename("r1", &remotefsv1.RenameReq{OldPath: "plain.txt", NewPath: ".env"}, opts, deps),
		},
		{
			name: "rename from sensitive source",
			resp: handleRename("r2", &remotefsv1.RenameReq{OldPath: ".env", NewPath: "renamed.txt"}, opts, deps),
		},
		{
			name: "truncate sensitive file",
			resp: handleTruncate("t1", &remotefsv1.TruncateReq{Path: ".env", Size: 0}, opts, deps),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			assert.False(t, tc.resp.GetSuccess())
			assert.Contains(t, tc.resp.GetError(), "permission denied")
		})
	}
}

func TestHandleWriteAndTruncate_DenySensitiveSymlinkAliases(t *testing.T) {
	t.Parallel()

	opts, deps, root := newWriteOpts(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0o600))
	require.NoError(t, os.Symlink(".env", filepath.Join(root, "visible.txt")))
	require.NoError(t, os.Symlink(".env.new", filepath.Join(root, "future.txt")))

	writeResp := handleWrite(context.Background(), "w1", &remotefsv1.WriteFileReq{
		Path:    "visible.txt",
		Content: []byte("mutated"),
	}, opts, deps)
	assert.False(t, writeResp.GetSuccess())
	assert.Contains(t, writeResp.GetError(), "permission denied")

	truncateResp := handleTruncate("t1", &remotefsv1.TruncateReq{
		Path: "visible.txt",
		Size: 0,
	}, opts, deps)
	assert.False(t, truncateResp.GetSuccess())
	assert.Contains(t, truncateResp.GetError(), "permission denied")

	createResp := handleWrite(context.Background(), "w2", &remotefsv1.WriteFileReq{
		Path:    "future.txt",
		Content: []byte("new secret"),
	}, opts, deps)
	assert.False(t, createResp.GetSuccess())
	assert.Contains(t, createResp.GetError(), "permission denied")

	//nolint:gosec // test reads from a tempdir fixture
	data, err := os.ReadFile(filepath.Join(root, ".env"))
	require.NoError(t, err)
	assert.Equal(t, "secret", string(data))

	_, err = os.Stat(filepath.Join(root, ".env.new"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestHandleRename_DenyDirectoryContainingSensitiveDescendant(t *testing.T) {
	t.Parallel()

	opts, deps, root := newWriteOpts(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "config"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "config", ".env"), []byte("secret"), 0o600))

	resp := handleRename("rn1", &remotefsv1.RenameReq{
		OldPath: "config",
		NewPath: "moved",
	}, opts, deps)
	assert.False(t, resp.GetSuccess())
	assert.Contains(t, resp.GetError(), "permission denied")

	_, err := os.Stat(filepath.Join(root, "config", ".env"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, "moved"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}
