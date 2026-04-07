package session

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/auth"
)

func TestServiceConnectRequiresUserID(t *testing.T) {
	t.Parallel()

	manager := NewManager()
	svc, err := NewService(manager)
	require.NoError(t, err)

	stream := newMockConnectStream(context.Background())
	err = svc.Connect(stream)
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestServiceConnectRegistersAndRemovesSession(t *testing.T) {
	t.Parallel()

	manager := NewManager(ManagerOptions{CacheMaxBytes: 64})
	svc, err := NewService(manager)
	require.NoError(t, err)

	ctx := auth.WithUserID(context.Background(), "user-1")
	stream := newMockConnectStream(ctx)
	errCh := make(chan error, 1)

	go func() {
		errCh <- svc.Connect(stream)
	}()

	stream.PushRecv(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{
			FileTree: &remotefsv1.FileTree{
				Files: []*remotefsv1.FileInfo{
					{Path: "dir", IsDir: true, Mode: 0o755},
				},
			},
		},
	})

	require.Eventually(t, func() bool {
		_, ok := manager.Get("user-1")
		return ok
	}, time.Second, 10*time.Millisecond)

	current, ok := manager.Get("user-1")
	require.True(t, ok)
	assert.True(t, current.PutCachedContent("dir/a.txt", newInfo("dir/a.txt", false, 1, 1), []byte("a")))

	reqDone := make(chan *remotefsv1.FileResponse, 1)
	go func() {
		resp, reqErr := current.Request(ctx, &remotefsv1.FileRequest{
			Operation: &remotefsv1.FileRequest_ListDir{
				ListDir: &remotefsv1.ListDirReq{Path: ""},
			},
		})
		require.NoError(t, reqErr)
		reqDone <- resp
	}()

	outbound, err := stream.AwaitSend(2 * time.Second)
	require.NoError(t, err)
	require.NotNil(t, outbound.GetRequest())

	stream.PushRecv(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_Response{
			Response: &remotefsv1.FileResponse{
				RequestId: outbound.GetRequest().GetRequestId(),
				Success:   true,
				Result: &remotefsv1.FileResponse_Entries{
					Entries: &remotefsv1.FileTree{
						Files: []*remotefsv1.FileInfo{{Path: "dir", IsDir: true}},
					},
				},
			},
		},
	})

	resp := <-reqDone
	require.True(t, resp.GetSuccess())

	stream.CloseRecv()
	require.NoError(t, <-errCh)

	_, ok = manager.Get("user-1")
	assert.False(t, ok)
}

func TestServiceConnectReplacesPreviousSession(t *testing.T) {
	t.Parallel()

	manager := NewManager()
	svc, err := NewService(manager)
	require.NoError(t, err)

	ctx1 := auth.WithUserID(context.Background(), "user-1")
	stream1 := newMockConnectStream(ctx1)
	errCh1 := make(chan error, 1)
	go func() {
		errCh1 <- svc.Connect(stream1)
	}()
	stream1.PushRecv(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{}},
	})
	require.Eventually(t, func() bool {
		_, ok := manager.Get("user-1")
		return ok
	}, time.Second, 10*time.Millisecond)

	first, ok := manager.Get("user-1")
	require.True(t, ok)

	ctx2 := auth.WithUserID(context.Background(), "user-1")
	stream2 := newMockConnectStream(ctx2)
	errCh2 := make(chan error, 1)
	go func() {
		errCh2 <- svc.Connect(stream2)
	}()
	stream2.PushRecv(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{}},
	})

	require.Eventually(t, func() bool {
		current, exists := manager.Get("user-1")
		return exists && current != first
	}, time.Second, 10*time.Millisecond)

	_, err = first.Request(context.Background(), &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Stat{
			Stat: &remotefsv1.StatReq{Path: "foo"},
		},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSessionReplaced)

	stream1.CloseRecv()
	stream2.CloseRecv()
	require.NoError(t, <-errCh1)
	require.NoError(t, <-errCh2)
}

func TestServiceConnectRetainsOfflineReadonlyWithinTTL(t *testing.T) {
	t.Parallel()

	manager := NewManager(ManagerOptions{OfflineReadOnlyTTL: time.Minute})
	svc, err := NewService(manager)
	require.NoError(t, err)

	ctx := auth.WithUserID(context.Background(), "user-1")
	stream := newMockConnectStream(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Connect(stream)
	}()

	stream.PushRecv(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{}},
	})

	require.Eventually(t, func() bool {
		_, ok := manager.Get("user-1")
		return ok
	}, time.Second, 10*time.Millisecond)

	current, ok := manager.Get("user-1")
	require.True(t, ok)

	stream.CloseRecv()
	require.NoError(t, <-errCh)

	got, ok := manager.Get("user-1")
	require.True(t, ok)
	assert.Same(t, current, got)
	assert.True(t, got.IsOfflineReadonly(time.Time{}))
}

func TestServiceConnectReplacesOfflineReadonlySession(t *testing.T) {
	t.Parallel()

	manager := NewManager(ManagerOptions{OfflineReadOnlyTTL: time.Minute})
	svc, err := NewService(manager)
	require.NoError(t, err)

	ctx1 := auth.WithUserID(context.Background(), "user-1")
	stream1 := newMockConnectStream(ctx1)
	errCh1 := make(chan error, 1)
	go func() {
		errCh1 <- svc.Connect(stream1)
	}()
	stream1.PushRecv(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{}},
	})

	require.Eventually(t, func() bool {
		_, ok := manager.Get("user-1")
		return ok
	}, time.Second, 10*time.Millisecond)

	first, ok := manager.Get("user-1")
	require.True(t, ok)

	stream1.CloseRecv()
	require.NoError(t, <-errCh1)

	offline, ok := manager.Get("user-1")
	require.True(t, ok)
	assert.Same(t, first, offline)
	assert.True(t, offline.IsOfflineReadonly(time.Time{}))

	ctx2 := auth.WithUserID(context.Background(), "user-1")
	stream2 := newMockConnectStream(ctx2)
	errCh2 := make(chan error, 1)
	go func() {
		errCh2 <- svc.Connect(stream2)
	}()
	stream2.PushRecv(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{FileTree: &remotefsv1.FileTree{}},
	})

	require.Eventually(t, func() bool {
		current, exists := manager.Get("user-1")
		return exists && current != first
	}, time.Second, 10*time.Millisecond)

	current, ok := manager.Get("user-1")
	require.True(t, ok)
	assert.NotSame(t, first, current)
	assert.False(t, current.IsOfflineReadonly(time.Time{}))

	stream2.CloseRecv()
	require.NoError(t, <-errCh2)
}
