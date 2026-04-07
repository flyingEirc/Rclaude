package fusefs

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/internal/inmemtest"
)

func TestInmem_CreateAndWrite(t *testing.T) {
	t.Parallel()

	pair := inmemtest.Start(t, "")
	defer pair.Cleanup()

	require.NoError(t, createFile(context.Background(), pair.Manager, pair.UserID, "a.txt"))
	require.NoError(t, writeChunk(context.Background(), pair.Manager, pair.UserID, "a.txt", 0, []byte("hello")))

	got, err := os.ReadFile(pair.AbsPath("a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
}

func TestInmem_Mkdir_Unlink_Rmdir(t *testing.T) {
	t.Parallel()

	pair := inmemtest.Start(t, "")
	defer pair.Cleanup()

	require.NoError(t, mkdirAt(context.Background(), pair.Manager, pair.UserID, "d", false))
	require.NoError(t, createFile(context.Background(), pair.Manager, pair.UserID, "d/x"))
	require.NoError(t, removePath(context.Background(), pair.Manager, pair.UserID, "d/x"))
	require.NoError(t, removePath(context.Background(), pair.Manager, pair.UserID, "d"))

	_, err := os.Stat(pair.AbsPath("d"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestInmem_Truncate(t *testing.T) {
	t.Parallel()

	pair := inmemtest.Start(t, "")
	defer pair.Cleanup()

	require.NoError(t, createFile(context.Background(), pair.Manager, pair.UserID, "a.txt"))
	require.NoError(t, writeChunk(context.Background(), pair.Manager, pair.UserID, "a.txt", 0, []byte("hello world")))
	require.NoError(t, truncatePath(context.Background(), pair.Manager, pair.UserID, "a.txt", 3))

	got, err := os.ReadFile(pair.AbsPath("a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hel", string(got))
}

func TestInmem_Rename_AcrossDir(t *testing.T) {
	t.Parallel()

	pair := inmemtest.Start(t, "")
	defer pair.Cleanup()

	require.NoError(t, mkdirAt(context.Background(), pair.Manager, pair.UserID, "a", false))
	require.NoError(t, mkdirAt(context.Background(), pair.Manager, pair.UserID, "b", false))
	require.NoError(t, createFile(context.Background(), pair.Manager, pair.UserID, "a/x"))
	require.NoError(t, renamePath(context.Background(), pair.Manager, pair.UserID, "a/x", "b/x"))

	_, err := os.Stat(pair.AbsPath("a/x"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(pair.AbsPath("b/x"))
	assert.NoError(t, err)
}

func TestInmem_WriteFailure_NotFound(t *testing.T) {
	t.Parallel()

	pair := inmemtest.Start(t, "")
	defer pair.Cleanup()

	err := truncatePath(context.Background(), pair.Manager, pair.UserID, "missing", 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPathNotFound)
}

func TestInmem_ApplyWriteResult_Visible(t *testing.T) {
	t.Parallel()

	pair := inmemtest.Start(t, "")
	defer pair.Cleanup()

	require.NoError(t, createFile(context.Background(), pair.Manager, pair.UserID, "a.txt"))

	current, ok := pair.Manager.Get(pair.UserID)
	require.True(t, ok)
	info, ok := current.Lookup("a.txt")
	require.True(t, ok)
	assert.Equal(t, "a.txt", info.GetPath())
}

func TestInmem_Rename_NoDeadlock(t *testing.T) {
	t.Parallel()

	pair := inmemtest.Start(t, "")
	defer pair.Cleanup()

	require.NoError(t, createFile(context.Background(), pair.Manager, pair.UserID, "x"))
	require.NoError(t, createFile(context.Background(), pair.Manager, pair.UserID, "y"))

	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for range 5 {
			wg.Add(2)
			go func() {
				defer wg.Done()
				if err := renamePath(context.Background(), pair.Manager, pair.UserID, "x", "y"); err != nil {
					return
				}
			}()
			go func() {
				defer wg.Done()
				if err := renamePath(context.Background(), pair.Manager, pair.UserID, "y", "x"); err != nil {
					return
				}
			}()
		}
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("rename pair deadlocked under in-memory transport")
	}
}

func TestInmem_ReadUsesContentCache(t *testing.T) {
	t.Parallel()

	pair := inmemtest.Start(t, "")
	defer pair.Cleanup()

	require.NoError(t, createFile(context.Background(), pair.Manager, pair.UserID, "a.txt"))
	require.NoError(t, writeChunk(context.Background(), pair.Manager, pair.UserID, "a.txt", 0, []byte("hello")))

	first, err := readChunk(context.Background(), pair.Manager, pair.UserID, "a.txt", 0, 2)
	require.NoError(t, err)
	assert.Equal(t, []byte("he"), first)

	second, err := readChunk(context.Background(), pair.Manager, pair.UserID, "a.txt", 2, 3)
	require.NoError(t, err)
	assert.Equal(t, []byte("llo"), second)
	assert.EqualValues(t, 1, pair.ReadRequestCount())
}

func TestInmem_ChangeInvalidatesContentCache(t *testing.T) {
	t.Parallel()

	pair := inmemtest.Start(t, "")
	defer pair.Cleanup()

	require.NoError(t, createFile(context.Background(), pair.Manager, pair.UserID, "a.txt"))
	require.NoError(t, writeChunk(context.Background(), pair.Manager, pair.UserID, "a.txt", 0, []byte("hello")))

	_, err := readChunk(context.Background(), pair.Manager, pair.UserID, "a.txt", 0, 5)
	require.NoError(t, err)
	assert.EqualValues(t, 1, pair.ReadRequestCount())

	require.NoError(t, os.WriteFile(pair.AbsPath("a.txt"), []byte("world!"), 0o600))
	fi, err := os.Stat(pair.AbsPath("a.txt"))
	require.NoError(t, err)

	pair.PushChange(&remotefsv1.FileChange{
		Type: remotefsv1.ChangeType_CHANGE_TYPE_MODIFY,
		File: &remotefsv1.FileInfo{
			Path:    "a.txt",
			Size:    fi.Size(),
			ModTime: fi.ModTime().Unix(),
			Mode:    uint32(fi.Mode().Perm()),
		},
	})

	require.Eventually(t, func() bool {
		info, ok := pair.Session.Lookup("a.txt")
		return ok && info.GetSize() == fi.Size() && info.GetModTime() == fi.ModTime().Unix()
	}, time.Second, 10*time.Millisecond)

	got, err := readChunk(context.Background(), pair.Manager, pair.UserID, "a.txt", 0, 6)
	require.NoError(t, err)
	assert.Equal(t, []byte("world!"), got)
	assert.EqualValues(t, 2, pair.ReadRequestCount())
}
