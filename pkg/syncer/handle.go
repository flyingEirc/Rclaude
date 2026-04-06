package syncer

import (
	"fmt"
	"io/fs"
	"os"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/safepath"
)

// HandleOptions 是处理单次 FileRequest 的运行时上下文。
type HandleOptions struct {
	// Root 是工作区根绝对路径。
	Root string
	// MaxReadSize 限制单次 Read 可返回的最大字节数；0 表示不限制。
	MaxReadSize int64
}

// Handle 同步处理一条 FileRequest 并返回 FileResponse。
// Phase 2 只实现 Read / Stat / ListDir；其余 operation 返回 "not supported"。
// 任何内部错误都写在 FileResponse.error 中，函数不返回 Go error。
func Handle(req *remotefsv1.FileRequest, opts HandleOptions) *remotefsv1.FileResponse {
	if req == nil {
		return errResponse("", "syncer: nil request")
	}
	reqID := req.GetRequestId()
	switch op := req.GetOperation().(type) {
	case *remotefsv1.FileRequest_Read:
		return handleRead(reqID, op.Read, opts)
	case *remotefsv1.FileRequest_Stat:
		return handleStat(reqID, op.Stat, opts)
	case *remotefsv1.FileRequest_ListDir:
		return handleListDir(reqID, op.ListDir, opts)
	case *remotefsv1.FileRequest_Write,
		*remotefsv1.FileRequest_Delete,
		*remotefsv1.FileRequest_Mkdir,
		*remotefsv1.FileRequest_Rename:
		return errResponse(reqID, "syncer: operation not supported in phase 2")
	default:
		return errResponse(reqID, "syncer: unknown operation")
	}
}

// handleRead 处理 ReadFileReq。Phase 2 支持 offset + length 切片读取。
func handleRead(reqID string, r *remotefsv1.ReadFileReq, opts HandleOptions) *remotefsv1.FileResponse {
	if r == nil {
		return errResponse(reqID, "syncer: nil read request")
	}
	abs, err := resolveWorkspacePath(opts.Root, r.GetPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
	}
	data, err := os.ReadFile(abs) //nolint:gosec // 路径已通过 safepath.Join 校验
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

// handleStat 处理 StatReq，返回单文件的 FileInfo。
func handleStat(reqID string, r *remotefsv1.StatReq, opts HandleOptions) *remotefsv1.FileResponse {
	if r == nil {
		return errResponse(reqID, "syncer: nil stat request")
	}
	abs, err := resolveWorkspacePath(opts.Root, r.GetPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
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

// handleListDir 处理 ListDirReq，返回目录的直接子项。
func handleListDir(reqID string, r *remotefsv1.ListDirReq, opts HandleOptions) *remotefsv1.FileResponse {
	if r == nil {
		return errResponse(reqID, "syncer: nil list_dir request")
	}
	abs, err := resolveWorkspacePath(opts.Root, r.GetPath())
	if err != nil {
		return errResponse(reqID, fmt.Sprintf("syncer: unsafe path: %v", err))
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
		childRel := joinRelPath(r.GetPath(), entry.Name())
		out = append(out, fileInfoFromFS(childRel, info))
	}
	return &remotefsv1.FileResponse{
		RequestId: reqID,
		Success:   true,
		Result:    &remotefsv1.FileResponse_Entries{Entries: &remotefsv1.FileTree{Files: out}},
	}
}

// resolveWorkspacePath 把相对路径解析为绝对路径。空串 / "." / "/" 映射到 root 自身，
// 其它走 safepath.Join 做越界校验。
func resolveWorkspacePath(root, rel string) (string, error) {
	if rel == "" || rel == "." || rel == "/" {
		return root, nil
	}
	return safepath.Join(root, rel)
}

// sliceContent 按 offset + length 截取原始字节。
// offset 超出或为负按 0 处理；length<=0 表示读到结尾。
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

// fileInfoFromFS 把 fs.FileInfo 映射为协议 FileInfo，并使用传入的相对路径。
func fileInfoFromFS(rel string, fi fs.FileInfo) *remotefsv1.FileInfo {
	return &remotefsv1.FileInfo{
		Path:    safepath.ToSlash(rel),
		Size:    fi.Size(),
		ModTime: fi.ModTime().Unix(),
		IsDir:   fi.IsDir(),
		Mode:    uint32(fi.Mode().Perm()),
	}
}

// joinRelPath 拼接父相对路径与子 basename，空父路径特别处理避免前导 "/"。
func joinRelPath(parent, base string) string {
	parent = safepath.ToSlash(parent)
	if parent == "" || parent == "." {
		return base
	}
	return parent + "/" + base
}

// errResponse 构造失败的 FileResponse。
func errResponse(reqID, msg string) *remotefsv1.FileResponse {
	return &remotefsv1.FileResponse{
		RequestId: reqID,
		Success:   false,
		Error:     msg,
	}
}
