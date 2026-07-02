package syncer

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlocksSensitivePath_HandlesSymlinkedWorkspaceRoot(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	realRoot := filepath.Join(parent, "real")
	aliasRoot := filepath.Join(parent, "alias")
	require.NoError(t, os.Mkdir(realRoot, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(realRoot, ".env"), []byte("secret"), 0o600))
	require.NoError(t, os.Symlink(".env", filepath.Join(realRoot, "visible.txt")))
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			t.Skipf("skip symlink root test: %v", err)
		}
		require.NoError(t, err)
	}

	filter, err := NewSensitiveFilter(nil)
	require.NoError(t, err)

	blocked, err := blocksSensitivePath(aliasRoot, "visible.txt", filter, pathResolutionOptions{
		followFinalSymlink: true,
	})
	require.NoError(t, err)
	assert.True(t, blocked)
}
