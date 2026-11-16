package daemon

import (
	"strconv"
	"strings"
)

// Terminal replies in the attach input stream.
//
// While clients are attached, their real terminals answer the queries the
// child prints (cursor position, device attributes, colours, mode reports):
// the answers arrive in D frames mixed with keystrokes. They are not human
// input (they must not delay delivery or create drafts), and with N attached
// clients the child would get N answers, so they are forwarded only from the
// most recently active client.

// splitTermReplies separates terminal replies from the rest of an attach
// input chunk. Recognized replies:
//
//   - CSI <digits;digits> R        cursor position report (CPR / DECXCPR "?")
//   - CSI ? … c, CSI > … c, CSI = … c   device attributes (DA1/DA2/DA3)
//   - CSI ? … $ y, CSI … $ y       mode reports (DECRPM)
//   - CSI ? … u                    keyboard protocol flags report
//   - CSI … n                      device status reports (DSR "0n", the
//     colour-scheme report "?997;1n" of mode 2031, …)
//   - CSI … t                      window reports (XTWINOPS "8;rows;cols t", …)
//   - OSC 4/10/11/12/… ; … (BEL|ST) colour query answers (OSC with a numeric
//     code followed by "rgb:" data)
//   - DCS … ST                     DECRQSS / XTVERSION / XTGETTCAP answers
//
// Everything else (keys, pastes, focus and mouse events) is returned in input,
// in order. A sequence cut at the end of the chunk is left in input.
func splitTermReplies(p []byte) (input, replies []byte) {
	if indexByte(p, 0x1b) < 0 {
		return p, nil
	}
	input = make([]byte, 0, len(p))
	for i := 0; i < len(p); {
		if p[i] != 0x1b {
			j := i + 1
			for j < len(p) && p[j] != 0x1b {
				j++
			}
			input = append(input, p[i:j]...)
			i = j
			continue
		}
		end, reply := classifyEscape(p, i)
		if reply {
			replies = append(replies, p[i:end]...)
		} else {
			input = append(input, p[i:end]...)
		}
		i = end
	}
	return input, replies
}

func indexByte(p []byte, c byte) int {
	for i, b := range p {
		if b == c {
			return i
		}
	}
	return -1
}

// classifyEscape returns the end (exclusive) of the escape sequence starting
// at p[i] and whether it is a terminal reply.
func classifyEscape(p []byte, i int) (end int, reply bool) {
	if i+1 >= len(p) {
		return len(p), false
	}
	switch p[i+1] {
	case '[':
		j := i + 2
		for j < len(p) && (p[j] < 0x40 || p[j] > 0x7e) {
			if p[j] < 0x20 { // not a well-formed CSI
				return j, false
			}
			j++
		}
		if j >= len(p) {
			return len(p), false
		}
		return j + 1, isCSIReply(p[i+2:j], p[j])
	case ']', 'P':
		end := 0
		for j := i + 2; j < len(p) && end == 0; j++ {
			if p[j] == 0x07 {
				end = j + 1
			} else if p[j] == 0x1b && j+1 < len(p) && p[j+1] == '\\' {
				end = j + 2
			}
		}
		if end == 0 { // unterminated: leave it as input
			return len(p), false
		}
		if p[i+1] == 'P' {
			return end, true // terminals never send DCS as keyboard input
		}
		return end, isOSCReply(p[i+2 : end])
	default:
		return min(i+2, len(p)), false
	}
}

// isCSIReply classifies a CSI with parameter/intermediate bytes params and
// final byte final.
func isCSIReply(params []byte, final byte) bool {
	switch final {
	case 'R': // CPR: [?]digits;digits
		ps := params
		if len(ps) > 0 && ps[0] == '?' {
			ps = ps[1:]
		}
		return onlyDigitsSemis(ps) && indexByte(ps, ';') > 0
	case 'c': // DA1/DA2/DA3 answers carry a ? > or = prefix (queries do not)
		return len(params) > 0 && (params[0] == '?' || params[0] == '>' || params[0] == '=')
	case 'y': // DECRPM: … $ y
		return len(params) > 0 && params[len(params)-1] == '$'
	case 'u': // keyboard flags report: ? flags u
		return len(params) > 0 && params[0] == '?'
	case 'n': // DSR answers ("0n", "?997;1n", "?10n"); no key ends in n
		ps := params
		if len(ps) > 0 && ps[0] == '?' {
			ps = ps[1:]
		}
		return onlyDigitsSemis(ps)
	case 't': // XTWINOPS reports ("4;h;w t", "8;rows;cols t"); no key ends in t
		return onlyDigitsSemis(params)
	}
	return false
}

