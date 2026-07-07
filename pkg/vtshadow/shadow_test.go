package vtshadow

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func feedString(t *testing.T, s *Shadow, in string) Feedback {
	t.Helper()
	return s.Feed([]byte(in))
}

func TestPrintAndCursorAdvance(t *testing.T) {
	s := New(10, 4)
	feedString(t, s, "abc")

	row, col := s.Cursor()
	require.Equal(t, 0, row)
	require.Equal(t, 3, col)
	require.Equal(t, "abc", s.RowString(0))
	require.Equal(t, 'b', s.Cell(0, 1).Rune)
}

func TestCRLFAndBackspace(t *testing.T) {
	s := New(10, 4)
	feedString(t, s, "ab\r\ncd\bX")

	require.Equal(t, "ab", s.RowString(0))
	require.Equal(t, "cX", s.RowString(1))
	row, col := s.Cursor()
	require.Equal(t, 1, row)
	require.Equal(t, 2, col)
}

func TestCUPAndRelativeMoves(t *testing.T) {
	s := New(20, 5)
	feedString(t, s, "\x1b[3;5Hx")
	row, col := s.Cursor()
	require.Equal(t, 2, row)
	require.Equal(t, 5, col)
	require.Equal(t, 'x', s.Cell(2, 4).Rune)

	feedString(t, s, "\x1b[2A\x1b[3D")
	row, col = s.Cursor()
	require.Equal(t, 0, row)
	require.Equal(t, 2, col)
}

func TestDeferredWrap(t *testing.T) {
	s := New(5, 3)
	feedString(t, s, "abcde")
	row, col := s.Cursor()
	require.Equal(t, 0, row)
	require.Equal(t, 4, col)
	require.True(t, s.PendingWrap())

	feedString(t, s, "f")
	row, col = s.Cursor()
	require.Equal(t, 1, row)
	require.Equal(t, 1, col)
	require.Equal(t, "f", s.RowString(1))
}

func TestScrollOnLinefeedAtBottom(t *testing.T) {
	s := New(10, 3)
	feedString(t, s, "one\r\ntwo\r\nthree")
	fb := feedString(t, s, "\r\nfour")

	require.True(t, fb.Scrolled)
	require.Equal(t, "two", s.RowString(0))
	require.Equal(t, "three", s.RowString(1))
	require.Equal(t, "four", s.RowString(2))
}

func TestScrollRegion(t *testing.T) {
	s := New(10, 5)
	feedString(t, s, "aaa\r\nbbb\r\nccc\r\nddd\r\neee")
	// Region rows 2-4 (1-based), cursor to region bottom, then LF scrolls
	// only inside the region.
	feedString(t, s, "\x1b[2;4r\x1b[4;1H\n")

	require.Equal(t, "aaa", s.RowString(0))
	require.Equal(t, "ccc", s.RowString(1))
	require.Equal(t, "ddd", s.RowString(2))
	require.Equal(t, "", s.RowString(3))
	require.Equal(t, "eee", s.RowString(4))
}

func TestEraseLineAndDisplay(t *testing.T) {
	s := New(10, 3)
	feedString(t, s, "abcdef\x1b[1;3H\x1b[K")
	require.Equal(t, "ab", s.RowString(0))

	feedString(t, s, "\x1b[2J")
	require.Equal(t, "", s.RowString(0))
}

func TestInsertDeleteChars(t *testing.T) {
	s := New(10, 2)
	feedString(t, s, "abcdef\x1b[1;3H\x1b[2@")
	require.Equal(t, "ab  cdef", s.RowString(0))

	feedString(t, s, "\x1b[2P")
	require.Equal(t, "abcdef", s.RowString(0))
}

func TestSGRTracking(t *testing.T) {
	s := New(10, 2)
	feedString(t, s, "\x1b[1;31mA\x1b[0mB")

	a := s.Cell(0, 0)
	require.Equal(t, AttrBold, a.SGR.Attr&AttrBold)
	require.Equal(t, Color{Mode: ColorIndexed, R: 1}, a.SGR.Fg)

	b := s.Cell(0, 1)
	require.Equal(t, SGR{}, b.SGR)
}

func TestSGRExtendedColors(t *testing.T) {
	s := New(10, 2)
	feedString(t, s, "\x1b[38;5;196m\x1b[48;2;1;2;3mZ")

	z := s.Cell(0, 0)
	require.Equal(t, Color{Mode: ColorIndexed, R: 196}, z.SGR.Fg)
	require.Equal(t, Color{Mode: ColorRGB, R: 1, G: 2, B: 3}, z.SGR.Bg)
}

