package screen

// Option detection (design: docs/design/api/semantic-model.md §6): numbered
// lists, pointer menus, checkbox lists and yes/no prompts found on a Snapshot,
// the footer hints that go with them, and PlanChoose, the pure key plan that
// selects one option. Nothing here sends keys or reads the live session; the
// daemon verifies every step against fresh snapshots (op act, later).
//
// Ambiguity rules (documented deviations from the design are marked D):
//   - The input row passed by the caller is never part of a menu, and rows
//     below it (footer) are out of scope. Claude Code's "❯" prompt therefore
//     never becomes a menu row when the region split identified it.
//   - Independently of that, a pointer menu needs aligned siblings (same label
//     column, blank pointer column) and is discarded when the cursor is
//     visible on one of its rows (that is an input line being edited).
//   - A numbered list without pointer or selected style is prose unless (a) it
//     is the last content block: only blank rows, rules/borders and hint-only
//     rows ("Esc to cancel") follow it up to the input row (or the bottom of
//     the screen when there is no input row), and (b) a question line (ending
//     in "?") sits directly above it (at most one blank row between); a hint
//     row alone is not enough. Then it gets confidence 0.7, but only 0.5 (below
//     the default MinConfidence) when an input row is visible: Claude Code and
//     Codex replace the input box with a real dialog or draw a pointer, so an
//     unstyled list next to a live input box is transcript prose.
//   - A pointer menu whose pointer is a glyph needs the same "last content
//     block" position (or a frame), a pointer label of at most 80 runes, and no
//     transcript markers (⎿ ● ⏺) at the start of its rows: tool output such as
//     "❯ cmd" followed by indented "⎿ ok" rows is not a menu.
//   - A yes/no prompt must be on the cursor row or be the last content row
//     above the input row / bottom of the screen, and not start with a
//     transcript marker.
//   - D: option IDs hash the dialog question (whitespace- and case-insensitive,
//     the question can re-wrap with the terminal size), the kind and the
//     normalised labels; Option.MenuFingerprint is the menu-level part. Two
//     consecutive dialogs with the same labels but different questions
//     ("Do you want to edit a.go?" / "...b.go?") therefore get different IDs.
//     Limitations: the question is the contiguous non-blank block (at most
//     ContextRows rows) above the first option, so a question that wraps into
//     more than ContextRows rows at a narrow width fingerprints differently
//     than at a wide one; a menu that scrolls (viewport slides over a longer
//     list) or whose labels are truncated with "…" at a different width yields
//     different labels and so different IDs. Callers must treat IDs as valid
//     only for the snapshot they came from plus fingerprint-equal redraws.

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Option kinds.
const (
	KindNumbered = "numbered"
	KindPointer  = "pointer"
	KindCheckbox = "checkbox"
	KindYesNo    = "yesno"
)

// Option is one selectable entry of a detected menu.
type Option struct {
	// ID is "o" + 8 hex of sha1(menu fingerprint, index, label); identical for
	// the same menu (same question, kind and labels) across redraws, pointer
	// moves and terminal size changes.
	ID string
	// MenuFingerprint identifies the whole menu: "m" + 12 hex of
	// sha1(kind, normalised question, normalised labels). Equal on every
	// option of the menu; the executor of a choose plan compares it before
	// sending Enter (see Expect.Fingerprint).
	MenuFingerprint string
	Index           int // 1-based display order
	Row             int // screen row of the option's first line
	Label           string
	Kind            string
	// Selected: the pointer / selected style is on this option (yes/no: the
	// upper-case default of "[Y/n]").
	Selected bool
	Checked  *bool // checkbox lists only
	// Keys is the direct key ("2", "y"); empty = navigation only.
	Keys string
	// Submit: Keys must be followed by the submit key (line-mode yes/no with
	// the cursor right after the prompt).
	Submit     bool
	Confidence float64
}

// Hint is a key and what it does ("esc", "cancel").
type Hint struct{ Key, Action string }

// MenuRules is the compiled form of the profile's menu_* keys.
type MenuRules struct {
	// Pointers are the glyphs that mark the current row ("❯", "›").
	Pointers []string
	// NumberRegex matches a numbered row; named groups n (or n2), label and
	// optionally ptr. nil = DefaultNumberRegex.
	NumberRegex *regexp.Regexp
	// SelectedStyles are Attr names ("reverse", "bold"...) marking the selected
	// row when no pointer is drawn.
	SelectedStyles []string
	// HintRegex matches footer hints; groups 1 and 2 = key and action. nil =
	// DefaultHintRegex.
	HintRegex *regexp.Regexp
	// MinConfidence drops options below it (0 = keep all).
	MinConfidence float64
	// ContextRows is how many rows above a numbered list are searched for its
	// question (0 = 6).
	ContextRows int
	// ScopeRows is how many non-blank rows above the input row are searched
	// (0 = 15).
	ScopeRows int
}

