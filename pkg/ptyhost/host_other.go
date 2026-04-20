//go:build !unix

package ptyhost

import (
	"context"
	"io"
)

// Host is a stub on non-unix platforms; Spawn always returns ErrUnsupportedPlatform.
type Host struct{}

func Spawn(_ SpawnReq) (*Host, error) { return nil, ErrUnsupportedPlatform }

func (h *Host) Stdin() io.Writer { return io.Discard }

func (h *Host) Stdout() io.Reader { return eofReader{} }

func (h *Host) Resize(_ WindowSize) error { return ErrUnsupportedPlatform }

func (h *Host) Shutdown(_ bool) error { return ErrUnsupportedPlatform }

func (h *Host) Wait(_ context.Context) (ExitInfo, error) {
	return ExitInfo{}, ErrUnsupportedPlatform
}

type eofReader struct{}

func (eofReader) Read(_ []byte) (int, error) { return 0, io.EOF }
