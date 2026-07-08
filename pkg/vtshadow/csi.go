package vtshadow

// csiSeq carries one parsed CSI sequence to its handler.
type csiSeq struct {
	params  []int
	private byte
}

// param returns the i-th parameter, or def when absent or zero.
func (c csiSeq) param(i, def int) int {
	if i >= len(c.params) || c.params[i] == 0 {
		return def
	}
	return c.params[i]
}

// rawParam returns the i-th parameter with zero preserved.
func (c csiSeq) rawParam(i, def int) int {
	if i >= len(c.params) {
		return def
	}
	return c.params[i]
}

func (s *screen) csiDispatch(final byte, private byte, params []int, inter []byte) {
	seq := csiSeq{params: params, private: private}
	if len(inter) > 0 {
		s.csiDispatchInter(final, inter)
		return
	}
	if private != 0 {
		s.csiDispatchPrivate(final, seq)
		return
	}
	if handler, ok := csiHandlers[final]; ok {
		handler(s, seq)
		return
	}
	s.fb.Unknown++
}

// csiDispatchInter covers sequences with intermediate bytes; only cursor
// style (CSI Sp q) is recognized, everything else counts as unknown.
func (s *screen) csiDispatchInter(final byte, inter []byte) {
	if inter[0] == ' ' && final == 'q' {
		return
	}
	s.fb.Unknown++
}

// csiDispatchPrivate covers sequences opened with a ? > = < marker: DEC
// private modes plus device/keyboard-protocol chatter that a shadow ignores.
func (s *screen) csiDispatchPrivate(final byte, seq csiSeq) {
	switch final {
	case 'h':
		csiSetMode(s, seq)
	case 'l':
		csiResetMode(s, seq)
	case 'J':
		csiED(s, seq) // DECSED: treat as plain erase
	case 'K':
		csiEL(s, seq)
	case 'c', 'n', 'm', 'p', 'q', 'r', 's', 't', 'u', 'S':
		// DA2, DSR, modifyOtherKeys, kitty keyboard protocol, XTRESTORE...
		// answered (if at all) by the real terminal; no shadow effect.
	default:
		s.fb.Unknown++
	}
}

var csiHandlers = map[byte]func(*screen, csiSeq){
	'A': func(s *screen, q csiSeq) { s.moveCursor(s.cur.row-q.param(0, 1), s.cur.col) },
	'B': func(s *screen, q csiSeq) { s.moveCursor(s.cur.row+q.param(0, 1), s.cur.col) },
	'C': func(s *screen, q csiSeq) { s.moveCursor(s.cur.row, s.cur.col+q.param(0, 1)) },
	'D': func(s *screen, q csiSeq) { s.moveCursor(s.cur.row, s.cur.col-q.param(0, 1)) },
	'E': func(s *screen, q csiSeq) { s.moveCursor(s.cur.row+q.param(0, 1), 0) },
	'F': func(s *screen, q csiSeq) { s.moveCursor(s.cur.row-q.param(0, 1), 0) },
	'G': func(s *screen, q csiSeq) { s.moveCursor(s.cur.row, q.param(0, 1)-1) },
	'`': func(s *screen, q csiSeq) { s.moveCursor(s.cur.row, q.param(0, 1)-1) },
	'a': func(s *screen, q csiSeq) { s.moveCursor(s.cur.row, s.cur.col+q.param(0, 1)) },
	'd': func(s *screen, q csiSeq) { s.moveCursor(q.param(0, 1)-1, s.cur.col) },
	'e': func(s *screen, q csiSeq) { s.moveCursor(s.cur.row+q.param(0, 1), s.cur.col) },
	'H': csiCUP,
	'f': csiCUP,
	'J': csiED,
	'K': csiEL,
	'L': func(s *screen, q csiSeq) { s.insertLines(q.param(0, 1)) },
	'M': func(s *screen, q csiSeq) { s.deleteLines(q.param(0, 1)) },
	'P': func(s *screen, q csiSeq) { s.deleteCells(s.cur.row, s.cur.col, q.param(0, 1)); s.pendingWrap = false },
	'@': func(s *screen, q csiSeq) { s.insertCells(s.cur.row, s.cur.col, q.param(0, 1)); s.pendingWrap = false },
	'X': func(s *screen, q csiSeq) { s.eraseCells(s.cur.row, s.cur.col, q.param(0, 1)); s.pendingWrap = false },
	'S': func(s *screen, q csiSeq) { s.scrollUp(q.param(0, 1)) },
	'T': func(s *screen, q csiSeq) { s.scrollDown(q.param(0, 1)) },
	'm': csiSGR,
	'r': csiDECSTBM,
	'h': csiSetMode,
	'l': csiResetMode,
	'g': csiTBC,
	's': func(s *screen, _ csiSeq) { s.saveCursor() },
	'u': func(s *screen, _ csiSeq) { s.restoreCursor() },
	'c': func(*screen, csiSeq) {}, // DA: real terminal answers
	'n': func(*screen, csiSeq) {}, // DSR: real terminal answers
	't': func(*screen, csiSeq) {}, // window ops
	'q': func(*screen, csiSeq) {}, // DECLL
	'Z': func(s *screen, q csiSeq) { s.backTab(q.param(0, 1)) },
	'b': func(*screen, csiSeq) {}, // REP: repeat, needs last glyph; rare
}

