package screen

import (
	"regexp"
	"strings"
	"time"
)

// Region is the role of one screen row (semantic-model §4).
type Region string

// Row regions. RegionRule marks horizontal rules and border-only rows: they
// separate regions and never appear in any text.
const (
	RegionTranscript Region = "transcript"
	RegionInput      Region = "input"
	RegionFooter     Region = "footer"
	RegionDialog     Region = "dialog"
	RegionStatus     Region = "status"
	RegionRule       Region = "rule"
)

// Input states (same values as delivery.InputState).
const (
	InputEmpty      = "empty"
	InputSuggestion = "suggestion"
	InputDraft      = "draft"
)

// DraftMaxAge is how long a keystroke-based pending draft counts.
const DraftMaxAge = 10 * time.Minute

// InputInfo describes the harness input line and its box.
type InputInfo struct {
	// Text is the box content after the prompt marker, ghost text removed;
	// multi-row drafts are joined with "\n".
	Text string
	// Ghost is the placeholder / suggested prompt on the input line ("" if none).
	Ghost string
	// State is InputDraft (pending keystroke draft < 10 min, never inferred
	// from the screen) > InputSuggestion (ghost visible) > InputEmpty.
	State      string
	Draft      bool
	DraftSince time.Time
	// Row is the candidate row (the one holding the prompt / cursor); Top and
	// Bottom bound the input box (inclusive, rule rows excluded).
	Row, Top, Bottom int
	// Marker: the prompt marker was found at the start of the row.
	Marker bool
}

// DialogInfo describes a dialog block.
type DialogInfo struct {
	// Question is the context rows above an option list (or the first lines of
	// a framed block), reflowed and joined with "\n".
	Question    string
	Lines       []Line // the dialog's rows reflowed (Role dialog)
	First, Last int    // screen rows, inclusive
	// Options are the detected options when the dialog was found through an
	// option list.
	Options []Option
}

// Regions is the result of Split.
type Regions struct {
	// Of has one entry per screen row.
	Of []Region
	// Transcript is the reflowed transcript: scrollback (main screen only)
	// followed by the transcript rows of the screen.
	Transcript []Line
	// Status holds the spinner / busy rows between transcript and input.
	Status []Line
	// Footer holds the text of the footer rows, blanks dropped.
	Footer []string
	Dialog *DialogInfo
	// Input is valid when HasInput.
	Input    InputInfo
	HasInput bool
}

// rowText returns the display text of screen row y (ghost excluded when
// ghost is non-nil), trimmed on both sides; frame borders stripped.
func rowText(r Row, cols int) string {
	v := newRowView(r, cols)
	return strings.TrimLeft(v.text, "  ")
}

// isRuleRow: ≥ 80 % of the non-blank cells are rule glyphs and they span at
// least half the width (§4.1).
func isRuleRow(r Row, cols int, chars string) bool {
	n, rules := 0, 0
	lo, hi := -1, 0
	for _, p := range place(r) {
		if blankText(p.c.Text) {
			continue
		}
		n++
		if lo < 0 {
			lo = p.col
		}
		hi = p.col + p.w
		if rs := []rune(p.c.Text); len(rs) == 1 && strings.ContainsRune(chars, rs[0]) {
			rules++
		}
	}
	return n > 0 && rules*5 >= n*4 && (hi-lo)*2 >= cols
}

// isBorderOnly: every non-blank cell is a box-drawing glyph (rules, frame
// corners, empty frame sides).
func isBorderOnly(r Row) bool {
	n := 0
	for _, p := range place(r) {
		if blankText(p.c.Text) {
			continue
		}
		if !isBoxRune(p.c.Text) {
			return false
		}
		n++
	}
	return n > 0
}

func (s *Snapshot) isSeparator(y int, rules Rules) bool {
	r := s.Screen[y]
	return isRuleRow(r, s.Cols, rules.ruleChars()) || isBorderOnly(r)
}

// uniformBg: every cell of the row carries a background matching specs.
func uniformBg(r Row, rules Rules) bool {
	if len(rules.InputBgColors) == 0 || len(r.Cells) == 0 {
		return false
	}
	for _, p := range place(r) {
		if !ColorMatches(p.c.Bg, rules.InputBgColors) {
			return false
		}
	}
	return true
}

