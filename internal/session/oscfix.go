package session

// stringFilter works around a parser bug in the VT emulator: x/ansi ends an
// OSC/DCS/SOS/PM/APC string at the 8-bit ST byte 0x9C even when that byte is
// a UTF-8 continuation byte. Claude Code's window titles start with spinner
// glyphs such as ✳ (U+2733 = E2 9C B3), so `ESC]0;✳ Prueba de agentd BEL` set
// the title to "\xe2" and printed " Prueba de agentd" at the cursor, i.e. into
// the input line, where it looked like a draft or a suggested prompt (see
// docs/ghost-text.md). UTF-8 terminals never treat 0x9C as ST in that position.
//
// The filter only touches bytes inside a 7-bit-introduced control string
// (ESC ] / ESC P / ESC X / ESC ^ / ESC _): a complete UTF-8 rune that contains
// 0x9C is replaced by U+FFFD (whose encoding has no 0x9C), so the string keeps
// going until its real terminator. A 0x9C that is not part of a rune still
// terminates the string, as before. Everything else passes through unchanged.
// It is streaming: runes and escapes split across PTY reads are handled.
// The raw stream (log, ring buffer, attached clients) is never filtered.
type stringFilter struct {
	state   uint8
	osc     bool   // current string is an OSC (BEL also terminates it)
	pending []byte // incomplete UTF-8 rune inside a string
	need    int    // continuation bytes still expected for pending
}

const (
	sfGround uint8 = iota
	sfEsc          // saw ESC outside a string
	sfString       // inside a control string
)

var replacementRune = []byte("�")

// filter appends the filtered form of p to dst and returns it.
func (f *stringFilter) filter(dst, p []byte) []byte {
	for _, b := range p {
		switch f.state {
		case sfGround:
			if b == 0x1b {
				f.state = sfEsc
			}
			dst = append(dst, b)
		case sfEsc:
			switch b {
			case ']', 'P', 'X', '^', '_':
				f.state, f.osc = sfString, b == ']'
			case 0x1b:
				// stay: a new escape starts
			default:
				f.state = sfGround
			}
			dst = append(dst, b)
		case sfString:
			dst = f.stringByte(dst, b)
		}
	}
	return dst
}

func (f *stringFilter) stringByte(dst []byte, b byte) []byte {
	if f.need > 0 {
		if b >= 0x80 && b <= 0xbf {
			f.pending = append(f.pending, b)
			if f.need--; f.need == 0 {
				dst = f.flush(dst)
			}
			return dst
		}
		// Malformed rune: emit what we have and handle b on its own.
		dst = f.flush(dst)
	}
	switch {
	case b == 0x1b:
		// ESC dispatches the string; ESC \ is ST, anything else a new escape.
		f.state = sfEsc
	case b == 0x18 || b == 0x1a || b == 0x9c || (b == 0x07 && f.osc):
		f.state = sfGround
	case b >= 0xc2 && b <= 0xdf:
		f.pending, f.need = append(f.pending[:0], b), 1
		return dst
	case b >= 0xe0 && b <= 0xef:
		f.pending, f.need = append(f.pending[:0], b), 2
		return dst
	case b >= 0xf0 && b <= 0xf4:
		f.pending, f.need = append(f.pending[:0], b), 3
		return dst
	}
	return append(dst, b)
}

// flush emits the pending rune bytes, replaced by U+FFFD if any is 0x9C.
func (f *stringFilter) flush(dst []byte) []byte {
	out := f.pending
	for _, c := range f.pending {
		if c == 0x9c {
			out = replacementRune
			break
		}
	}
	dst = append(dst, out...)
	f.pending, f.need = f.pending[:0], 0
	return dst
}
