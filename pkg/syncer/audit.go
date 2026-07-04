package syncer

import (
	"time"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/audit"
)

// Auditor receives one record per handled remote file operation.
type Auditor interface {
	Record(rec *audit.Record)
}

func auditRequest(a Auditor, req *remotefsv1.FileRequest, resp *remotefsv1.FileResponse) {
	if a == nil {
		return
	}
	rec := &audit.Record{
		Time:      time.Now(),
		RequestID: req.GetRequestId(),
		Success:   resp.GetSuccess(),
		Error:     resp.GetError(),
	}
	if !fillReadLikeAudit(rec, req.GetOperation(), resp) {
		fillMutatingAudit(rec, req.GetOperation())
	}
	a.Record(rec)
}

func fillReadLikeAudit(rec *audit.Record, op any, resp *remotefsv1.FileResponse) bool {
	switch op := op.(type) {
	case *remotefsv1.FileRequest_Read:
		rec.Operation = "read"
		rec.Path = op.Read.GetPath()
		rec.Bytes = int64(len(resp.GetContent()))
	case *remotefsv1.FileRequest_Stat:
		rec.Operation = "stat"
		rec.Path = op.Stat.GetPath()
	case *remotefsv1.FileRequest_ListDir:
		rec.Operation = "list_dir"
		rec.Path = op.ListDir.GetPath()
	default:
		return false
	}
	return true
}

func fillMutatingAudit(rec *audit.Record, op any) {
	switch op := op.(type) {
	case *remotefsv1.FileRequest_Write:
		rec.Operation = "write"
		rec.Path = op.Write.GetPath()
		rec.Bytes = int64(len(op.Write.GetContent()))
	case *remotefsv1.FileRequest_Delete:
		rec.Operation = "delete"
		rec.Path = op.Delete.GetPath()
	case *remotefsv1.FileRequest_Mkdir:
		rec.Operation = "mkdir"
		rec.Path = op.Mkdir.GetPath()
	case *remotefsv1.FileRequest_Rename:
		rec.Operation = "rename"
		rec.Path = op.Rename.GetOldPath()
		rec.Target = op.Rename.GetNewPath()
	case *remotefsv1.FileRequest_Truncate:
		rec.Operation = "truncate"
		rec.Path = op.Truncate.GetPath()
		rec.Bytes = op.Truncate.GetSize()
	default:
		rec.Operation = "unknown"
	}
}
