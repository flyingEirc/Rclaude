//go:build unix

package ptyclient

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

// NewSIGWINCHResize emits the current terminal size once at startup and again
// on each SIGWINCH until ctx is done.
func NewSIGWINCHResize(ctx context.Context, fd int) <-chan WindowSize {
	out := make(chan WindowSize, 4)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGWINCH)

	send := func() {
		select {
		case out <- querySize(fd):
		default:
		}
	}

	go func() {
		defer signal.Stop(signals)
		defer close(out)

		send()
		for {
			select {
			case <-ctx.Done():
				return
			case <-signals:
				send()
			}
		}
	}()

	return out
}

func querySize(fd int) WindowSize {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil || ws == nil {
		return WindowSize{}
	}

	return WindowSize{
		Cols:   uint32(ws.Col),
		Rows:   uint32(ws.Row),
		XPixel: uint32(ws.Xpixel),
		YPixel: uint32(ws.Ypixel),
	}
}
