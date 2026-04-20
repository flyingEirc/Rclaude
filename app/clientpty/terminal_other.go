//go:build !unix && !windows

package main

import (
	"context"
	"errors"

	"flyingEirc/Rclaude/pkg/ptyclient"
)

type nativeTerminalController struct{}

func (nativeTerminalController) IsTerminal(int) bool {
	return false
}

func (nativeTerminalController) Prepare(context.Context, int, int) (terminalSession, error) {
	return terminalSession{
		InitialSize: ptyclient.WindowSize{},
		Resizes:     nil,
		Restore:     func() error { return nil },
	}, errors.New("clientpty: terminal control is not supported on this platform")
}
