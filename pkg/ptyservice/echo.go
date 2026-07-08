package ptyservice

import (
	"time"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
)

// echoTimeout is mosh's ECHO_TIMEOUT: an input byte is acknowledged only
// after it has been written to the PTY for at least this long, so any echo it
// produced has already been read and forwarded as stdout.
const (
	echoTimeout      = 50 * time.Millisecond
	echoTickInterval = 25 * time.Millisecond
)

type echoStamp struct {
	offset uint64
	at     time.Time
}

// echoTracker maintains the echo-ack watermark over the cumulative count of
// stdin bytes written to the PTY.
type echoTracker struct {
	pending []echoStamp
	offset  uint64
	sent    uint64
}

// record notes that n more stdin bytes were written to the PTY at time now.
func (t *echoTracker) record(n int, now time.Time) {
	if t == nil || n <= 0 {
		return
	}
	t.offset += uint64(n)
	t.pending = append(t.pending, echoStamp{offset: t.offset, at: now})
}

// watermark pops every stdin write older than echoTimeout and reports the new
// ack offset; ok is false while nothing new can be acknowledged.
func (t *echoTracker) watermark(now time.Time) (uint64, bool) {
	if t == nil {
		return 0, false
	}
	ack := t.sent
	for len(t.pending) > 0 && now.Sub(t.pending[0].at) >= echoTimeout {
		ack = t.pending[0].offset
		t.pending = t.pending[1:]
	}
	if ack <= t.sent {
		return 0, false
	}
	t.sent = ack
	return ack, true
}

func echoAckFrame(offset uint64) *remotefsv1.ServerFrame {
	return &remotefsv1.ServerFrame{
		Payload: &remotefsv1.ServerFrame_EchoAck{
			EchoAck: &remotefsv1.EchoAck{Offset: offset},
		},
	}
}
