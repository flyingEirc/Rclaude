package vtshadow

import "unicode/utf8"

// pstate is a parser state in the VT500-style escape sequence state machine
// (after Paul Williams' parser), reduced to what a shadow tracker needs.
type pstate uint8

const (
	stGround pstate = iota
	stEscape
	stEscInter
	stCSIParam
	stCSIInter
	stCSIIgnore
	stOSC
	stOSCEsc
	stString // DCS / SOS / PM / APC payloads, consumed and ignored
	stStringEsc
)

const (
	maxCSIParams   = 32
	maxCSIParamVal = 65535
)

type parser struct {
	state   pstate
	params  []int
	inter   []byte
	private byte

	utf8buf  [utf8.UTFMax]byte
	utf8len  int
	utf8need int
}

func newParser() *parser {
	return &parser{
		params: make([]int, 0, maxCSIParams),
		inter:  make([]byte, 0, 4),
	}
}

// atGround reports whether the parser sits between complete sequences and
// runes, i.e. a safe point to inject locally generated output.
func (p *parser) atGround() bool {
	return p.state == stGround && p.utf8len == 0
}

// feed consumes one byte and drives scr. It is the single entry point used by
// Shadow.Feed.
func (p *parser) feed(scr *screen, b byte) {
	if p.utf8len > 0 && p.state == stGround {
		p.feedUTF8Continuation(scr, b)
		return
	}
	switch {
	case b == 0x1b:
		p.feedEscByte(scr)
	case b == 0x18 || b == 0x1a: // CAN / SUB abort any sequence
		p.state = stGround
	case b < 0x20:
		p.feedC0(scr, b)
	default:
		p.feedData(scr, b)
	}
}

func (p *parser) feedEscByte(scr *screen) {
	switch p.state {
	case stOSC:
		p.state = stOSCEsc
	case stString:
		p.state = stStringEsc
	default:
		p.resetSequence()
		p.state = stEscape
		_ = scr
	}
}

// feedC0 handles C0 controls, which execute even in the middle of escape and
// CSI sequences but are swallowed inside OSC/DCS strings.
func (p *parser) feedC0(scr *screen, b byte) {
	switch p.state {
	case stOSC, stOSCEsc:
		if b == 0x07 { // BEL terminates OSC
			p.state = stGround
		}
	case stString, stStringEsc:
		// ignored
	default:
		scr.execC0(b)
	}
}

// dataHandlers dispatches printable/parameter bytes by parser state.
var dataHandlers = [...]func(*parser, *screen, byte){
	stGround:    (*parser).feedGround,
	stEscape:    (*parser).feedEscape,
	stEscInter:  (*parser).feedEscInter,
	stCSIParam:  (*parser).feedCSIParam,
	stCSIInter:  (*parser).feedCSIInter,
	stCSIIgnore: (*parser).feedCSIIgnore,
	stOSC:       (*parser).feedStringPayload,
	stOSCEsc:    (*parser).feedOSCEsc,
	stString:    (*parser).feedStringPayload,
	stStringEsc: (*parser).feedStrEsc,
}

func (p *parser) feedData(scr *screen, b byte) {
	dataHandlers[p.state](p, scr, b)
}

func (p *parser) feedCSIIgnore(_ *screen, b byte) {
	if b >= 0x40 && b <= 0x7e {
		p.state = stGround
	}
}

// feedStringPayload swallows OSC/DCS payload bytes.
func (p *parser) feedStringPayload(*screen, byte) {}

func (p *parser) feedOSCEsc(scr *screen, b byte) {
	p.finishStringEsc(scr, b, stOSC)
}

func (p *parser) feedStrEsc(scr *screen, b byte) {
	p.finishStringEsc(scr, b, stString)
}

// finishStringEsc resolves an ESC seen inside an OSC/DCS string: ESC \ (ST)
// ends the string, anything else re-enters the string state.
func (p *parser) finishStringEsc(scr *screen, b byte, back pstate) {
	if b == '\\' {
		p.state = stGround
		return
	}
	_ = scr
	p.state = back
}

