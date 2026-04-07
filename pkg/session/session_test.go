package session

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

func TestSessionBootstrapAndApplyChange(t *testing.T) {
	t.Parallel()

	s := NewSession("user-1")
	err := s.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{
			FileTree: &remotefsv1.FileTree{
				Files: []*remotefsv1.FileInfo{
					{Path: "dir", IsDir: true, Mode: 0o755},
					{Path: "dir/file.txt", Size: 5, Mode: 0o644},
				},
			},
		},
	})
	require.NoError(t, err)

	info, ok := s.Lookup("dir/file.txt")
	require.True(t, ok)
	assert.EqualValues(t, 5, info.GetSize())

	err = s.handleDaemonMessage(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_Change{
			Change: &remotefsv1.FileChange{
				Type: remotefsv1.ChangeType_CHANGE_TYPE_CREATE,
				File: &remotefsv1.FileInfo{Path: "new.txt", Size: 3, Mode: 0o644},
			},
		},
	})
	require.NoError(t, err)

	_, ok = s.Lookup("new.txt")
	assert.True(t, ok)
	assert.False(t, s.LastHeartbeat().IsZero())
}

func TestSessionRequestMatchesResponse(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := newMockConnectStream(ctx)
	s := NewSession("user-1")
	require.NoError(t, s.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{}},
	}))

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Serve(ctx, stream)
	}()

	reqDone := make(chan *remotefsv1.FileResponse, 1)
	go func() {
		resp, err := s.Request(ctx, &remotefsv1.FileRequest{
			Operation: &remotefsv1.FileRequest_Read{
				Read: &remotefsv1.ReadFileReq{Path: "hello.txt"},
			},
		})
		require.NoError(t, err)
		reqDone <- resp
	}()

	msg, err := stream.AwaitSend(2 * time.Second)
	require.NoError(t, err)
	require.NotNil(t, msg.GetRequest())
	requestID := msg.GetRequest().GetRequestId()
	assert.NotEmpty(t, requestID)

	stream.PushRecv(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_Response{
			Response: &remotefsv1.FileResponse{
				RequestId: requestID,
				Success:   true,
				Result:    &remotefsv1.FileResponse_Content{Content: []byte("hello")},
			},
		},
	})

	resp := <-reqDone
	require.True(t, resp.GetSuccess())
	assert.Equal(t, []byte("hello"), resp.GetContent())

	stream.CloseRecv()
	require.NoError(t, <-errCh)
}

func TestSessionRequestFailsWhenReplaced(t *testing.T) {
	t.Parallel()

	s := NewSession("user-1")
	s.closeWithError(ErrSessionReplaced)

	_, err := s.Request(context.Background(), &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Stat{
			Stat: &remotefsv1.StatReq{Path: "foo"},
		},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSessionReplaced)
}

func TestSessionApplyWriteResultVisibleImmediately(t *testing.T) {
	t.Parallel()

	s := NewSession("user-1")
	require.NoError(t, s.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{}},
	}))

	s.ApplyWriteResult(&remotefsv1.FileInfo{Path: "a.txt", Size: 5, Mode: 0o644})

	info, ok := s.Lookup("a.txt")
	require.True(t, ok)
	assert.EqualValues(t, 5, info.GetSize())
}

func TestSessionApplyDelete(t *testing.T) {
	t.Parallel()

	s := NewSession("user-1")
	require.NoError(t, s.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{
			FileTree: &remotefsv1.FileTree{Files: []*remotefsv1.FileInfo{
				{Path: "dir", IsDir: true, Mode: 0o755},
				{Path: "dir/a.txt", Size: 1, Mode: 0o644},
			}},
		},
	}))

	s.ApplyDelete("dir/a.txt")
	_, ok := s.Lookup("dir/a.txt")
	assert.False(t, ok)
}

func TestSessionApplyRename(t *testing.T) {
	t.Parallel()

	s := NewSession("user-1")
	require.NoError(t, s.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{
			FileTree: &remotefsv1.FileTree{Files: []*remotefsv1.FileInfo{
				{Path: "old.txt", Size: 1, Mode: 0o644},
			}},
		},
	}))

	s.ApplyRename("old.txt", &remotefsv1.FileInfo{Path: "new.txt", Size: 2, Mode: 0o644})

	_, ok := s.Lookup("old.txt")
	assert.False(t, ok)

	info, ok := s.Lookup("new.txt")
	require.True(t, ok)
	assert.EqualValues(t, 2, info.GetSize())
}

