package fusefs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/session"
)

var (
	ErrUnsupportedPlatform = errors.New("fusefs: unsupported platform")
	ErrNilManager          = errors.New("fusefs: nil manager")
	ErrEmptyMountpoint     = errors.New("fusefs: empty mountpoint")
	ErrSessionOffline      = errors.New("fusefs: session offline")
	ErrPathNotFound        = errors.New("fusefs: path not found")
	ErrNotDirectory        = errors.New("fusefs: not a directory")
	ErrIsDirectory         = errors.New("fusefs: is a directory")
	ErrReadFailed          = errors.New("fusefs: read failed")
	ErrPermissionDenied    = errors.New("fusefs: permission denied")
	ErrAlreadyExists       = errors.New("fusefs: already exists")
	ErrDirectoryNotEmpty   = errors.New("fusefs: directory not empty")
	ErrCrossDevice         = errors.New("fusefs: cross device")
	ErrInvalidArgument     = errors.New("fusefs: invalid argument")
	ErrIOFailed            = errors.New("fusefs: io failed")
	ErrRequestTimeout      = errors.New("fusefs: request timeout")
	ErrSessionFailed       = errors.New("fusefs: session failed")
)

type Options struct {
	Mountpoint      string
	Manager         *session.Manager
	EntryTimeout    time.Duration
	AttrTimeout     time.Duration
	NegativeTimeout time.Duration
}

type Mounted interface {
	Close() error
}

func validateOptions(opts Options) error {
	if opts.Manager == nil {
		return ErrNilManager
	}
	if strings.TrimSpace(opts.Mountpoint) == "" {
		return ErrEmptyMountpoint
	}
	return nil
}

func defaultedOptions(opts Options) Options {
	if opts.EntryTimeout <= 0 {
		opts.EntryTimeout = time.Second
	}
	if opts.AttrTimeout <= 0 {
		opts.AttrTimeout = time.Second
	}
	return opts
}

func lookupInfo(manager *session.Manager, userID, relPath string) (*remotefsv1.FileInfo, error) {
	current, err := requireSession(manager, userID)
	if err != nil {
		return nil, err
	}
	if relPath == "" || relPath == "." || relPath == "/" {
		return &remotefsv1.FileInfo{
			Path:  "",
			IsDir: true,
			Mode:  0o555,
		}, nil
	}

	info, ok := current.Lookup(relPath)
	if !ok {
		return nil, ErrPathNotFound
	}
	return info, nil
}

func listInfos(manager *session.Manager, userID, relPath string) ([]*remotefsv1.FileInfo, error) {
	current, err := requireSession(manager, userID)
	if err != nil {
		return nil, err
	}

	if relPath != "" && relPath != "." && relPath != "/" {
		info, ok := current.Lookup(relPath)
		if !ok {
			return nil, ErrPathNotFound
		}
		if !info.GetIsDir() {
			return nil, ErrNotDirectory
		}
	}

	infos, ok := current.List(relPath)
	if !ok {
		return nil, ErrNotDirectory
	}
	return infos, nil
}

func readChunk(ctx context.Context, manager *session.Manager, userID, relPath string, off int64, size int) ([]byte, error) {
	info, err := lookupInfo(manager, userID, relPath)
	if err != nil {
		return nil, err
	}
	if info.GetIsDir() {
		return nil, ErrIsDirectory
	}

	current, err := requireSession(manager, userID)
	if err != nil {
		return nil, err
	}
	if size < 0 {
		size = 0
	}

	if data, ok := current.GetCachedContent(relPath, info); ok {
		return sliceReadContent(data, off, size), nil
	}

	if shouldUseContentCache(manager, info) {
		data, err := requestRead(ctx, manager, current, relPath, 0, 0)
		if err != nil {
			return nil, err
		}
		current.PutCachedContent(relPath, info, data)
		return sliceReadContent(data, off, size), nil
	}

	return requestRead(ctx, manager, current, relPath, off, int64(size))
}

func writeChunk(ctx context.Context, manager *session.Manager, userID, relPath string, offset int64, data []byte) error {
	resp, current, err := requestFileOp(ctx, manager, userID, &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Write{
			Write: &remotefsv1.WriteFileReq{
				Path:    relPath,
				Content: data,
				Offset:  offset,
			},
		},
	})
	if err != nil {
		return err
	}
	if resp.GetInfo() == nil {
		return ErrIOFailed
	}
	current.ApplyWriteResult(resp.GetInfo())
	return nil
}

func createFile(ctx context.Context, manager *session.Manager, userID, relPath string) error {
	resp, current, err := requestFileOp(ctx, manager, userID, &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Write{
			Write: &remotefsv1.WriteFileReq{
				Path: relPath,
				Mode: 0o644,
			},
		},
	})
	if err != nil {
		return err
	}
	if resp.GetInfo() == nil {
		return ErrIOFailed
	}
	current.ApplyWriteResult(resp.GetInfo())
	return nil
}

func mkdirAt(ctx context.Context, manager *session.Manager, userID, relPath string, recursive bool) error {
	resp, current, err := requestFileOp(ctx, manager, userID, &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Mkdir{
			Mkdir: &remotefsv1.MkdirReq{
				Path:      relPath,
				Recursive: recursive,
			},
		},
	})
	if err != nil {
		return err
	}
	if resp.GetInfo() == nil {
		return ErrIOFailed
	}
	current.ApplyWriteResult(resp.GetInfo())
	return nil
}

