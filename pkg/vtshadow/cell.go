package vtshadow

import (
	"strconv"
	"unicode"

	"golang.org/x/text/width"
)

// Attr bit flags for SGR attributes tracked by the shadow terminal.
const (
	AttrBold uint16 = 1 << iota
	AttrFaint
	AttrItalic
	AttrUnderline
	AttrBlink
	AttrInverse
	AttrHidden
	AttrStrike
)

// Color modes.
const (
	ColorDefault uint8 = iota
	ColorIndexed
	ColorRGB
)

// Color is a terminal color in default/indexed/RGB form. For ColorIndexed the
// index lives in R.
type Color struct {
	Mode uint8
	R    uint8
	G    uint8
	B    uint8
}

// SGR is the graphic rendition state of a cell or the current pen.
type SGR struct {
	Fg   Color
	Bg   Color
	Attr uint16
}

// Cell is one terminal grid cell. A zero Rune renders as blank. Width 0 marks
// the continuation half of a wide rune stored in the cell to its left.
type Cell struct {
	Rune  rune
	Width uint8
	SGR   SGR
}

// Blank reports whether the cell displays as an empty space glyph.
func (c Cell) Blank() bool {
	return c.Rune == 0 || c.Rune == ' '
}

// DisplayRune returns the rune the cell shows, mapping zero to space.
func (c Cell) DisplayRune() rune {
	if c.Rune == 0 {
		return ' '
	}
	return c.Rune
}

// AppendSGR appends a full reset-and-set SGR sequence for s to dst.
func AppendSGR(dst []byte, s SGR) []byte {
	dst = append(dst, "\x1b[0"...)
	dst = appendAttrParams(dst, s.Attr)
	dst = appendColorParams(dst, s.Fg, 38, 30, 90)
	dst = appendColorParams(dst, s.Bg, 48, 40, 100)
	return append(dst, 'm')
}

var attrParams = []struct {
	bit   uint16
	param string
}{
	{AttrBold, "1"},
	{AttrFaint, "2"},
	{AttrItalic, "3"},
	{AttrUnderline, "4"},
	{AttrBlink, "5"},
	{AttrInverse, "7"},
	{AttrHidden, "8"},
	{AttrStrike, "9"},
}

func appendAttrParams(dst []byte, attr uint16) []byte {
	for _, ap := range attrParams {
		if attr&ap.bit != 0 {
			dst = append(dst, ';')
			dst = append(dst, ap.param...)
		}
	}
	return dst
}

// appendColorParams appends the color selection params for one plane.
// extended is 38/48, base is 30/40, bright is 90/100.
func appendColorParams(dst []byte, c Color, extended, base, bright int) []byte {
	switch c.Mode {
	case ColorIndexed:
		return appendIndexedColor(dst, c, extended, base, bright)
	case ColorRGB:
		dst = append(dst, ';')
		dst = strconv.AppendInt(dst, int64(extended), 10)
		dst = append(dst, ";2;"...)
		dst = strconv.AppendInt(dst, int64(c.R), 10)
		dst = append(dst, ';')
		dst = strconv.AppendInt(dst, int64(c.G), 10)
		dst = append(dst, ';')
		dst = strconv.AppendInt(dst, int64(c.B), 10)
		return dst
	default:
		return dst
	}
}

func appendIndexedColor(dst []byte, c Color, extended, base, bright int) []byte {
	idx := int(c.R)
	dst = append(dst, ';')
	switch {
	case idx < 8:
		dst = strconv.AppendInt(dst, int64(base+idx), 10)
	case idx < 16:
		dst = strconv.AppendInt(dst, int64(bright+idx-8), 10)
	default:
		dst = strconv.AppendInt(dst, int64(extended), 10)
		dst = append(dst, ";5;"...)
		dst = strconv.AppendInt(dst, int64(idx), 10)
	}
	return dst
}

// CellWidth returns the number of columns r occupies: 0 for combining and
// zero-width runes, 2 for East Asian wide/fullwidth runes and common emoji
// blocks, 1 otherwise.
func CellWidth(r rune) int {
	if r == 0 || unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) {
		return 0
	}
	if r < 0x1100 {
		return 1
	}
	if isEmojiWide(r) {
		return 2
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	default:
		return 1
	}
}

// isEmojiWide covers emoji blocks terminals commonly render two columns wide
// but that x/text/width classifies as neutral.
func isEmojiWide(r rune) bool {
	switch {
	case r >= 0x1F300 && r <= 0x1FAFF: // pictographs, emoticons, symbols
		return true
	case r >= 0x1F1E6 && r <= 0x1F1FF: // regional indicators
		return true
	case r >= 0x2614 && r <= 0x2615, r >= 0x26A0 && r <= 0x26FF:
		return true
	default:
		return false
	}
}