func TestSessionBootstrapClearsContentCache(t *testing.T) {
	t.Parallel()

	s := NewSession("user-1", SessionOptions{CacheMaxBytes: 64})
	require.True(t, s.PutCachedContent("a.txt", newInfo("a.txt", false, 5, 1), []byte("hello")))

	require.NoError(t, s.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{
			FileTree: &remotefsv1.FileTree{Files: []*remotefsv1.FileInfo{
				newInfo("a.txt", false, 5, 2),
			}},
		},
	}))

	_, ok := s.GetCachedContent("a.txt", newInfo("a.txt", false, 5, 2))
	assert.False(t, ok)
}

func TestSessionApplyWriteResultInvalidatesContent(t *testing.T) {
	t.Parallel()

	s := newCachedSession(t, []*remotefsv1.FileInfo{
		newInfo("a.txt", false, 5, 1),
	})
	require.True(t, s.PutCachedContent("a.txt", newInfo("a.txt", false, 5, 1), []byte("hello")))

	s.ApplyWriteResult(newInfo("a.txt", false, 6, 2))

	_, ok := s.GetCachedContent("a.txt", newInfo("a.txt", false, 6, 2))
	assert.False(t, ok)
}

func TestSessionApplyDeleteInvalidatesDirectoryPrefix(t *testing.T) {
	t.Parallel()

	s := newCachedSession(t, []*remotefsv1.FileInfo{
		newInfo("dir", true, 0, 1),
		newInfo("dir/a.txt", false, 1, 1),
		newInfo("dir/sub/b.txt", false, 1, 1),
	})
	require.True(t, s.PutCachedContent("dir/a.txt", newInfo("dir/a.txt", false, 1, 1), []byte("a")))
	require.True(t, s.PutCachedContent("dir/sub/b.txt", newInfo("dir/sub/b.txt", false, 1, 1), []byte("b")))

	s.ApplyDelete("dir")

	_, ok := s.GetCachedContent("dir/a.txt", newInfo("dir/a.txt", false, 1, 1))
	assert.False(t, ok)
	_, ok = s.GetCachedContent("dir/sub/b.txt", newInfo("dir/sub/b.txt", false, 1, 1))
	assert.False(t, ok)
}

func TestSessionApplyRenameInvalidatesOldAndNew(t *testing.T) {
	t.Parallel()

	s := newCachedSession(t, []*remotefsv1.FileInfo{
		newInfo("old.txt", false, 1, 1),
		newInfo("new.txt", false, 1, 1),
	})
	require.True(t, s.PutCachedContent("old.txt", newInfo("old.txt", false, 1, 1), []byte("old")))
	require.True(t, s.PutCachedContent("new.txt", newInfo("new.txt", false, 1, 1), []byte("new")))

	s.ApplyRename("old.txt", newInfo("new.txt", false, 2, 2))

	_, ok := s.GetCachedContent("old.txt", newInfo("old.txt", false, 1, 1))
	assert.False(t, ok)
	_, ok = s.GetCachedContent("new.txt", newInfo("new.txt", false, 2, 2))
	assert.False(t, ok)
}

func TestSessionApplyChangeInvalidatesContent(t *testing.T) {
	t.Parallel()

	s := newCachedSession(t, []*remotefsv1.FileInfo{
		newInfo("dir", true, 0, 1),
		newInfo("dir/a.txt", false, 1, 1),
	})
	require.True(t, s.PutCachedContent("dir/a.txt", newInfo("dir/a.txt", false, 1, 1), []byte("a")))

	err := s.handleDaemonMessage(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_Change{
			Change: &remotefsv1.FileChange{
				Type: remotefsv1.ChangeType_CHANGE_TYPE_MODIFY,
				File: newInfo("dir/a.txt", false, 2, 2),
			},
		},
	})
	require.NoError(t, err)

	_, ok := s.GetCachedContent("dir/a.txt", newInfo("dir/a.txt", false, 2, 2))
	assert.False(t, ok)
}

func TestSessionRequestFailsWhenOfflineReadonly(t *testing.T) {
	t.Parallel()

	s := NewSession("user-1")
	require.True(t, s.RetainOffline(time.Now().Add(time.Minute)))

	_, err := s.Request(context.Background(), &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Stat{
			Stat: &remotefsv1.StatReq{Path: "a.txt"},
		},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSessionOffline)
	assert.True(t, s.IsOfflineReadonly(time.Time{}))
	assert.False(t, s.IsExpired(time.Time{}))
}

func newCachedSession(t *testing.T, files []*remotefsv1.FileInfo) *Session {
	t.Helper()

	s := NewSession("user-1", SessionOptions{CacheMaxBytes: 64})
	require.NoError(t, s.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{
			FileTree: &remotefsv1.FileTree{Files: files},
		},
	}))
	return s
}

func newInfo(path string, isDir bool, size, modTime int64) *remotefsv1.FileInfo {
	return &remotefsv1.FileInfo{
		Path:    path,
		IsDir:   isDir,
		Size:    size,
		ModTime: modTime,
		Mode:    0o644,
	}
}
