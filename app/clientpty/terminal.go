package main

import (
	"context"

	"flyingEirc/Rclaude/pkg/ptyclient"
)

type terminalSession struct {
	InitialSize ptyclient.WindowSize
	Resizes     <-chan ptyclient.WindowSize
	Restore     func() error
}

type terminalController interface {
	IsTerminal(fd int) bool
	Prepare(ctx context.Context, stdinFD, stdoutFD int) (terminalSession, error)
}
