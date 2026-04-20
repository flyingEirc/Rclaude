//go:build unix

package ptyhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

const defaultGracefulTimeout = 5 * time.Second

// Host is a running PTY-bound child process.
type Host struct {
	cmd      *exec.Cmd
	ptmx     *os.File
	graceful time.Duration

	waitDone       chan struct{}
	gracefulKill   sync.Once
	waitResultOnce sync.Once
	waitErr        error
	info           ExitInfo
}

// Spawn starts a PTY-bound child according to req.
func Spawn(req SpawnReq) (*Host, error) {
	if strings.TrimSpace(req.Binary) == "" {
		return nil, ErrBinaryEmpty
	}

	cmd := exec.Command(req.Binary)
	cmd.Dir = req.Cwd
	if len(req.Env) > 0 {
		cmd.Env = append([]string(nil), req.Env...)
	}

	var initialSize *pty.Winsize
	if req.InitSize.Cols > 0 || req.InitSize.Rows > 0 || req.InitSize.XPixel > 0 || req.InitSize.YPixel > 0 {
		initialSize = &pty.Winsize{
			Cols: uint16(req.InitSize.Cols),
			Rows: uint16(req.InitSize.Rows),
			X:    uint16(req.InitSize.XPixel),
			Y:    uint16(req.InitSize.YPixel),
		}
	}

	ptmx, err := pty.StartWithSize(cmd, initialSize)
	if err != nil {
		return nil, fmt.Errorf("ptyhost: start: %w", err)
	}

	gracefulTimeout := req.GracefulTimeout
	if gracefulTimeout <= 0 {
		gracefulTimeout = defaultGracefulTimeout
	}

	h := &Host{
		cmd:      cmd,
		ptmx:     ptmx,
		graceful: gracefulTimeout,
		waitDone: make(chan struct{}),
	}

	go h.reap()
	return h, nil
}

// Stdin returns the PTY master as the child's input writer.
func (h *Host) Stdin() io.Writer {
	return h.ptmx
}

// Stdout returns the PTY master as the child's merged stdout/stderr reader.
func (h *Host) Stdout() io.Reader {
	return h.ptmx
}

// Resize forwards window-size changes to the PTY.
func (h *Host) Resize(ws WindowSize) error {
	return pty.Setsize(h.ptmx, &pty.Winsize{
		Cols: uint16(ws.Cols),
		Rows: uint16(ws.Rows),
		X:    uint16(ws.XPixel),
		Y:    uint16(ws.YPixel),
	})
}

// Shutdown asks the child to exit.
func (h *Host) Shutdown(graceful bool) error {
	if h.cmd == nil || h.cmd.Process == nil {
		return nil
	}

	if !graceful {
		return h.signal(syscall.SIGKILL, "SIGKILL")
	}

	if err := h.signal(syscall.SIGHUP, "SIGHUP"); err != nil {
		return err
	}

	h.gracefulKill.Do(func() {
		go func() {
			timer := time.NewTimer(h.graceful)
			defer timer.Stop()

			select {
			case <-h.waitDone:
				return
			case <-timer.C:
				_ = h.signal(syscall.SIGKILL, "SIGKILL")
			}
		}()
	})

	return nil
}

// Wait blocks until the child exits or ctx fires.
func (h *Host) Wait(ctx context.Context) (ExitInfo, error) {
	select {
	case <-h.waitDone:
		return h.info, h.waitErr
	case <-ctx.Done():
		return ExitInfo{}, ctx.Err()
	}
}

func (h *Host) signal(sig syscall.Signal, name string) error {
	err := h.cmd.Process.Signal(sig)
	if err == nil {
		if sig == syscall.SIGKILL {
			_ = h.ptmx.Close()
		}
		return nil
	}
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}

	return fmt.Errorf("ptyhost: %s: %w", name, err)
}

func (h *Host) reap() {
	defer close(h.waitDone)
	defer func() {
		_ = h.ptmx.Close()
	}()

	err := h.cmd.Wait()
	h.waitResultOnce.Do(func() {
		h.info, h.waitErr = classifyExit(err, h.cmd.ProcessState)
	})
}

func classifyExit(err error, state *os.ProcessState) (ExitInfo, error) {
	var info ExitInfo

	if state != nil {
		if waitStatus, ok := state.Sys().(syscall.WaitStatus); ok {
			if waitStatus.Signaled() {
				info.Signal = uint32(waitStatus.Signal())
				info.Code = int32(state.ExitCode())
			} else {
				info.Code = int32(waitStatus.ExitStatus())
			}
		} else {
			info.Code = int32(state.ExitCode())
		}
	}

	if err == nil {
		return info, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return info, nil
	}

	return info, err
}
