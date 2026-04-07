package fusefs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/internal/inmemtest"
)

func TestInmem_SensitivePathsAreHiddenAndWriteDenied(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".ssh"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".ssh", "id_ed25519"), []byte("private"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "visible.txt"), []byte("ok"), 0o600))

	harness := inmemtest.NewHarness(t)
	defer harness.Cleanup()

	user := harness.AddUser(inmemtest.UserOptions{
		UserID:     "user-a",
		DaemonRoot: root,
	})

	entries, err := listInfos(harness.Manager, user.UserID, "")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.ElementsMatch(t, []string{".ssh", "visible.txt"}, []string{
		entries[0].GetPath(),
		entries[1].GetPath(),
	})

	_, err = lookupInfo(harness.Manager, user.UserID, ".env")
	require.ErrorIs(t, err, ErrPathNotFound)

	_, err = readChunk(context.Background(), harness.Manager, user.UserID, ".env", 0, 6)
	require.ErrorIs(t, err, ErrPathNotFound)

	err = createFile(context.Background(), harness.Manager, user.UserID, ".env")
	require.ErrorIs(t, err, ErrPermissionDenied)

	err = renamePath(context.Background(), harness.Manager, user.UserID, "visible.txt", ".env")
	require.ErrorIs(t, err, ErrPermissionDenied)
}
