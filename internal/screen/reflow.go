package screen

import (
	"regexp"
	"strings"
	"unicode"
)

// Role classifies a logical line (semantic-model appendix).
type Role string

// Line roles.
const (
	RoleTranscript Role = "transcript"
	RoleStatus     Role = "status"
	RoleDialog     Role = "dialog"
	RoleGap        Role = "gap"
)

// Join reasons carried in Line.Joined.
const (
	JoinedAutowrap = "autowrap" // row filled the width: continued without separator
	JoinedWordwrap = "wordwrap" // next word would not have fit: joined with one space
	JoinedFlag     = "flag"     // Row.Wrapped was set
)

// Line is one logical line: one or more display rows joined by Reflow.
type Line struct {
	Text string
	Role Role
	// Rows is the number of display rows joined into the line.
	Rows int
	// Joined is the reason of the first join ("" when Rows == 1).
	Joined string
	// Row is the index of the line's first row in the reflowed row stream
	// (Scrollback ⧺ Screen for Reflow, the slice index for ReflowRows).
	Row int
}

// ReflowOpts tunes Reflow.
type ReflowOpts struct {
	// Rules supplies WrapMargin, NoJoin, the prompt marker and menu pointers.
	// Use DefaultRules() for the profile defaults.
	Rules Rules
	// Skip marks rows (by stream index) that are not part of the text: they
	// emit nothing and break the logical line (used by Split to drop rules,
	// input and footer rows).
	Skip func(i int) bool
	// StripFrames removes │ ┃ ║ borders around a row (the dialog and input
	// regions are drawn in frames). Off for transcript text, where such rows
	// are tables or tool output and are kept intact (and never joined).
	StripFrames bool
}

// StreamRows returns the rows Reflow works on: the scrollback tail followed by
// the screen on the main screen, only the screen on the alternate screen
// (semantic-model §3.4).
func StreamRows(s *Snapshot) []Row {
	if s.AltScreen || len(s.Scrollback) == 0 {
		return s.Screen
	}
	out := make([]Row, 0, len(s.Scrollback)+len(s.Screen))
	out = append(out, s.Scrollback...)
	return append(out, s.Screen...)
}

// Reflow joins soft-wrapped rows of the snapshot into logical lines (§3.3),
// scrollback and screen as one stream, trailing blank lines dropped. It is a
// pure function of s and opts, independent of the terminal width for the
// same content.
func Reflow(s *Snapshot, opts ReflowOpts) []Line {
	return ReflowRows(StreamRows(s), s.Cols, opts)
}

// ReflowRows reflows rows laid out for a terminal cols wide.
func ReflowRows(rows []Row, cols int, opts ReflowOpts) []Line {
	views := make([]rowView, len(rows))
	skip := make([]bool, len(rows))
	for i, r := range rows {
		skip[i] = opts.Skip != nil && opts.Skip(i)
		if !skip[i] {
			views[i] = newRowViewOpt(r, cols, opts.StripFrames)
		}
	}
	fenced := fenceMask(views, skip)
	// A stream whose rows show the word-wrap shape (a row that stops short of
	// the edge because the next word would not fit) comes from a TUI that wraps
	// at word boundaries; there a row that is exactly full is also a word
	// boundary (the separator space is trimmed), not a mid-word cut.
	ww := false
	for i := 1; i < len(rows) && !ww; i++ {
		if skip[i] || skip[i-1] || views[i-1].blank || views[i].blank {
			continue
		}
		a := &views[i-1]
		if _, why, ok := canJoin(a, &views[i], a.contentIndent(), a.hasMarker, false, opts.Rules, false); ok && !fenced[i] && !fenced[i-1] && why == JoinedWordwrap && a.end < a.avail {
			ww = true
		}
	}
	var out []Line
	var cur *Line
	var curView *rowView
	var curIndent int
	curHang := false // the logical line started with a list marker
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur, curView = nil, nil
		}
	}
	for i := range rows {
		if skip[i] {
			flush()
			continue
		}
		v := &views[i]
		if v.blank {
			flush()
			out = append(out, Line{Role: RoleTranscript, Rows: 1, Row: i})
			continue
		}
		if cur != nil && !fenced[i] && !fenced[i-1] {
			if sep, why, ok := canJoin(curView, v, curIndent, curHang, rows[i-1].Wrapped, opts.Rules, ww); ok {
				if why == JoinedAutowrap && v.indent != curIndent {
					cur.Text += v.text // the cut fell on a blank: keep it
				} else {
					cur.Text += sep + strings.TrimLeft(v.text, " \u00a0")
				}
				cur.Rows++
				if cur.Joined == "" {
					cur.Joined = why
				}
				curView = v
				continue
			}
		}
		flush()
		cur = &Line{Text: v.text, Role: RoleTranscript, Rows: 1, Row: i}
		curView = v
		curIndent = v.contentIndent()
		curHang = v.hasMarker
	}
	flush()
	for len(out) > 0 && out[len(out)-1].Text == "" {
		out = out[:len(out)-1]
	}
	return out
}

