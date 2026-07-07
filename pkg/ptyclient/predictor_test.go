package ptyclient_test

import (
	"bytes"
	"io"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	remotefsv1 "flyingEirc/Rclaude/api/proto/remotefs/v1"
	"flyingEirc/Rclaude/pkg/ptyclient"
	"flyingEirc/Rclaude/pkg/ptypredict"
)

func (f *fakeStream) pushAttachedEchoAck(sessionID, cwd string) {
	f.incoming <- &remotefsv1.ServerFrame{
		Payload: &remotefsv1.ServerFrame_Attached{
			Attached: &remotefsv1.Attached{SessionId: sessionID, Cwd: cwd, EchoAck: true},
		},
	}
}

func (f *fakeStream) pushEchoAck(offset uint64) {
	f.incoming <- &remotefsv1.ServerFrame{
		Payload: &remotefsv1.ServerFrame_EchoAck{
			EchoAck: &remotefsv1.EchoAck{Offset: offset},
		},
	}
}

// lockedRecorder is a threadsafe stdout sink tests can poll.
type lockedRecorder struct {
	mu  chan struct{}
	buf bytes.Buffer
}

func newLockedRecorder() *lockedRecorder {
	r := &lockedRecorder{mu: make(chan struct{}, 1)}
	r.mu <- struct{}{}
	return r
}

func (r *lockedRecorder) Write(p []byte) (int, error) {
	<-r.mu
	defer func() { r.mu <- struct{}{} }()
	return r.buf.Write(p)
}

func (r *lockedRecorder) contains(b byte) bool {
	<-r.mu
	defer func() { r.mu <- struct{}{} }()
	return bytes.IndexByte(r.buf.Bytes(), b) >= 0
}

// echoServer simulates a remote shell behind an artificial RTT: every stdin
// frame is echoed back verbatim after the delay, followed by an EchoAck for
// the cumulative offset.
func echoServer(stream *fakeStream, rtt time.Duration) {
	var offset uint64
	for {
		select {
		case <-stream.closed:
			return
		case frame, ok := <-stream.sent:
			if !ok {
				return
			}
			stdin := frame.GetStdin()
			if stdin == nil {
				continue
			}
			offset += uint64(len(stdin))
			time.Sleep(rtt)
			stream.pushStdout(append([]byte(nil), stdin...))
			stream.pushEchoAck(offset)
		}
	}
}

func waitFor(t *testing.T, deadline time.Duration, cond func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for time.Since(start) < deadline {
		if cond() {
			return time.Since(start)
		}
		time.Sleep(200 * time.Microsecond)
	}
	t.Fatalf("condition not met within %v", deadline)
	return 0
}

// runPredictiveSession starts a client with a predictive engine against an
// echo server with the given RTT and returns the pieces the test drives.
func runPredictiveSession(t *testing.T, rtt time.Duration) (io.WriteCloser, *lockedRecorder, *fakeStream, chan ptyclient.ExitResult) {
	t.Helper()

	stream := newFakeStream()
	stdinR, stdinW := io.Pipe()
	recorder := newLockedRecorder()

	engine := ptypredict.New(ptypredict.Config{
		Out:  recorder,
		Cols: 80,
		Rows: 24,
		Mode: ptypredict.ModeAlways,
	})
	require.NotNil(t, engine)

	client := ptyclient.New(ptyclient.Config{
		Stream:    stream,
		Stdin:     stdinR,
		Stdout:    recorder,
		Predictor: engine,
		Attach: ptyclient.AttachParams{
			InitialSize:    ptyclient.WindowSize{Cols: 80, Rows: 24},
			Term:           "xterm-256color",
			PredictiveEcho: true,
		},
	})

	stream.pushAttachedEchoAck("sess", "/workspace/alice")
	go echoServer(stream, rtt)

	done := make(chan ptyclient.ExitResult, 1)
	go func() {
		done <- client.Run(t.Context())
	}()

	t.Cleanup(func() {
		if err := stdinW.Close(); err != nil {
			t.Logf("close stdin writer: %v", err)
		}
	})
	return stdinW, recorder, stream, done
}

// prime confirms the first tentative epoch through one full echo round.
func primeSession(t *testing.T, stdin io.Writer, recorder *lockedRecorder, rtt time.Duration) {
	t.Helper()
	_, err := stdin.Write([]byte("x"))
	require.NoError(t, err)
	waitFor(t, rtt+2*time.Second, func() bool { return recorder.contains('x') })
	// Give the trailing EchoAck frame time to be consumed.
	time.Sleep(rtt/2 + 50*time.Millisecond)
}