const maxBoxRows = 20 // search distance for input box borders

// Split assigns every screen row a Region and extracts the input line,
// dialog, footer, status line and reflowed transcript (§4). Pure function of
// s and r.
func Split(s *Snapshot, r Rules) Regions {
	n := len(s.Screen)
	res := Regions{Of: make([]Region, n)}
	for y := range res.Of {
		res.Of[y] = RegionTranscript
	}
	if n == 0 {
		return res
	}
	sep := make([]bool, n)
	for y := range sep {
		sep[y] = s.isSeparator(y, r)
	}

	// Input line and box.
	if row, ok := findInput(s, r, sep); ok {
		top, bottom := inputBox(s, r, sep, row)
		res.HasInput = true
		res.Input = buildInput(s, r, row, top, bottom)
		for y := top; y <= bottom; y++ {
			res.Of[y] = RegionInput
		}
	}
	boxTop, boxBottom := n, -1
	if res.HasInput {
		boxTop, boxBottom = res.Input.Top, res.Input.Bottom
	}

	// Footer: everything below the box, or trailing rows matching footer_regex.
	footerFrom := n
	if res.HasInput {
		footerFrom = boxBottom + 1
	} else if r.FooterRegex != nil {
		for y := n - 1; y >= 0; y-- {
			t := rowText(s.Screen[y], s.Cols)
			if t == "" {
				continue
			}
			if !r.FooterRegex.MatchString(t) {
				break
			}
			footerFrom = y
		}
	}
	for y := footerFrom; y < n; y++ {
		if !sep[y] {
			res.Of[y] = RegionFooter
			if t := rowText(s.Screen[y], s.Cols); t != "" {
				res.Footer = append(res.Footer, t)
			}
		}
	}

	// Dialog.
	limit := min(boxTop, footerFrom)
	if d := findDialog(s, r, sep, res.HasInput, res.Input, limit); d != nil {
		for y := d.First; y <= d.Last; y++ {
			res.Of[y] = RegionDialog
		}
		var rows []Row
		for y := d.First; y <= d.Last; y++ {
			rows = append(rows, s.Screen[y])
		}
		d.Lines = ReflowRows(rows, s.Cols, ReflowOpts{Rules: r, StripFrames: true, Skip: func(i int) bool { return sep[d.First+i] && isRuleRow(rows[i], s.Cols, r.ruleChars()) }})
		for i := range d.Lines {
			d.Lines[i].Role = RoleDialog
			d.Lines[i].Row += d.First
		}
		res.Dialog = d
	}

	// Status: busy / spinner rows directly above the input box (or footer),
	// separated from the transcript only by blank rows.
	for y := limit - 1; y >= 0 && res.Of[y] == RegionTranscript; y-- {
		if sep[y] {
			continue
		}
		t := rowText(s.Screen[y], s.Cols)
		if t == "" {
			continue
		}
		if !isStatusRow(t, r) {
			break
		}
		res.Of[y] = RegionStatus
	}
	for y := 0; y < n; y++ {
		if res.Of[y] == RegionStatus {
			lines := ReflowRows([]Row{s.Screen[y]}, s.Cols, ReflowOpts{Rules: r})
			for _, l := range lines {
				l.Role, l.Row = RoleStatus, y
				res.Status = append(res.Status, l)
			}
		}
		if sep[y] && res.Of[y] != RegionDialog {
			res.Of[y] = RegionRule
		}
	}

	// Transcript: scrollback (main screen) + transcript rows of the screen.
	off := 0
	if !s.AltScreen {
		off = len(s.Scrollback)
	}
	res.Transcript = ReflowRows(StreamRows(s), s.Cols, ReflowOpts{
		Rules: r,
		Skip: func(i int) bool {
			y := i - off
			if y < 0 {
				return isBorderOnly(s.Scrollback[i]) || isRuleRow(s.Scrollback[i], s.Cols, r.ruleChars())
			}
			return res.Of[y] != RegionTranscript
		},
	})
	return res
}

func isStatusRow(t string, r Rules) bool {
	if r.BusyRegex != nil && r.BusyRegex.MatchString(t) {
		return true
	}
	if r.SpinnerChars != "" {
		for _, c := range t {
			return strings.ContainsRune(r.SpinnerChars, c)
		}
	}
	return false
}

