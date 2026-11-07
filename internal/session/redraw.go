package session

import (
	"bytes"
	"strconv"
	"strings"
)

// redrawModes are the DEC private modes re-applied on a client terminal by
// Redraw, in this order. Defaults on a fresh terminal: only 7 (autowrap) set.
var redrawModes = []struct {
	mode int
	def  bool
}{
	{1, false},    // DECCKM application cursor keys
	{7, true},     // DECAWM autowrap
	{1000, false}, // mouse: normal tracking
	{1002, false}, // mouse: button-event tracking
	{1003, false}, // mouse: any-event tracking
	{1004, false}, // focus events
	{1005, false}, // mouse: UTF-8 encoding
	{1006, false}, // mouse: SGR encoding
	{1015, false}, // mouse: urxvt encoding
	{2004, false}, // bracketed paste
}

// redrawLocked builds the redraw sequence. Caller holds outMu (read or write).
//
// Layout:
//
//	ESC[?2026h                       begin synchronized update (ignored if unsupported)
//	ESC[?1049h                       only if the child is in the alternate screen
//	ESC[0m ESC[r ESC[H ESC[2J        reset SGR, scroll region, home, clear
//	ESC[<row>;1H <rendered line>     for every non-empty row (avoids relying on LF/CR)
//	ESC[?<mode>h|l                   tracked input/terminal modes (see redrawModes)
//	ESC[<n> q                        cursor style (DECSCUSR)
//	ESC[<row>;<col>H                 cursor position
//	ESC[?25h|l                       cursor visibility
//	ESC[?2026l                       end synchronized update
//
// Not restored (not exposed by x/vt): the child's scroll region, current SGR
// pen, origin mode, character sets, window title and keyboard protocol flags.
func (s *Session) redrawLocked() []byte {
	e := s.emu.Emulator
	var b bytes.Buffer
	b.WriteString("\x1b[?2026h")
	if e.IsAltScreen() {
		b.WriteString("\x1b[?1049h")
	}
	b.WriteString("\x1b[0m\x1b[r\x1b[H\x1b[2J")

	for i, line := range strings.Split(e.Render(), "\n") {
		if line == "" {
			continue
		}
		b.WriteString("\x1b[")
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(";1H")
		b.WriteString(line)
	}
	b.WriteString("\x1b[0m")

	for _, m := range redrawModes {
		on, ok := s.decModes[m.mode]
		if !ok {
			on = m.def
		}
		b.WriteString("\x1b[?")
		b.WriteString(strconv.Itoa(m.mode))
		if on {
			b.WriteByte('h')
		} else {
			b.WriteByte('l')
		}
	}

	style, blink := e.CursorStyle()
	n := int(style)*2 + 1
	if !blink {
		n++
	}
	if n == 1 {
		n = 0 // emulator default (blinking block): use the client's own default
	}
	b.WriteString("\x1b[")
	b.WriteString(strconv.Itoa(n))
	b.WriteString(" q")

	pos := e.CursorPosition()
	b.WriteString("\x1b[")
	b.WriteString(strconv.Itoa(pos.Y + 1))
	b.WriteByte(';')
	b.WriteString(strconv.Itoa(pos.X + 1))
	b.WriteByte('H')

	if e.CursorHidden() {
		b.WriteString("\x1b[?25l")
	} else {
		b.WriteString("\x1b[?25h")
	}
	b.WriteString("\x1b[?2026l")
	return b.Bytes()
}