func (p *parser) feedGround(scr *screen, b byte) {
	if b < 0x80 {
		scr.print(rune(b))
		return
	}
	p.startUTF8(scr, b)
}

func (p *parser) startUTF8(scr *screen, b byte) {
	switch {
	case b&0xe0 == 0xc0:
		p.utf8need = 2
	case b&0xf0 == 0xe0:
		p.utf8need = 3
	case b&0xf8 == 0xf0:
		p.utf8need = 4
	default:
		scr.print(utf8.RuneError)
		return
	}
	p.utf8buf[0] = b
	p.utf8len = 1
}

func (p *parser) feedUTF8Continuation(scr *screen, b byte) {
	if b&0xc0 != 0x80 {
		// Broken sequence: emit replacement and reprocess b from scratch.
		p.utf8len = 0
		scr.print(utf8.RuneError)
		p.feed(scr, b)
		return
	}
	p.utf8buf[p.utf8len] = b
	p.utf8len++
	if p.utf8len < p.utf8need {
		return
	}
	r, _ := utf8.DecodeRune(p.utf8buf[:p.utf8len])
	p.utf8len = 0
	scr.print(r)
}

func (p *parser) feedEscape(scr *screen, b byte) {
	switch {
	case b >= 0x20 && b <= 0x2f:
		p.inter = append(p.inter, b)
		p.state = stEscInter
	case b == '[':
		p.state = stCSIParam
	case b == ']':
		p.state = stOSC
	case b == 'P' || b == 'X' || b == '^' || b == '_': // DCS SOS PM APC
		p.state = stString
	default:
		p.state = stGround
		scr.escDispatch(b, p.inter)
	}
}

func (p *parser) feedEscInter(scr *screen, b byte) {
	if b >= 0x20 && b <= 0x2f {
		p.inter = append(p.inter, b)
		return
	}
	p.state = stGround
	scr.escDispatch(b, p.inter)
}

func (p *parser) feedCSIParam(scr *screen, b byte) {
	switch {
	case b >= '0' && b <= '9':
		p.pushParamDigit(b)
	case b == ';' || b == ':':
		p.nextParam()
	default:
		p.feedCSIParamOther(scr, b)
	}
}

func (p *parser) feedCSIParamOther(scr *screen, b byte) {
	switch {
	case b == '?' || b == '>' || b == '=' || b == '<':
		p.acceptPrivateMarker(b)
	case b >= 0x20 && b <= 0x2f:
		p.inter = append(p.inter, b)
		p.state = stCSIInter
	case b >= 0x40 && b <= 0x7e:
		p.state = stGround
		p.csiDispatch(scr, b)
	default:
		p.state = stCSIIgnore
	}
}

func (p *parser) acceptPrivateMarker(b byte) {
	if len(p.params) == 0 && p.private == 0 {
		p.private = b
		return
	}
	p.state = stCSIIgnore
}

func (p *parser) feedCSIInter(scr *screen, b byte) {
	switch {
	case b >= 0x20 && b <= 0x2f:
		p.inter = append(p.inter, b)
	case b >= 0x40 && b <= 0x7e:
		p.state = stGround
		p.csiDispatch(scr, b)
	default:
		p.state = stCSIIgnore
	}
}

func (p *parser) pushParamDigit(b byte) {
	if len(p.params) == 0 {
		p.params = append(p.params, 0)
	}
	if len(p.params) > maxCSIParams {
		return
	}
	last := len(p.params) - 1
	v := p.params[last]*10 + int(b-'0')
	if v > maxCSIParamVal {
		v = maxCSIParamVal
	}
	p.params[last] = v
}

func (p *parser) nextParam() {
	if len(p.params) == 0 {
		p.params = append(p.params, 0)
	}
	if len(p.params) <= maxCSIParams {
		p.params = append(p.params, 0)
	}
}

func (p *parser) csiDispatch(scr *screen, final byte) {
	scr.csiDispatch(final, p.private, p.params, p.inter)
	p.resetSequence()
}

func (p *parser) resetSequence() {
	p.params = p.params[:0]
	p.inter = p.inter[:0]
	p.private = 0
}
