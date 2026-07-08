package ptypredict

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"flyingEirc/Rclaude/pkg/vtshadow"
)

// fakeClock is a manual clock; the engine only reads it via Config.Now.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Unix(1000, 0)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// realTerminal records everything the engine writes and replays it into a
// second shadow, standing in for the user's actual terminal.
type realTerminal struct {
	screen *vtshadow.Shadow
	log    bytes.Buffer
}

func (r *realTerminal) Write(p []byte) (int, error) {
	r.log.Write(p)
	r.screen.Feed(p)
	return len(p), nil
}

// harness drives one engine against a scripted server.
type harness struct {
	t      *testing.T
	engine *Engine
	real   *realTerminal
	clock  *fakeClock
	offset uint64
}

func newHarness(t *testing.T, mode Mode) *harness {
	t.Helper()
	clock := newFakeClock()
	real := &realTerminal{screen: vtshadow.New(40, 6)}
	engine := New(Config{Out: real, Cols: 40, Rows: 6, Mode: mode, Now: clock.now})
	require.NotNil(t, engine)
	return &harness{t: t, engine: engine, real: real, clock: clock}
}

func (h *harness) typeKeys(s string) {
	h.t.Helper()
	h.offset += uint64(len(s))
	require.NoError(h.t, h.engine.OnStdin([]byte(s), h.offset))
}

func (h *harness) server(s string) {
	h.t.Helper()
	require.NoError(h.t, h.engine.OnServerOutput([]byte(s)))
}

// ack advances the clock by rtt and acknowledges everything sent so far.
func (h *harness) ack(rtt time.Duration) {
	h.t.Helper()
	h.clock.advance(rtt)
	require.NoError(h.t, h.engine.OnEchoAck(h.offset))
}

// prime replays one confirmed echo round so the initial tentative epoch is
// confirmed (mosh: first predictions stay hidden until proven).
func (h *harness) prime(rtt time.Duration) {
	h.t.Helper()
	h.server("$ ")
	h.typeKeys("x")
	h.clock.advance(rtt / 2)
	h.server("x")
	h.clock.advance(rtt / 2)
	require.NoError(h.t, h.engine.OnEchoAck(h.offset))
}

// converged asserts the real terminal ended up exactly in the authoritative
// server state: same rows, same cursor.
func (h *harness) converged() {
	h.t.Helper()
	_, rows := h.engine.shadow.Size()
	for r := 0; r < rows; r++ {
		require.Equal(h.t, h.engine.shadow.RowString(r), h.real.screen.RowString(r), "row %d", r)
	}
	sr, sc := h.engine.shadow.Cursor()
	rr, rc := h.real.screen.Cursor()
	require.Equal(h.t, sr, rr, "cursor row")
	require.Equal(h.t, sc, rc, "cursor col")
}

func TestFirstEpochIsTentative(t *testing.T) {
	h := newHarness(t, ModeAlways)
	h.server("$ ")
	before := h.real.log.Len()
	h.typeKeys("a")
	require.Equal(t, before, h.real.log.Len(), "tentative prediction must stay dark")
}

func TestLocalEchoImmediateAfterConfirmation(t *testing.T) {
	h := newHarness(t, ModeAlways)
	h.prime(200 * time.Millisecond)

	before := h.real.log.Len()
	h.typeKeys("b")
	require.Greater(t, h.real.log.Len(), before, "confirmed epoch must paint immediately")
	require.Equal(t, 'b', h.real.screen.Cell(0, 3).Rune)
	_, col := h.real.screen.Cursor()
	require.Equal(t, 4, col, "cursor advances with the prediction")
}

func TestShellRelativeEchoNoDoubleWrite(t *testing.T) {
	h := newHarness(t, ModeAlways)
	h.prime(200 * time.Millisecond)

	h.typeKeys("b")
	h.server("b") // relative echo, exactly what a shell sends
	h.ack(200 * time.Millisecond)

	require.Equal(t, "$ xb", h.real.screen.RowString(0))
	h.converged()
	require.Empty(t, h.engine.cells, "confirmed prediction is retired")
}