// Default menu regexes (design §6.1, §6.4). NBSP is normalised to a space
// before matching; box borders (│ ┃ ║) are blanked.
const (
	DefaultNumberPattern = `^\s*(?:(?P<ptr>[^\w\s])\s*)?(?:(?P<n>\d{1,2})[.)]|\[(?P<n2>\d{1,2})\])\s+(?P<label>\S.*)$`
	// DefaultHintPattern requires "to": "Esc to cancel" is a hint, "enter the
	// value" is prose. No \b before the key: it must also match "↑/↓ to select".
	DefaultHintPattern = `(?i)(?:^|[^\w])(esc|enter|tab|shift\+tab|ctrl\+[a-z]|space|↑/↓|←/→|↑|↓)\s+to\s+([a-z][\w '-]{1,30})`
	// DefaultMenuPointers is the built-in glyph set.
)

// DefaultMenuPointers are the built-in pointer glyphs.
// ">" is deliberately absent (blockquotes, shell prompts); it stays
// configurable per profile (menu_pointers).
var DefaultMenuPointers = []string{"❯", "›", "▶", "▸", "➜", "→"}

var (
	// DefaultNumberRegex and DefaultHintRegex are the compiled defaults.
	DefaultNumberRegex = regexp.MustCompile(DefaultNumberPattern)
	DefaultHintRegex   = regexp.MustCompile(DefaultHintPattern)

	reYesNo    = regexp.MustCompile(`(?i)[\[(]\s*(y(?:es)?)\s*/\s*(n(?:o)?)\s*[\])]`)
	reCheckbox = regexp.MustCompile(`^\s*(?:(?P<ptr>[^\w\s])\s+)?(?P<box>\[[ xX✓✔*]\]|[◯◉○●☐☑☒◻◼])\s*(?P<label>\S.*)$`)
)

// DefaultMenuRules returns the built-in default profile's rules.
func DefaultMenuRules() MenuRules {
	return MenuRules{
		Pointers:       append([]string(nil), DefaultMenuPointers...),
		NumberRegex:    DefaultNumberRegex,
		SelectedStyles: []string{"reverse"},
		HintRegex:      DefaultHintRegex,
		MinConfidence:  0.6,
		ContextRows:    6,
		ScopeRows:      15,
	}
}

// optLine is one screen row prepared for matching.
type optLine struct {
	str    string // one rune per cell, NBSP and borders as spaces (matching only)
	runes  []rune
	cells  []Cell
	first  int // first non-blank column, -1 if blank
	inView bool
	framed bool // a │ ┃ ║ border was blanked at the start or end of the row
}

func (l *optLine) blank() bool { return !l.inView || l.first < 0 }

// contRune stands for a Width:0 continuation cell; stripped from labels.
const contRune = '\x00'

// prepLine keeps str/runes at one rune per cell so that rune index == column
// (regex offsets map back to columns); labels are rebuilt from the full cell
// text with sub, so combining marks and ZWJ sequences survive.
func prepLine(r Row) optLine {
	rs := make([]rune, len(r.Cells))
	for i, c := range r.Cells {
		ru := ' '
		if i > 0 && c.Width == 0 && c.Text == "" && r.Cells[i-1].Width == 2 {
			ru = contRune // wide-char continuation: keeps index == column
		} else if c.Text != "" {
			ru, _ = utf8.DecodeRuneInString(c.Text)
			if ru == 0xa0 || ru == utf8.RuneError {
				ru = ' '
			}
		}
		rs[i] = ru
	}
	isBorder := func(ru rune) bool { return ru == '│' || ru == '┃' || ru == '║' }
	framed := false
	for i := 0; i < len(rs); i++ {
		if rs[i] == ' ' {
			continue
		}
		if isBorder(rs[i]) {
			rs[i] = ' '
			framed = true
		}
		break
	}
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i] == ' ' {
			continue
		}
		if isBorder(rs[i]) {
			rs[i] = ' '
			framed = true
		}
		break
	}
	l := optLine{runes: rs, cells: r.Cells, first: -1, inView: true, str: string(rs), framed: framed}
	for i, ru := range rs {
		if ru != ' ' {
			l.first = i
			break
		}
	}
	return l
}

func (l *optLine) text() string { return clean(l.sub(0, len(l.runes))) }

// sub returns the display text of columns [c0, c1): the full cell text
// (grapheme clusters intact), blanked cells (borders, NBSP) as spaces,
// continuation cells dropped.
func (l *optLine) sub(c0, c1 int) string {
	c0, c1 = max(c0, 0), min(c1, len(l.runes))
	var b strings.Builder
	for i := c0; i < c1; i++ {
		switch {
		case l.runes[i] == contRune:
		case l.runes[i] == ' ' || l.cells[i].Text == "":
			b.WriteByte(' ')
		default:
			b.WriteString(l.cells[i].Text)
		}
	}
	return b.String()
}