// contentIndent is the column the row's text continues at when wrapped: its
// indent, or the hanging indent after a list marker.
func (v *rowView) contentIndent() int {
	if v.hasMarker {
		return v.hang
	}
	return v.indent
}

var diffGutter = regexp.MustCompile(`^\s*\d+\s+[+-](\s|$)`)

// canJoin decides whether b continues the logical line whose last row is a
// (content indent contentIndent), returning the separator and reason.
func canJoin(a, b *rowView, contentIndent int, hang, flag bool, r Rules, wordWrapper bool) (sep, why string, ok bool) {
	if a.blank || b.blank || a.frameL != b.frameL || a.frameR != b.frameR {
		return
	}
	if flag {
		// Row.Wrapped is authoritative; only the indent must agree.
		if b.indent == contentIndent {
			return "", JoinedFlag, true
		}
		return
	}
	full := a.end >= a.avail
	if a.codeish(hang, full) || b.codeish(hang, true) || (!full && b.startsStructural()) {
		return
	}
	if a.verbatim || b.verbatim || a.boxy || b.boxy ||
		diffGutter.MatchString(a.match) || diffGutter.MatchString(b.match) {
		return
	}
	if b.hasMarker || b.promptLike(r) || (r.NoJoin != nil && r.NoJoin.MatchString(strings.TrimLeft(b.match, " "))) {
		return
	}
	indentOK := b.indent == contentIndent
	switch {
	case a.end >= a.avail:
		if wordWrapper && a.multiWord() && indentOK {
			return " ", JoinedWordwrap, true
		}
		// Exact-width autowrap: the row's last cell is non-blank (a.end is the
		// end of its last non-blank cell), the continuation is at the same
		// indent and starts with a non-blank cell. Never in a narrow area,
		// where full rows are common for plain text.
		// A single extra blank column is the blank the cut fell on.
		if (indentOK || b.indent == contentIndent+1) && a.avail >= minAutowrapWidth {
			return "", JoinedAutowrap, true
		}
	case a.end == a.avail-1 && b.firstW == 2 && indentOK && a.avail >= minAutowrapWidth:
		return "", JoinedAutowrap, true
	case indentOK && a.end+1+b.wordW > a.avail-r.WrapMargin:
		return " ", JoinedWordwrap, true
	}
	return
}

// minAutowrapWidth is the narrowest terminal area in which a completely full
// row counts as an autowrap cut.
const minAutowrapWidth = 20

// codeish: the row looks like source code or command output that must keep its
// line breaks: indented by 4 or more columns without being the hanging indent
// of a list item, or (unless skipPunct: a full row cut mid-word by autowrap)
// ending in typical code punctuation.
func (v *rowView) codeish(hang, skipPunct bool) bool {
	if v.blank {
		return false
	}
	if v.indent >= 4 && !hang && !v.hasMarker {
		return true
	}
	if n := len(v.cells); n > 0 && !skipPunct {
		if t := v.cells[n-1].c.Text; len(t) == 1 && strings.ContainsRune(";{}()", rune(t[0])) {
			return true
		}
	}
	return false
}

// startsStructural: the row starts with punctuation or a symbol that opens a
// new construct (quote marker, table pipe, comment, option dash...), so it is
// not the continuation of a sentence. Opening brackets and quotes and inline
// code backticks stay joinable.
func (v *rowView) startsStructural() bool {
	for _, ru := range v.match {
		if ru == ' ' {
			continue
		}
		if strings.ContainsRune("([{\"'`“‘«", ru) {
			return false
		}
		return unicode.IsPunct(ru) || unicode.IsSymbol(ru)
	}
	return false
}

// fenceMask marks rows inside (and including) ``` fenced blocks.
func fenceMask(views []rowView, skip []bool) []bool {
	out := make([]bool, len(views))
	in := false
	for i := range views {
		if skip[i] || views[i].blank {
			out[i] = in
			continue
		}
		if strings.HasPrefix(strings.TrimLeft(views[i].match, " "), "```") {
			out[i] = true
			in = !in
			continue
		}
		out[i] = in
	}
	return out
}

// multiWord: the row's text contains a blank between non-blank cells.
func (v *rowView) multiWord() bool {
	seen := false
	blank := false
	for _, p := range v.cells {
		if blankText(p.c.Text) {
			blank = blank || seen
		} else if blank {
			return true
		} else {
			seen = true
		}
	}
	return false
}

