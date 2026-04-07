package fusefs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/internal/testutil"
	"flyingEirc/Rclaude/pkg/session"
)

func TestLookupAndListInfos(t *testing.T) {
	t.Parallel()

	manager, _, cleanup := startViewSession(t, []*remotefsv1.FileInfo{
		{Path: "dir", IsDir: true, Mode: 0o755},
		{Path: "dir/file.txt", Size: 5, Mode: 0o644},
	}, 0, nil)
	defer cleanup()

	info, err := lookupInfo(manager, "user-1", "")
	require.NoError(t, err)
	assert.True(t, info.GetIsDir())

	info, err = lookupInfo(manager, "user-1", "dir/file.txt")
	require.NoError(t, err)
	assert.EqualValues(t, 5, info.GetSize())

	entries, err := listInfos(manager, "user-1", "dir")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "dir/file.txt", entries[0].GetPath())

	_, err = listInfos(manager, "user-1", "dir/file.txt")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotDirectory)
}

func TestReadChunkRoutesThroughSessionRequest(t *testing.T) {
	t.Parallel()

	manager, _, cleanup := startViewSession(t, []*remotefsv1.FileInfo{
		{Path: "file.txt", Size: 5, Mode: 0o644},
	}, 0, func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		read := req.GetRead()
		require.NotNil(t, read)
		assert.Equal(t, "file.txt", read.GetPath())
		return &remotefsv1.FileResponse{
			Success: true,
			Result:  &remotefsv1.FileResponse_Content{Content: []byte("hello")},
		}
	})
	defer cleanup()

	data, err := readChunk(context.Background(), manager, "user-1", "file.txt", 0, 5)
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), data)
}

func TestReadChunkUsesCachedWholeFile(t *testing.T) {
	t.Parallel()

	var calls int
	manager, _, cleanup := startViewSessionWithCache(t, []*remotefsv1.FileInfo{
		{Path: "file.txt", Size: 5, ModTime: 1, Mode: 0o644},
	}, 0, 64, func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		calls++
		read := req.GetRead()
		require.NotNil(t, read)
		assert.EqualValues(t, 0, read.GetOffset())
		assert.EqualValues(t, 0, read.GetLength())
		return &remotefsv1.FileResponse{
			Success: true,
			Result:  &remotefsv1.FileResponse_Content{Content: []byte("hello")},
		}
	})
	defer cleanup()

	first, err := readChunk(context.Background(), manager, "user-1", "file.txt", 0, 2)
	require.NoError(t, err)
	assert.Equal(t, []byte("he"), first)

	second, err := readChunk(context.Background(), manager, "user-1", "file.txt", 2, 3)
	require.NoError(t, err)
	assert.Equal(t, []byte("llo"), second)
	assert.Equal(t, 1, calls)
}

func TestReadChunkWriteInvalidatesCache(t *testing.T) {
	t.Parallel()

	var calls int
	manager, _, cleanup := startViewSessionWithCache(t, []*remotefsv1.FileInfo{
		{Path: "file.txt", Size: 5, ModTime: 1, Mode: 0o644},
	}, 0, 64, func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		switch op := req.GetOperation().(type) {
		case *remotefsv1.FileRequest_Read:
			calls++
			if calls == 1 {
				return &remotefsv1.FileResponse{
					Success: true,
					Result:  &remotefsv1.FileResponse_Content{Content: []byte("hello")},
				}
			}
			return &remotefsv1.FileResponse{
				Success: true,
				Result:  &remotefsv1.FileResponse_Content{Content: []byte("world!")},
			}
		case *remotefsv1.FileRequest_Write:
			return &remotefsv1.FileResponse{
				Success: true,
				Result: &remotefsv1.FileResponse_Info{
					Info: &remotefsv1.FileInfo{Path: op.Write.GetPath(), Size: 6, ModTime: 2, Mode: 0o644},
				},
			}
		default:
			t.Fatalf("unexpected op %T", op)
			return nil
		}
	})
	defer cleanup()

	_, err := readChunk(context.Background(), manager, "user-1", "file.txt", 0, 5)
	require.NoError(t, err)
	require.NoError(t, writeChunk(context.Background(), manager, "user-1", "file.txt", 0, []byte("world!")))

	got, err := readChunk(context.Background(), manager, "user-1", "file.txt", 0, 6)
	require.NoError(t, err)
	assert.Equal(t, []byte("world!"), got)
	assert.Equal(t, 2, calls)
}

func TestReadChunkChangeInvalidatesCache(t *testing.T) {
	t.Parallel()

	var calls int
	manager, current, cleanup := startViewSessionWithCache(t, []*remotefsv1.FileInfo{
		{Path: "file.txt", Size: 5, ModTime: 1, Mode: 0o644},
	}, 0, 64, func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		calls++
		if calls == 1 {
			return &remotefsv1.FileResponse{
				Success: true,
				Result:  &remotefsv1.FileResponse_Content{Content: []byte("hello")},
			}
		}
		return &remotefsv1.FileResponse{
			Success: true,
			Result:  &remotefsv1.FileResponse_Content{Content: []byte("world!")},
		}
	})
	defer cleanup()

	_, err := readChunk(context.Background(), manager, "user-1", "file.txt", 0, 5)
	require.NoError(t, err)

	require.NoError(t, current.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{
			FileTree: &remotefsv1.FileTree{Files: []*remotefsv1.FileInfo{
				{Path: "file.txt", Size: 6, ModTime: 2, Mode: 0o644},
			}},
		},
	}))

	got, err := readChunk(context.Background(), manager, "user-1", "file.txt", 0, 6)
	require.NoError(t, err)
	assert.Equal(t, []byte("world!"), got)
	assert.Equal(t, 2, calls)
}