func csiCUP(s *screen, q csiSeq) {
	s.cursorTo(q.param(0, 1)-1, q.param(1, 1)-1)
}

func (s *screen) backTab(n int) {
	col := s.cur.col
	for ; n > 0 && col > 0; n-- {
		col--
		for col > 0 && !s.tabs[col] {
			col--
		}
	}
	s.moveCursor(s.cur.row, col)
}

func csiED(s *screen, q csiSeq) {
	switch q.rawParam(0, 0) {
	case 0:
		s.eraseCells(s.cur.row, s.cur.col, s.cols-s.cur.col)
		for r := s.cur.row + 1; r < s.rows; r++ {
			s.eraseCells(r, 0, s.cols)
		}
	case 1:
		for r := 0; r < s.cur.row; r++ {
			s.eraseCells(r, 0, s.cols)
		}
		s.eraseCells(s.cur.row, 0, s.cur.col+1)
	case 2, 3:
		for r := 0; r < s.rows; r++ {
			s.eraseCells(r, 0, s.cols)
		}
	}
	s.pendingWrap = false
}

func csiEL(s *screen, q csiSeq) {
	switch q.rawParam(0, 0) {
	case 0:
		s.eraseCells(s.cur.row, s.cur.col, s.cols-s.cur.col)
	case 1:
		s.eraseCells(s.cur.row, 0, s.cur.col+1)
	case 2:
		s.eraseCells(s.cur.row, 0, s.cols)
	}
	s.pendingWrap = false
}

// insertLines/deleteLines act inside the scroll region at the cursor row.
func (s *screen) insertLines(n int) {
	if s.cur.row < s.top || s.cur.row > s.bot {
		return
	}
	savedTop := s.top
	s.top = s.cur.row
	s.scrollDown(n)
	s.top = savedTop
	s.cur.col = 0
	s.pendingWrap = false
}

func (s *screen) deleteLines(n int) {
	if s.cur.row < s.top || s.cur.row > s.bot {
		return
	}
	savedTop := s.top
	s.top = s.cur.row
	s.scrollUp(n)
	s.top = savedTop
	s.cur.col = 0
	s.pendingWrap = false
}

func csiDECSTBM(s *screen, q csiSeq) {
	top := q.param(0, 1) - 1
	bot := q.param(1, s.rows) - 1
	if top < 0 || bot >= s.rows || top >= bot {
		top, bot = 0, s.rows-1
	}
	s.top, s.bot = top, bot
	s.cursorTo(0, 0)
}

func csiTBC(s *screen, q csiSeq) {
	switch q.rawParam(0, 0) {
	case 0:
		s.tabs[clampInt(s.cur.col, 0, s.cols-1)] = false
	case 3:
		for c := range s.tabs {
			s.tabs[c] = false
		}
	}
}

func csiSetMode(s *screen, q csiSeq) {
	s.applyModes(q, true)
}

func csiResetMode(s *screen, q csiSeq) {
	s.applyModes(q, false)
}

func (s *screen) applyModes(q csiSeq, set bool) {
	if len(q.params) == 0 {
		return
	}
	for _, mode := range q.params {
		if q.private == '?' {
			s.applyPrivateMode(mode, set)
		} else {
			s.applyANSIMode(mode, set)
		}
	}
}

func (s *screen) applyANSIMode(mode int, set bool) {
	switch mode {
	case 4:
		s.insertMode = set
	case 20:
		s.lnm = set
	default:
		// Other ANSI modes have no shadow-visible effect.
	}
}

func (s *screen) applyPrivateMode(mode int, set bool) {
	switch mode {
	case 6:
		s.originMode = set
		s.cursorTo(0, 0)
	case 7:
		s.autowrap = set
		s.pendingWrap = false
	case 47:
		s.switchAlt(set, false)
	case 1047:
		s.switchAlt(set, set)
	case 1048:
		s.xtermSaveRestore(set)
	case 1049:
		s.xtermSaveRestore(set)
		s.switchAlt(set, set)
	default:
		s.privateModeNoEffect(mode)
	}
}

