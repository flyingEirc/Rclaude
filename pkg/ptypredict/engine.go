package ptypredict

import (
	"io"
	"sync"
	"time"

	"flyingEirc/Rclaude/pkg/vtshadow"
)

// Mode selects when predictions are shown (mosh --predict equivalents; Off
// means the engine is not installed at all).
type Mode uint8

const (
	ModeOff Mode = iota
	ModeAdaptive
	ModeAlways
)

// ParseMode maps a config string to a Mode. Empty defaults to adaptive.
func ParseMode(s string) (Mode, bool) {
	switch s {
	case "", "adaptive":
		return ModeAdaptive, true
	case "always":
		return ModeAlways, true
	case "off":
		return ModeOff, true
	default:
		return ModeOff, false
	}
}

// Mosh-derived constants (docs/reference/mosh.md). Times in engine terms;
// interval thresholds apply to sendInterval = clamp(SRTT/2, 20, 250) ms.
const (
	echoTimeout             = 50 * time.Millisecond
	srttTriggerHighMs       = 30
	srttTriggerLowMs        = 20
	flagTriggerHighMs       = 80
	flagTriggerLowMs        = 50
	glitchThreshold         = 250 * time.Millisecond
	glitchFlagThreshold     = 5000 * time.Millisecond
	glitchRepairCount       = 10
	glitchRepairMinInterval = 150 * time.Millisecond
	bulkPasteBytes          = 100
	quenchDuration          = 300 * time.Millisecond
	maxPendingSendTimes     = 1024
)

// cellPrediction is one predicted glyph (mosh ConditionalOverlayCell,
// overwrite semantics: exactly one cell per prediction).
type cellPrediction struct {
	row, col   int
	glyph      rune
	sgr        vtshadow.SGR // rendition to paint with
	original   vtshadow.Cell
	expiration uint64 // stdin offset that must be echo-acked before judging
	epoch      uint64
	madeAt     time.Time
}

// cursorPrediction tracks where the cursor should sit if all predictions
// hold (mosh ConditionalCursorMove, collapsed to the latest).
type cursorPrediction struct {
	row, col   int
	expiration uint64
	epoch      uint64
	active     bool
}

type paintedCell struct {
	row, col int
}

// Config configures an Engine.
type Config struct {
	// Out receives all bytes destined for the local terminal. While the
	// engine is active it must be the only writer.
	Out io.Writer
	// Cols/Rows are the initial terminal dimensions.
	Cols, Rows int
	// Mode is the display gating mode (ModeOff yields a nil engine).
	Mode Mode
	// Now overrides time.Now for tests.
	Now func() time.Time
}

// Engine is the predictive echo engine. All methods are safe for concurrent
// use; the engine serializes every write to the local terminal.
type Engine struct {
	mu     sync.Mutex
	out    io.Writer
	shadow *vtshadow.Shadow
	mode   Mode
	now    func() time.Time

	keys   keyParser
	events []keyEvent

	cells  []cellPrediction
	cursor cursorPrediction

	predictionEpoch uint64
	confirmedEpoch  uint64

	sentOffset uint64
	lateAck    uint64
	sendTimes  []offsetTime

	painted       []paintedCell
	paintedCursor bool

	srtt             float64
	srttValid        bool
	srttTrigger      bool
	flagging         bool
	glitchTrigger    int
	lastQuickConfirm time.Time

	quenchUntil time.Time
}

type offsetTime struct {
	offset uint64
	at     time.Time
}