func TestReadChunkCacheDisabledFallsBackToRangeRead(t *testing.T) {
	t.Parallel()

	manager, _, cleanup := startViewSessionWithCache(t, []*remotefsv1.FileInfo{
		{Path: "file.txt", Size: 5, ModTime: 1, Mode: 0o644},
	}, 0, 0, func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		read := req.GetRead()
		require.NotNil(t, read)
		assert.EqualValues(t, 2, read.GetOffset())
		assert.EqualValues(t, 2, read.GetLength())
		return &remotefsv1.FileResponse{
			Success: true,
			Result:  &remotefsv1.FileResponse_Content{Content: []byte("ll")},
		}
	})
	defer cleanup()

	got, err := readChunk(context.Background(), manager, "user-1", "file.txt", 2, 2)
	require.NoError(t, err)
	assert.Equal(t, []byte("ll"), got)
}

func TestReadChunkMissingPath(t *testing.T) {
	t.Parallel()

	manager, _, cleanup := startViewSession(t, nil, 0, nil)
	defer cleanup()

	_, err := readChunk(context.Background(), manager, "user-1", "missing.txt", 0, 4)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPathNotFound)
}

func TestClassifyErrorKeywords(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		msg  string
		want error
	}{
		{"linux not found", `open x: no such file or directory`, ErrPathNotFound},
		{"windows denied", `open x: Access is denied.`, ErrPermissionDenied},
		{"already exists", `mkdir x: file exists`, ErrAlreadyExists},
		{"not dir", `list x: not a directory`, ErrNotDirectory},
		{"is dir", `read x: is a directory`, ErrIsDirectory},
		{"not empty", `remove x: directory not empty`, ErrDirectoryNotEmpty},
		{"cross device", `rename x y: invalid cross-device link`, ErrCrossDevice},
		{"invalid arg", `truncate x: invalid argument`, ErrInvalidArgument},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyError(tc.msg)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestCreateFileAppliesWriteResult(t *testing.T) {
	t.Parallel()

	manager, current, cleanup := startViewSession(t, nil, 0, func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		write := req.GetWrite()
		require.NotNil(t, write)
		assert.Equal(t, "a.txt", write.GetPath())
		return &remotefsv1.FileResponse{
			Success: true,
			Result: &remotefsv1.FileResponse_Info{
				Info: &remotefsv1.FileInfo{Path: "a.txt", Mode: 0o644},
			},
		}
	})
	defer cleanup()

	require.NoError(t, createFile(context.Background(), manager, "user-1", "a.txt"))
	info, ok := current.Lookup("a.txt")
	require.True(t, ok)
	assert.Equal(t, "a.txt", info.GetPath())
}

func TestWriteChunkForwardsOffset(t *testing.T) {
	t.Parallel()

	manager, current, cleanup := startViewSession(t, []*remotefsv1.FileInfo{
		{Path: "a.txt", Size: 1, Mode: 0o644},
	}, 0, func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		write := req.GetWrite()
		require.NotNil(t, write)
		assert.EqualValues(t, 3, write.GetOffset())
		assert.Equal(t, []byte("xx"), write.GetContent())
		return &remotefsv1.FileResponse{
			Success: true,
			Result: &remotefsv1.FileResponse_Info{
				Info: &remotefsv1.FileInfo{Path: "a.txt", Size: 5, Mode: 0o644},
			},
		}
	})
	defer cleanup()

	require.NoError(t, writeChunk(context.Background(), manager, "user-1", "a.txt", 3, []byte("xx")))
	info, ok := current.Lookup("a.txt")
	require.True(t, ok)
	assert.EqualValues(t, 5, info.GetSize())
}