// ambientOnly reports whether attach input p (terminal replies already
// removed) consists only of pointer and focus reports that a terminal sends
// without a keystroke: focus in/out (CSI I, CSI O; DEC 1004), mouse motion
// with no button held and mouse wheel events (SGR "CSI < b;x;y M|m", urxvt
// "CSI b;x;y M" and X10 "CSI M bxy"). Such input reaches the child but is
// not human typing: it does not update LastHumanInput (human_grace_ms), the
// draft state or the turn origin, and does not make the client active. A
// terminal view that only has the pointer over it (a desktop client's
// Terminal tab with Claude Code's any-event mouse tracking, mode 1003) would
// otherwise keep acts busy. Clicks and drags still count as human input.
func ambientOnly(p []byte) bool {
	if len(p) == 0 {
		return false
	}
	for i := 0; i < len(p); {
		n := ambientLen(p[i:])
		if n == 0 {
			return false
		}
		i += n
	}
	return true
}

// ambientLen returns the length of the focus or ambient mouse report at the
// start of p, or 0 when p does not start with one.
func ambientLen(p []byte) int {
	if len(p) < 3 || p[0] != 0x1b || p[1] != '[' {
		return 0
	}
	switch p[2] {
	case 'I', 'O': // focus in / out
		return 3
	case 'M': // X10: CSI M then three bytes (button+32, x+32, y+32)
		if len(p) < 6 || !ambientButton(int(p[3])-32) {
			return 0
		}
		return 6
	}
	j := 2
	sgr := p[j] == '<'
	if sgr {
		j++
	}
	start := j
	for j < len(p) && (p[j] >= '0' && p[j] <= '9' || p[j] == ';') {
		j++
	}
	if j >= len(p) || j == start || (p[j] != 'M' && (!sgr || p[j] != 'm')) {
		return 0
	}
	fields := strings.Split(string(p[start:j]), ";")
	if len(fields) != 3 {
		return 0
	}
	b, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0
	}
	if !sgr {
		b -= 32 // urxvt (1015) sends the button code + 32 like X10
	}
	if !ambientButton(b) {
		return 0
	}
	return j + 1
}

// ambientButton reports whether mouse button code b (modifier bits included)
// is a motion with no button held (32 + 3) or a wheel event (64..127).
func ambientButton(b int) bool {
	if b < 0 {
		return false
	}
	if b&64 != 0 {
		return true // wheel (and extra buttons 8-11 sent as 128+: not here)
	}
	return b&32 != 0 && b&3 == 3
}

// isOSCReply reports whether an OSC body (without ESC ]) is a colour answer:
// "<number>[;<index>];rgb:…" (OSC 4, 10, 11, 12, 17, 19, …).
func isOSCReply(body []byte) bool {
	k := 0
	for k < len(body) && body[k] >= '0' && body[k] <= '9' {
		k++
	}
	if k == 0 || k >= len(body) || body[k] != ';' {
		return false
	}
	return containsRGB(body[k:])
}

func containsRGB(b []byte) bool {
	for i := 0; i+4 <= len(b); i++ {
		if b[i] == 'r' && b[i+1] == 'g' && b[i+2] == 'b' && (b[i+3] == ':' || b[i+3] == 'a') {
			return true
		}
	}
	return false
}

func onlyDigitsSemis(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if (c < '0' || c > '9') && c != ';' {
			return false
		}
	}
	return true
}

// isActive reports whether c is the most recently active attached client.
func (e *entry) isActive(c *attachClient) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active == c
}