func clean(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, string(contRune), ""))
}

func (l *optLine) colOf(byteOff int) int {
	if byteOff < 0 || byteOff > len(l.str) {
		return -1
	}
	return utf8.RuneCountInString(l.str[:byteOff])
}

// ruleLine: a non-blank row made only of box-drawing glyphs (rules, frame
// corners and bottoms).
func (l *optLine) ruleLine() bool {
	if l.blank() {
		return false
	}
	for _, ru := range l.runes {
		if ru != ' ' && ru != contRune && (ru < 0x2500 || ru > 0x257f) {
			return false
		}
	}
	return true
}

// styledFrom: every text cell from col on carries one of the styles.
func (l *optLine) styledFrom(col int, styles []string) bool {
	if len(styles) == 0 {
		return false
	}
	n := 0
	for i := col; i < len(l.cells); i++ {
		c := l.cells[i]
		if c.Text == "" || strings.TrimSpace(c.Text) == "" {
			continue
		}
		ok := false
		for _, s := range styles {
			for _, nm := range c.Attrs.Names() {
				if nm == s {
					ok = true
				}
			}
		}
		if !ok {
			return false
		}
		n++
	}
	return n > 0
}

// hintOnly: the row is a footer hint line ("Esc to cancel · Enter to
// confirm"): the hint regex matches at its start and it has few words.
func (l *optLine) hintOnly(r MenuRules) bool {
	if l.blank() {
		return false
	}
	t := l.text()
	m := r.hintRe().FindStringSubmatchIndex(t)
	if m == nil || len(m) < 4 || m[2] < 0 {
		return false
	}
	return strings.Trim(t[:m[2]], " ·•|-–—*") == "" && len(strings.Fields(t)) <= 12
}

// tailClear: rows [from, end) hold only blanks, rules/borders and hint-only
// rows, i.e. nothing but the input box area follows.
func tailClear(lines []optLine, from, end int, r MenuRules) bool {
	for k := from; k < end && k < len(lines); k++ {
		l := &lines[k]
		if l.blank() || l.ruleLine() || l.hintOnly(r) {
			continue
		}
		return false
	}
	return true
}

