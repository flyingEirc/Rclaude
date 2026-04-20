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

	cmd, err := commandForSpawn(req)
	if err != nil {
		return nil, err
	}
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

func commandForSpawn(req SpawnReq) (*exec.Cmd, error) {
	binary, err := exec.LookPath(strings.TrimSpace(req.Binary))
	if err != nil {
		return nil, fmt.Errorf("ptyhost: binary %q: %w", req.Binary, err)
	}

	if strings.TrimSpace(req.Cwd) == "" {
		return exec.Command(binary), nil //nolint:gosec // binary has been resolved with exec.LookPath above.
	}
	if err := validateCwd(req.Cwd); err != nil {
		return nil, err
	}

	// Avoid exec.Cmd.Dir for self-hosted FUSE workspaces. Go applies Cmd.Dir in
	// the child before exec, while the parent waits for exec to finish; if the
	// cwd is served by this process' own FUSE server, that pre-exec chdir can
	// deadlock the PTY attach. Exec /bin/sh first, then cd inside the child.
	//nolint:gosec // shell source is fixed; cwd and resolved binary are passed as positional args.
	cmd := exec.Command("/bin/sh", "-c", `cd -- "$1" && exec "$2"`, "rclaude-pty", req.Cwd, binary)
	cmd.Dir = string(os.PathSeparator)
	return cmd, nil
}

func validateCwd(cwd string) error {
	info, err := os.Stat(cwd)
	if err != nil {
		return fmt.Errorf("ptyhost: stat cwd %q: %w", cwd, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("ptyhost: cwd %q is not a directory", cwd)
	}
	return nil
}

// Stdin returns the PTY master as the child's input writer.
func (h *Host) Stdin() io.Writer {
	return h.ptmx
}

// Stdout returns the PTY master as the child's merged stdout/stderr reader.
func (h *Host) Stdout() io.Reader {
	return eofOnEIOReader{r: h.ptmx}
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

type eofOnEIOReader struct {
	r io.Reader
}

func (r eofOnEIOReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed) {
		err = io.EOF
	}
	return n, err
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
				if err := h.signal(syscall.SIGKILL, "SIGKILL"); err != nil {
					return
				}
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
			if closeErr := h.ptmx.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				return fmt.Errorf("ptyhost: close after %s: %w", name, closeErr)
			}
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
		if err := h.ptmx.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			return
		}
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