func (s *screen) switchAlt(enter, clear bool) {
	if enter {
		s.enterAlt(clear)
		return
	}
	s.exitAlt()
}

func (s *screen) xtermSaveRestore(save bool) {
	if save {
		s.xtermSaved = savedCursor{cur: s.cur, pen: s.pen, origin: s.originMode, valid: true}
		return
	}
	if s.xtermSaved.valid {
		s.cur = s.xtermSaved.cur
		s.pen = s.xtermSaved.pen
		s.originMode = s.xtermSaved.origin
		s.moveCursor(s.cur.row, s.cur.col)
	}
}

// privateModeNoEffect lists private modes that are recognized (so they do not
// count as unknown) but have no shadow-visible effect.
func (s *screen) privateModeNoEffect(mode int) {
	switch mode {
	case 1, 5, 12, 25, 1000, 1002, 1003, 1004, 1005, 1006, 1015, 1016, 2004, 2026, 2027, 2048:
		// cursor keys, reverse video, blink, visibility, mouse, focus,
		// bracketed paste, synchronized output...
	default:
		s.fb.Unknown++
	}
}

func csiSGR(s *screen, q csiSeq) {
	params := q.params
	if len(params) == 0 {
		params = []int{0}
	}
	for i := 0; i < len(params); {
		i = s.applySGRParam(params, i)
	}
}

// applySGRParam applies the SGR parameter at index i and returns the index of
// the next parameter (extended colors consume several).
func (s *screen) applySGRParam(params []int, i int) int {
	p := params[i]
	if p == 38 || p == 48 {
		return s.applyExtendedColor(params, i)
	}
	if !s.applyBasicColor(p) {
		s.applySimpleSGR(p)
	}
	return i + 1
}

// applyBasicColor handles the classic 16-color SGR ranges; it reports whether
// p was one of them.
func (s *screen) applyBasicColor(p int) bool {
	switch {
	case p >= 30 && p <= 37:
		s.pen.Fg = Color{Mode: ColorIndexed, R: uint8(p - 30)}
	case p >= 40 && p <= 47:
		s.pen.Bg = Color{Mode: ColorIndexed, R: uint8(p - 40)}
	case p >= 90 && p <= 97:
		s.pen.Fg = Color{Mode: ColorIndexed, R: uint8(p - 90 + 8)}
	case p >= 100 && p <= 107:
		s.pen.Bg = Color{Mode: ColorIndexed, R: uint8(p - 100 + 8)}
	default:
		return false
	}
	return true
}

var sgrSetBits = map[int]uint16{
	1: AttrBold, 2: AttrFaint, 3: AttrItalic, 4: AttrUnderline,
	5: AttrBlink, 6: AttrBlink, 7: AttrInverse, 8: AttrHidden, 9: AttrStrike,
}

var sgrClearBits = map[int]uint16{
	22: AttrBold | AttrFaint, 23: AttrItalic, 24: AttrUnderline,
	25: AttrBlink, 27: AttrInverse, 28: AttrHidden, 29: AttrStrike,
}

func (s *screen) applySimpleSGR(p int) {
	switch p {
	case 0:
		s.pen = SGR{}
	case 39:
		s.pen.Fg = Color{}
	case 49:
		s.pen.Bg = Color{}
	default:
		if bit, ok := sgrSetBits[p]; ok {
			s.pen.Attr |= bit
			return
		}
		if bits, ok := sgrClearBits[p]; ok {
			s.pen.Attr &^= bits
		}
	}
}

// applyExtendedColor parses 38/48;5;idx and 38/48;2;r;g;b forms.
func (s *screen) applyExtendedColor(params []int, i int) int {
	isFg := params[i] == 38
	if i+1 >= len(params) {
		return i + 1
	}
	var c Color
	var next int
	switch params[i+1] {
	case 5:
		if i+2 >= len(params) {
			return len(params)
		}
		c = Color{Mode: ColorIndexed, R: uint8(clampInt(params[i+2], 0, 255))}
		next = i + 3
	case 2:
		if i+4 >= len(params) {
			return len(params)
		}
		c = Color{
			Mode: ColorRGB,
			R:    uint8(clampInt(params[i+2], 0, 255)),
			G:    uint8(clampInt(params[i+3], 0, 255)),
			B:    uint8(clampInt(params[i+4], 0, 255)),
		}
		next = i + 5
	default:
		return i + 2
	}
	if isFg {
		s.pen.Fg = c
	} else {
		s.pen.Bg = c
	}
	return next
}
