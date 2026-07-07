package ptypredict

import (
	"strconv"

	"flyingEirc/Rclaude/pkg/vtshadow"
)

// Painting model: the real terminal always shows "shadow state ⊕ painted
// overlay". Every injection is generated against the current shadow state and
// batched into one Write so the terminal never renders an intermediate state.
//
// Invariants:
//   - appendUndo restores the exact server-side terminal context (cells,
//     cursor, pen, deferred wrap, IRM/DECOM) so following server bytes apply
//     to the state the server expects;
//   - whenever the painted set ends up empty, the real terminal state equals
//     the shadow state, keeping the no-overlay passthrough fast path valid;
//   - injections happen only at sequence boundaries (shadow.AtGround).

// flushPaint reconciles the painted overlay with what should be painted now
// and writes buf plus any repaint bytes in a single Write.
func (e *Engine) flushPaint(buf []byte) error {
	target, cursorOn := e.paintTarget()
	buf = e.appendPaintDiff(buf, target, cursorOn)
	if len(buf) == 0 {
		return nil
	}
	_, err := e.out.Write(buf)
	return err
}

// paintTarget selects the predictions that should be visible right now.
func (e *Engine) paintTarget() ([]cellPrediction, bool) {
	if !e.displayGate() || !e.shadow.AtGround() || e.shadow.AltScreen() {
		return nil, false
	}
	target := make([]cellPrediction, 0, len(e.cells))
	for i := range e.cells {
		if e.displayable(e.cells[i].epoch) {
			target = append(target, e.cells[i])
		}
	}
	cursorOn := e.cursor.active && e.displayable(e.cursor.epoch)
	return target, cursorOn
}

// displayGate is mosh's display test: always, or triggered by high SRTT or a
// glitch (predictions pending too long).
func (e *Engine) displayGate() bool {
	if e.mode == ModeAlways {
		return true
	}
	return e.srttTrigger || e.glitchTrigger > 0
}

// appendPaintDiff moves the terminal from the currently painted overlay to
// the target overlay: undo what was painted, then paint the target.
func (e *Engine) appendPaintDiff(buf []byte, target []cellPrediction, cursorOn bool) []byte {
	if len(e.painted) == 0 && !e.paintedCursor && len(target) == 0 && !cursorOn {
		return buf
	}
	if !e.shadow.AtGround() {
		// Cannot inject mid-sequence. Server bytes in buf still go out; the
		// overlay stays down (undo already ran when buf was assembled).
		return buf
	}
	buf = e.appendModePrologue(buf)
	buf = e.appendUndoCells(buf)
	for i := range target {
		buf = e.appendPaintCell(buf, &target[i])
	}
	e.painted = e.painted[:0]
	for i := range target {
		e.painted = append(e.painted, paintedCell{row: target[i].row, col: target[i].col})
	}
	e.paintedCursor = cursorOn
	return e.appendEpilogue(buf, cursorOn)
}

// appendUndo restores the server-side terminal context before server bytes
// are appended: repaint overlaid cells from the shadow and rebuild cursor,
// pen and deferred-wrap state. Caller appends the server chunk right after.
func (e *Engine) appendUndo(buf []byte) []byte {
	if len(e.painted) == 0 && !e.paintedCursor {
		return buf
	}
	buf = e.appendModePrologue(buf)
	buf = e.appendUndoCells(buf)
	e.painted = e.painted[:0]
	e.paintedCursor = false
	return e.appendServerContext(buf)
}

func (e *Engine) appendModePrologue(buf []byte) []byte {
	if e.shadow.OriginMode() {
		buf = append(buf, "\x1b[?6l"...)
	}
	if e.shadow.InsertMode() {
		buf = append(buf, "\x1b[4l"...)
	}
	return buf
}

func (e *Engine) appendModeRestore(buf []byte) []byte {
	if e.shadow.InsertMode() {
		buf = append(buf, "\x1b[4h"...)
	}
	// DECOM restore last: setting it homes the cursor, callers reposition
	// afterwards with region-relative coordinates.
	if e.shadow.OriginMode() {
		buf = append(buf, "\x1b[?6h"...)
	}
	return buf
}