// leftOfCursor is the text of row y left of column x, trimmed of blanks.
func leftOfCursor(row Row, x int) string {
	var b strings.Builder
	for _, p := range place(row) {
		if p.col >= x {
			break
		}
		b.WriteString(cellText(p.c))
	}
	return strings.TrimRight(b.String(), "  \t")
}

// findInput picks the input candidate row (§4.2, first match wins).
func findInput(s *Snapshot, r Rules, sep []bool) (int, bool) {
	n := len(s.Screen)
	marker := r.Ghost.PromptMarker
	cy, cx := s.Cursor.Y, s.Cursor.X
	curOK := !s.CursorHidden && cy >= 0 && cy < n
	// 1. cursor right after the prompt marker. A menu row never qualifies.
	if curOK && marker != "" && strings.HasSuffix(leftOfCursor(s.Screen[cy], cx), marker) &&
		!menuRow(s, r, cy, cy) {
		return cy, true
	}
	// 1b. a typed draft: the cursor is after the text, on the marker row or on
	// a continuation row of a multi-row draft; the marker row is at or above
	// it inside the same box.
	if curOK && marker != "" && !sep[cy] {
		for y := cy; y >= 0 && cy-y <= maxBoxRows && !sep[y]; y-- {
			if strings.HasPrefix(rowText(s.Screen[y], s.Cols), marker) {
				if menuRow(s, r, y, cy) {
					break
				}
				return y, true
			}
		}
	}
	// 2. cursor hidden: last row matching prompt_regex that is not a menu row.
	if s.CursorHidden && r.PromptRegex != nil {
		for y := n - 1; y >= 0; y-- {
			if sep[y] || !r.PromptRegex.MatchString(inputRowText(s, y, r)) || menuRow(s, r, y, y) {
				continue
			}
			return y, true
		}
	}
	// 3. cursor row entirely on an input background.
	if curOK && uniformBg(s.Screen[cy], r) {
		return cy, true
	}
	// 4. no profile marker: cursor row in the bottom third.
	if curOK && marker == "" && r.PromptRegex == nil && len(r.InputBgColors) == 0 && cy*3 >= n*2 {
		return cy, true
	}
	return 0, false
}

// inputRowText is the text of row y with ghost cells removed (ghost only
// exists on the cursor row).
func inputRowText(s *Snapshot, y int, r Rules) string {
	row := s.Screen[y]
	if y == s.Cursor.Y {
		row = WithoutColumns(row, GhostColumns(row, s.Cursor.X, s.Cols, r.Ghost))
	}
	return newRowView(row, s.Cols).text
}

// numberedPointer: a pointer glyph followed by a numbered option ("❯ 1. Yes").
var numberedPointer = regexp.MustCompile(`^\s*[❯›▶▸➜→]\s*\d{1,2}[.)]\s`)

// menuRow reports whether row y is an option row of a menu rather than the
// input line: it reads "❯ 1. Yes", or an adjacent row below/above the draft
// rows [y, last] sits at its label column (and is not a hint or footer row).
// Rows y+1..last are the continuation rows of a multi-row draft (the cursor is
// on last) and never count as siblings.
func menuRow(s *Snapshot, r Rules, y, last int) bool {
	if numberedPointer.MatchString(rowText(s.Screen[y], s.Cols)) {
		return true
	}
	return hasMenuSibling(s, y, r.Ghost.PromptMarker, last, r)
}

// hasMenuSibling: an adjacent non-blank row whose text starts at the label
// column of row y, i.e. y is a pointer row of a menu rather than a prompt
// (§6.2: the prompt row has rule rows around it instead).
func hasMenuSibling(s *Snapshot, y int, marker string, last int, r Rules) bool {
	v := newRowView(s.Screen[y], s.Cols)
	if v.blank || len(v.cells) < 2 {
		return false
	}
	label := -1
	for i := 1; i < len(v.cells); i++ {
		if !blankText(v.cells[i].c.Text) && blankText(v.cells[i-1].c.Text) {
			label = v.cells[i].col
			break
		}
	}
	if label < 0 {
		return false
	}
	for _, z := range []int{y - 1, last + 1} {
		if z < 0 || z >= len(s.Screen) {
			continue
		}
		o := newRowView(s.Screen[z], s.Cols)
		if o.blank || o.boxy || o.indent != label {
			continue
		}
		if r.FooterRegex != nil && r.FooterRegex.MatchString(strings.TrimSpace(o.match)) {
			continue
		}
		if l := prepLine(s.Screen[z]); l.hintOnly(r.Menu) {
			continue
		}
		return true
	}
	return false
}

