package ptypredict

import (
	"flyingEirc/Rclaude/pkg/vtshadow"
)

// predictedCursor returns where typing goes next: the active cursor
// prediction, else the shadow cursor.
func (e *Engine) predictedCursor() (row, col int) {
	if e.cursor.active {
		return e.cursor.row, e.cursor.col
	}
	return e.shadow.Cursor()
}

// mayPredict gates new predictions: never on the alternate screen, never
// while quenched after suspicious output.
func (e *Engine) mayPredict() bool {
	if e.shadow.AltScreen() {
		return false
	}
	return e.quenchUntil.IsZero() || e.now().After(e.quenchUntil)
}

// predictRune predicts the echo of one printable rune with overwrite
// semantics (mosh MOSH_PREDICTION_OVERWRITE).
func (e *Engine) predictRune(r rune) {
	if !e.mayPredict() || vtshadow.CellWidth(r) != 1 || r < 0x20 {
		e.becomeTentative()
		return
	}
	row, col := e.predictedCursor()
	cols, _ := e.shadow.Size()
	if col >= cols-1 {
		// Last column: wrap behavior is app-dependent (mosh goes tentative).
		e.becomeTentative()
		return
	}
	original := e.shadow.Cell(row, col)
	if original.Width != 1 {
		// Half of a wide rune: painting over it garbles real terminals.
		e.becomeTentative()
		return
	}
	e.upsertCell(cellPrediction{
		row:        row,
		col:        col,
		glyph:      r,
		sgr:        e.predictionSGR(row, col),
		original:   original,
		expiration: e.sentOffset,
		epoch:      e.predictionEpoch,
		madeAt:     e.now(),
	})
	e.setCursorPrediction(row, col+1)
}

// predictionSGR inherits the rendition of the glyph to the left of the
// prediction (mosh heuristic).
func (e *Engine) predictionSGR(row, col int) vtshadow.SGR {
	if col > 0 {
		left := e.shadow.Cell(row, col-1)
		if !left.Blank() {
			return left.SGR
		}
	}
	return e.shadow.Pen()
}

func (e *Engine) upsertCell(pred cellPrediction) {
	for i := range e.cells {
		if e.cells[i].row == pred.row && e.cells[i].col == pred.col {
			// Keep the first original snapshot: it reflects the true
			// pre-prediction content of the cell.
			pred.original = e.cells[i].original
			e.cells[i] = pred
			return
		}
	}
	e.cells = append(e.cells, pred)
}

// predictBackspace only cancels the engine's own trailing prediction;
// anything else is app-dependent (mosh predicts more, we stay conservative).
func (e *Engine) predictBackspace() {
	if !e.mayPredict() {
		e.becomeTentative()
		return
	}
	row, col := e.predictedCursor()
	idx := e.findCell(row, col-1)
	if idx < 0 {
		e.becomeTentative()
		return
	}
	e.cells = append(e.cells[:idx], e.cells[idx+1:]...)
	e.setCursorPrediction(row, col-1)
}

func (e *Engine) findCell(row, col int) int {
	for i := range e.cells {
		if e.cells[i].row == row && e.cells[i].col == col {
			return i
		}
	}
	return -1
}

// predictNewline handles CR: tentative first, then a cursor-only prediction
// to the next line start; never predicts scroll (mosh).
func (e *Engine) predictNewline() {
	e.becomeTentative()
	if !e.mayPredict() {
		return
	}
	row, _ := e.predictedCursor()
	_, rows := e.shadow.Size()
	if row >= rows-1 {
		return
	}
	e.setCursorPrediction(row+1, 0)
}

func (e *Engine) predictCursorMove(dx int) {
	if !e.mayPredict() {
		e.becomeTentative()
		return
	}
	row, col := e.predictedCursor()
	cols, _ := e.shadow.Size()
	col = clamp(col+dx, 0, cols-1)
	e.setCursorPrediction(row, col)
}

