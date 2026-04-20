package ptyhost

import (
	"errors"
	"time"
)

// ErrUnsupportedPlatform is returned when ptyhost cannot spawn a PTY on the
// current GOOS.
var ErrUnsupportedPlatform = errors.New("ptyhost: PTY spawn is unsupported on this platform")

// SpawnReq describes how to launch a PTY-bound child process.
type SpawnReq struct {
	Binary   string
	Cwd      string
	Env      []string
	InitSize WindowSize

	// GracefulTimeout is the upper bound between SIGHUP and SIGKILL when
	// Shutdown(graceful=true) is called. Zero means "use default 5s".
	GracefulTimeout time.Duration
}

// WindowSize is the terminal geometry sent through TIOCSWINSZ.
type WindowSize struct {
	Cols   uint32
	Rows   uint32
	XPixel uint32
	YPixel uint32
}

// ExitInfo captures a finished child process result.
type ExitInfo struct {
	Code   int32
	Signal uint32
}
