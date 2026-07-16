package ptyclient

import (
	"errors"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

var (
	// ErrNilStream indicates Config.Stream was not provided.
	ErrNilStream = errors.New("ptyclient: nil stream")
	// ErrFirstFrameNotAttached indicates the server violated the attach handshake.
	ErrFirstFrameNotAttached = errors.New("ptyclient: first server frame was not attached")
	// ErrStreamClosedUnexpectedly indicates the stream ended before exited/error.
	ErrStreamClosedUnexpectedly = errors.New("ptyclient: stream closed before exit or server error")
)

// Stream is the minimal bidi surface the client needs from a transport.
type Stream interface {
	Send(*remotefsv1.ClientFrame) error
	Recv() (*remotefsv1.ServerFrame, error)
	CloseSend() error
}

// WindowSize is the local terminal geometry.
type WindowSize struct {
	Cols   uint32
	Rows   uint32
	XPixel uint32
	YPixel uint32
}

// ExitResult describes how the remote PTY session finished.
type ExitResult struct {
	Code        int32
	Signal      uint32
	ServerError *remotefsv1.Error
	Err         error
}