// inputBox returns the first and last row of the input box around row.
func inputBox(s *Snapshot, r Rules, sep []bool, row int) (int, int) {
	n := len(s.Screen)
	if uniformBg(s.Screen[row], r) {
		top, bottom := row, row
		for top > 0 && uniformBg(s.Screen[top-1], r) {
			top--
		}
		for bottom < n-1 && uniformBg(s.Screen[bottom+1], r) {
			bottom++
		}
		return top, bottom
	}
	up, down := -1, -1
	for y := row - 1; y >= 0 && row-y <= maxBoxRows; y-- {
		if sep[y] {
			up = y
			break
		}
	}
	for y := row + 1; y < n && y-row <= maxBoxRows; y++ {
		if sep[y] {
			down = y
			break
		}
	}
	if up >= 0 && down >= 0 {
		return up + 1, down - 1
	}
	return row, row
}

// ghostCursorX returns the column ghost classification starts from on the
// input row: the real cursor when it is visible on that row, else a synthetic
// cursor at the first non-blank cell after the prompt marker (column 0 with no
// marker). With no style rule (every cell right of the marker would be ghost)
// a synthetic cursor is not used while a keystroke draft is pending, since the
// text could be the draft.
func ghostCursorX(s *Snapshot, r Rules, row Row, y int) (int, bool) {
	g := r.Ghost
	if !g.Enabled() {
		return 0, false
	}
	if !s.CursorHidden && s.Cursor.Y == y {
		return s.Cursor.X, true
	}
	if !g.StyleRules() {
		now := s.Taken
		if now.IsZero() {
			now = time.Now()
		}
		if !s.DraftSince.IsZero() && now.Sub(s.DraftSince) < DraftMaxAge {
			return 0, false
		}
	}
	if g.PromptMarker == "" {
		return 0, true
	}
	var b strings.Builder
	end := -1
	for _, p := range place(row) {
		if end >= 0 {
			if !blankText(p.c.Text) {
				return p.col, true
			}
			continue
		}
		b.WriteString(cellText(p.c))
		if strings.HasSuffix(strings.TrimRight(b.String(), " \u00a0\t"), g.PromptMarker) {
			end = p.col + p.w
		}
	}
	return 0, false
}