// appendUndoCells repaints every painted overlay cell with its authoritative
// shadow content.
func (e *Engine) appendUndoCells(buf []byte) []byte {
	for _, pc := range e.painted {
		cell := e.shadow.Cell(pc.row, pc.col)
		if cell.Width != 1 {
			// The shadow now holds part of a wide rune here; repaint the
			// full rune from its leading cell.
			buf = e.appendWideRepair(buf, pc.row, pc.col)
			continue
		}
		buf = appendCUP(buf, pc.row, pc.col)
		buf = vtshadow.AppendSGR(buf, cell.SGR)
		buf = appendRune(buf, cell.DisplayRune())
	}
	return buf
}

func (e *Engine) appendWideRepair(buf []byte, row, col int) []byte {
	lead := col
	cell := e.shadow.Cell(row, lead)
	if cell.Width == 0 && lead > 0 {
		lead--
		cell = e.shadow.Cell(row, lead)
	}
	if cell.Width != 2 {
		return buf
	}
	buf = appendCUP(buf, row, lead)
	buf = vtshadow.AppendSGR(buf, cell.SGR)
	return appendRune(buf, cell.DisplayRune())
}

func (e *Engine) appendPaintCell(buf []byte, pred *cellPrediction) []byte {
	buf = appendCUP(buf, pred.row, pred.col)
	sgr := pred.sgr
	if e.flagging {
		sgr.Attr |= vtshadow.AttrUnderline
	}
	buf = vtshadow.AppendSGR(buf, sgr)
	return appendRune(buf, pred.glyph)
}

// appendEpilogue finishes an injection batch that leaves overlay painted:
// modes restored and the cursor parked at the predicted (or shadow) spot.
func (e *Engine) appendEpilogue(buf []byte, cursorOn bool) []byte {
	if len(e.painted) == 0 && !cursorOn {
		return e.appendServerContext(buf)
	}
	buf = vtshadow.AppendSGR(buf, e.shadow.Pen())
	buf = e.appendModeRestore(buf)
	row, col := e.shadow.Cursor()
	if cursorOn {
		row, col = e.cursor.row, e.cursor.col
	}
	return e.appendFinalCUP(buf, row, col)
}

// appendServerContext restores the terminal to the exact shadow state: pen,
// modes, cursor position, and the deferred-wrap flag.
func (e *Engine) appendServerContext(buf []byte) []byte {
	buf = vtshadow.AppendSGR(buf, e.shadow.Pen())
	buf = e.appendModeRestore(buf)
	if e.shadow.PendingWrap() {
		return e.appendWrapRearm(buf)
	}
	row, col := e.shadow.Cursor()
	return e.appendFinalCUP(buf, row, col)
}

// appendWrapRearm reprints the glyph that armed the deferred wrap so the real
// terminal ends in the same wrap-pending state the server believes it is in.
func (e *Engine) appendWrapRearm(buf []byte) []byte {
	row, col := e.shadow.Cursor()
	cell := e.shadow.Cell(row, col)
	lead := col
	if cell.Width == 0 && col > 0 {
		lead = col - 1
		cell = e.shadow.Cell(row, lead)
	}
	buf = e.appendFinalCUP(buf, row, lead)
	buf = vtshadow.AppendSGR(buf, cell.SGR)
	buf = appendRune(buf, cell.DisplayRune())
	return vtshadow.AppendSGR(buf, e.shadow.Pen())
}

// appendFinalCUP positions the cursor, honoring origin mode with
// region-relative coordinates.
func (e *Engine) appendFinalCUP(buf []byte, row, col int) []byte {
	if e.shadow.OriginMode() {
		return appendCUP(buf, row-e.shadow.ScrollTop(), col)
	}
	return appendCUP(buf, row, col)
}

func appendCUP(buf []byte, row, col int) []byte {
	buf = append(buf, "\x1b["...)
	buf = strconv.AppendInt(buf, int64(row+1), 10)
	buf = append(buf, ';')
	buf = strconv.AppendInt(buf, int64(col+1), 10)
	return append(buf, 'H')
}

func appendRune(buf []byte, r rune) []byte {
	if r == 0 {
		r = ' '
	}
	return append(buf, string(r)...)
}