func TestInkStyleRedrawConverges(t *testing.T) {
	h := newHarness(t, ModeAlways)
	h.prime(200 * time.Millisecond)

	h.typeKeys("b")
	// Ink repaints the whole line absolutely instead of echoing one byte.
	h.server("\r\x1b[2K$ xb")
	h.ack(200 * time.Millisecond)

	require.Equal(t, "$ xb", h.real.screen.RowString(0))
	h.converged()
}

func TestMispredictionRollsBack(t *testing.T) {
	h := newHarness(t, ModeAlways)
	h.prime(200 * time.Millisecond)

	h.typeKeys("b")
	require.Equal(t, 'b', h.real.screen.Cell(0, 3).Rune)
	h.server("Z") // application echoed something else
	h.ack(200 * time.Millisecond)

	require.Equal(t, "$ xZ", h.real.screen.RowString(0))
	h.converged()
	require.Empty(t, h.engine.cells)
}

func TestNoEchoInputStaysSilent(t *testing.T) {
	h := newHarness(t, ModeAlways)
	h.prime(200 * time.Millisecond)

	h.typeKeys("\r") // uncertain input: epoch becomes tentative again
	before := h.real.log.Len()
	h.typeKeys("secret")
	require.Equal(t, before, h.real.log.Len(), "tentative predictions stay dark")

	h.ack(200 * time.Millisecond) // no echo came back
	require.Equal(t, before, h.real.log.Len(), "silent kill leaves no trace")
	require.Empty(t, h.engine.cells)
}

func TestBackspaceCancelsOwnPrediction(t *testing.T) {
	h := newHarness(t, ModeAlways)
	h.prime(200 * time.Millisecond)

	h.typeKeys("b")
	h.typeKeys("c")
	require.Equal(t, "$ xbc", h.real.screen.RowString(0))
	h.typeKeys("\x7f")

	require.Equal(t, "$ xb", h.real.screen.RowString(0))
	_, col := h.real.screen.Cursor()
	require.Equal(t, 4, col)
}

func TestAdaptiveHidesOnFastLink(t *testing.T) {
	h := newHarness(t, ModeAdaptive)
	h.prime(20 * time.Millisecond) // SRTT far below the display trigger

	before := h.real.log.Len()
	h.typeKeys("b")
	require.Equal(t, before, h.real.log.Len(), "fast link: no visible prediction")

	h.server("b")
	h.ack(20 * time.Millisecond)
	h.converged()
}

func TestAdaptiveShowsOnSlowLink(t *testing.T) {
	h := newHarness(t, ModeAdaptive)
	h.prime(300 * time.Millisecond) // first RTT sample ≈ 250ms => srtt trigger

	before := h.real.log.Len()
	h.typeKeys("b")
	require.Greater(t, h.real.log.Len(), before, "slow link: prediction shown")
	require.Equal(t, 'b', h.real.screen.Cell(0, 3).Rune)
}

func TestGlitchTriggerShowsPendingPrediction(t *testing.T) {
	h := newHarness(t, ModeAdaptive)
	h.prime(20 * time.Millisecond) // gate closed

	h.typeKeys("b")
	before := h.real.log.Len()
	h.clock.advance(glitchThreshold + 10*time.Millisecond)
	require.NoError(t, h.engine.Tick())

	require.Greater(t, h.real.log.Len(), before, "glitch: pending prediction becomes visible")
	require.Equal(t, 'b', h.real.screen.Cell(0, 3).Rune)
}

func TestBulkPasteResetsPredictions(t *testing.T) {
	h := newHarness(t, ModeAlways)
	h.prime(200 * time.Millisecond)

	paste := bytes.Repeat([]byte("y"), bulkPasteBytes+1)
	h.offset += uint64(len(paste))
	require.NoError(t, h.engine.OnStdin(paste, h.offset))
	require.Empty(t, h.engine.cells, "bulk paste is never predicted")
}

