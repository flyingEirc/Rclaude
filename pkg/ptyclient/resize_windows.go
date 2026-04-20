//go:build windows

package ptyclient

import (
	"context"
	"time"

	"golang.org/x/sys/windows"
)

const defaultResizePollInterval = 250 * time.Millisecond

// NewPollResize polls the console size and emits when it changes.
func NewPollResize(ctx context.Context, fd int, interval time.Duration) <-chan WindowSize {
	if interval <= 0 {
		interval = defaultResizePollInterval
	}

	out := make(chan WindowSize, 4)

	go func() {
		defer close(out)

		last := querySize(fd)
		emitResize(out, last)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		pollResizeLoop(ctx, ticker.C, fd, last, out)
	}()

	return out
}

func querySize(fd int) WindowSize {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(fd), &info); err != nil {
		return WindowSize{}
	}

	cols := info.Window.Right - info.Window.Left + 1
	rows := info.Window.Bottom - info.Window.Top + 1
	if cols <= 0 || rows <= 0 {
		return WindowSize{}
	}

	return WindowSize{
		Cols: uint32(cols),
		Rows: uint32(rows),
	}
}

func pollResizeLoop(ctx context.Context, ticks <-chan time.Time, fd int, last WindowSize, out chan<- WindowSize) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			current := querySize(fd)
			if current == last {
				continue
			}
			last = current
			emitResize(out, current)
		}
	}
}

func emitResize(out chan<- WindowSize, size WindowSize) {
	select {
	case out <- size:
	default:
	}
}