func TestMkdirRemoveRenameTruncateHelpers(t *testing.T) {
	t.Parallel()

	manager, current, cleanup := startViewSession(t, []*remotefsv1.FileInfo{
		{Path: "old.txt", Size: 4, Mode: 0o644},
	}, 0, func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		switch op := req.GetOperation().(type) {
		case *remotefsv1.FileRequest_Mkdir:
			return &remotefsv1.FileResponse{
				Success: true,
				Result: &remotefsv1.FileResponse_Info{
					Info: &remotefsv1.FileInfo{Path: op.Mkdir.GetPath(), IsDir: true, Mode: 0o755},
				},
			}
		case *remotefsv1.FileRequest_Delete:
			return &remotefsv1.FileResponse{Success: true}
		case *remotefsv1.FileRequest_Rename:
			return &remotefsv1.FileResponse{
				Success: true,
				Result: &remotefsv1.FileResponse_Info{
					Info: &remotefsv1.FileInfo{Path: op.Rename.GetNewPath(), Size: 4, Mode: 0o644},
				},
			}
		case *remotefsv1.FileRequest_Truncate:
			return &remotefsv1.FileResponse{
				Success: true,
				Result: &remotefsv1.FileResponse_Info{
					Info: &remotefsv1.FileInfo{Path: op.Truncate.GetPath(), Size: op.Truncate.GetSize(), Mode: 0o644},
				},
			}
		default:
			t.Fatalf("unexpected op %T", op)
			return nil
		}
	})
	defer cleanup()

	require.NoError(t, mkdirAt(context.Background(), manager, "user-1", "dir", false))
	_, ok := current.Lookup("dir")
	require.True(t, ok)

	require.NoError(t, renamePath(context.Background(), manager, "user-1", "old.txt", "new.txt"))
	_, ok = current.Lookup("old.txt")
	assert.False(t, ok)
	info, ok := current.Lookup("new.txt")
	require.True(t, ok)
	assert.Equal(t, "new.txt", info.GetPath())

	require.NoError(t, truncatePath(context.Background(), manager, "user-1", "new.txt", 2))
	info, ok = current.Lookup("new.txt")
	require.True(t, ok)
	assert.EqualValues(t, 2, info.GetSize())

	require.NoError(t, removePath(context.Background(), manager, "user-1", "new.txt"))
	_, ok = current.Lookup("new.txt")
	assert.False(t, ok)
}

func TestWriteChunkClassifiesResponseError(t *testing.T) {
	t.Parallel()

	manager, _, cleanup := startViewSession(t, []*remotefsv1.FileInfo{
		{Path: "a.txt", Size: 1, Mode: 0o644},
	}, 0, func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		return &remotefsv1.FileResponse{
			Success: false,
			Error:   "syncer: write \"a.txt\": permission denied",
		}
	})
	defer cleanup()

	err := writeChunk(context.Background(), manager, "user-1", "a.txt", 0, []byte("x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPermissionDenied)
}

func TestWriteChunkTimeout(t *testing.T) {
	t.Parallel()

	manager, _, cleanup := startViewSession(t, []*remotefsv1.FileInfo{
		{Path: "a.txt", Size: 1, Mode: 0o644},
	}, 20*time.Millisecond, func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		<-time.After(200 * time.Millisecond)
		return &remotefsv1.FileResponse{
			Success: true,
			Result: &remotefsv1.FileResponse_Info{
				Info: &remotefsv1.FileInfo{Path: "a.txt", Size: 2, Mode: 0o644},
			},
		}
	})
	defer cleanup()

	err := writeChunk(context.Background(), manager, "user-1", "a.txt", 0, []byte("x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRequestTimeout)
}

func startViewSession(
	t *testing.T,
	files []*remotefsv1.FileInfo,
	timeout time.Duration,
	responder func(*remotefsv1.FileRequest) *remotefsv1.FileResponse,
) (*session.Manager, *session.Session, func()) {
	return startViewSessionWithCache(t, files, timeout, 0, responder)
}

func startViewSessionWithCache(
	t *testing.T,
	files []*remotefsv1.FileInfo,
	timeout time.Duration,
	cacheMaxBytes int64,
	responder func(*remotefsv1.FileRequest) *remotefsv1.FileResponse,
) (*session.Manager, *session.Session, func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	stream := testutil.NewMockConnectStream(ctx)
	manager := session.NewManager(session.ManagerOptions{
		RequestTimeout: timeout,
		CacheMaxBytes:  cacheMaxBytes,
	})
	current := manager.NewSession("user-1")
	require.NoError(t, current.Bootstrap(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_FileTree{
			FileTree: &remotefsv1.FileTree{Files: files},
		},
	}))
	_, err := manager.Register(current)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- current.Serve(ctx, stream)
	}()

	done := startResponderLoop(ctx, stream, responder)

	cleanup := func() {
		cancel()
		<-done
		<-errCh
		manager.Remove(current)
	}
	return manager, current, cleanup
}

func startResponderLoop(
	ctx context.Context,
	stream *testutil.MockConnectStream,
	responder func(*remotefsv1.FileRequest) *remotefsv1.FileResponse,
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if responder == nil {
			<-ctx.Done()
			return
		}
		for {
			msg, err := stream.AwaitSend(50 * time.Millisecond)
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					continue
				}
			}
			respondToRequest(stream, msg.GetRequest(), responder)
		}
	}()
	return done
}

func respondToRequest(
	stream *testutil.MockConnectStream,
	req *remotefsv1.FileRequest,
	responder func(*remotefsv1.FileRequest) *remotefsv1.FileResponse,
) {
	if req == nil {
		return
	}
	resp := responder(req)
	if resp == nil {
		return
	}
	if resp.GetRequestId() == "" {
		resp.RequestId = req.GetRequestId()
	}
	stream.PushRecv(&remotefsv1.DaemonMessage{
		Msg: &remotefsv1.DaemonMessage_Response{Response: resp},
	})
}
