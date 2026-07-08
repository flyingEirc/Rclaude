package vtshadow

import "strings"

// Shadow is a passive terminal-state tracker: it consumes the byte stream a
// real terminal receives and answers questions about the resulting screen
// (cursor, cells, pen, modes). It never renders and never answers back.
// It is not safe for concurrent use; callers serialize access.
type Shadow struct {
	scr *screen
	par *parser
}

// New returns a Shadow sized cols x rows (defaults 80x24 for zero values).
func New(cols, rows int) *Shadow {
	return &Shadow{scr: newScreen(cols, rows), par: newParser()}
}

// Feed applies one chunk of terminal output and reports what happened.
func (s *Shadow) Feed(p []byte) Feedback {
	s.scr.fb = Feedback{}
	for _, b := range p {
		s.par.feed(s.scr, b)
	}
	return s.scr.fb
}

// Resize changes the grid size, preserving what fits.
func (s *Shadow) Resize(cols, rows int) {
	s.scr.setSize(cols, rows)
}

// AtGround reports whether the parser sits at a sequence boundary, i.e. it is
// safe to inject locally generated escape sequences into the output stream.
func (s *Shadow) AtGround() bool {
	return s.par.atGround()
}

// Cursor returns the current cursor position (0-based row, col).
func (s *Shadow) Cursor() (row, col int) {
	return s.scr.cur.row, s.scr.cur.col
}

// Size returns the grid dimensions.
func (s *Shadow) Size() (cols, rows int) {
	return s.scr.cols, s.scr.rows
}

// Cell returns the cell at 0-based (row, col); out of range yields a zero cell.
func (s *Shadow) Cell(row, col int) Cell {
	return s.scr.cell(row, col)
}

// Pen returns the current graphic rendition state.
func (s *Shadow) Pen() SGR {
	return s.scr.pen
}

// PendingWrap reports whether the cursor sits in the deferred-wrap state
// after printing into the last column.
func (s *Shadow) PendingWrap() bool {
	return s.scr.pendingWrap
}

// AltScreen reports whether the alternate screen is active.
func (s *Shadow) AltScreen() bool {
	return s.scr.altActive
}

// OriginMode reports DECOM state.
func (s *Shadow) OriginMode() bool {
	return s.scr.originMode
}

// InsertMode reports IRM state.
func (s *Shadow) InsertMode() bool {
	return s.scr.insertMode
}

// ScrollTop returns the 0-based top row of the scroll region (for
// origin-mode-relative addressing).
func (s *Shadow) ScrollTop() int {
	return s.scr.top
}

// RowString renders one row's display content as a string, trailing blanks
// trimmed. Intended for tests and diagnostics.
func (s *Shadow) RowString(row int) string {
	if row < 0 || row >= s.scr.rows {
		return ""
	}
	var b strings.Builder
	for col := 0; col < s.scr.cols; col++ {
		cell := s.scr.cell(row, col)
		if cell.Width == 0 {
			continue // continuation half of a wide rune
		}
		b.WriteRune(cell.DisplayRune())
	}
	return strings.TrimRight(b.String(), " ")
}
