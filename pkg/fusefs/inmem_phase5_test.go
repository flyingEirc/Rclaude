package fusefs

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/internal/inmemtest"
)

func TestInmem_MultiUserIsolation(t *testing.T) {
	t.Parallel()

	harness := inmemtest.NewHarness(t)
	defer harness.Cleanup()

	userA := harness.AddUser(inmemtest.UserOptions{UserID: "user-a"})
	userB := harness.AddUser(inmemtest.UserOptions{UserID: "user-b"})

	require.NoError(t, createFile(context.Background(), harness.Manager, userA.UserID, "same.txt"))
	require.NoError(t, writeChunk(context.Background(), harness.Manager, userA.UserID, "same.txt", 0, []byte("alpha")))
	require.NoError(t, createFile(context.Background(), harness.Manager, userB.UserID, "same.txt"))
	require.NoError(t, writeChunk(context.Background(), harness.Manager, userB.UserID, "same.txt", 0, []byte("bravo")))

	gotA, err := readChunk(context.Background(), harness.Manager, userA.UserID, "same.txt", 0, 5)
	require.NoError(t, err)
	gotB, err := readChunk(context.Background(), harness.Manager, userB.UserID, "same.txt", 0, 5)
	require.NoError(t, err)

	assert.Equal(t, []byte("alpha"), gotA)
	assert.Equal(t, []byte("bravo"), gotB)
	assert.Equal(t, []string{"user-a", "user-b"}, harness.Manager.UserIDs())
	assert.EqualValues(t, 1, userA.ReadRequestCount())
	assert.EqualValues(t, 1, userB.ReadRequestCount())
}

func TestInmem_ChangeOnOneUserDoesNotInvalidateOther(t *testing.T) {
	t.Parallel()

	harness := inmemtest.NewHarness(t)
	defer harness.Cleanup()

	userA := harness.AddUser(inmemtest.UserOptions{UserID: "user-a"})
	userB := harness.AddUser(inmemtest.UserOptions{UserID: "user-b"})

	require.NoError(t, createFile(context.Background(), harness.Manager, userA.UserID, "same.txt"))
	require.NoError(t, writeChunk(context.Background(), harness.Manager, userA.UserID, "same.txt", 0, []byte("alpha")))
	require.NoError(t, createFile(context.Background(), harness.Manager, userB.UserID, "same.txt"))
	require.NoError(t, writeChunk(context.Background(), harness.Manager, userB.UserID, "same.txt", 0, []byte("bravo")))

	_, err := readChunk(context.Background(), harness.Manager, userA.UserID, "same.txt", 0, 5)
	require.NoError(t, err)
	_, err = readChunk(context.Background(), harness.Manager, userB.UserID, "same.txt", 0, 5)
	require.NoError(t, err)
	assert.EqualValues(t, 1, userA.ReadRequestCount())
	assert.EqualValues(t, 1, userB.ReadRequestCount())

	require.NoError(t, os.WriteFile(userA.AbsPath("same.txt"), []byte("delta!"), 0o600))
	fi, err := os.Stat(userA.AbsPath("same.txt"))
	require.NoError(t, err)

	userA.PushChange(&remotefsv1.FileChange{
		Type: remotefsv1.ChangeType_CHANGE_TYPE_MODIFY,
		File: &remotefsv1.FileInfo{
			Path:    "same.txt",
			Size:    fi.Size(),
			ModTime: fi.ModTime().Unix(),
			Mode:    uint32(fi.Mode().Perm()),
		},
	})
	require.True(t, userA.WaitForPath("same.txt", time.Second, func(info *remotefsv1.FileInfo, ok bool) bool {
		return ok && info.GetModTime() == fi.ModTime().Unix() && info.GetSize() == fi.Size()
	}))

	gotA, err := readChunk(context.Background(), harness.Manager, userA.UserID, "same.txt", 0, 6)
	require.NoError(t, err)
	gotB, err := readChunk(context.Background(), harness.Manager, userB.UserID, "same.txt", 0, 5)
	require.NoError(t, err)

	assert.Equal(t, []byte("delta!"), gotA)
	assert.Equal(t, []byte("bravo"), gotB)
	assert.EqualValues(t, 2, userA.ReadRequestCount())
	assert.EqualValues(t, 1, userB.ReadRequestCount())
}

func TestInmem_ReadTimeout(t *testing.T) {
	t.Parallel()

	harness := inmemtest.NewHarness(t, inmemtest.HarnessOptions{
		RequestTimeout: 20 * time.Millisecond,
		CacheMaxBytes:  0,
	})
	defer harness.Cleanup()

	user := harness.AddUser(inmemtest.UserOptions{UserID: "user-a"})
	require.NoError(t, createFile(context.Background(), harness.Manager, user.UserID, "slow.txt"))
	require.NoError(t, writeChunk(context.Background(), harness.Manager, user.UserID, "slow.txt", 0, []byte("slow")))

	user.SetFaults(inmemtest.FaultHooks{
		BeforeHandle: func(req *remotefsv1.FileRequest) inmemtest.Action {
			if req.GetRead() != nil {
				return inmemtest.Delay(120 * time.Millisecond)
			}
			return inmemtest.Pass()
		},
	})

	start := time.Now()
	_, err := readChunk(context.Background(), harness.Manager, user.UserID, "slow.txt", 0, 4)
	require.ErrorIs(t, err, ErrRequestTimeout)
	assert.Less(t, time.Since(start), time.Second)
}