// measureEchoLatencies types one key at a time and measures how long the
// glyph takes to reach the local terminal.
func measureEchoLatencies(t *testing.T, stdin io.Writer, recorder *lockedRecorder, keys string) []time.Duration {
	t.Helper()
	latencies := make([]time.Duration, 0, len(keys))
	for _, key := range []byte(keys) {
		_, err := stdin.Write([]byte{key})
		require.NoError(t, err)
		latencies = append(latencies, waitFor(t, 2*time.Second, func() bool {
			return recorder.contains(key)
		}))
	}
	return latencies
}

func median(ds []time.Duration) time.Duration {
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

// TestPredictiveEchoLatencyDecoupledFromRTT is the acceptance test for this
// phase: with predictions confirmed, locally echoed keystrokes must appear in
// single-digit milliseconds regardless of the server RTT. The engine path is
// pure in-process compute (microseconds); the asserted bounds only leave
// headroom for scheduler noise on CI machines.
func TestPredictiveEchoLatencyDecoupledFromRTT(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	for _, rtt := range []time.Duration{100 * time.Millisecond, 300 * time.Millisecond} {
		rtt := rtt
		t.Run(rtt.String(), func(t *testing.T) {
			stdin, recorder, _, _ := runPredictiveSession(t, rtt)
			primeSession(t, stdin, recorder, rtt)

			latencies := measureEchoLatencies(t, stdin, recorder, "bcdefg")

			med := median(latencies)
			t.Logf("rtt=%v local-echo latencies=%v median=%v", rtt, latencies, med)
			require.Less(t, med, 10*time.Millisecond,
				"median local echo latency must be single-digit ms, got %v (all: %v)", med, latencies)
			for _, l := range latencies {
				require.Less(t, l, rtt/2,
					"every local echo must beat the round trip, got %v at rtt %v", l, rtt)
			}
		})
	}
}

// TestPassthroughEchoWaitsForRTT is the control group: without the engine the
// echo cannot appear before the server round trip.
func TestPassthroughEchoWaitsForRTT(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	rtt := 150 * time.Millisecond

	stream := newFakeStream()
	stdinR, stdinW := io.Pipe()
	recorder := newLockedRecorder()

	client := ptyclient.New(ptyclient.Config{
		Stream: stream,
		Stdin:  stdinR,
		Stdout: recorder,
		Attach: ptyclient.AttachParams{
			InitialSize: ptyclient.WindowSize{Cols: 80, Rows: 24},
		},
	})

	stream.pushAttached("sess", "/workspace/alice")
	go echoServer(stream, rtt)
	go client.Run(t.Context())
	t.Cleanup(func() {
		if err := stdinW.Close(); err != nil {
			t.Logf("close stdin writer: %v", err)
		}
	})

	_, err := stdinW.Write([]byte("b"))
	require.NoError(t, err)
	got := waitFor(t, 2*time.Second, func() bool { return recorder.contains('b') })
	require.Greater(t, got, rtt/2, "passthrough echo cannot beat the round trip")
}

// TestPredictorDisabledWithoutServerSupport covers version skew: a server
// that does not confirm echo-ack support must yield plain passthrough.
func TestPredictorDisabledWithoutServerSupport(t *testing.T) {
	stream := newFakeStream()
	stdinR, stdinW := io.Pipe()
	recorder := newLockedRecorder()

	engine := ptypredict.New(ptypredict.Config{
		Out:  recorder,
		Cols: 80,
		Rows: 24,
		Mode: ptypredict.ModeAlways,
	})
	require.NotNil(t, engine)

	client := ptyclient.New(ptyclient.Config{
		Stream:    stream,
		Stdin:     stdinR,
		Stdout:    recorder,
		Predictor: engine,
		Attach: ptyclient.AttachParams{
			InitialSize:    ptyclient.WindowSize{Cols: 80, Rows: 24},
			PredictiveEcho: true,
		},
	})

	stream.pushAttached("sess", "/workspace/alice") // no echo-ack capability
	go echoServer(stream, 50*time.Millisecond)
	go client.Run(t.Context())
	t.Cleanup(func() {
		if err := stdinW.Close(); err != nil {
			t.Logf("close stdin writer: %v", err)
		}
	})

	_, err := stdinW.Write([]byte("b"))
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)
	require.False(t, recorder.contains('b'), "no local echo when the server lacks echo-ack")
	waitFor(t, 2*time.Second, func() bool { return recorder.contains('b') })
}
