package ptypredict

import "unicode/utf8"

type keyKind uint8

const (
	keyRune keyKind = iota
	keyBackspace
	keyCR
	keyCursorRight
	keyCursorLeft
	keyOther
)

type keyEvent struct {
	kind keyKind
	r    rune
}

type keyParserState uint8

const (
	kpGround keyParserState = iota
	kpEsc
	kpCSI
	kpSS3
)

// keyParser incrementally splits the raw stdin byte stream into key events.
// It never withholds bytes from the server; it only classifies them.
type keyParser struct {
	state    keyParserState
	utf8buf  [utf8.UTFMax]byte
	utf8len  int
	utf8need int
	csiFinal byte
	csiPlain bool // no parameters / intermediates seen so far
}

// feed consumes one chunk and returns the completed key events.
func (k *keyParser) feed(p []byte, events []keyEvent) []keyEvent {
	for _, b := range p {
		events = k.feedByte(b, events)
	}
	return events
}

func (k *keyParser) feedByte(b byte, events []keyEvent) []keyEvent {
	switch k.state {
	case kpEsc:
		return k.feedEscByte(b, events)
	case kpCSI:
		return k.feedCSIByte(b, events)
	case kpSS3:
		k.state = kpGround
		return append(events, ss3Event(b))
	default:
		return k.feedGroundByte(b, events)
	}
}

func (k *keyParser) feedGroundByte(b byte, events []keyEvent) []keyEvent {
	if k.utf8len > 0 {
		return k.feedUTF8(b, events)
	}
	switch {
	case b == 0x1b:
		k.state = kpEsc
		return events
	case b == 0x7f:
		return append(events, keyEvent{kind: keyBackspace})
	case b == '\r':
		return append(events, keyEvent{kind: keyCR})
	case b < 0x20:
		return append(events, keyEvent{kind: keyOther})
	case b < 0x80:
		return append(events, keyEvent{kind: keyRune, r: rune(b)})
	default:
		return k.startUTF8(b, events)
	}
}

func (k *keyParser) startUTF8(b byte, events []keyEvent) []keyEvent {
	switch {
	case b&0xe0 == 0xc0:
		k.utf8need = 2
	case b&0xf0 == 0xe0:
		k.utf8need = 3
	case b&0xf8 == 0xf0:
		k.utf8need = 4
	default:
		return append(events, keyEvent{kind: keyOther})
	}
	k.utf8buf[0] = b
	k.utf8len = 1
	return events
}

func (k *keyParser) feedUTF8(b byte, events []keyEvent) []keyEvent {
	if b&0xc0 != 0x80 {
		k.utf8len = 0
		events = append(events, keyEvent{kind: keyOther})
		return k.feedGroundByte(b, events)
	}
	k.utf8buf[k.utf8len] = b
	k.utf8len++
	if k.utf8len < k.utf8need {
		return events
	}
	r, _ := utf8.DecodeRune(k.utf8buf[:k.utf8len])
	k.utf8len = 0
	return append(events, keyEvent{kind: keyRune, r: r})
}

func (k *keyParser) feedEscByte(b byte, events []keyEvent) []keyEvent {
	switch b {
	case '[':
		k.state = kpCSI
		k.csiFinal = 0
		k.csiPlain = true
		return events
	case 'O':
		k.state = kpSS3
		return events
	default:
		// ESC-prefixed key (alt-x, bare ESC before other input...).
		k.state = kpGround
		return append(events, keyEvent{kind: keyOther})
	}
}

func (k *keyParser) feedCSIByte(b byte, events []keyEvent) []keyEvent {
	if b >= 0x40 && b <= 0x7e {
		k.state = kpGround
		return append(events, k.csiEvent(b))
	}
	// Parameter or intermediate byte: arrows we predict carry none.
	k.csiPlain = false
	return events
}

func (k *keyParser) csiEvent(final byte) keyEvent {
	if !k.csiPlain {
		return keyEvent{kind: keyOther}
	}
	switch final {
	case 'C':
		return keyEvent{kind: keyCursorRight}
	case 'D':
		return keyEvent{kind: keyCursorLeft}
	default:
		return keyEvent{kind: keyOther}
	}
}

// ss3Event mirrors mosh: ESC O C / ESC O D (application cursor mode) are
// treated like their CSI forms.
func ss3Event(final byte) keyEvent {
	switch final {
	case 'C':
		return keyEvent{kind: keyCursorRight}
	case 'D':
		return keyEvent{kind: keyCursorLeft}
	default:
		return keyEvent{kind: keyOther}
	}
}