// questionAbove returns the contiguous block of non-blank rows (at most
// ContextRows, no rules) directly above row y, skipping one blank row, joined
// with spaces.
func questionAbove(lines []optLine, y int, r MenuRules) string {
	k := y - 1
	if k >= 0 && lines[k].blank() {
		k--
	}
	var rows []string
	for ; k >= 0 && len(rows) < r.contextRows(); k-- {
		if lines[k].blank() || lines[k].ruleLine() {
			break
		}
		rows = append(rows, lines[k].text())
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return strings.Join(rows, " ")
}

// questionAdjacent: the row directly above y (or above one blank row) ends
// with "?".
func questionAdjacent(lines []optLine, y int) bool {
	k := y - 1
	if k >= 0 && lines[k].blank() {
		k--
	}
	return k >= 0 && !lines[k].blank() && strings.HasSuffix(lines[k].text(), "?")
}

type menu struct {
	opts []Option
	end  int // last screen row used
	conf float64
	// question is the yes/no prompt text (list menus derive it from the rows
	// above their first option).
	question string
}

// DetectOptions returns the options of the menu or prompt on screen, or nil.
// inputRow is the screen row of the harness input line (from the region
// split), -1 when unknown; it and everything below it are ignored. At most one
// menu is returned: the lowest one on screen, lists before yes/no prompts.
func DetectOptions(s *Snapshot, inputRow int, r MenuRules) []Option {
	if s == nil || len(s.Screen) == 0 {
		return nil
	}
	lines, start, end := scopeLines(s, inputRow, r)
	hasInput := inputRow >= 0 && inputRow < len(s.Screen)
	var cands []menu
	used := map[int]bool{}
	for _, m := range detectNumbered(s, lines, start, end, r, hasInput) {
		cands = append(cands, m)
		markUsed(used, m)
	}
	for _, m := range detectCheckbox(s, lines, start, end, r, hasInput) {
		cands = append(cands, m)
		markUsed(used, m)
	}
	cands = append(cands, detectPointer(s, lines, start, end, r, used)...)
	var best *menu
	for i := range cands {
		m := &cands[i]
		if m.conf < r.MinConfidence {
			continue
		}
		if best == nil || m.end > best.end || (m.end == best.end && m.conf > best.conf) {
			best = m
		}
	}
	if best != nil {
		finish(best.opts, questionAbove(lines, best.opts[0].Row, r))
		return best.opts
	}
	if m := detectYesNo(s, lines, start, end, r); m != nil && m.conf >= r.MinConfidence {
		finish(m.opts, m.question)
		return m.opts
	}
	return nil
}

// DetectHints returns the footer hints ("Esc to cancel") found in the scope
// rows, in screen order.
func DetectHints(s *Snapshot, inputRow int, r MenuRules) []Hint {
	if s == nil {
		return nil
	}
	lines, start, end := scopeLines(s, inputRow, r)
	re := r.HintRegex
	if re == nil {
		re = DefaultHintRegex
	}
	var out []Hint
	for y := start; y < end; y++ {
		if lines[y].blank() {
			continue
		}
		for _, m := range re.FindAllStringSubmatch(lines[y].text(), -1) {
			if len(m) >= 3 {
				out = append(out, Hint{Key: strings.ToLower(m[1]), Action: strings.ToLower(strings.TrimSpace(m[2]))})
			}
		}
	}
	return out
}

func markUsed(used map[int]bool, m menu) {
	for _, o := range m.opts {
		used[o.Row] = true
	}
}

// scopeLines prepares every row; rows outside [start,end) come back blank.
func scopeLines(s *Snapshot, inputRow int, r MenuRules) ([]optLine, int, int) {
	n := len(s.Screen)
	end := n
	if inputRow >= 0 && inputRow < n {
		end = inputRow
	}
	scope := r.ScopeRows
	if scope <= 0 {
		scope = 15
	}
	lines := make([]optLine, n)
	for y := range lines {
		lines[y] = optLine{first: -1}
	}
	start := end
	cnt := 0
	for y := end - 1; y >= 0 && cnt < scope; y-- {
		l := prepLine(s.Screen[y])
		lines[y] = l
		start = y
		if l.first >= 0 {
			cnt++
		}
	}
	return lines, start, end
}

func (r MenuRules) isPointer(g string) bool {
	for _, p := range r.Pointers {
		if p == g {
			return true
		}
	}
	return false
}

func (r MenuRules) hintRe() *regexp.Regexp {
	if r.HintRegex != nil {
		return r.HintRegex
	}
	return DefaultHintRegex
}

func (r MenuRules) contextRows() int {
	if r.ContextRows <= 0 {
		return 6
	}
	return r.ContextRows
}

// isContinuation: row continues the previous option label (indented to the
// label column, not a hint row).
func isContinuation(l *optLine, labelCol int, r MenuRules) bool {
	return !l.blank() && l.first >= labelCol && !r.hintRe().MatchString(l.text())
}

func detectNumbered(s *Snapshot, lines []optLine, start, end int, r MenuRules, hasInput bool) []menu {
	re := r.NumberRegex
	if re == nil {
		re = DefaultNumberRegex
	}
	iN, iN2, iL, iP := re.SubexpIndex("n"), re.SubexpIndex("n2"), re.SubexpIndex("label"), re.SubexpIndex("ptr")
	type nrow struct {
		n, numCol, labelCol int
		label               string
		ptr                 bool
	}
	parse := func(y int) (nrow, bool) {
		var z nrow
		l := &lines[y]
		if l.blank() || iL < 0 || (iN < 0 && iN2 < 0) {
			return z, false
		}
		m := re.FindStringSubmatchIndex(l.str)
		if m == nil || m[2*iL] < 0 {
			return z, false
		}
		var num string
		switch {
		case iN >= 0 && m[2*iN] >= 0:
			num = l.str[m[2*iN]:m[2*iN+1]]
			z.numCol = l.colOf(m[2*iN])
		case iN2 >= 0 && m[2*iN2] >= 0:
			num = l.str[m[2*iN2]:m[2*iN2+1]]
			z.numCol = l.colOf(m[2*iN2]) - 1
		default:
			return z, false
		}
		z.n, _ = strconv.Atoi(num)
		z.labelCol = l.colOf(m[2*iL])
		z.label = clean(l.sub(z.labelCol, l.colOf(m[2*iL+1])))
		if iP >= 0 && m[2*iP] >= 0 {
			z.ptr = r.isPointer(l.str[m[2*iP]:m[2*iP+1]])
		}
		return z, true
	}
	var out []menu
	for y := start; y < end; y++ {
		first, ok := parse(y)
		if !ok || first.n != 1 {
			continue
		}
		type item struct {
			nrow
			y      int
			styled bool
		}
		items := []item{{first, y, lines[y].styledFrom(first.labelCol, r.SelectedStyles)}}
		last := y
		for k := y + 1; k < end; k++ {
			if nr, ok := parse(k); ok && nr.n == items[len(items)-1].n+1 && nr.numCol == first.numCol {
				items = append(items, item{nr, k, lines[k].styledFrom(nr.labelCol, r.SelectedStyles)})
				last = k
				continue
			}
			if !lines[k].blank() && isContinuation(&lines[k], items[len(items)-1].labelCol, r) {
				it := &items[len(items)-1]
				it.label += " " + lines[k].text()
				last = k
				continue
			}
			break
		}
		if len(items) < 2 {
			continue
		}
		m := menu{end: last}
		sel, styled := -1, 0
		for i, it := range items {
			if it.ptr && sel < 0 {
				sel = i
			}
			if it.styled {
				styled++
			}
		}
		conf := 0.0
		switch {
		case sel >= 0:
			conf = 0.9
		case styled == 1 && len(items) >= 2:
			for i, it := range items {
				if it.styled {
					sel = i
				}
			}
			conf = 0.9
		default:
			// Unstyled: prose unless it is the last content block with its
			// question directly above (or a hint row below); with a live
			// input row on screen it stays under the confidence floor.
			if tailClear(lines, last+1, end, r) && questionAdjacent(lines, y) {
				conf = 0.7
				if hasInput {
					conf = 0.5
				}
			}
		}
		if conf == 0 {
			y = last
			continue
		}
		m.conf = conf
		for i, it := range items {
			o := Option{Index: i + 1, Row: it.y, Label: it.label, Kind: KindNumbered, Selected: i == sel, Confidence: conf}
			if it.n <= 9 {
				o.Keys = strconv.Itoa(it.n)
			}
			m.opts = append(m.opts, o)
		}
		out = append(out, m)
		y = last
	}
	return out
}

func detectCheckbox(s *Snapshot, lines []optLine, start, end int, r MenuRules, hasInput bool) []menu {
	iP, iB, iL := reCheckbox.SubexpIndex("ptr"), reCheckbox.SubexpIndex("box"), reCheckbox.SubexpIndex("label")
	type crow struct {
		boxCol, labelCol int
		label            string
		checked, ptr     bool
	}
	parse := func(y int) (crow, bool) {
		var z crow
		l := &lines[y]
		if l.blank() {
			return z, false
		}
		m := reCheckbox.FindStringSubmatchIndex(l.str)
		if m == nil {
			return z, false
		}
		if m[2*iP] >= 0 {
			if !r.isPointer(l.str[m[2*iP]:m[2*iP+1]]) {
				return z, false
			}
			z.ptr = true
		}
		box := l.str[m[2*iB]:m[2*iB+1]]
		z.checked = strings.ContainsAny(box, "xX✓✔*◉●☑☒◼")
		z.boxCol = l.colOf(m[2*iB])
		z.labelCol = l.colOf(m[2*iL])
		z.label = clean(l.sub(z.labelCol, l.colOf(m[2*iL+1])))
		return z, true
	}
	var out []menu
	for y := start; y < end; y++ {
		first, ok := parse(y)
		if !ok {
			continue
		}
		type item struct {
			crow
			y int
		}
		items := []item{{first, y}}
		last := y
		for k := y + 1; k < end; k++ {
			if cr, ok := parse(k); ok && cr.boxCol == first.boxCol {
				items = append(items, item{cr, k})
				last = k
				continue
			}
			if isContinuation(&lines[k], items[len(items)-1].labelCol, r) {
				items[len(items)-1].label += " " + lines[k].text()
				last = k
				continue
			}
			break
		}
		if len(items) < 2 {
			y = last
			continue
		}
		sel, styled := -1, 0
		for i, it := range items {
			if it.ptr && sel < 0 {
				sel = i
			}
			if lines[it.y].styledFrom(it.labelCol, r.SelectedStyles) {
				styled++
				if sel < 0 {
					sel = i
				}
			}
		}
		conf := 0.7
		if sel >= 0 {
			conf = 0.9
		} else if !tailClear(lines, last+1, end, r) || !questionAdjacent(lines, y) {
			// Unstyled box glyphs ("● one", "○ two") are as often bullets of
			// prose as a checkbox list: same position and question rule as
			// unstyled numbered lists.
			y = last
			continue
		} else if hasInput {
			conf = 0.5
		}
		m := menu{end: last, conf: conf}
		for i, it := range items {
			c := it.checked
			m.opts = append(m.opts, Option{Index: i + 1, Row: it.y, Label: it.label, Kind: KindCheckbox,
				Selected: i == sel, Checked: &c, Confidence: conf})
		}
		out = append(out, m)
		y = last
	}
	return out
}

// detectPointer finds menus of plain rows where one row carries a pointer glyph
// (or a selected style) and the others are aligned siblings.
func detectPointer(s *Snapshot, lines []optLine, start, end int, r MenuRules, used map[int]bool) []menu {
	ok := func(y int) bool { return y >= start && y < end && !used[y] && !lines[y].blank() }
	var out []menu
	taken := map[int]bool{}
	for y := start; y < end; y++ {
		if !ok(y) || taken[y] {
			continue
		}
		l := &lines[y]
		p := l.first
		g := string(l.runes[p])
		styleOnly := false
		labelCol := -1
		if r.isPointer(g) && p+2 < len(l.runes) && l.runes[p+1] == ' ' {
			for c := p + 1; c < len(l.runes); c++ {
				if l.runes[c] != ' ' {
					labelCol = c
					break
				}
			}
		}
		if labelCol < 0 {
			if !l.styledFrom(p, r.SelectedStyles) {
				continue
			}
			styleOnly, labelCol = true, p
		}
		if s.CursorHidden == false && s.Cursor.Y == y {
			continue // cursor on the row: an input line, not a menu
		}
		sibling := func(k int) bool {
			if !ok(k) {
				return false
			}
			lk := &lines[k]
			if lk.first != labelCol {
				return false
			}
			if styleOnly {
				return !lk.styledFrom(labelCol, r.SelectedStyles)
			}
			return true
		}
		cont := func(k int) bool {
			return ok(k) && lines[k].first > labelCol && !r.hintRe().MatchString(lines[k].text())
		}
		// Extend the block up and down from the pointer row.
		top := y
		for k := y - 1; k >= start; k-- {
			if sibling(k) || (cont(k) && k > start) {
				top = k
				continue
			}
			break
		}
		for top < y && !sibling(top) {
			top++ // block must start on an option row, not a continuation
		}
		bot := y
		for k := y + 1; k < end; k++ {
			if sibling(k) || cont(k) {
				bot = k
				continue
			}
			break
		}
		type item struct {
			y     int
			label string
			sel   bool
		}
		var items []item
		for k := top; k <= bot; k++ {
			lk := &lines[k]
			lab := clean(lk.sub(labelCol, len(lk.runes)))
			if k == y || sibling(k) {
				items = append(items, item{k, lab, k == y})
			} else if len(items) > 0 {
				items[len(items)-1].label += " " + lab
			}
			taken[k] = true
		}
		minItems := 2
		if styleOnly {
			minItems = 3
		}
		if len(items) < minItems {
			continue
		}
		if s.CursorHidden == false {
			for _, it := range items {
				if s.Cursor.Y == it.y {
					items = nil
					break
				}
			}
			if items == nil {
				continue
			}
		}
		rows := make([]int, len(items))
		for i, it := range items {
			rows[i] = it.y
		}
		if !pointerMenuPlausible(lines, rows, y, bot, end, labelCol, r) {
			continue
		}
		conf := 0.7
		switch {
		case styleOnly && s.CursorHidden:
			conf = 0.75
		case styleOnly:
			conf = 0.5
		case s.CursorHidden && len(items) == 2:
			conf = 0.8
		case s.CursorHidden:
			conf = 0.85
		case len(items) == 2:
			conf = 0.5
		}
		m := menu{end: bot, conf: conf}
		for i, it := range items {
			m.opts = append(m.opts, Option{Index: i + 1, Row: it.y, Label: it.label, Kind: KindPointer, Selected: it.sel, Confidence: conf})
		}
		out = append(out, m)
	}
	return out
}

// pointerMenuPlausible rejects pointer / selected-row blocks that are tool
// output or prose rather than a live menu: the pointer label must be short,
// no row may start with a transcript marker, and the menu must be the last
// content block (only blanks, rules and hint rows below it) or sit in a frame.
func pointerMenuPlausible(lines []optLine, rows []int, ptrRow, bot, end, labelCol int, r MenuRules) bool {
	framed := true
	for _, y := range rows {
		l := &lines[y]
		switch l.runes[l.first] {
		case '⎿', '●', '⏺':
			return false
		}
		framed = framed && l.framed
	}
	if utf8.RuneCountInString(lines[ptrRow].text()) > 82 { // pointer + blank + 80-rune label
		return false
	}
	return framed || tailClear(lines, bot+1, end, r)
}

// detectYesNo finds "[y/N]" / "(yes/no)" on the cursor row or on the last
// content row (rules and blanks skipped) above the input row / bottom of the
// screen. Keys is the first letter ("y"/"n"); in line mode (cursor on the
// prompt row after the prompt) the answer must be submitted, and the typed word
// is the one the prompt shows ("yes" for "(yes/no)", "y" for "[y/n]").
func detectYesNo(s *Snapshot, lines []optLine, start, end int, r MenuRules) *menu {
	y := -1
	if !s.CursorHidden && s.Cursor.Y >= start && s.Cursor.Y < end && !lines[s.Cursor.Y].blank() &&
		reYesNo.MatchString(lines[s.Cursor.Y].str) {
		y = s.Cursor.Y
	}
	if y < 0 {
		y = end - 1
		for y >= start && (lines[y].blank() || lines[y].ruleLine()) {
			y--
		}
	}
	if y < start {
		return nil
	}
	l := &lines[y]
	m := reYesNo.FindStringSubmatchIndex(l.str)
	if m == nil {
		return nil
	}
	switch l.runes[l.first] {
	case '⎿', '●', '⏺':
		return nil // transcript output quoting a prompt
	}
	yes, no := l.str[m[2]:m[3]], l.str[m[4]:m[5]]
	lineMode := !s.CursorHidden && s.Cursor.Y == y && s.Cursor.X >= l.colOf(m[1])
	mk := func(i int, label, tok string) Option {
		sel := unicode.IsUpper(rune(tok[0]))
		key := strings.ToLower(tok[:1])
		if lineMode && strings.EqualFold(tok, "yes") || lineMode && strings.EqualFold(tok, "no") {
			key = strings.ToLower(tok)
		}
		return Option{Index: i, Row: y, Label: label, Kind: KindYesNo, Selected: sel,
			Keys: key, Submit: lineMode, Confidence: 0.8}
	}
	q := clean(l.sub(0, l.colOf(m[1])))
	return &menu{end: y, conf: 0.8, question: q, opts: []Option{mk(1, "Yes", yes), mk(2, "No", no)}}
}

// finish assigns the stable IDs and the menu fingerprint. question is the
// raw question text above the options (normalised here).
func finish(opts []Option, question string) {
	labels := make([]string, len(opts))
	for i, o := range opts {
		labels[i] = normLabel(o.Label)
	}
	kind := ""
	if len(opts) > 0 {
		kind = opts[0].Kind
	}
	h := sha1.Sum([]byte(kind + "\x00" + normLabel(question) + "\x00" + strings.Join(labels, "\x01")))
	fp := "m" + hex.EncodeToString(h[:])[:12]
	for i := range opts {
		h := sha1.Sum([]byte(fp + "\x00" + strconv.Itoa(opts[i].Index) + "\x00" + labels[i]))
		opts[i].ID = "o" + hex.EncodeToString(h[:])[:8]
		opts[i].MenuFingerprint = fp
	}
}

func normLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if !unicode.IsSpace(r) && r != '…' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---- choose plan ----

// Expect is what the executor must observe after a step before going on.
// SelectedID: the option with that ID is selected; Gone: the option list
// disappeared. When both are set either satisfies. Checked (checkbox toggle):
// the target's checked state equals it. The zero Expect means nothing to check.
//
// Fingerprint is the MenuFingerprint of the menu the plan was made for. The
// executor (op act choose, daemon) MUST:
//   - after every key wait for a snapshot with a newer OutputSeq than the one
//     before the key AND a screen that changed, before evaluating Expect (a
//     stale snapshot still shows the old state and would satisfy anything);
//   - evaluate SelectedID / Checked only against a menu whose MenuFingerprint
//     equals Fingerprint (an identical-looking dialog with another question has
//     another fingerprint and must not satisfy the step);
//   - treat Gone as satisfied only when no menu with that Fingerprint is on
//     screen; a different menu appearing in its place is reported, not chosen
//     from, and never gets further keys of this plan.
//
// Instance is the identity of the menu INSTANCE the plan was made for: the
// fingerprint plus a context hash (MenuContext: the rows above the dialog
// question) and, at the daemon, the transcript length/tail. Two dialogs with
// the same question and labels but shown for different commands share a
// fingerprint and differ in instance. The executor fills it (the plan
// builder cannot know it) and applies every rule above to "same fingerprint AND
// same instance": a menu with another instance is another dialog (Gone is
// satisfied, SelectedID is not).
type Expect struct {
	SelectedID  string
	Gone        bool
	Checked     *bool
	Fingerprint string
	Instance    string
}

// KeyStep is one write to the PTY.
type KeyStep struct {
	Keys   string
	Expect Expect
	// IfNotGone: send only when the previous step's Expect held through
	// SelectedID but the list is still there (digit moved the pointer only).
	// The executor MUST send this Enter only if the menu with the same
	// Expect.Fingerprint (question + labels) and the same Expect.Instance is
	// still on screen AND its pointer
	// is on the target (the previous step's SelectedID) in a snapshot newer
	// than the one taken before the previous key. If the menu is gone, or a
	// different or identical-looking new dialog appeared (other fingerprint, or
	// same fingerprint but an OutputSeq-ordered state showing the choice was
	// already consumed), the Enter must not be sent: it could confirm the wrong
	// dialog.
	IfNotGone bool
}

// ChooseKeys are the profile's key settings for PlanChooseKeys.
type ChooseKeys struct {
	Select string // "auto" (default), "digit", "arrows"
	// DigitAction (profile menu_digit_action) says what a digit does in a
	// numbered menu: "select" = it chooses and confirms by itself, so the plan
	// is the digit ONLY (never an Enter afterwards: a late Enter would confirm
	// whatever dialog came next); "move" (also "") = it only moves the pointer,
	// so the plan is the digit and then an Enter that runs only when the same
	// menu instance is still up with the pointer on the target.
	DigitAction string
	NavUp       string // "" or "auto": CSI/SS3 by application cursor mode
	NavDown     string
	Submit      string // "" = "\r"
	Toggle      string // "" = " "
}

// PlanChoose plans the keys that select target among opts with default keys:
// the digit for numbered options, the letter for yes/no, otherwise arrow steps
// from the selected option (SS3 arrows when appCursor) and Enter. It returns
// nil when the option is not navigable (target unknown, or no known current
// selection and no direct key); callers fall back to raw keys.
func PlanChoose(opts []Option, target Option, appCursor bool) []KeyStep {
	return PlanChooseKeys(opts, target, appCursor, ChooseKeys{})
}

// PlanChooseKeys is PlanChoose with explicit key settings.
func PlanChooseKeys(opts []Option, target Option, appCursor bool, k ChooseKeys) []KeyStep {
	steps := planChooseKeys(opts, target, appCursor, k)
	for i := range steps {
		for _, o := range opts {
			if o.ID == target.ID {
				steps[i].Expect.Fingerprint = o.MenuFingerprint
			}
		}
	}
	return steps
}

func planChooseKeys(opts []Option, target Option, appCursor bool, k ChooseKeys) []KeyStep {
	ti := -1
	for i, o := range opts {
		if o.ID == target.ID {
			ti = i
		}
	}
	if ti < 0 {
		return nil
	}
	target = opts[ti]
	submit := k.Submit
	if submit == "" {
		submit = "\r"
	}
	toggle := k.Toggle
	if toggle == "" {
		toggle = " "
	}
	switch target.Kind {
	case KindYesNo:
		if target.Keys == "" {
			return nil
		}
		steps := []KeyStep{{Keys: target.Keys}}
		if target.Submit {
			steps = append(steps, KeyStep{Keys: submit, Expect: Expect{Gone: true}})
		}
		return steps
	case KindNumbered:
		if target.Keys != "" && k.Select != "arrows" {
			if k.DigitAction == "select" {
				// Verified by the menu instance disappearing or changing.
				return []KeyStep{{Keys: target.Keys, Expect: Expect{Gone: true}}}
			}
			return []KeyStep{
				{Keys: target.Keys, Expect: Expect{SelectedID: target.ID, Gone: true}},
				{Keys: submit, Expect: Expect{Gone: true}, IfNotGone: true},
			}
		}
		if k.Select == "digit" {
			return nil
		}
	}
	cur := -1
	for i, o := range opts {
		if o.Selected {
			cur = i
			break
		}
	}
	if cur < 0 {
		return nil
	}
	up, down := k.NavUp, k.NavDown
	if appCursor {
		if up == "" || up == "auto" {
			up = "\x1bOA"
		}
		if down == "" || down == "auto" {
			down = "\x1bOB"
		}
	} else {
		if up == "" || up == "auto" {
			up = "\x1b[A"
		}
		if down == "" || down == "auto" {
			down = "\x1b[B"
		}
	}
	var steps []KeyStep
	for cur != ti {
		if ti > cur {
			cur++
			steps = append(steps, KeyStep{Keys: down, Expect: Expect{SelectedID: opts[cur].ID}})
		} else {
			cur--
			steps = append(steps, KeyStep{Keys: up, Expect: Expect{SelectedID: opts[cur].ID}})
		}
	}
	if target.Kind == KindCheckbox {
		var want *bool
		if target.Checked != nil {
			v := !*target.Checked
			want = &v
		}
		return append(steps, KeyStep{Keys: toggle, Expect: Expect{SelectedID: target.ID, Checked: want}})
	}
	return append(steps, KeyStep{Keys: submit, Expect: Expect{Gone: true}})
}

// MenuContextRows is how many non-blank rows above a dialog question feed
// MenuContext.
const MenuContextRows = 8

// MenuContext hashes the screen context of the menu whose options are opts:
// the non-blank rows above the dialog question (the contiguous block above the
// first option, at most 12 rows), at most MenuContextRows of them, normalised
// like labels. It tells apart two dialogs that share question and labels but
// are shown for different things ("Command: rm a" / "Command: rm b" above an
// identical "Proceed?"). "" when opts is empty. Rows of the menu itself are
// not part of it, so a moving pointer does not change it.
func MenuContext(s *Snapshot, opts []Option) string {
	if len(opts) == 0 || s == nil {
		return ""
	}
	lines := s.Lines()
	first := opts[0].Row
	for _, o := range opts {
		first = min(first, o.Row)
	}
	if first >= len(lines) {
		return ""
	}
	y := first - 1
	for n := 0; y >= 0 && strings.TrimSpace(lines[y]) != "" && n < 12; n++ {
		y--
	}
	var ctx []string
	for ; y >= 0 && len(ctx) < MenuContextRows; y-- {
		if n := normLabel(lines[y]); n != "" {
			ctx = append(ctx, n)
		}
	}
	h := sha1.Sum([]byte(strings.Join(ctx, "\x01")))
	return hex.EncodeToString(h[:])[:12]
}