// New returns an engine, or nil when cfg.Mode is ModeOff (callers treat a nil
// engine as plain passthrough).
func New(cfg Config) *Engine {
	if cfg.Mode == ModeOff || cfg.Out == nil {
		return nil
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Engine{
		out:    cfg.Out,
		shadow: vtshadow.New(cfg.Cols, cfg.Rows),
		mode:   cfg.Mode,
		now:    now,
		// mosh starts at prediction_epoch=1 / confirmed_epoch=0: the first
		// predictions stay hidden until the server confirms one of them.
		predictionEpoch: 1,
	}
}

// OnStdin observes one stdin chunk about to be sent to the server and paints
// predicted echo locally. offsetAfter is the cumulative count of stdin bytes
// sent, including this chunk.
func (e *Engine) OnStdin(p []byte, offsetAfter uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.sentOffset = offsetAfter
	e.recordSendTime(offsetAfter)

	if len(p) > bulkPasteBytes {
		// mosh: bulk paste, don't predict any of it.
		e.resetPredictions()
		return e.flushPaint(nil)
	}

	e.events = e.keys.feed(p, e.events[:0])
	for _, ev := range e.events {
		e.handleKeyEvent(ev)
	}
	return e.flushPaint(nil)
}

// OnServerOutput forwards one server stdout chunk to the local terminal,
// wrapped with overlay undo/redraw, and advances the shadow state.
func (e *Engine) OnServerOutput(chunk []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if len(e.painted) == 0 && !e.paintedCursor && len(e.cells) == 0 && !e.cursor.active {
		// Fast path: nothing predicted, plain passthrough.
		e.feedShadow(chunk)
		_, err := e.out.Write(chunk)
		return err
	}

	buf := e.appendUndo(nil)
	buf = append(buf, chunk...)
	e.feedShadow(chunk)
	return e.flushPaint(buf)
}

// OnEchoAck advances the server echo watermark, samples RTT, and judges
// predictions old enough to be checked (mosh cull).
func (e *Engine) OnEchoAck(offset uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if offset > e.lateAck {
		e.lateAck = offset
	}
	e.sampleRTT(offset)
	e.cull()
	return e.flushPaint(nil)
}

// OnResize adjusts the shadow grid and drops all predictions (mosh reset on
// resize).
func (e *Engine) OnResize(cols, rows int) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.shadow.Resize(cols, rows)
	e.resetPredictions()
	// The server will repaint after its own resize handling; painted overlay
	// coordinates are void, do not attempt to undo them.
	e.painted = e.painted[:0]
	e.paintedCursor = false
}

// Tick runs time-based state transitions (glitch display escalation, quench
// expiry) and repaints if the outcome changed.
func (e *Engine) Tick() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.escalateGlitch()
	return e.flushPaint(nil)
}

// feedShadow applies server bytes to the shadow and reacts to what happened.
func (e *Engine) feedShadow(chunk []byte) {
	fb := e.shadow.Feed(chunk)
	if fb.Scrolled || fb.ScreenSwitch {
		// Cell anchors are void after scrolls and screen switches; predictions
		// cannot be validated anymore (mosh does not predict scroll).
		e.resetPredictions()
	}
	if fb.Unknown > 0 {
		// The shadow may have drifted: stop predicting for a moment.
		e.resetPredictions()
		e.quenchUntil = e.now().Add(quenchDuration)
	}
}

func (e *Engine) handleKeyEvent(ev keyEvent) {
	switch ev.kind {
	case keyRune:
		e.predictRune(ev.r)
	case keyBackspace:
		e.predictBackspace()
	case keyCR:
		e.predictNewline()
	case keyCursorRight:
		e.predictCursorMove(1)
	case keyCursorLeft:
		e.predictCursorMove(-1)
	default:
		e.becomeTentative()
	}
}

func (e *Engine) recordSendTime(offset uint64) {
	if len(e.sendTimes) >= maxPendingSendTimes {
		e.sendTimes = e.sendTimes[1:]
	}
	e.sendTimes = append(e.sendTimes, offsetTime{offset: offset, at: e.now()})
}

func (e *Engine) becomeTentative() {
	e.predictionEpoch++
}

func (e *Engine) resetPredictions() {
	e.cells = e.cells[:0]
	e.cursor.active = false
	e.becomeTentative()
}
