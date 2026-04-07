package syncer

import (
	"fmt"
	"io/fs"
	"os"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

type writeDeps struct {
	locker     *pathLocker
	selfWrites *selfWriteFilter
}

func depsFromOptions(opts HandleOptions) writeDeps {
	return writeDeps{
		locker:     opts.Locker,
		selfWrites: opts.SelfWrites,
	}
}

func formatErr(op, path string, err error) string {
	return fmt.Sprintf("syncer: %s %q: %v", op, path, err)
}

func formatRenameErr(oldPath, newPath string, err error) string {
	return fmt.Sprintf("syncer: rename %q->%q: %v", oldPath, newPath, err)
}

func lstatToFileInfo(abs, rel string) (*remotefsv1.FileInfo, error) {
	fi, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	return fileInfoFromFS(rel, fi), nil
}

func successWithInfo(reqID string, info *remotefsv1.FileInfo) *remotefsv1.FileResponse {
	return &remotefsv1.FileResponse{
		RequestId: reqID,
		Success:   true,
		Result:    &remotefsv1.FileResponse_Info{Info: info},
	}
}

func successNoResult(reqID string) *remotefsv1.FileResponse {
	return &remotefsv1.FileResponse{
		RequestId: reqID,
		Success:   true,
	}
}

func handleWrite(reqID string, r *remotefsv1.WriteFileReq, opts HandleOptions, deps writeDeps) *remotefsv1.FileResponse {
	if r == nil {
		return errResponse(reqID, "syncer: nil write request")
	}
	if !r.GetAppend() && r.GetOffset() < 0 {
		return errResponse(reqID, formatErr("write", r.GetPath(), fmt.Errorf("invalid argument")))
	}

	abs, err := resolveWorkspacePath(opts.Root, r.GetPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
	}

	unlock := deps.locker.Lock(r.GetPath())
	defer unlock()
	deps.selfWrites.Remember(r.GetPath())

	mode := fs.FileMode(r.GetMode())
	if mode == 0 {
		mode = 0o644
	}

	file, err := openWriteTarget(abs, mode, r.GetAppend())
	if err != nil {
		return errResponse(reqID, formatErr("write", r.GetPath(), err))
	}

	err = writeContent(file, r.GetContent(), r.GetOffset(), r.GetAppend())
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return errResponse(reqID, formatErr("write", r.GetPath(), err))
	}

	info, err := lstatToFileInfo(abs, r.GetPath())
	if err != nil {
		return errResponse(reqID, formatErr("write", r.GetPath(), err))
	}
	return successWithInfo(reqID, info)
}

func handleMkdir(reqID string, r *remotefsv1.MkdirReq, opts HandleOptions, deps writeDeps) *remotefsv1.FileResponse {
	if r == nil {
		return errResponse(reqID, "syncer: nil mkdir request")
	}

	abs, err := resolveWorkspacePath(opts.Root, r.GetPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
	}

	unlock := deps.locker.Lock(r.GetPath())
	defer unlock()
	deps.selfWrites.Remember(r.GetPath())

	if r.GetRecursive() {
		//nolint:gosec // daemon intentionally exposes executable workspace dirs
		err = os.MkdirAll(abs, 0o755)
	} else {
		//nolint:gosec // daemon intentionally exposes executable workspace dirs
		err = os.Mkdir(abs, 0o755)
	}
	if err != nil {
		return errResponse(reqID, formatErr("mkdir", r.GetPath(), err))
	}

	info, err := lstatToFileInfo(abs, r.GetPath())
	if err != nil {
		return errResponse(reqID, formatErr("mkdir", r.GetPath(), err))
	}
	return successWithInfo(reqID, info)
}

func handleDelete(reqID string, r *remotefsv1.DeleteReq, opts HandleOptions, deps writeDeps) *remotefsv1.FileResponse {
	if r == nil {
		return errResponse(reqID, "syncer: nil delete request")
	}

	abs, err := resolveWorkspacePath(opts.Root, r.GetPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
	}

	unlock := deps.locker.Lock(r.GetPath())
	defer unlock()
	deps.selfWrites.Remember(r.GetPath())

	if err := os.Remove(abs); err != nil {
		return errResponse(reqID, formatErr("delete", r.GetPath(), err))
	}
	return successNoResult(reqID)
}

func handleRename(reqID string, r *remotefsv1.RenameReq, opts HandleOptions, deps writeDeps) *remotefsv1.FileResponse {
	if r == nil {
		return errResponse(reqID, "syncer: nil rename request")
	}

	oldAbs, err := resolveWorkspacePath(opts.Root, r.GetOldPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
	}
	newAbs, err := resolveWorkspacePath(opts.Root, r.GetNewPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
	}

	unlock := deps.locker.LockMany(r.GetOldPath(), r.GetNewPath())
	defer unlock()
	deps.selfWrites.Remember(r.GetOldPath(), r.GetNewPath())

	renameErr := os.Rename(oldAbs, newAbs)
	if renameErr != nil {
		return errResponse(reqID, formatRenameErr(r.GetOldPath(), r.GetNewPath(), renameErr))
	}

	info, err := lstatToFileInfo(newAbs, r.GetNewPath())
	if err != nil {
		return errResponse(reqID, formatRenameErr(r.GetOldPath(), r.GetNewPath(), err))
	}
	return successWithInfo(reqID, info)
}

func handleTruncate(reqID string, r *remotefsv1.TruncateReq, opts HandleOptions, deps writeDeps) *remotefsv1.FileResponse {
	if r == nil {
		return errResponse(reqID, "syncer: nil truncate request")
	}
	if r.GetSize() < 0 {
		return errResponse(reqID, formatErr("truncate", r.GetPath(), fmt.Errorf("invalid argument")))
	}

	abs, err := resolveWorkspacePath(opts.Root, r.GetPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
	}

	unlock := deps.locker.Lock(r.GetPath())
	defer unlock()
	deps.selfWrites.Remember(r.GetPath())

	truncateErr := os.Truncate(abs, r.GetSize())
	if truncateErr != nil {
		return errResponse(reqID, formatErr("truncate", r.GetPath(), truncateErr))
	}

	info, err := lstatToFileInfo(abs, r.GetPath())
	if err != nil {
		return errResponse(reqID, formatErr("truncate", r.GetPath(), err))
	}
	return successWithInfo(reqID, info)
}

func openWriteTarget(abs string, mode fs.FileMode, appendMode bool) (*os.File, error) {
	if appendMode {
		//nolint:gosec // abs is validated by resolveWorkspacePath/safepath.Join
		return os.OpenFile(abs, os.O_WRONLY|os.O_APPEND|os.O_CREATE, mode)
	}
	//nolint:gosec // abs is validated by resolveWorkspacePath/safepath.Join
	return os.OpenFile(abs, os.O_RDWR|os.O_CREATE, mode)
}

func writeContent(file *os.File, content []byte, offset int64, appendMode bool) error {
	if appendMode {
		_, err := file.Write(content)
		return err
	}
	_, err := file.WriteAt(content, offset)
	return err
}