func (e *Engine) setCursorPrediction(row, col int) {
	e.cursor = cursorPrediction{
		row:        row,
		col:        col,
		expiration: e.sentOffset,
		epoch:      e.predictionEpoch,
		active:     true,
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// validity outcomes, mirroring mosh's Validity enum.
type validity uint8

const (
	validityPending validity = iota
	validityCorrect
	validityCorrectNoCredit
	validityIncorrect
)

func (e *Engine) cellValidity(pred *cellPrediction) validity {
	if e.lateAck < pred.expiration {
		return validityPending
	}
	cols, rows := e.shadow.Size()
	if pred.row >= rows || pred.col >= cols {
		return validityIncorrect
	}
	cell := e.shadow.Cell(pred.row, pred.col)
	if cell.Rune != pred.glyph {
		return validityIncorrect
	}
	if pred.original.DisplayRune() == pred.glyph {
		// The prediction matched what was already there: no epoch credit
		// (mosh original_contents rule).
		return validityCorrectNoCredit
	}
	return validityCorrect
}

func (e *Engine) cursorValidity() validity {
	if !e.cursor.active {
		return validityCorrectNoCredit
	}
	if e.lateAck < e.cursor.expiration {
		return validityPending
	}
	row, col := e.shadow.Cursor()
	if row == e.cursor.row && col == e.cursor.col {
		return validityCorrect
	}
	return validityIncorrect
}

// cull judges all predictions against the shadow at the current echo-ack
// watermark: mosh PredictionEngine::cull.
func (e *Engine) cull() {
	for {
		restart, didReset := e.cullCellsOnce()
		if didReset {
			return
		}
		if !restart {
			break
		}
	}
	e.cullCursor()
}

// cullCellsOnce judges each cell prediction. It returns restart=true after a
// killEpoch mutated the set mid-scan, and didReset=true when a non-tentative
// misprediction forced a full reset (mosh: seen predictions fail loudly,
// unseen ones die quietly).
func (e *Engine) cullCellsOnce() (restart, didReset bool) {
	kept := make([]cellPrediction, 0, len(e.cells))
	for i := range e.cells {
		pred := e.cells[i]
		switch e.cellValidity(&pred) {
		case validityPending:
			kept = append(kept, pred)
		case validityCorrect:
			e.confirmEpoch(pred.epoch)
			e.repairGlitch(pred.madeAt)
		case validityCorrectNoCredit:
			// Drop silently, no credit.
		case validityIncorrect:
			if e.displayable(pred.epoch) {
				e.resetPredictions()
				return false, true
			}
			e.killEpoch(pred.epoch)
			return true, false
		}
	}
	e.cells = kept
	return false, false
}

func (e *Engine) cullCursor() {
	if !e.cursor.active {
		return
	}
	switch e.cursorValidity() {
	case validityCorrect:
		e.cursor.active = false
	case validityIncorrect:
		// mosh: a wrong cursor prediction cannot be repaired locally.
		e.resetPredictions()
	case validityPending, validityCorrectNoCredit:
	}
}

func (e *Engine) confirmEpoch(epoch uint64) {
	if epoch > e.confirmedEpoch {
		e.confirmedEpoch = epoch
	}
}

// killEpoch drops all predictions of the given epoch and later; the user
// never saw them (mosh kill_epoch).
func (e *Engine) killEpoch(epoch uint64) {
	kept := make([]cellPrediction, 0, len(e.cells))
	for i := range e.cells {
		if e.cells[i].epoch < epoch {
			kept = append(kept, e.cells[i])
		}
	}
	e.cells = kept
	if e.cursor.active && e.cursor.epoch >= epoch {
		e.cursor.active = false
	}
	e.becomeTentative()
}

// displayable reports whether a prediction has left the tentative state
// (mosh: tentative_until_epoch > confirmed_epoch means hidden).
func (e *Engine) displayable(epoch uint64) bool {
	return epoch <= e.confirmedEpoch
}
