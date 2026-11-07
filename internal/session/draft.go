package session

import (
	"strings"
	"time"
	"unicode/utf8"
)

// StripControls drops every C0 control character except \t, \n and \r, DEL
// and the C1 controls (U+0080–U+009F, which some terminals treat like ESC
// sequences) from s. Invalid UTF-8 bytes are dropped as well. With ESC gone,
// no escape sequence — in particular no bracketed-paste end marker, however
// it is nested or split — can survive, so text pasted with Paste stays inside
// the paste.
func StripControls(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			return r
		case r < 0x20 || r == 0x7f:
			return -1
		case r >= 0x80 && r <= 0x9f:
			return -1
		case r == utf8.RuneError:
			return -1
		}
		return r
	}, s)
}

// PasteSafe prepares text for a bracketed paste: paste markers are removed
// repeatedly (so markers nested inside markers vanish too) and then every
// remaining control character is dropped with StripControls, so no ESC — and
// hence no paste end marker — can reach the terminal.
func PasteSafe(text string) string {
	for strings.Contains(text, pasteStart) || strings.Contains(text, pasteEnd) {
		text = strings.ReplaceAll(text, pasteStart, "")
		text = strings.ReplaceAll(text, pasteEnd, "")
	}
	return StripControls(text)
}

// Draft tracking: a human who typed into the agent's input line but has not
// submitted yet has a pending draft, which the delivery loop must not paste
// into (the notice would be glued to the half-written prompt).

// Draft effects of a chunk of human input.
const (
	draftNone  = iota // no effect (only escape sequences: cursor keys, focus, mouse...)
	draftSet          // ends with uncommitted input
	draftClear        // ends with a submit (\r, \n) or Ctrl-C
)

// draftEffect classifies human input p: the last relevant byte wins. Escape
// sequences (CSI, SS3, OSC/DCS strings and two-byte ESC sequences) are
// ignored, except that the text inside a bracketed paste counts as input.
func draftEffect(p []byte) int {
	effect := draftNone
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == 0x1b:
			i = skipEscape(p, i)
		case c == '\r' || c == '\n' || c == 0x03:
			effect = draftClear
		default:
			effect = draftSet
		}
	}
	return effect
}

// skipEscape returns the index of the last byte of the escape sequence that
// starts at p[i] (an ESC).
func skipEscape(p []byte, i int) int {
	if i+1 >= len(p) {
		return i
	}
	switch p[i+1] {
	case '[': // CSI: parameters/intermediates 0x20–0x3f, final 0x40–0x7e
		for j := i + 2; j < len(p); j++ {
			if p[j] >= 0x40 && p[j] <= 0x7e {
				return j
			}
		}
		return len(p) - 1
	case 'O': // SS3 + one byte (application cursor keys, F1–F4)
		return min(i+2, len(p)-1)
	case ']', 'P', '_', '^': // OSC / DCS / APC / PM: until BEL or ST
		for j := i + 2; j < len(p); j++ {
			if p[j] == 0x07 {
				return j
			}
			if p[j] == 0x1b && j+1 < len(p) && p[j+1] == '\\' {
				return j + 1
			}
		}
		return len(p) - 1
	default: // ESC + one byte (Alt-<key>)
		return i + 1
	}
}

// trackDraft updates the draft state after human input p.
func (s *Session) trackDraft(p []byte) {
	switch draftEffect(p) {
	case draftSet:
		s.draftAt.Store(time.Now().UnixNano())
	case draftClear:
		s.draftAt.Store(0)
	}
}

// HumanDraftSince returns when a human last typed input that is still
// uncommitted (not followed by \r, \n or Ctrl-C), or the zero time if there is
// no pending draft.
func (s *Session) HumanDraftSince() time.Time { return unixNano(s.draftAt.Load()) }