func TestInmem_WriteTimeout(t *testing.T) {
	t.Parallel()

	harness := inmemtest.NewHarness(t, inmemtest.HarnessOptions{
		RequestTimeout: 20 * time.Millisecond,
		CacheMaxBytes:  0,
	})
	defer harness.Cleanup()

	user := harness.AddUser(inmemtest.UserOptions{UserID: "user-a"})
	require.NoError(t, createFile(context.Background(), harness.Manager, user.UserID, "slow.txt"))

	user.SetFaults(inmemtest.FaultHooks{
		BeforeHandle: func(req *remotefsv1.FileRequest) inmemtest.Action {
			if req.GetWrite() != nil {
				return inmemtest.Delay(120 * time.Millisecond)
			}
			return inmemtest.Pass()
		},
	})

	start := time.Now()
	err := writeChunk(context.Background(), harness.Manager, user.UserID, "slow.txt", 0, []byte("slow"))
	require.ErrorIs(t, err, ErrRequestTimeout)
	assert.Less(t, time.Since(start), time.Second)
}

func TestInmem_DisconnectDoesNotBlockFurtherRequests(t *testing.T) {
	t.Parallel()

	harness := inmemtest.NewHarness(t, inmemtest.HarnessOptions{
		RequestTimeout: 200 * time.Millisecond,
		CacheMaxBytes:  0,
	})
	defer harness.Cleanup()

	user := harness.AddUser(inmemtest.UserOptions{UserID: "user-a"})
	require.NoError(t, createFile(context.Background(), harness.Manager, user.UserID, "a.txt"))
	require.NoError(t, writeChunk(context.Background(), harness.Manager, user.UserID, "a.txt", 0, []byte("hello")))

	user.SetFaults(inmemtest.FaultHooks{
		BeforeHandle: func(req *remotefsv1.FileRequest) inmemtest.Action {
			if req.GetRead() != nil {
				return inmemtest.DropConnection(nil)
			}
			return inmemtest.Pass()
		},
	})

	start := time.Now()
	_, err := readChunk(context.Background(), harness.Manager, user.UserID, "a.txt", 0, 5)
	require.ErrorIs(t, err, ErrSessionFailed)
	assert.Less(t, time.Since(start), time.Second)

	start = time.Now()
	_, err = readChunk(context.Background(), harness.Manager, user.UserID, "a.txt", 0, 5)
	require.ErrorIs(t, err, ErrSessionOffline)
	assert.Less(t, time.Since(start), time.Second)
}

func TestInmem_OfflineReadonlyUsesCacheUntilTTLExpires(t *testing.T) {
	t.Parallel()

	harness := inmemtest.NewHarness(t, inmemtest.HarnessOptions{
		RequestTimeout:     200 * time.Millisecond,
		CacheMaxBytes:      64,
		OfflineReadOnlyTTL: 60 * time.Millisecond,
	})
	defer harness.Cleanup()

	user := harness.AddUser(inmemtest.UserOptions{UserID: "user-a"})
	require.NoError(t, createFile(context.Background(), harness.Manager, user.UserID, "cached.txt"))
	require.NoError(t, writeChunk(context.Background(), harness.Manager, user.UserID, "cached.txt", 0, []byte("hello")))
	require.NoError(t, createFile(context.Background(), harness.Manager, user.UserID, "cold.txt"))
	require.NoError(t, writeChunk(context.Background(), harness.Manager, user.UserID, "cold.txt", 0, []byte("world")))

	_, err := readChunk(context.Background(), harness.Manager, user.UserID, "cached.txt", 0, 5)
	require.NoError(t, err)
	assert.EqualValues(t, 1, user.ReadRequestCount())

	user.Disconnect()
	require.Eventually(t, func() bool {
		return user.Session.IsOfflineReadonly(time.Time{})
	}, time.Second, 10*time.Millisecond)

	got, err := readChunk(context.Background(), harness.Manager, user.UserID, "cached.txt", 0, 5)
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), got)
	assert.EqualValues(t, 1, user.ReadRequestCount())

	_, err = readChunk(context.Background(), harness.Manager, user.UserID, "cold.txt", 0, 5)
	require.ErrorIs(t, err, ErrSessionOffline)
	assert.EqualValues(t, 1, user.ReadRequestCount())

	require.Eventually(t, func() bool {
		_, ok := harness.Manager.Get(user.UserID)
		return !ok
	}, time.Second, 10*time.Millisecond)

	_, err = readChunk(context.Background(), harness.Manager, user.UserID, "cached.txt", 0, 5)
	require.ErrorIs(t, err, ErrSessionOffline)
	assert.Empty(t, harness.Manager.UserIDs())
}
