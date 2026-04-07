//go:build linux

package fusefs

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/internal/testutil"
	"flyingEirc/Rclaude/pkg/session"
)

func TestWorkspaceNode_Open_AcceptsWriteFlags(t *testing.T) {
	t.Parallel()

	manager, cleanup := startFakeSession(t, []*remotefsv1.FileInfo{
		{Path: "a.txt", Size: 1, Mode: 0o644},
	}, func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		t.Fatalf("unexpected request: %T", req.GetOperation())
		return nil
	})
	defer cleanup()

	node := &workspaceNode{manager: manager, userID: "u1", relPath: "a.txt"}
	for _, flags := range []uint32{syscall.O_RDONLY, syscall.O_WRONLY, syscall.O_RDWR} {
		_, _, errno := node.Open(context.Background(), flags)
		assert.Equal(t, syscall.Errno(0), errno)
	}
}

func TestWorkspaceNode_Create_Success(t *testing.T) {
	t.Parallel()

	manager, cleanup := startFakeSession(t, nil, func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		write := req.GetWrite()
		require.NotNil(t, write)
		assert.Equal(t, "dir/new.txt", write.GetPath())
		return &remotefsv1.FileResponse{
			RequestId: req.GetRequestId(),
			Success:   true,
			Result: &remotefsv1.FileResponse_Info{
				Info: &remotefsv1.FileInfo{Path: "dir/new.txt", Mode: 0o644},
			},
		}
	})
	defer cleanup()

	node := &workspaceNode{manager: manager, userID: "u1", relPath: "dir"}
	out := &fuse.EntryOut{}
	inode, _, _, errno := node.Create(context.Background(), "new.txt", 0, 0o644, out)
	assert.Equal(t, syscall.Errno(0), errno)
	assert.NotNil(t, inode)
	assert.Equal(t, uint32(syscall.S_IFREG|0o644), out.Attr.Mode)
}

func TestWorkspaceNode_Write_ForwardsOffset(t *testing.T) {
	t.Parallel()

	manager, cleanup := startFakeSession(t, []*remotefsv1.FileInfo{
		{Path: "a.txt", Size: 1, Mode: 0o644},
	}, func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		write := req.GetWrite()
		require.NotNil(t, write)
		assert.EqualValues(t, 3, write.GetOffset())
		assert.Equal(t, []byte("xx"), write.GetContent())
		return &remotefsv1.FileResponse{
			RequestId: req.GetRequestId(),
			Success:   true,
			Result: &remotefsv1.FileResponse_Info{
				Info: &remotefsv1.FileInfo{Path: "a.txt", Size: 5, Mode: 0o644},
			},
		}
	})
	defer cleanup()

	node := &workspaceNode{manager: manager, userID: "u1", relPath: "a.txt"}
	written, errno := node.Write(context.Background(), nil, []byte("xx"), 3)
	assert.Equal(t, uint32(2), written)
	assert.Equal(t, syscall.Errno(0), errno)
}

func TestWorkspaceNode_MkdirUnlinkRmdir(t *testing.T) {
	t.Parallel()

	manager, cleanup := startFakeSession(t, []*remotefsv1.FileInfo{
		{Path: "d/x.txt", Size: 1, Mode: 0o644},
		{Path: "d", IsDir: true, Mode: 0o755},
	}, func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		switch op := req.GetOperation().(type) {
		case *remotefsv1.FileRequest_Mkdir:
			return &remotefsv1.FileResponse{
				RequestId: req.GetRequestId(),
				Success:   true,
				Result: &remotefsv1.FileResponse_Info{
					Info: &remotefsv1.FileInfo{Path: op.Mkdir.GetPath(), IsDir: true, Mode: 0o755},
				},
			}
		case *remotefsv1.FileRequest_Delete:
			return &remotefsv1.FileResponse{RequestId: req.GetRequestId(), Success: true}
		default:
			t.Fatalf("unexpected op %T", op)
			return nil
		}
	})
	defer cleanup()

	root := &workspaceNode{manager: manager, userID: "u1", relPath: ""}
	out := &fuse.EntryOut{}
	_, errno := root.Mkdir(context.Background(), "newdir", 0o755, out)
	assert.Equal(t, syscall.Errno(0), errno)
	assert.Equal(t, syscall.Errno(0), (&workspaceNode{manager: manager, userID: "u1", relPath: "d"}).Unlink(context.Background(), "x.txt"))
	assert.Equal(t, syscall.Errno(0), root.Rmdir(context.Background(), "d"))
}