func removePath(ctx context.Context, manager *session.Manager, userID, relPath string) error {
	_, current, err := requestFileOp(ctx, manager, userID, &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Delete{
			Delete: &remotefsv1.DeleteReq{Path: relPath},
		},
	})
	if err != nil {
		return err
	}
	current.ApplyDelete(relPath)
	return nil
}

func renamePath(ctx context.Context, manager *session.Manager, userID, oldRel, newRel string) error {
	resp, current, err := requestFileOp(ctx, manager, userID, &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Rename{
			Rename: &remotefsv1.RenameReq{
				OldPath: oldRel,
				NewPath: newRel,
			},
		},
	})
	if err != nil {
		return err
	}
	if resp.GetInfo() == nil {
		return ErrIOFailed
	}
	current.ApplyRename(oldRel, resp.GetInfo())
	return nil
}

func truncatePath(ctx context.Context, manager *session.Manager, userID, relPath string, size int64) error {
	resp, current, err := requestFileOp(ctx, manager, userID, &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Truncate{
			Truncate: &remotefsv1.TruncateReq{
				Path: relPath,
				Size: size,
			},
		},
	})
	if err != nil {
		return err
	}
	if resp.GetInfo() == nil {
		return ErrIOFailed
	}
	current.ApplyWriteResult(resp.GetInfo())
	return nil
}

func requestFileOp(
	ctx context.Context,
	manager *session.Manager,
	userID string,
	req *remotefsv1.FileRequest,
) (*remotefsv1.FileResponse, *session.Session, error) {
	current, err := requireSession(manager, userID)
	if err != nil {
		return nil, nil, err
	}

	ctx, cancel := withRequestTimeout(ctx, manager)
	defer cancel()

	resp, err := current.Request(ctx, req)
	if err != nil {
		return nil, nil, classifyRequestErr(err, "")
	}
	if resp == nil {
		return nil, nil, ErrIOFailed
	}
	if !resp.GetSuccess() {
		return nil, nil, classifyRequestErr(nil, resp.GetError())
	}
	return resp, current, nil
}

func requireSession(manager *session.Manager, userID string) (*session.Session, error) {
	if manager == nil {
		return nil, ErrNilManager
	}
	current, ok := manager.Get(userID)
	if !ok {
		return nil, ErrSessionOffline
	}
	return current, nil
}

func requestRead(
	ctx context.Context,
	manager *session.Manager,
	current *session.Session,
	relPath string,
	off int64,
	length int64,
) ([]byte, error) {
	ctx, cancel := withRequestTimeout(ctx, manager)
	defer cancel()

	resp, err := current.Request(ctx, &remotefsv1.FileRequest{
		Operation: &remotefsv1.FileRequest_Read{
			Read: &remotefsv1.ReadFileReq{
				Path:   relPath,
				Offset: off,
				Length: length,
			},
		},
	})
	if err != nil {
		return nil, classifyRequestErr(err, "")
	}
	if resp == nil {
		return nil, ErrReadFailed
	}
	if !resp.GetSuccess() {
		return nil, classifyRequestErr(nil, resp.GetError())
	}
	return resp.GetContent(), nil
}

func withRequestTimeout(ctx context.Context, manager *session.Manager) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if manager == nil || manager.RequestTimeout() <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, manager.RequestTimeout())
}

func shouldUseContentCache(manager *session.Manager, info *remotefsv1.FileInfo) bool {
	if manager == nil || info == nil || info.GetIsDir() {
		return false
	}
	maxBytes := manager.CacheMaxBytes()
	return maxBytes > 0 && info.GetSize() >= 0 && info.GetSize() <= maxBytes
}

func sliceReadContent(data []byte, offset int64, size int) []byte {
	total := int64(len(data))
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []byte{}
	}

	end := total
	if size > 0 {
		length := int64(size)
		if offset+length < total {
			end = offset + length
		}
	}

	out := make([]byte, end-offset)
	copy(out, data[offset:end])
	return out
}

func classifyRequestErr(reqErr error, respErr string) error {
	if reqErr != nil {
		switch {
		case errors.Is(reqErr, context.DeadlineExceeded):
			return ErrRequestTimeout
		case errors.Is(reqErr, context.Canceled):
			return context.Canceled
		default:
			return fmt.Errorf("%w: %w", ErrSessionFailed, reqErr)
		}
	}
	if respErr == "" {
		return nil
	}
	return classifyError(respErr)
}

func classifyError(msg string) error {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "no such file"),
		strings.Contains(lower, "cannot find the file"),
		strings.Contains(lower, "cannot find the path"):
		return fmt.Errorf("%w: %s", ErrPathNotFound, msg)
	case strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "access is denied"):
		return fmt.Errorf("%w: %s", ErrPermissionDenied, msg)
	case strings.Contains(lower, "file exists"),
		strings.Contains(lower, "already exists"):
		return fmt.Errorf("%w: %s", ErrAlreadyExists, msg)
	case strings.Contains(lower, "not a directory"):
		return fmt.Errorf("%w: %s", ErrNotDirectory, msg)
	case strings.Contains(lower, "is a directory"):
		return fmt.Errorf("%w: %s", ErrIsDirectory, msg)
	case strings.Contains(lower, "directory not empty"):
		return fmt.Errorf("%w: %s", ErrDirectoryNotEmpty, msg)
	case strings.Contains(lower, "cross-device"),
		strings.Contains(lower, "invalid cross-device"):
		return fmt.Errorf("%w: %s", ErrCrossDevice, msg)
	case strings.Contains(lower, "invalid argument"):
		return fmt.Errorf("%w: %s", ErrInvalidArgument, msg)
	default:
		return fmt.Errorf("%w: %s", ErrIOFailed, msg)
	}
}
