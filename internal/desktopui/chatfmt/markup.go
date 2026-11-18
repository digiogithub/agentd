package chatfmt

import "strings"

// Unmarkup turns chat markup (the CommonMark of the daemon's view markup)
// back into its text: backslash escapes resolved, "**" and the backticks of
// code spans dropped, a leading "### " removed.
func Unmarkup(s string) string {
	s = strings.TrimPrefix(s, "### ")
	if !strings.ContainsAny(s, "\\*`") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch ch := s[i]; {
		case ch == '\\' && i+1 < len(s) && isPunct(s[i+1]):
			i++
			b.WriteByte(s[i])
		case ch == '*' && i+1 < len(s) && s[i+1] == '*':
			i++
		case ch == '`':
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

func isPunct(c byte) bool {
	return c < 128 && strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c) >= 0
}

// TableText is tb with Unmarkup applied to every cell.
func TableText(tb *Table) *Table {
	out := &Table{Align: tb.Align}
	if tb.Header != nil {
		out.Header = make([]string, len(tb.Header))
		for i, c := range tb.Header {
			out.Header[i] = Unmarkup(c)
		}
	}
	out.Rows = make([][]string, len(tb.Rows))
	for r, row := range tb.Rows {
		out.Rows[r] = make([]string, len(row))
		for i, c := range row {
			out.Rows[r][i] = Unmarkup(c)
		}
	}
	return out
}
