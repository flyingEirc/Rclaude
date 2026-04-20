//go:build unix

package main

import (
	"context"
	"fmt"

	"golang.org/x/sys/unix"

	"flyingEirc/Rclaude/pkg/ptyclient"
)

type nativeTerminalController struct{}

type terminalState struct {
	termios unix.Termios
}

func (nativeTerminalController) IsTerminal(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	return err == nil
}

func (nativeTerminalController) Prepare(
	ctx context.Context,
	stdinFD, stdoutFD int,
) (terminalSession, error) {
	state, err := makeRaw(stdinFD)
	if err != nil {
		return terminalSession{}, fmt.Errorf("clientpty: make terminal raw: %w", err)
	}

	size, err := currentWindowSize(stdoutFD)
	if err != nil {
		if restoreErr := restore(stdinFD, state); restoreErr != nil {
			return terminalSession{}, fmt.Errorf(
				"clientpty: query terminal size: %w; restore terminal: %w",
				err,
				restoreErr,
			)
		}
		return terminalSession{}, fmt.Errorf("clientpty: query terminal size: %w", err)
	}

	return terminalSession{
		InitialSize: size,
		Resizes:     ptyclient.NewSIGWINCHResize(ctx, stdoutFD),
		Restore: func() error {
			return restore(stdinFD, state)
		},
	}, nil
}

func makeRaw(fd int) (*terminalState, error) {
	termios, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return nil, err
	}

	oldState := &terminalState{termios: *termios}

	termios.Iflag &^= unix.IGNBRK |
		unix.BRKINT |
		unix.PARMRK |
		unix.ISTRIP |
		unix.INLCR |
		unix.IGNCR |
		unix.ICRNL |
		unix.IXON
	termios.Oflag &^= unix.OPOST
	termios.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	termios.Cflag &^= unix.CSIZE | unix.PARENB
	termios.Cflag |= unix.CS8
	termios.Cc[unix.VMIN] = 1
	termios.Cc[unix.VTIME] = 0

	if err := unix.IoctlSetTermios(fd, ioctlWriteTermios, termios); err != nil {
		return nil, err
	}

	return oldState, nil
}

func restore(fd int, state *terminalState) error {
	if state == nil {
		return nil
	}
	return unix.IoctlSetTermios(fd, ioctlWriteTermios, &state.termios)
}

func currentWindowSize(fd int) (ptyclient.WindowSize, error) {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil {
		return ptyclient.WindowSize{}, err
	}

	return ptyclient.WindowSize{
		Cols:   uint32(ws.Col),
		Rows:   uint32(ws.Row),
		XPixel: uint32(ws.Xpixel),
		YPixel: uint32(ws.Ypixel),
	}, nil
}