func TestAltScreenSwitch(t *testing.T) {
	s := New(10, 3)
	feedString(t, s, "main")
	fb := feedString(t, s, "\x1b[?1049h")
	require.True(t, fb.ScreenSwitch)
	require.True(t, s.AltScreen())
	require.Equal(t, "", s.RowString(0))

	feedString(t, s, "alt")
	fb = feedString(t, s, "\x1b[?1049l")
	require.True(t, fb.ScreenSwitch)
	require.False(t, s.AltScreen())
	require.Equal(t, "main", s.RowString(0))
}

func TestWideRunePlacement(t *testing.T) {
	s := New(10, 2)
	feedString(t, s, "中a")

	cell := s.Cell(0, 0)
	require.Equal(t, '中', cell.Rune)
	require.Equal(t, uint8(2), cell.Width)
	require.Equal(t, uint8(0), s.Cell(0, 1).Width)
	require.Equal(t, 'a', s.Cell(0, 2).Rune)

	_, col := s.Cursor()
	require.Equal(t, 3, col)
}

func TestUTF8AcrossChunks(t *testing.T) {
	s := New(10, 2)
	raw := []byte("中")
	s.Feed(raw[:1])
	require.False(t, s.AtGround())
	s.Feed(raw[1:])
	require.True(t, s.AtGround())
	require.Equal(t, '中', s.Cell(0, 0).Rune)
}

func TestOSCConsumed(t *testing.T) {
	s := New(10, 2)
	feedString(t, s, "\x1b]0;title\x07a")
	require.Equal(t, "a", s.RowString(0))

	feedString(t, s, "\x1b]8;;http://x\x1b\\b")
	require.Equal(t, "ab", s.RowString(0))
}

func TestSequenceSplitAcrossChunks(t *testing.T) {
	s := New(20, 3)
	s.Feed([]byte("\x1b[3"))
	require.False(t, s.AtGround())
	s.Feed([]byte(";7Hx"))
	require.True(t, s.AtGround())
	require.Equal(t, 'x', s.Cell(2, 6).Rune)
}

func TestUnknownSequenceCounted(t *testing.T) {
	s := New(10, 2)
	fb := feedString(t, s, "\x1b[1;2y") // DECTST, unhandled
	require.Equal(t, 1, fb.Unknown)

	fb = feedString(t, s, "\x1b[?2004h") // bracketed paste: known no-op
	require.Equal(t, 0, fb.Unknown)
}

func TestSaveRestoreCursor(t *testing.T) {
	s := New(20, 5)
	feedString(t, s, "\x1b[2;3H\x1b7\x1b[4;1Hzz\x1b8")
	row, col := s.Cursor()
	require.Equal(t, 1, row)
	require.Equal(t, 2, col)
}

func TestOriginMode(t *testing.T) {
	s := New(20, 6)
	feedString(t, s, "\x1b[3;5r\x1b[?6h\x1b[1;1Hq")
	require.Equal(t, 'q', s.Cell(2, 0).Rune)
	require.True(t, s.OriginMode())
	require.Equal(t, 2, s.ScrollTop())
}

func TestResizePreservesContent(t *testing.T) {
	s := New(10, 3)
	feedString(t, s, "hello")
	s.Resize(20, 5)
	require.Equal(t, "hello", s.RowString(0))
	cols, rows := s.Size()
	require.Equal(t, 20, cols)
	require.Equal(t, 5, rows)
}

func TestAppendSGRRoundTrip(t *testing.T) {
	src := SGR{
		Fg:   Color{Mode: ColorIndexed, R: 12},
		Bg:   Color{Mode: ColorRGB, R: 10, G: 20, B: 30},
		Attr: AttrBold | AttrUnderline,
	}
	seq := AppendSGR(nil, src)

	s := New(4, 2)
	s.Feed(seq)
	s.Feed([]byte("x"))
	require.Equal(t, src, s.Cell(0, 0).SGR)
}

func TestSpecialGraphicsMapping(t *testing.T) {
	s := New(10, 2)
	feedString(t, s, "\x1b(0qx\x1b(Bq")
	require.Equal(t, '─', s.Cell(0, 0).Rune)
	require.Equal(t, '│', s.Cell(0, 1).Rune)
	require.Equal(t, 'q', s.Cell(0, 2).Rune)
}

func TestTabStops(t *testing.T) {
	s := New(20, 2)
	feedString(t, s, "\ta")
	require.Equal(t, 'a', s.Cell(0, 8).Rune)
}

func TestDECAWMOff(t *testing.T) {
	s := New(5, 2)
	feedString(t, s, "\x1b[?7labcdefg")
	// Autowrap off: extra glyphs overwrite the last column.
	require.Equal(t, "abcdg", s.RowString(0))
	_, col := s.Cursor()
	require.Equal(t, 4, col)
	require.False(t, s.PendingWrap())
}
