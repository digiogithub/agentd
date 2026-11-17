package screen

import "strings"

// PlainText normalizes the text of a screen for reading by an agent or a
// script: TUIs that draw their transcript in boxes, cards and coloured blocks
// (Pando, many Ink/Bubble Tea apps) otherwise come out as tables of border
// glyphs. It is a pure, harness-agnostic text pass:
//
//   - lines made only of box-drawing / block-element glyphs and spaces
//     (horizontal rules, box tops and bottoms, shaded bars) are dropped;
//   - frame glyphs at the start and end of a line are stripped, along with the
//     corner and rule glyphs of a titled border (`╭─ Title ─╮` keeps `Title`);
//   - a side panel (a vertical rule `│` at the same column in most rows, e.g.
//     a sidebar beside the chat) is read as its own block after the main one
//     instead of interleaving the two columns line by line;
//   - trailing blanks are trimmed, runs of blank lines collapse to one, and
//     the common indentation left by the frame is removed.
//
// Glyphs inside a line are kept (a table cell separator stays), and ASCII
// `|`, `-` and `+` are never touched: only U+2500–U+259F is treated as frame.
func PlainText(s string) string {
	lines := splitColumns(strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n"))
	out := make([]string, 0, len(lines))
	blank := false
	for _, ln := range lines {
		t, drop := plainLine(ln)
		if drop || strings.TrimSpace(t) == "" {
			// A dropped frame line or an empty one is a (single) blank.
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, t)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	dedent(out)
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

// isFrameRune reports box-drawing (U+2500–257F) and block elements
// (U+2580–259F: half/full blocks, shades, quadrants).
func isFrameRune(r rune) bool { return r >= 0x2500 && r <= 0x259F }

// isRuleRune is a frame rune with no vertical stroke worth keeping next to
// text: horizontal lines, corners, tees, crosses and every block element.
func isRuleRune(r rune) bool { return isFrameRune(r) }

func plainLine(ln string) (string, bool) {
	ln = strings.TrimRight(ln, " \t ")
	if ln == "" {
		return "", false
	}
	rs := []rune(ln)
	only := true
	for _, r := range rs {
		if !isFrameRune(r) && r != ' ' && r != ' ' {
			only = false
			break
		}
	}
	if only {
		return "", true
	}
	// Strip frame runs (and the spaces between them) at both ends.
	lo, hi := 0, len(rs)
	for lo < hi && (isRuleRune(rs[lo]) || rs[lo] == ' ' || rs[lo] == ' ') {
		if rs[lo] == ' ' || rs[lo] == ' ' {
			// Keep the indent of a line that has no frame at its start.
			if !frameAhead(rs[lo:]) {
				break
			}
		}
		lo++
	}
	for hi > lo && (isRuleRune(rs[hi-1]) || rs[hi-1] == ' ' || rs[hi-1] == ' ') {
		hi--
	}
	return strings.TrimRight(string(rs[lo:hi]), "  "), false
}

// frameAhead reports whether the blanks at the start of rs are followed by a
// frame rune (so they belong to the border, not to the text's indent).
func frameAhead(rs []rune) bool {
	for _, r := range rs {
		if r == ' ' || r == ' ' {
			continue
		}
		return isFrameRune(r)
	}
	return false
}

// dedent removes the indentation shared by every non-blank line.
func dedent(lines []string) {
	min := -1
	for _, l := range lines {
		if l == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if min < 0 || n < min {
			min = n
		}
	}
	if min <= 0 {
		return
	}
	for i, l := range lines {
		if l != "" {
			lines[i] = l[min:]
		}
	}
}

// splitColumns detects a vertical separator: a single column (rune index) that
// holds a vertical frame rune in at least 60% of the non-blank rows and at
// least 4 rows. Rows are cut there; the left parts come first, then a blank
// line and the right parts. Rows without the separator stay on the left.
func splitColumns(lines []string) []string {
	counts := map[int]int{}
	nonBlank := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		nonBlank++
		for i, r := range []rune(l) {
			if r == '│' || r == '┃' || r == '║' {
				counts[i]++
			}
		}
	}
	col, best := -1, 0
	for i, n := range counts {
		if n > best || n == best && i < col {
			col, best = i, n
		}
	}
	if col < 0 || best < 4 || best*10 < nonBlank*6 {
		return lines
	}
	var left, right []string
	for _, l := range lines {
		rs := []rune(l)
		if col < len(rs) && (rs[col] == '│' || rs[col] == '┃' || rs[col] == '║') {
			left = append(left, string(rs[:col]))
			right = append(right, string(rs[col+1:]))
			continue
		}
		left = append(left, l)
	}
	// Each block loses its own shared indentation (the frame's padding).
	for i := range right {
		right[i] = strings.TrimRight(right[i], " ")
	}
	for i := range left {
		left[i] = strings.TrimRight(left[i], " ")
	}
	dedent(left)
	dedent(right)
	return append(append(left, ""), right...)
}