func TestWorkspaceNode_Rename_FullPath(t *testing.T) {
	t.Parallel()

	manager, cleanup := startFakeSession(t, nil, func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		rn := req.GetRename()
		require.NotNil(t, rn)
		assert.Equal(t, "a/x", rn.GetOldPath())
		assert.Equal(t, "b/x", rn.GetNewPath())
		return &remotefsv1.FileResponse{
			RequestId: req.GetRequestId(),
			Success:   true,
			Result: &remotefsv1.FileResponse_Info{
				Info: &remotefsv1.FileInfo{Path: "b/x", Mode: 0o644},
			},
		}
	})
	defer cleanup()

	src := &workspaceNode{manager: manager, userID: "u1", relPath: "a"}
	dst := &workspaceNode{manager: manager, userID: "u1", relPath: "b"}
	errno := src.Rename(context.Background(), "x", dst, "x", 0)
	assert.Equal(t, syscall.Errno(0), errno)
}

func TestWorkspaceNode_Setattr_OnlySize(t *testing.T) {
	t.Parallel()

	var truncateSeen bool
	manager, cleanup := startFakeSession(t, []*remotefsv1.FileInfo{
		{Path: "a.txt", Size: 10, Mode: 0o644},
	}, func(req *remotefsv1.FileRequest) *remotefsv1.FileResponse {
		if tr := req.GetTruncate(); tr != nil {
			truncateSeen = true
			assert.EqualValues(t, 7, tr.GetSize())
			return &remotefsv1.FileResponse{
				RequestId: req.GetRequestId(),
				Success:   true,
				Result: &remotefsv1.FileResponse_Info{
					Info: &remotefsv1.FileInfo{Path: "a.txt", Size: 7, Mode: 0o644},
				},
			}
		}
		t.Fatalf("unexpected req: %T", req.GetOperation())
		return nil
	})
	defer cleanup()

	node := &workspaceNode{manager: manager, userID: "u1", relPath: "a.txt"}
	in := &fuse.SetAttrIn{}
	in.Size = 7
	in.Valid = fuse.FATTR_SIZE
	out := &fuse.AttrOut{}
	errno := node.Setattr(context.Background(), nil, in, out)
	assert.Equal(t, syscall.Errno(0), errno)
	assert.True(t, truncateSeen)
	assert.EqualValues(t, 7, out.Attr.Size)
}

func TestErrnoFromError(t *testing.T) {
	t.Parallel()

	assert.Equal(t, syscall.ENOENT, errnoFromError(ErrPathNotFound))
	assert.Equal(t, syscall.EACCES, errnoFromError(ErrPermissionDenied))
	assert.Equal(t, syscall.EXDEV, errnoFromError(ErrCrossDevice))
	assert.Equal(t, syscall.EINVAL, errnoFromError(ErrInvalidArgument))
	assert.Equal(t, syscall.ETIMEDOUT, errnoFromError(ErrRequestTimeout))
}

func startFakeSession(
	t *testing.T,
	files []*remotefsv1.FileInfo,
	responder func(*remotefsv1.FileRequest) *remotefsv1.FileResponse,
) (*session.Manager, func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	stream := testutil.NewMockConnectStream(ctx)
	manager := session.NewManager(session.ManagerOptions{RequestTimeout: time.Second})
	current := session.NewSession("u1")
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

	done := make(chan struct{})
	go func() {
		defer close(done)
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
			req := msg.GetRequest()
			if req == nil {
				continue
			}
			resp := responder(req)
			if resp == nil {
				continue
			}
			if resp.GetRequestId() == "" {
				resp.RequestId = req.GetRequestId()
			}
			stream.PushRecv(&remotefsv1.DaemonMessage{
				Msg: &remotefsv1.DaemonMessage_Response{Response: resp},
			})
		}
	}()

	cleanup := func() {
		cancel()
		<-done
		_ = <-errCh
		manager.Remove(current)
	}
	return manager, cleanup
}

var _ fs.FileHandle = nil
