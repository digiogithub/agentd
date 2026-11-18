package screen

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Span is a styled run of a Line's Text: bytes [Start, End) drawn with Attrs
// (only Bold, Faint, Italic, Underline, Strike are kept) and foreground Fg
// ("" default, "N" palette index, "#rrggbb").
type Span struct {
	Start, End int
	Attrs      Attr
	Fg         string
}

// spanAttrs are the attributes a Span keeps.
const spanAttrs = Bold | Faint | Italic | Underline | Strike

// maxSpans caps the spans of one line (a pathological rainbow row).
const maxSpans = 256

// addSpan records cell c drawn at bytes [start, end): extends the last span
// when the style is the same and contiguous, else appends one. Unstyled
// cells add nothing.
func addSpan(spans []Span, start, end int, c Cell) []Span {
	a := c.Attrs & spanAttrs
	if a == 0 && c.Fg == "" {
		return spans
	}
	if n := len(spans); n > 0 && spans[n-1].End == start && spans[n-1].Attrs == a && spans[n-1].Fg == c.Fg {
		spans[n-1].End = end
		return spans
	}
	if len(spans) >= maxSpans {
		return spans
	}
	return append(spans, Span{Start: start, End: end, Attrs: a, Fg: c.Fg})
}

// appendSpans appends add (offsets of a row's text) to dst, shifted to
// position at and dropping the first cut bytes of the row's text (trimmed
// indentation).
func appendSpans(dst, add []Span, at, cut int) []Span {
	for _, s := range add {
		if s.End <= cut {
			continue
		}
		s.Start = max(s.Start, cut) - cut + at
		s.End = s.End - cut + at
		if len(dst) >= maxSpans {
			break
		}
		dst = append(dst, s)
	}
	return dst
}

// Markup renders l as "chat markup", the small markdown subset agentd clients
// draw in the chat view (view op with markup):
//
//   - a line whose letters and digits are all bold and that starts with one
//     (not with a bullet or a mark) and is at most 100 characters is a
//     heading: its indentation, "### ", the text;
//   - other bold runs are **bold**;
//   - a run in a foreground colour that holds letters or digits, is not bold,
//     and is not the whole line is `code` (TUIs such as Claude Code draw
//     inline code that way);
//   - CommonMark's inline punctuation in the text (see markupSpecial) is
//     escaped with `\`, so the result is CommonMark that shows the text.
//
// Other styles (italic, faint, underline) are dropped. Markup of a line
// without spans is the escaped text.
func Markup(l Line) string {
	text := l.Text
	body := strings.TrimLeft(text, "  ")
	indent := text[:len(text)-len(body)]
	if body == "" {
		return text
	}
	if isHeading(l, len(indent), body) {
		return indent + "### " + escapeMarkup(strings.TrimRight(body, "  "))
	}
	if t := strings.TrimSpace(body); strings.Trim(t, "-= ") == "" {
		// A setext underline or a thematic break: keep it text.
		return indent + "\\" + escapeMarkup(body)
	}
	spans := mergeSpans(l.Spans, text)
	whole := wholeLine(spans, text)
	isCode := func(sp Span) bool {
		seg := strings.TrimSpace(text[sp.Start:sp.End])
		return sp.Fg != "" && !whole && hasAlnum(seg) && !strings.Contains(seg, "`")
	}
	var b strings.Builder
	pos := 0
	plain := func(to int) {
		if to > pos {
			b.WriteString(escapeMarkup(text[pos:to]))
			pos = to
		}
	}
	// code writes text[from:to] as `code`, its blanks outside the backticks.
	code := func(from, to int) {
		seg := text[from:to]
		inner := strings.TrimSpace(seg)
		lead := strings.Index(seg, inner)
		plain(from + lead)
		b.WriteString("`" + inner + "`")
		pos = from + lead + len(inner)
	}
	for i := 0; i < len(spans); {
		sp := spans[i]
		if sp.End <= pos {
			i++
			continue
		}
		if sp.Attrs&Bold == 0 {
			if isCode(sp) {
				code(max(sp.Start, pos), sp.End)
			}
			i++
			continue
		}
		// A bold region: consecutive bold spans with only blanks between
		// them; a run in another colour inside it is code (Claude Code draws
		// **text `code` text** that way).
		j := i + 1
		for j < len(spans) && spans[j].Attrs&Bold != 0 && strings.TrimSpace(text[spans[j-1].End:spans[j].Start]) == "" {
			j++
		}
		region := spans[i:j]
		i = j
		start, end := max(region[0].Start, pos), region[len(region)-1].End
		seg := text[start:end]
		inner := strings.TrimSpace(seg)
		if inner == "" {
			continue
		}
		start += strings.Index(seg, inner)
		end = start + len(inner)
		// The region's own colour: the default one when any part uses it,
		// else the colour of its longest part.
		base, longest := region[0].Fg, -1
		for _, r := range region {
			if r.Fg == "" {
				base, longest = "", 1<<30
			} else if n := r.End - r.Start; n > longest {
				base, longest = r.Fg, n
			}
		}
		plain(start)
		b.WriteString("**")
		for _, r := range region {
			from, to := max(r.Start, pos), min(r.End, end)
			if from >= to {
				continue
			}
			if r.Fg != base && isCode(r) && strings.TrimSpace(text[from:to]) != "" {
				code(from, to)
			} else {
				plain(to)
			}
		}
		plain(end)
		b.WriteString("**")
	}
	plain(len(text))
	return b.String()
}

// mergeSpans joins spans of the same style separated only by blanks (a TUI
// may leave the spaces between bold words unstyled).
func mergeSpans(spans []Span, text string) []Span {
	var out []Span
	for _, s := range spans {
		if s.End > len(text) || s.Start >= s.End {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Attrs == s.Attrs && out[n-1].Fg == s.Fg &&
			strings.TrimSpace(text[out[n-1].End:s.Start]) == "" {
			out[n-1].End = s.End
			continue
		}
		out = append(out, s)
	}
	return out
}

// wholeLine reports whether one coloured span covers every letter and digit
// of text (a fully coloured line is a style of the line, not inline code).
func wholeLine(spans []Span, text string) bool {
	for _, s := range spans {
		if s.Fg == "" {
			continue
		}
		if !hasAlnum(text[:s.Start]) && !hasAlnum(text[s.End:]) {
			return true
		}
	}
	return false
}

// isHeading: see Markup. at is the byte offset of body in l.Text.
func isHeading(l Line, at int, body string) bool {
	r, _ := utf8.DecodeRuneInString(body)
	if !unicode.IsLetter(r) && !unicode.IsDigit(r) || utf8.RuneCountInString(strings.TrimSpace(body)) > 100 {
		return false
	}
	for i, r := range body {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			continue
		}
		if !boldAt(l.Spans, at+i) {
			return false
		}
	}
	return true
}

func boldAt(spans []Span, i int) bool {
	for _, s := range spans {
		if s.Start <= i && i < s.End {
			return s.Attrs&Bold != 0
		}
	}
	return false
}

func hasAlnum(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// markupSpecial are the characters escaped in chat markup: CommonMark's
// inline syntax (emphasis, code, links, raw HTML, entities, tables,
// strikethrough) and a leading '#' or '>'.
const markupSpecial = "\\*_`[]<>|~#&"

// escapeMarkup escapes the markup characters of s with a backslash.
func escapeMarkup(s string) string {
	if !strings.ContainsAny(s, markupSpecial) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if r < 128 && strings.ContainsRune(markupSpecial, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
