package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

func TestManagerRegisterAndUserIDs(t *testing.T) {
	t.Parallel()

	manager := NewManager()
	s1 := NewSession("user-b")
	s2 := NewSession("user-a")

	prev, err := manager.Register(s1)
	require.NoError(t, err)
	assert.Nil(t, prev)

	prev, err = manager.Register(s2)
	require.NoError(t, err)
	assert.Nil(t, prev)

	assert.Equal(t, []string{"user-a", "user-b"}, manager.UserIDs())

	got, ok := manager.Get("user-a")
	require.True(t, ok)
	assert.Same(t, s2, got)

	manager.Remove(s1)
	assert.Equal(t, []string{"user-a"}, manager.UserIDs())
}

func TestManagerRegisterReplacesPrevious(t *testing.T) {
	t.Parallel()

	manager := NewManager()
	first := NewSession("user-1")
	second := NewSession("user-1")

	_, err := manager.Register(first)
	require.NoError(t, err)

	prev, err := manager.Register(second)
	require.NoError(t, err)
	assert.Same(t, first, prev)

	got, ok := manager.Get("user-1")
	require.True(t, ok)
	assert.Same(t, second, got)

	manager.Remove(first)
	got, ok = manager.Get("user-1")
	require.True(t, ok)
	assert.Same(t, second, got)
}

func TestManagerRequestTimeout(t *testing.T) {
	t.Parallel()

	manager := NewManager(ManagerOptions{RequestTimeout: 5 * time.Second})
	assert.Equal(t, 5*time.Second, manager.RequestTimeout())
}

func TestManagerNewSessionCarriesCacheConfig(t *testing.T) {
	t.Parallel()

	manager := NewManager(ManagerOptions{CacheMaxBytes: 64})
	current := manager.NewSession("user-1")

	require.True(t, current.PutCachedContent("a.txt", newInfo("a.txt", false, 1, 1), []byte("a")))
	got, ok := current.GetCachedContent("a.txt", newInfo("a.txt", false, 1, 1))
	require.True(t, ok)
	assert.Equal(t, []byte("a"), got)
	assert.EqualValues(t, 64, manager.CacheMaxBytes())
}

func TestManagerHandleDisconnectRetainsOfflineReadonly(t *testing.T) {
	t.Parallel()

	manager := NewManager(ManagerOptions{OfflineReadOnlyTTL: time.Minute})
	current := NewSession("user-1")
	require.NoError(t, current.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{}},
	}))
	_, err := manager.Register(current)
	require.NoError(t, err)

	current.closeWithError(nil)
	manager.HandleDisconnect(current, nil)

	got, ok := manager.Get("user-1")
	require.True(t, ok)
	assert.Same(t, current, got)
	assert.True(t, got.IsOfflineReadonly(time.Time{}))
}

func TestManagerGetPrunesExpiredOfflineSession(t *testing.T) {
	t.Parallel()

	manager := NewManager()
	current := NewSession("user-1")
	require.NoError(t, current.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{}},
	}))
	_, err := manager.Register(current)
	require.NoError(t, err)

	require.True(t, current.RetainOffline(time.Now().Add(-time.Millisecond)))

	got, ok := manager.Get("user-1")
	assert.Nil(t, got)
	assert.False(t, ok)
	assert.Empty(t, manager.UserIDs())
}
