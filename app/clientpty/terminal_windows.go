//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/windows"

	"flyingEirc/Rclaude/pkg/ptyclient"
)

type nativeTerminalController struct{}

type terminalState struct {
	inputMode  uint32
	outputMode uint32
}

func (nativeTerminalController) IsTerminal(fd int) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(fd), &mode) == nil
}

func (nativeTerminalController) Prepare(
	ctx context.Context,
	stdinFD, stdoutFD int,
) (terminalSession, error) {
	state, err := enableConsoleModes(stdinFD, stdoutFD)
	if err != nil {
		return terminalSession{}, err
	}

	size, err := currentWindowSize(stdoutFD)
	if err != nil {
		if restoreErr := restoreConsole(stdinFD, stdoutFD, state); restoreErr != nil {
			return terminalSession{}, errors.Join(
				fmt.Errorf("clientpty: query terminal size: %w", err),
				fmt.Errorf("clientpty: restore console: %w", restoreErr),
			)
		}
		return terminalSession{}, fmt.Errorf("clientpty: query terminal size: %w", err)
	}

	return terminalSession{
		InitialSize: size,
		Resizes:     ptyclient.NewPollResize(ctx, stdoutFD, 0),
		Restore: func() error {
			return restoreConsole(stdinFD, stdoutFD, state)
		},
	}, nil
}

func enableConsoleModes(stdinFD, stdoutFD int) (*terminalState, error) {
	var inputMode uint32
	if err := windows.GetConsoleMode(windows.Handle(stdinFD), &inputMode); err != nil {
		return nil, fmt.Errorf("clientpty: query stdin console mode: %w", err)
	}
	rawInput := inputMode &^
		(windows.ENABLE_ECHO_INPUT |
			windows.ENABLE_LINE_INPUT |
			windows.ENABLE_PROCESSED_INPUT)
	rawInput |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	if err := windows.SetConsoleMode(windows.Handle(stdinFD), rawInput); err != nil {
		return nil, fmt.Errorf("clientpty: set stdin raw mode: %w", err)
	}

	var outputMode uint32
	if err := windows.GetConsoleMode(windows.Handle(stdoutFD), &outputMode); err != nil {
		if restoreErr := windows.SetConsoleMode(windows.Handle(stdinFD), inputMode); restoreErr != nil {
			return nil, errors.Join(
				fmt.Errorf("clientpty: query stdout console mode: %w", err),
				fmt.Errorf("clientpty: restore stdin console mode: %w", restoreErr),
			)
		}
		return nil, fmt.Errorf("clientpty: query stdout console mode: %w", err)
	}
	output := outputMode | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
	if err := windows.SetConsoleMode(windows.Handle(stdoutFD), output); err != nil {
		if restoreErr := windows.SetConsoleMode(windows.Handle(stdinFD), inputMode); restoreErr != nil {
			return nil, errors.Join(
				fmt.Errorf("clientpty: enable VT output: %w", err),
				fmt.Errorf("clientpty: restore stdin console mode: %w", restoreErr),
			)
		}
		return nil, fmt.Errorf("clientpty: enable VT output: %w", err)
	}

	return &terminalState{
		inputMode:  inputMode,
		outputMode: outputMode,
	}, nil
}

func restoreConsole(stdinFD, stdoutFD int, state *terminalState) error {
	if state == nil {
		return nil
	}
	if err := windows.SetConsoleMode(windows.Handle(stdinFD), state.inputMode); err != nil {
		return err
	}
	return windows.SetConsoleMode(windows.Handle(stdoutFD), state.outputMode)
}

func currentWindowSize(fd int) (ptyclient.WindowSize, error) {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(fd), &info); err != nil {
		return ptyclient.WindowSize{}, err
	}

	cols := info.Window.Right - info.Window.Left + 1
	rows := info.Window.Bottom - info.Window.Top + 1
	if cols <= 0 || rows <= 0 {
		return ptyclient.WindowSize{}, fmt.Errorf("invalid console size %dx%d", cols, rows)
	}

	return ptyclient.WindowSize{
		Cols: uint32(cols),
		Rows: uint32(rows),
	}, nil
}
