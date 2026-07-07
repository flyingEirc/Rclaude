package vtshadow

// Feedback accumulates side observations of one Feed call that the prediction
// engine needs: scrolls invalidate cell anchors, screen switches and unknown
// sequences mean the shadow may be less trustworthy for a moment.
type Feedback struct {
	Scrolled     bool
	ScreenSwitch bool
	Unknown      int
}

type cursor struct {
	row, col int
}

type savedCursor struct {
	cur       cursor
	pen       SGR
	origin    bool
	valid     bool
	g0Special bool
	shiftOut  bool
}

type screen struct {
	cols, rows int

	main, alt   [][]Cell
	altActive   bool
	savedMain   savedCursor
	savedAlt    savedCursor
	xtermSaved  savedCursor // DECSET 1048/1049 saved cursor
	cur         cursor
	pen         SGR
	pendingWrap bool

	top, bot int // scroll region, inclusive rows

	autowrap   bool
	originMode bool
	insertMode bool
	lnm        bool

	tabs []bool

	g0Special bool // DEC special graphics designated as G0
	g1Special bool
	shiftOut  bool // SO active: G1 in use

	fb Feedback
}

func newScreen(cols, rows int) *screen {
	s := &screen{autowrap: true}
	s.setSize(cols, rows)
	return s
}