func buildInput(s *Snapshot, r Rules, row, top, bottom int) InputInfo {
	in := InputInfo{Row: row, Top: top, Bottom: bottom, State: InputEmpty}
	marker := r.Ghost.PromptMarker
	var lines []string
	for y := top; y <= bottom; y++ {
		src := s.Screen[y]
		if y == row {
			// Ghost text is removed even when the cursor is hidden or parked
			// elsewhere (a synthetic cursor right after the marker): Text
			// never contains ghost.
			if cx, ok := ghostCursorX(s, r, src, y); ok {
				if cols := GhostColumns(src, cx, s.Cols, r.Ghost); len(cols) > 0 {
					in.Ghost = GhostText(src, cols, cx)
					src = WithoutColumns(src, cols)
				}
			}
		}
		t := rowText(src, s.Cols)
		if y == row && marker != "" && strings.HasPrefix(t, marker) {
			in.Marker = true
			t = strings.TrimLeft(t[len(marker):], "  ")
		}
		lines = append(lines, t)
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	in.Text = strings.Join(lines, "\n")
	now := s.Taken
	if now.IsZero() {
		now = time.Now()
	}
	if !s.DraftSince.IsZero() && now.Sub(s.DraftSince) < DraftMaxAge {
		in.Draft, in.DraftSince, in.State = true, s.DraftSince, InputDraft
	} else if in.Ghost != "" {
		in.State = InputSuggestion
	}
	return in
}

var (
	frameTops    = "╭┌┏╔"
	frameBottoms = "╰└┗╚"
)

// findDialog looks for a framed block, else an option list, in rows [0, limit).
func findDialog(s *Snapshot, r Rules, sep []bool, hasInput bool, in InputInfo, limit int) *DialogInfo {
	// Framed block ╭…╮ … ╰…╯ not containing the input row.
	for y := limit - 1; y >= 0; y-- {
		if !sep[y] || !isFrameCorner(s.Screen[y], frameBottoms) {
			continue
		}
		for t := y - 1; t >= 0; t-- {
			if isFrameCorner(s.Screen[t], frameTops) {
				if hasInput && in.Row >= t && in.Row <= y {
					break // the input box itself
				}
				if tableFrame(s.Screen[t]) || tableFrame(s.Screen[y]) {
					break // a table (column junctions on its top / bottom), not a dialog
				}
				d := &DialogInfo{First: t, Last: y}
				d.Question = framedQuestion(s, t, y)
				inputRow := -1
				if hasInput {
					inputRow = in.Top
				}
				for _, o := range DetectOptions(s, inputRow, r.Menu) {
					if o.Row > t && o.Row < y {
						d.Options = append(d.Options, o)
					}
				}
				return d
			}
		}
	}
	// Option list plus the question rows above it.
	inputRow := -1
	if hasInput {
		inputRow = in.Top
	}
	opts := DetectOptions(s, inputRow, r.Menu)
	if len(opts) == 0 {
		return nil
	}
	first, last := opts[0].Row, opts[len(opts)-1].Row
	for _, o := range opts {
		first, last = min(first, o.Row), max(last, o.Row)
	}
	if last >= limit {
		return nil
	}
	// Without a visible input prompt nothing says the block is a dialog and
	// not transcript: require a pointer / selected style / frame (a selected
	// option), or the cursor on a yes/no prompt row. An unstyled list stays
	// in the transcript.
	if !hasInput && !realDialogOptions(opts) {
		return nil
	}
	v := newRowView(s.Screen[last], s.Cols)
	for y := last + 1; y < limit && !sep[y]; y++ {
		o := newRowView(s.Screen[y], s.Cols)
		if o.blank || o.indent <= v.indent {
			break
		}
		last = y
	}
	// Question: skip one blank row, then up to contextRows non-blank rows.
	top := first
	y := first - 1
	if y >= 0 && newRowView(s.Screen[y], s.Cols).blank {
		y--
	}
	cnt := 0
	for ; y >= 0 && cnt < r.contextRows() && !sep[y]; y-- {
		if newRowView(s.Screen[y], s.Cols).blank {
			break
		}
		top = y
		cnt++
	}
	d := &DialogInfo{First: top, Last: last, Options: opts}
	var ctx []Row
	for y := top; y < first; y++ {
		ctx = append(ctx, s.Screen[y])
	}
	var qs []string
	for _, l := range ReflowRows(ctx, s.Cols, ReflowOpts{Rules: r, StripFrames: true}) {
		if l.Text != "" {
			qs = append(qs, strings.TrimLeft(l.Text, "  "))
		}
	}
	d.Question = strings.Join(qs, "\n")
	return d
}

// realDialogOptions: some option carries a pointer / selected style, or the
// menu is a yes/no prompt awaiting a line answer at the cursor.
func realDialogOptions(opts []Option) bool {
	for _, o := range opts {
		if o.Kind == KindYesNo {
			if o.Submit {
				return true
			}
			continue
		}
		if o.Selected {
			return true
		}
	}
	return false
}

// tableFrame: the frame row carries column junctions (┬ ┴ ┼ ...): a table.
func tableFrame(r Row) bool {
	for _, p := range place(r) {
		if strings.Contains("┬┴┼╦╩╬┳┻╋", p.c.Text) && p.c.Text != "" {
			return true
		}
	}
	return false
}

func isFrameCorner(r Row, set string) bool {
	for _, p := range place(r) {
		if blankText(p.c.Text) {
			continue
		}
		rs := []rune(p.c.Text)
		return len(rs) == 1 && strings.ContainsRune(set, rs[0])
	}
	return false
}

func framedQuestion(s *Snapshot, top, bottom int) string {
	for y := top + 1; y < bottom; y++ {
		if t := rowText(s.Screen[y], s.Cols); t != "" {
			return t
		}
	}
	return ""
}