// rowView is one display row prepared for reflow: frame borders stripped,
// columns rebased to the frame's inner area.
type rowView struct {
	cells []placed // inner cells, trailing blanks trimmed
	text  string   // display text (NBSP kept), indent kept, trailing blanks trimmed
	match string   // text with NBSP mapped to space, for matching only
	// indent: leading blank columns; end: column after the last non-blank
	// cell; avail: width of the area the row was laid out in.
	indent, end, avail int
	frameL, frameR     int // border columns, -1 when not framed
	blank              bool
	boxy               bool // starts with a box-drawing glyph
	verbatim           bool // whole row on one non-default background
	hasMarker          bool // starts with a list marker
	hang               int  // content column after the list marker
	firstW             int  // width of the first non-blank cell
	wordW              int  // width of the first word
}

var listMarkers = map[string]bool{
	"⏺": true, "●": true, "-": true, "*": true, "•": true, "▪": true, "◦": true,
	"‣": true, "⎿": true, "│": true, "└": true,
}

func isBorderGlyph(t string) bool { return t == "│" || t == "┃" || t == "║" }

func isBoxRune(t string) bool {
	for _, r := range t {
		return r >= 0x2500 && r <= 0x257f
	}
	return false
}

func newRowView(r Row, cols int) rowView { return newRowViewOpt(r, cols, true) }

// newRowViewOpt builds the view; strip: remove │ ┃ ║ frame borders.
func newRowViewOpt(r Row, cols int, strip bool) rowView {
	v := rowView{frameL: -1, frameR: -1, avail: cols}
	all := place(r)
	n := len(all)
	for n > 0 && blankText(all[n-1].c.Text) {
		n--
	}
	if n == 0 {
		v.blank = true
		return v
	}
	if bg := all[0].c.Bg; bg != "" {
		uniform := true
		for _, p := range all {
			if p.c.Bg != bg {
				uniform = false
				break
			}
		}
		last := all[len(all)-1]
		v.verbatim = uniform && last.col+last.w >= cols
	}
	cs := all[:n]
	f := 0
	for f < n && blankText(cs[f].c.Text) {
		f++
	}
	if strip && f < n-1 && isBorderGlyph(cs[f].c.Text) && isBorderGlyph(cs[n-1].c.Text) {
		v.frameL, v.frameR = cs[f].col, cs[n-1].col
		inner := cs[f+1 : n-1]
		base := v.frameL + cs[f].w
		pad := 0
		if len(inner) > 0 && blankText(inner[0].c.Text) {
			pad = inner[0].w
			inner = inner[1:]
		}
		base += pad
		v.avail = max(1, v.frameR-base-pad)
		m := len(inner)
		for m > 0 && blankText(inner[m-1].c.Text) {
			m--
		}
		inner = inner[:m]
		if m == 0 {
			v.blank = true
			return v
		}
		cs = make([]placed, m)
		for i, p := range inner {
			p.col -= base
			cs[i] = p
		}
	}
	v.cells = cs
	var b strings.Builder
	first := -1
	for i, p := range cs {
		b.WriteString(cellText(p.c))
		if first < 0 && !blankText(p.c.Text) {
			first = i
		}
	}
	v.text = b.String()
	v.match = strings.ReplaceAll(v.text, " ", " ")
	last := cs[len(cs)-1]
	v.end = last.col + last.w
	v.indent = cs[first].col
	v.firstW = cs[first].w
	v.boxy = isBoxRune(cs[first].c.Text)
	for i := first; i < len(cs) && !blankText(cs[i].c.Text); i++ {
		v.wordW += cs[i].w
	}
	v.hang, v.hasMarker = markerHang(cs, first)
	return v
}

// markerHang detects a list marker at cell index i (a glyph or "12." / "3)"
// followed by a blank) and returns the column where the item's text starts.
func markerHang(cs []placed, i int) (int, bool) {
	j := i
	if listMarkers[cs[i].c.Text] {
		j = i + 1
	} else {
		d := 0
		for j < len(cs) && d < 2 && len(cs[j].c.Text) == 1 && cs[j].c.Text[0] >= '0' && cs[j].c.Text[0] <= '9' {
			j++
			d++
		}
		if d == 0 || j >= len(cs) || (cs[j].c.Text != "." && cs[j].c.Text != ")") {
			return 0, false
		}
		j++
	}
	if j >= len(cs) || !blankText(cs[j].c.Text) {
		return 0, false
	}
	for j < len(cs) && blankText(cs[j].c.Text) {
		j++
	}
	if j >= len(cs) {
		return 0, false
	}
	return cs[j].col, true
}

// promptLike: the row starts with the prompt marker or a menu pointer.
func (v *rowView) promptLike(r Rules) bool {
	t := strings.TrimLeft(v.match, " ")
	check := func(g string) bool {
		if g == "" || !strings.HasPrefix(t, g) {
			return false
		}
		rest := t[len(g):]
		return rest == "" || rest[0] == ' '
	}
	if check(r.Ghost.PromptMarker) {
		return true
	}
	for _, p := range r.pointers() {
		if check(p) {
			return true
		}
	}
	return false
}
