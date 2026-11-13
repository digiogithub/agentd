package screen

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// width2 approximates East Asian width for the runes used in tests.
func width2(r rune) int {
	if r >= 0x2e80 && r <= 0xa4cf || r >= 0xac00 && r <= 0xd7a3 || r >= 0xff00 && r <= 0xff60 || r >= 0x1f300 && r <= 0x1faff {
		return 2
	}
	return 1
}

// mkRow builds a row of cols cells for text (continuation cells of wide
// characters kept as Width 0, index == column); bg, when set, is applied to
// every cell, and faintFrom >= 0 makes cells from that column faint.
func mkRow(text string, cols int) Row {
	cells := make([]Cell, 0, cols)
	for _, r := range text {
		w := width2(r)
		t := string(r)
		if r == ' ' {
			t = ""
		}
		cells = append(cells, Cell{Text: t, Width: uint8(w)})
		if w == 2 {
			cells = append(cells, Cell{})
		}
	}
	for len(cells) < cols {
		cells = append(cells, Cell{Width: 1})
	}
	return Row{Cells: cells}
}

func setBg(r Row, bg string) Row {
	for i := range r.Cells {
		r.Cells[i].Bg = bg
	}
	return r
}

func setFaint(r Row, from int) Row {
	for i := from; i < len(r.Cells); i++ {
		r.Cells[i].Attrs |= Faint
	}
	return r
}

func rowsOf(cols int, lines ...string) []Row {
	out := make([]Row, len(lines))
	for i, l := range lines {
		out[i] = mkRow(l, cols)
	}
	return out
}

func snapOf(cols int, lines ...string) *Snapshot {
	return &Snapshot{Cols: cols, Rows: len(lines), Screen: rowsOf(cols, lines...)}
}

// wrapWords greedy word-wraps text to width, the way an Ink/ratatui TUI does;
// continuation rows start with hang.
func wrapWords(text string, width int, first, hang string) []string {
	var out []string
	cur := first
	empty := true
	for _, w := range strings.Fields(text) {
		if !empty && len(cur)+1+len(w) > width {
			out = append(out, cur)
			cur, empty = hang, true
		}
		if !empty {
			cur += " "
		}
		cur += w
		empty = false
	}
	return append(out, cur)
}

// wrapChars hard-wraps text at width columns (autowrap), by cells.
func wrapChars(text string, width int) []string {
	var out []string
	var cur strings.Builder
	col := 0
	for _, r := range text {
		w := width2(r)
		if col+w > width {
			out = append(out, cur.String())
			cur.Reset()
			col = 0
		}
		cur.WriteRune(r)
		col += w
	}
	return append(out, cur.String())
}

const (
	para1 = "The quick brown fox jumps over the lazy dog while the agent keeps working through a long list of files, reading each one carefully before deciding what to change next in the repository."
	item1 = "Rewrite the delivery loop so that queued messages are injected only when the target session has been idle for the configured grace period and no human draft is pending on the input line."
	item2 = "Add golden tests."
)

func TestReflowWidthIndependent(t *testing.T) {
	want := []string{
		"⏺ " + para1,
		"",
		"  ⎿  short tool output line",
		"",
		"- " + item1,
		"- " + item2,
		"1. numbered item",
		"Hard break one",
		"Hard break two",
		"",
		"Final paragraph: " + para1,
	}
	for _, w := range []int{80, 120, 160} {
		var lines []string
		lines = append(lines, wrapWords(para1, w, "⏺ ", "  ")...)
		lines = append(lines, "", "  ⎿  short tool output line", "")
		lines = append(lines, wrapWords(item1, w, "- ", "  ")...)
		lines = append(lines, "- "+item2, "1. numbered item", "Hard break one", "Hard break two", "")
		lines = append(lines, wrapWords("Final paragraph: "+para1, w, "", "")...)
		got := Reflow(snapOf(w, lines...), ReflowOpts{Rules: DefaultRules()})
		var texts []string
		for _, l := range got {
			texts = append(texts, l.Text)
		}
		if !reflect.DeepEqual(texts, want) {
			t.Errorf("width %d:\n got %q\nwant %q", w, texts, want)
		}
		if w == 80 && (got[0].Rows < 3 || got[0].Joined != JoinedWordwrap) {
			t.Errorf("first line rows=%d joined=%q", got[0].Rows, got[0].Joined)
		}
		if got[0].Row != 0 {
			t.Errorf("Row = %d", got[0].Row)
		}
	}
}

func TestReflowAutowrapWideChars(t *testing.T) {
	cjk := strings.Repeat("日本語のテキストは空白なしで続きます。", 6) + "end" // 2-wide runes, no spaces
	url := "see https://example.com/" + strings.Repeat("a1b2c3d4/", 25) + "end"
	for _, w := range []int{81, 79, 120} { // odd widths force a wide char to wrap one column early
		for _, text := range []string{cjk, url} {
			got := Reflow(snapOf(w, wrapChars(text, w)...), ReflowOpts{Rules: DefaultRules()})
			if len(got) != 1 || got[0].Text != text {
				t.Errorf("width %d: got %d lines %q\nwant %q", w, len(got), texts(got), text)
			}
		}
	}
}

func texts(ls []Line) []string {
	var o []string
	for _, l := range ls {
		o = append(o, l.Text)
	}
	return o
}

func TestReflowWideCharIndex(t *testing.T) {
	// Row layout without continuation cells (older snapshots) gives the same text.
	r := Row{Cells: []Cell{{Text: "中", Width: 2}, {Text: "x", Width: 1}}}
	got := ReflowRows([]Row{r}, 10, ReflowOpts{Rules: DefaultRules()})
	if len(got) != 1 || got[0].Text != "中x" {
		t.Fatalf("%q", texts(got))
	}
}

