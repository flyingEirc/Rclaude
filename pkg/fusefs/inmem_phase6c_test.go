package fusefs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/internal/inmemtest"
)

func TestInmem_PrefetchWarmsCacheAndChangeInvalidates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "prefetch.txt"), []byte("hello"), 0o600))

	harness := inmemtest.NewHarness(t)
	defer harness.Cleanup()

	user := harness.AddUser(inmemtest.UserOptions{
		UserID:     "user-a",
		DaemonRoot: root,
	})

	infos, err := listInfos(harness.Manager, user.UserID, "")
	require.NoError(t, err)
	startPrefetch(context.Background(), harness.Manager, user.UserID, infos)

	require.Eventually(t, func() bool {
		info, ok := user.Session.Lookup("prefetch.txt")
		if !ok {
			return false
		}
		data, cached := user.Session.GetCachedContent("prefetch.txt", info)
		return cached && string(data) == "hello"
	}, time.Second, 10*time.Millisecond)
	assert.EqualValues(t, 1, user.ReadRequestCount())

	got, err := readChunk(context.Background(), harness.Manager, user.UserID, "prefetch.txt", 0, 5)
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), got)
	assert.EqualValues(t, 1, user.ReadRequestCount())

	require.NoError(t, os.WriteFile(filepath.Join(root, "prefetch.txt"), []byte("world!"), 0o600))
	fi, err := os.Stat(filepath.Join(root, "prefetch.txt"))
	require.NoError(t, err)

	user.PushChange(&remotefsv1.FileChange{
		Type: remotefsv1.ChangeType_CHANGE_TYPE_MODIFY,
		File: &remotefsv1.FileInfo{
			Path:    "prefetch.txt",
			Size:    fi.Size(),
			ModTime: fi.ModTime().Unix(),
			Mode:    uint32(fi.Mode().Perm()),
		},
	})
	require.True(t, user.WaitForPath("prefetch.txt", time.Second, func(info *remotefsv1.FileInfo, ok bool) bool {
		return ok && info.GetSize() == fi.Size() && info.GetModTime() == fi.ModTime().Unix()
	}))
	info, ok := user.Session.Lookup("prefetch.txt")
	require.True(t, ok)
	_, cached := user.Session.GetCachedContent("prefetch.txt", info)
	assert.False(t, cached)

	got, err = readChunk(context.Background(), harness.Manager, user.UserID, "prefetch.txt", 0, 6)
	require.NoError(t, err)
	assert.Equal(t, []byte("world!"), got)
	assert.EqualValues(t, 2, user.ReadRequestCount())
}
