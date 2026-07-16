package syncer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateWorkspaceRoot_DerivesProjectName(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "my-project")
	require.NoError(t, os.MkdirAll(root, 0o750))

	name, err := ValidateWorkspaceRoot(root)
	require.NoError(t, err)
	assert.Equal(t, "my-project", name)
}

func TestValidateWorkspaceRoot_Rejections(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		_, err := ValidateWorkspaceRoot("")
		assert.ErrorIs(t, err, ErrWorkspaceRootRequired)
	})

	t.Run("relative", func(t *testing.T) {
		t.Parallel()
		_, err := ValidateWorkspaceRoot("relative/path")
		assert.ErrorIs(t, err, ErrWorkspaceRootNotAbs)
	})

	t.Run("missing directory", func(t *testing.T) {
		t.Parallel()
		_, err := ValidateWorkspaceRoot(filepath.Join(t.TempDir(), "nope"))
		assert.ErrorIs(t, err, ErrWorkspaceRootNotDir)
	})

	t.Run("file instead of directory", func(t *testing.T) {
		t.Parallel()
		file := filepath.Join(t.TempDir(), "f.txt")
		require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
		_, err := ValidateWorkspaceRoot(file)
		assert.ErrorIs(t, err, ErrWorkspaceRootNotDir)
	})

	t.Run("unsafe project name", func(t *testing.T) {
		t.Parallel()
		root := filepath.Join(t.TempDir(), "bad name\tx")
		require.NoError(t, os.MkdirAll(root, 0o750))
		_, err := ValidateWorkspaceRoot(root)
		assert.ErrorIs(t, err, ErrWorkspaceNameUnsafe)
	})
}

func TestResolveWorkspaceRoot_UsesCwd(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj-x")
	require.NoError(t, os.MkdirAll(root, 0o750))
	t.Chdir(root)

	got, err := ResolveWorkspaceRoot()
	require.NoError(t, err)
	// macOS 的 TempDir 带 /private 符号链接前缀，比较经 EvalSymlinks 归一化的路径。
	wantResolved, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	gotResolved, err := filepath.EvalSymlinks(got)
	require.NoError(t, err)
	assert.Equal(t, wantResolved, gotResolved)

	name, err := ValidateWorkspaceRoot(got)
	require.NoError(t, err)
	assert.Equal(t, "proj-x", name)
}

func TestResolveWorkspaceRoot_RejectsHomeDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	t.Chdir(home)

	_, err = ResolveWorkspaceRoot()
	assert.ErrorIs(t, err, ErrWorkspaceRootUnscoped)
}

func TestResolveWorkspaceRoot_RejectsFilesystemRoot(t *testing.T) {
	t.Chdir(string(filepath.Separator))

	_, err := ResolveWorkspaceRoot()
	assert.ErrorIs(t, err, ErrWorkspaceRootUnscoped)
}