func TestReflowNBSPAndTrim(t *testing.T) {
	rows := rowsOf(40, "❯\u00a0hello\u00a0world\u00a0 ", "  indented  ", "\u00a0\u00a0")
	rows[2] = setBg(rows[2], "") // styled blank handled below
	rows = append(rows, Row{Cells: []Cell{{Text: "", Width: 1, Bg: "4"}, {Text: "", Width: 1, Bg: "4"}}})
	got := texts(ReflowRows(rows, 40, ReflowOpts{Rules: DefaultRules()}))
	want := []string{"❯\u00a0hello\u00a0world", "  indented"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestReflowNBSPMatching(t *testing.T) {
	// A wrapped row ending near the edge; the next row's first word is
	// separated by NBSP in the source text, matching treats it as a space.
	rows := rowsOf(20, "aaaa bbbb cccc dddd", "eeee\u00a0ffff")
	got := texts(ReflowRows(rows, 20, ReflowOpts{Rules: DefaultRules()}))
	if !reflect.DeepEqual(got, []string{"aaaa bbbb cccc dddd eeee\u00a0ffff"}) {
		t.Fatalf("%q", got)
	}
}

func TestReflowHardBreaks(t *testing.T) {
	cases := map[string][]string{
		"short rows":     {"alpha beta", "gamma delta"},
		"blank between":  {"alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi", "", "omicron pi"},
		"list marker":    {"- alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu x", "- yi"},
		"prompt marker":  {"alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi om", "❯ prompt"},
		"indent differs": {"alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi om", "    code"},
		"box drawing":    {"alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi om", "─────"},
	}
	for name, rows := range cases {
		got := ReflowRows(rowsOf(80, rows...), 80, ReflowOpts{Rules: DefaultRules()})
		if len(got) != len(rows) {
			t.Errorf("%s: %d lines, want %d: %q", name, len(got), len(rows), texts(got))
		}
	}
}

func TestReflowNoJoinRegex(t *testing.T) {
	r := DefaultRules()
	r.NoJoin = mustRe(t, `^result`)
	rows := rowsOf(40, "alpha beta gamma delta epsilon zeta", "result of the run")
	if got := texts(ReflowRows(rows, 40, ReflowOpts{Rules: r})); len(got) != 2 {
		t.Fatalf("%q", got)
	}
	if got := texts(ReflowRows(rows, 40, ReflowOpts{Rules: DefaultRules()})); len(got) != 1 {
		t.Fatalf("without NoJoin: %q", got)
	}
}

func TestReflowVerbatimBackground(t *testing.T) {
	rows := rowsOf(20, "aaaa bbbb cccc dddd", "eeee ffff")
	rows[0], rows[1] = setBg(rows[0], "#202020"), setBg(rows[1], "#202020")
	got := texts(ReflowRows(rows, 20, ReflowOpts{Rules: DefaultRules()}))
	if len(got) != 2 {
		t.Fatalf("code block rows must be kept: %q", got)
	}
	d := ReflowRows(rowsOf(30, "  12 + added line", "  13 - removed line"), 30, ReflowOpts{Rules: DefaultRules()})
	if len(d) != 2 {
		t.Fatalf("diff gutter rows must be kept: %q", texts(d))
	}
}

func TestReflowWrappedFlag(t *testing.T) {
	rows := rowsOf(40, "short", "rows")
	rows[0].Wrapped = true
	got := ReflowRows(rows, 40, ReflowOpts{Rules: DefaultRules()})
	if len(got) != 1 || got[0].Text != "shortrows" || got[0].Joined != JoinedFlag {
		t.Fatalf("%+v", got)
	}
}

func TestReflowFrame(t *testing.T) {
	w := 30
	inner := wrapWords("alpha beta gamma delta epsilon zeta eta theta iota kappa", w-4, "", "")
	var lines []string
	lines = append(lines, "╭"+strings.Repeat("─", w-2)+"╮")
	for _, l := range inner {
		lines = append(lines, "│ "+l+strings.Repeat(" ", w-4-len(l))+" │")
	}
	lines = append(lines, "│"+strings.Repeat(" ", w-2)+"│", "╰"+strings.Repeat("─", w-2)+"╯")
	got := texts(ReflowRows(rowsOf(w, lines...), w, ReflowOpts{Rules: DefaultRules(), StripFrames: true}))
	want := []string{lines[0], "alpha beta gamma delta epsilon zeta eta theta iota kappa", "", lines[len(lines)-1]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestReflowScrollbackAndAlt(t *testing.T) {
	s := snapOf(20, "tail of the line", "", "next paragraph", "", "")
	// "first words of the" ends at col 18 (< 20): the next word "tail" (4) overflows.
	s.Scrollback = rowsOf(20, "first words of the")
	got := texts(Reflow(s, ReflowOpts{Rules: DefaultRules()}))
	want := []string{"first words of the tail of the line", "", "next paragraph"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
	s.AltScreen = true
	got = texts(Reflow(s, ReflowOpts{Rules: DefaultRules()}))
	if !reflect.DeepEqual(got, []string{"tail of the line", "", "next paragraph"}) {
		t.Fatalf("alt: %q", got)
	}
}

func TestReflowSkip(t *testing.T) {
	rows := rowsOf(20, "aaaa bbbb cccc dddd", "eeee", "xxxx")
	got := texts(ReflowRows(rows, 20, ReflowOpts{Rules: DefaultRules(), Skip: func(i int) bool { return i == 1 }}))
	if !reflect.DeepEqual(got, []string{"aaaa bbbb cccc dddd", "xxxx"}) {
		t.Fatalf("%q", got)
	}
}

func mustRe(t *testing.T, s string) *regexp.Regexp {
	t.Helper()
	return regexp.MustCompile(s)
}