func clampDim(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

func (s *screen) setSize(cols, rows int) {
	cols = clampDim(cols, 80)
	rows = clampDim(rows, 24)
	s.main = resizeGrid(s.main, cols, rows)
	s.alt = resizeGrid(s.alt, cols, rows)
	s.cols, s.rows = cols, rows
	s.top, s.bot = 0, rows-1
	s.cur.row = clampInt(s.cur.row, 0, rows-1)
	s.cur.col = clampInt(s.cur.col, 0, cols-1)
	s.pendingWrap = false
	s.tabs = defaultTabs(cols)
}

func resizeGrid(grid [][]Cell, cols, rows int) [][]Cell {
	out := make([][]Cell, rows)
	for r := range out {
		row := newBlankRow(cols)
		if r < len(grid) {
			copy(row, grid[r])
		}
		out[r] = row
	}
	return out
}

func newBlankRow(cols int) []Cell {
	row := make([]Cell, cols)
	for c := range row {
		row[c] = Cell{Width: 1}
	}
	return row
}

func defaultTabs(cols int) []bool {
	tabs := make([]bool, cols)
	for c := 8; c < cols; c += 8 {
		tabs[c] = true
	}
	return tabs
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (s *screen) grid() [][]Cell {
	if s.altActive {
		return s.alt
	}
	return s.main
}

func (s *screen) cell(row, col int) Cell {
	if row < 0 || row >= s.rows || col < 0 || col >= s.cols {
		return Cell{}
	}
	return s.grid()[row][col]
}

// print writes one rune at the cursor with current pen, handling deferred
// wrap, wide runes, insert mode, and DEC special graphics mapping.
func (s *screen) print(r rune) {
	if s.shiftOut && s.g1Special || !s.shiftOut && s.g0Special {
		r = mapSpecialGraphics(r)
	}
	w := CellWidth(r)
	if w == 0 {
		return
	}
	if s.pendingWrap && s.autowrap {
		s.wrapToNextLine()
	}
	if w == 2 && s.cur.col == s.cols-1 {
		s.handleWideAtLastColumn()
	}
	s.placeRune(r, w)
}

func (s *screen) wrapToNextLine() {
	s.pendingWrap = false
	s.cur.col = 0
	s.linefeedNoCR()
}

func (s *screen) handleWideAtLastColumn() {
	// No room for a wide rune in the last column: blank it and wrap (or clip
	// against the edge when autowrap is off).
	s.setCell(s.cur.row, s.cur.col, Cell{Rune: ' ', Width: 1, SGR: s.pen})
	if s.autowrap {
		s.cur.col = 0
		s.linefeedNoCR()
		return
	}
	s.cur.col = s.cols - 2
}

func (s *screen) placeRune(r rune, w int) {
	if s.insertMode {
		s.insertCells(s.cur.row, s.cur.col, w)
	}
	s.splitWideAt(s.cur.row, s.cur.col)
	if w == 2 {
		s.splitWideAt(s.cur.row, s.cur.col+1)
	}
	grid := s.grid()
	grid[s.cur.row][s.cur.col] = Cell{Rune: r, Width: uint8(w), SGR: s.pen}
	if w == 2 && s.cur.col+1 < s.cols {
		grid[s.cur.row][s.cur.col+1] = Cell{Width: 0, SGR: s.pen}
	}
	if s.cur.col+w >= s.cols {
		s.cur.col = s.cols - 1
		if s.autowrap {
			s.pendingWrap = true
		}
		return
	}
	s.cur.col += w
}

func (s *screen) inBounds(row, col int) bool {
	return row >= 0 && row < s.rows && col >= 0 && col < s.cols
}

// splitWideAt repairs the halves of any wide rune that overlaps (row, col)
// before that position is overwritten, so no dangling halves remain.
func (s *screen) splitWideAt(row, col int) {
	if !s.inBounds(row, col) {
		return
	}
	grid := s.grid()
	old := grid[row][col]
	if old.Width == 0 && col > 0 && grid[row][col-1].Width == 2 {
		grid[row][col-1] = Cell{Rune: ' ', Width: 1, SGR: grid[row][col-1].SGR}
	}
	if old.Width == 2 && col+1 < s.cols && grid[row][col+1].Width == 0 {
		grid[row][col+1] = Cell{Rune: ' ', Width: 1, SGR: grid[row][col+1].SGR}
	}
}

// setCell stores a cell, splitting any wide rune it partially overwrites.
func (s *screen) setCell(row, col int, c Cell) {
	if !s.inBounds(row, col) {
		return
	}
	s.splitWideAt(row, col)
	s.grid()[row][col] = c
}

func (s *screen) execC0(b byte) {
	switch b {
	case '\b':
		s.moveCursor(s.cur.row, s.cur.col-1)
	case '\t':
		s.horizontalTab()
	case '\n', 0x0b, 0x0c:
		s.linefeedNoCR()
		if s.lnm {
			s.cur.col = 0
		}
	case '\r':
		s.cur.col = 0
		s.pendingWrap = false
	case 0x0e: // SO
		s.shiftOut = true
	case 0x0f: // SI
		s.shiftOut = false
	default:
		// BEL, NUL, ENQ...: no shadow effect.
	}
}

func (s *screen) horizontalTab() {
	col := s.cur.col + 1
	for col < s.cols-1 && !s.tabs[col] {
		col++
	}
	s.cur.col = clampInt(col, 0, s.cols-1)
	s.pendingWrap = false
}

// linefeedNoCR moves the cursor down one line, scrolling when it sits on the
// bottom of the scroll region.
func (s *screen) linefeedNoCR() {
	s.pendingWrap = false
	if s.cur.row == s.bot {
		s.scrollUp(1)
		return
	}
	if s.cur.row < s.rows-1 {
		s.cur.row++
	}
}

func (s *screen) reverseIndex() {
	s.pendingWrap = false
	if s.cur.row == s.top {
		s.scrollDown(1)
		return
	}
	if s.cur.row > 0 {
		s.cur.row--
	}
}

func (s *screen) scrollUp(n int) {
	s.scrollRegion(n, true)
}

func (s *screen) scrollDown(n int) {
	s.scrollRegion(n, false)
}

func (s *screen) scrollRegion(n int, up bool) {
	if n <= 0 {
		return
	}
	height := s.bot - s.top + 1
	if n > height {
		n = height
	}
	grid := s.grid()
	if up {
		copy(grid[s.top:s.bot+1], grid[s.top+n:s.bot+1])
		for r := s.bot - n + 1; r <= s.bot; r++ {
			grid[r] = s.blankRow()
		}
	} else {
		copy(grid[s.top+n:s.bot+1], grid[s.top:s.bot+1-n])
		for r := s.top; r < s.top+n; r++ {
			grid[r] = s.blankRow()
		}
	}
	s.fb.Scrolled = true
}

func (s *screen) blankRow() []Cell {
	row := make([]Cell, s.cols)
	blank := s.blankCell()
	for c := range row {
		row[c] = blank
	}
	return row
}

// blankCell is the erase cell: background color erase keeps the pen bg.
func (s *screen) blankCell() Cell {
	return Cell{Rune: 0, Width: 1, SGR: SGR{Bg: s.pen.Bg}}
}

// moveCursor moves to an absolute position, clamped, clearing deferred wrap.
func (s *screen) moveCursor(row, col int) {
	s.cur.row = clampInt(row, 0, s.rows-1)
	s.cur.col = clampInt(col, 0, s.cols-1)
	s.pendingWrap = false
}

// cursorTo implements CUP/HVP honoring origin mode.
func (s *screen) cursorTo(row, col int) {
	if s.originMode {
		row += s.top
		row = clampInt(row, s.top, s.bot)
	}
	s.moveCursor(row, col)
}

func (s *screen) insertCells(row, col, n int) {
	if row < 0 || row >= s.rows || col < 0 || col >= s.cols || n <= 0 {
		return
	}
	grid := s.grid()
	line := grid[row]
	if n > s.cols-col {
		n = s.cols - col
	}
	copy(line[col+n:], line[col:s.cols-n])
	blank := s.blankCell()
	for c := col; c < col+n; c++ {
		line[c] = blank
	}
}

func (s *screen) deleteCells(row, col, n int) {
	if row < 0 || row >= s.rows || col < 0 || col >= s.cols || n <= 0 {
		return
	}
	grid := s.grid()
	line := grid[row]
	if n > s.cols-col {
		n = s.cols - col
	}
	copy(line[col:], line[col+n:])
	blank := s.blankCell()
	for c := s.cols - n; c < s.cols; c++ {
		line[c] = blank
	}
}

func (s *screen) eraseCells(row, col, n int) {
	blank := s.blankCell()
	for c := col; c < col+n && c < s.cols; c++ {
		s.setCell(row, c, blank)
	}
}

func (s *screen) escDispatch(final byte, inter []byte) {
	if len(inter) > 0 {
		s.escDispatchInter(final, inter)
		return
	}
	if handler, ok := escHandlers[final]; ok {
		handler(s)
		return
	}
	s.fb.Unknown++
}

var escHandlers = map[byte]func(*screen){
	'7':  (*screen).saveCursor,
	'8':  (*screen).restoreCursor,
	'D':  (*screen).linefeedNoCR,
	'E':  func(s *screen) { s.linefeedNoCR(); s.cur.col = 0 },
	'M':  (*screen).reverseIndex,
	'H':  func(s *screen) { s.tabs[clampInt(s.cur.col, 0, s.cols-1)] = true },
	'c':  (*screen).fullReset,
	'=':  func(*screen) {},
	'>':  func(*screen) {},
	'\\': func(*screen) {}, // stray ST
	'Z':  func(*screen) {}, // DECID; real terminal answers, shadow ignores
}

func (s *screen) escDispatchInter(final byte, inter []byte) {
	switch inter[0] {
	case '(':
		s.g0Special = final == '0'
	case ')':
		s.g1Special = final == '0'
	case '*', '+':
		// G2/G3 designation: accepted, unused.
	case '#':
		s.decAlign(final)
	default:
		s.fb.Unknown++
	}
}

func (s *screen) decAlign(final byte) {
	if final != '8' {
		s.fb.Unknown++
		return
	}
	grid := s.grid()
	for r := range grid {
		for c := range grid[r] {
			grid[r][c] = Cell{Rune: 'E', Width: 1}
		}
	}
}

func (s *screen) saveCursor() {
	saved := savedCursor{
		cur:       s.cur,
		pen:       s.pen,
		origin:    s.originMode,
		valid:     true,
		g0Special: s.g0Special,
		shiftOut:  s.shiftOut,
	}
	if s.altActive {
		s.savedAlt = saved
	} else {
		s.savedMain = saved
	}
}

func (s *screen) restoreCursor() {
	saved := s.savedMain
	if s.altActive {
		saved = s.savedAlt
	}
	if !saved.valid {
		s.moveCursor(0, 0)
		s.pen = SGR{}
		return
	}
	s.cur = saved.cur
	s.pen = saved.pen
	s.originMode = saved.origin
	s.g0Special = saved.g0Special
	s.shiftOut = saved.shiftOut
	s.moveCursor(s.cur.row, s.cur.col)
}

func (s *screen) fullReset() {
	cols, rows := s.cols, s.rows
	*s = *newScreen(cols, rows)
	s.fb.ScreenSwitch = true
}

func (s *screen) enterAlt(clear bool) {
	if !s.altActive {
		s.altActive = true
		s.fb.ScreenSwitch = true
	}
	if clear {
		s.alt = resizeGrid(nil, s.cols, s.rows)
	}
}

func (s *screen) exitAlt() {
	if s.altActive {
		s.altActive = false
		s.fb.ScreenSwitch = true
	}
}

// mapSpecialGraphics translates the DEC special graphics charset range used
// for line drawing (ESC ( 0) into their Unicode equivalents.
func mapSpecialGraphics(r rune) rune {
	if r < 0x60 || r > 0x7e {
		return r
	}
	return specialGraphics[r-0x60]
}

var specialGraphics = [31]rune{
	'◆', '▒', '␉', '␌', '␍', '␊', '°', '±', '␤', '␋',
	'┘', '┐', '┌', '└', '┼', '⎺', '⎻', '─', '⎼', '⎽',
	'├', '┤', '┴', '┬', '│', '≤', '≥', 'π', '≠', '£', '·',
}
