package syncer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

func newWriteOpts(t *testing.T) (HandleOptions, writeDeps, string) {
	t.Helper()

	root := t.TempDir()
	opts := HandleOptions{
		Root:       root,
		Locker:     newPathLocker(),
		SelfWrites: newSelfWriteFilter(2 * time.Second),
	}
	return opts, depsFromOptions(opts), root
}

func TestHandleWrite_CreateAndOverwriteAtOffset(t *testing.T) {
	t.Parallel()

	opts, deps, root := newWriteOpts(t)
	resp := handleWrite("r1", &remotefsv1.WriteFileReq{
		Path:    "a.txt",
		Content: []byte("hello"),
		Mode:    0o644,
	}, opts, deps)
	require.True(t, resp.GetSuccess(), resp.GetError())
	assert.Equal(t, "a.txt", resp.GetInfo().GetPath())

	resp = handleWrite("r2", &remotefsv1.WriteFileReq{
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

	resp := handleWrite("r1", &remotefsv1.WriteFileReq{
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
	resp := handleWrite("r1", &remotefsv1.WriteFileReq{
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
	require.True(t, handleWrite("w1", &remotefsv1.WriteFileReq{
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