// TestAltScreenPredictsLikeClaudeCode replays the echo shape captured from a
// real Claude Code session: alt screen active, hardware cursor at the input
// caret, absolute-addressed partial redraw. Predictions must work there.
func TestAltScreenPredictsLikeClaudeCode(t *testing.T) {
	h := newHarness(t, ModeAlways)
	// Claude-style startup: enter alt screen, draw prompt, park cursor at
	// the caret (row 4, col 2 after "❯ ").
	h.server("\x1b[?1049h\x1b[2J\x1b[5;1H❯ ")

	h.typeKeys("a") // first epoch: tentative, dark
	// Claude echoes with an absolute partial redraw, cursor back to caret+1.
	h.server("\x1b[?25l\x1b[H\r\x1b[2C\x1b[4Ba\x1b[K\x1b[5;4H\x1b[?25h")
	h.ack(200 * time.Millisecond)
	require.Empty(t, h.engine.cells, "first prediction confirmed and retired")

	before := h.real.log.Len()
	h.typeKeys("b")
	require.Greater(t, h.real.log.Len(), before, "confirmed epoch paints instantly on the alt screen")
	require.Equal(t, 'b', h.real.screen.Cell(4, 3).Rune)

	h.server("\x1b[?25l\x1b[H\r\x1b[2C\x1b[4B\x1b[1Cb\x1b[K\x1b[5;5H\x1b[?25h")
	h.ack(200 * time.Millisecond)
	h.converged()
}

func TestWideRunesAreNotPredicted(t *testing.T) {
	h := newHarness(t, ModeAlways)
	h.prime(200 * time.Millisecond)

	before := h.real.log.Len()
	h.typeKeys("中")
	require.Equal(t, before, h.real.log.Len())
	require.Empty(t, h.engine.cells)
}

func TestCursorPredictionArrows(t *testing.T) {
	h := newHarness(t, ModeAlways)
	h.prime(200 * time.Millisecond)

	h.typeKeys("b")
	h.typeKeys("\x1b[D") // cursor left
	_, col := h.real.screen.Cursor()
	require.Equal(t, 3, col, "left arrow moves the predicted cursor")

	// Server agrees: echo for 'b' then cursor-left.
	h.server("b\x1b[D")
	h.ack(200 * time.Millisecond)
	h.converged()
}

func TestServerOutputWhilePredictionPendingKeepsOverlay(t *testing.T) {
	h := newHarness(t, ModeAlways)
	h.prime(200 * time.Millisecond)

	h.typeKeys("b")
	// Unrelated server output on another row must not disturb the overlay.
	h.server("\x1b7\x1b[3;1Hlog line\x1b8")
	require.Equal(t, 'b', h.real.screen.Cell(0, 3).Rune, "prediction survives unrelated output")
	require.Equal(t, "log line", h.real.screen.RowString(2))

	h.server("b")
	h.ack(200 * time.Millisecond)
	h.converged()
}

func TestMidSequenceChunkBoundaryIsSafe(t *testing.T) {
	h := newHarness(t, ModeAlways)
	h.prime(200 * time.Millisecond)

	h.typeKeys("b")
	// Chunk ends in the middle of a CSI sequence: the overlay must come down
	// and stay down until the sequence completes.
	h.server("\x1b[3")
	h.server(";1Hq")
	h.server("\x1b[1;5H") // server puts cursor back on the input row
	h.server("b")
	h.ack(200 * time.Millisecond)

	require.Equal(t, 'q', h.real.screen.Cell(2, 0).Rune)
	h.converged()
}

func TestResizeDropsPredictions(t *testing.T) {
	h := newHarness(t, ModeAlways)
	h.prime(200 * time.Millisecond)

	h.typeKeys("b")
	h.engine.OnResize(60, 10)
	require.Empty(t, h.engine.cells)
	cols, rows := h.engine.shadow.Size()
	require.Equal(t, 60, cols)
	require.Equal(t, 10, rows)
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": ModeAdaptive, "adaptive": ModeAdaptive, "always": ModeAlways, "off": ModeOff} {
		got, ok := ParseMode(in)
		require.True(t, ok, in)
		require.Equal(t, want, got, in)
	}
	_, ok := ParseMode("bogus")
	require.False(t, ok)
}

func TestNewOffModeReturnsNil(t *testing.T) {
	require.Nil(t, New(Config{Out: &bytes.Buffer{}, Mode: ModeOff}))
}
