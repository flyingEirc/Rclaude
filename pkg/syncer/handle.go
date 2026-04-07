package syncer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/safepath"
)

type HandleOptions struct {
	Root            string
	MaxReadSize     int64
	Locker          *pathLocker
	SelfWrites      *selfWriteFilter
	SensitiveFilter *SensitiveFilter
}

func Handle(req *remotefsv1.FileRequest, opts HandleOptions) *remotefsv1.FileResponse {
	if req == nil {
		return errResponse("", "syncer: nil request")
	}

	reqID := req.GetRequestId()
	deps := depsFromOptions(opts)

	if resp := handleReadLike(reqID, req.GetOperation(), opts); resp != nil {
		return resp
	}
	return handleMutating(reqID, req.GetOperation(), opts, deps)
}

func handleReadLike(reqID string, op any, opts HandleOptions) *remotefsv1.FileResponse {
	switch op := op.(type) {
	case *remotefsv1.FileRequest_Read:
		return handleRead(reqID, op.Read, opts)
	case *remotefsv1.FileRequest_Stat:
		return handleStat(reqID, op.Stat, opts)
	case *remotefsv1.FileRequest_ListDir:
		return handleListDir(reqID, op.ListDir, opts)
	default:
		return nil
	}
}

func handleMutating(reqID string, op any, opts HandleOptions, deps writeDeps) *remotefsv1.FileResponse {
	switch op := op.(type) {
	case *remotefsv1.FileRequest_Write:
		return handleWrite(reqID, op.Write, opts, deps)
	case *remotefsv1.FileRequest_Delete:
		return handleDelete(reqID, op.Delete, opts, deps)
	case *remotefsv1.FileRequest_Mkdir:
		return handleMkdir(reqID, op.Mkdir, opts, deps)
	case *remotefsv1.FileRequest_Rename:
		return handleRename(reqID, op.Rename, opts, deps)
	case *remotefsv1.FileRequest_Truncate:
		return handleTruncate(reqID, op.Truncate, opts, deps)
	default:
		return errResponse(reqID, "syncer: unknown operation")
	}
}

func handleRead(reqID string, r *remotefsv1.ReadFileReq, opts HandleOptions) *remotefsv1.FileResponse {
	if r == nil {
		return errResponse(reqID, "syncer: nil read request")
	}

	abs, err := resolveWorkspacePath(opts.Root, r.GetPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
	}
	blocked, err := blocksSensitivePath(opts.Root, r.GetPath(), opts.SensitiveFilter, pathResolutionOptions{
		followFinalSymlink: true,
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errResponse(reqID, fmt.Sprintf("syncer: read %q: %v", r.GetPath(), err))
	}
	if blocked {
		return errResponse(reqID, formatErr("read", r.GetPath(), syscall.ENOENT))
	}

	//nolint:gosec // abs is validated by resolveWorkspacePath/safepath.Join
	data, err := os.ReadFile(abs)
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: read %q: %v", r.GetPath(), err))
	}

	sliced := sliceContent(data, r.GetOffset(), r.GetLength())
	if opts.MaxReadSize > 0 && int64(len(sliced)) > opts.MaxReadSize {
		sliced = sliced[:opts.MaxReadSize]
	}

	return &remotefsv1.FileResponse{
		RequestId: reqID,
		Success:   true,
		Result:    &remotefsv1.FileResponse_Content{Content: sliced},
	}
}

func handleStat(reqID string, r *remotefsv1.StatReq, opts HandleOptions) *remotefsv1.FileResponse {
	if r == nil {
		return errResponse(reqID, "syncer: nil stat request")
	}

	abs, err := resolveWorkspacePath(opts.Root, r.GetPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
	}
	blocked, err := blocksSensitivePath(opts.Root, r.GetPath(), opts.SensitiveFilter, pathResolutionOptions{})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errResponse(reqID, fmt.Sprintf("syncer: stat %q: %v", r.GetPath(), err))
	}
	if blocked {
		return errResponse(reqID, formatErr("stat", r.GetPath(), syscall.ENOENT))
	}

	fi, err := os.Lstat(abs)
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: stat %q: %v", r.GetPath(), err))
	}

	return &remotefsv1.FileResponse{
		RequestId: reqID,
		Success:   true,
		Result: &remotefsv1.FileResponse_Info{
			Info: fileInfoFromFS(r.GetPath(), fi),
		},
	}
}

func handleListDir(reqID string, r *remotefsv1.ListDirReq, opts HandleOptions) *remotefsv1.FileResponse {
	if r == nil {
		return errResponse(reqID, "syncer: nil list_dir request")
	}

	abs, err := resolveWorkspacePath(opts.Root, r.GetPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
	}
	blocked, err := blocksSensitivePath(opts.Root, r.GetPath(), opts.SensitiveFilter, pathResolutionOptions{
		followFinalSymlink: true,
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errResponse(reqID, fmt.Sprintf("syncer: list %q: %v", r.GetPath(), err))
	}
	if blocked {
		return errResponse(reqID, formatErr("list", r.GetPath(), syscall.ENOENT))
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: list %q: %v", r.GetPath(), err))
	}

	out := make([]*remotefsv1.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		relPath := joinRelPath(r.GetPath(), entry.Name())
		if isSensitivePath(opts.SensitiveFilter, relPath) {
			continue
		}
		out = append(out, fileInfoFromFS(relPath, info))
	}

	return &remotefsv1.FileResponse{
		RequestId: reqID,
		Success:   true,
		Result:    &remotefsv1.FileResponse_Entries{Entries: &remotefsv1.FileTree{Files: out}},
	}
}

func resolveWorkspacePath(root, rel string) (string, error) {
	if rel == "" || rel == "." || rel == "/" {
		return root, nil
	}
	return safepath.Join(root, rel)
}

func sliceContent(data []byte, offset, length int64) []byte {
	total := int64(len(data))
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []byte{}
	}

	end := total
	if length > 0 && offset+length < total {
		end = offset + length
	}
	return data[offset:end]
}

func fileInfoFromFS(rel string, fi fs.FileInfo) *remotefsv1.FileInfo {
	return &remotefsv1.FileInfo{
		Path:    safepath.ToSlash(rel),
		Size:    fi.Size(),
		ModTime: fi.ModTime().Unix(),
		IsDir:   fi.IsDir(),
		Mode:    uint32(fi.Mode().Perm()),
	}
}

func joinRelPath(parent, base string) string {
	parent = safepath.ToSlash(parent)
	if parent == "" || parent == "." {
		return base
	}
	return parent + "/" + base
}

func errResponse(reqID, msg string) *remotefsv1.FileResponse {
	return &remotefsv1.FileResponse{
		RequestId: reqID,
		Success:   false,
		Error:     msg,
	}
}

func isSensitivePath(filter *SensitiveFilter, relPath string) bool {
	return filter != nil && filter.Match(relPath)
}
