package screen

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// Regression tests for the wave-2 code review (docs/changes/wave2-review-fixes.md).

func rowTexts(ls []Line) string { return strings.Join(texts(ls), "|") }

// ---- 1, 2: unstyled prose lists are not options / dialogs ----

func TestReviewUnstyledProseListsAreNotOptions(t *testing.T) {
	r := DefaultMenuRules()
	w := 60
	rl := rule(w)
	tests := []struct {
		name  string
		rows  []string
		input int
	}{
		{"question and list above the input box", []string{"● Which approach do you prefer?", "", "  1. Rewrite the parser", "  2. Patch the lexer", "", rl, "❯ ", rl}, 6},
		{"later prose says enter the", []string{"● Steps:", "  1. Open the file", "  2. Edit the line", "  Then enter the value and save.", rl, "❯ "}, 5},
		{"hint footer without question", []string{"● Steps:", "  1. Open the file", "  2. Edit the line", "  esc to interrupt"}, -1},
		{"question far above the list", []string{"Which one?", "", "", "  1. Open the file", "  2. Edit the line"}, -1},
		{"list followed by other prose", []string{"Which one?", "  1. Open", "  2. Edit", "", "That is all I have to say about it."}, -1},
		{"question not adjacent (text between)", []string{"Which one?", "Some explanation here.", "  1. Open", "  2. Edit"}, -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectOptions(optSnap(w, tc.rows...), tc.input, r); got != nil {
				t.Fatalf("detected %+v", got)
			}
		})
	}
	// The same list at the bottom with its question directly above stays an
	// option list (confidence 0.7).
	got := DetectOptions(optSnap(w, "Which approach?", "", "  1. Rewrite", "  2. Patch"), -1, r)
	if len(got) != 2 || got[0].Confidence != 0.7 {
		t.Fatalf("adjacent question, bottom: %+v", got)
	}
	// With an input row visible the unstyled list is below MinConfidence...
	rows := []string{"Which approach?", "  1. Rewrite", "  2. Patch", rl, "❯ ", rl}
	if got := DetectOptions(optSnap(w, rows...), 4, r); got != nil {
		t.Fatalf("unstyled list above input: %+v", got)
	}
	// ...but a pointer list there is a real dialog.
	rows[1] = "❯ 1. Rewrite"
	if got := DetectOptions(optSnap(w, rows...), 4, r); len(got) != 2 {
		t.Fatalf("pointer list above input: %+v", got)
	}
}

func TestReviewSplitKeepsProseInTranscript(t *testing.T) {
	w := 60
	rules := claudeRules()
	// No input prompt visible: an unstyled list adjacent to its question is
	// still transcript (needs pointer / style / frame to be a dialog).
	s := optSnap(w, "● Which approach do you prefer?", "  1. Rewrite the parser", "  2. Patch the lexer")
	r := Split(s, rules)
	if r.Dialog != nil {
		t.Fatalf("unstyled list became a dialog: %+v", r.Dialog)
	}
	for y, reg := range r.Of {
		if reg != RegionTranscript {
			t.Errorf("row %d region %s, want transcript", y, reg)
		}
	}
	if got := rowTexts(r.Transcript); !strings.Contains(got, "1. Rewrite the parser") {
		t.Errorf("list rows missing from transcript: %q", got)
	}
	// With the input box visible: still transcript.
	rl := rule(w)
	s = optSnap(w, "● Which approach do you prefer?", "", "  1. Rewrite the parser", "  2. Patch the lexer", "", rl, "❯ ", rl, "  ? for shortcuts")
	s.CursorHidden = false
	s.Cursor = Pos{X: 2, Y: 6}
	r = Split(s, rules)
	if r.Dialog != nil || !r.HasInput {
		t.Fatalf("dialog=%+v hasInput=%v", r.Dialog, r.HasInput)
	}
	if r.Of[2] != RegionTranscript || r.Of[3] != RegionTranscript {
		t.Errorf("list rows not transcript: %v", r.Of)
	}
	// A pointer dialog without input is still found.
	s = optSnap(w, "⏺ Bash(rm x)", "", " Do you want to proceed?", " ❯ 1. Yes", "   2. No")
	if r = Split(s, rules); r.Dialog == nil || len(r.Dialog.Options) != 2 {
		t.Fatalf("pointer dialog lost: %+v", r.Dialog)
	}
}

// ---- 3: option identity ----

func TestReviewIDsSaltedWithQuestion(t *testing.T) {
	r := DefaultMenuRules()
	a := DetectOptions(optSnap(60, " Do you want to edit a.go?", " ❯ 1. Yes", "   2. No"), -1, r)
	b := DetectOptions(optSnap(60, " Do you want to edit b.go?", " ❯ 1. Yes", "   2. No"), -1, r)
	if len(a) != 2 || len(b) != 2 {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
	if a[0].ID == b[0].ID || a[1].ID == b[1].ID {
		t.Errorf("identical-label dialogs with different questions share IDs: %s %s", a[0].ID, b[0].ID)
	}
	if a[0].MenuFingerprint == "" || a[0].MenuFingerprint == b[0].MenuFingerprint || a[0].MenuFingerprint != a[1].MenuFingerprint {
		t.Errorf("fingerprints: %q %q %q", a[0].MenuFingerprint, a[1].MenuFingerprint, b[0].MenuFingerprint)
	}
	// Same question re-wrapped / re-spaced and pointer moved: same IDs.
	c := DetectOptions(optSnap(30, "  Do you want to", "  edit   a.go?", "    1. Yes", "  ❯ 2. No"), -1, r)
	if len(c) != 2 || c[0].ID != a[0].ID || c[1].ID != a[1].ID || c[0].MenuFingerprint != a[0].MenuFingerprint {
		t.Errorf("re-wrapped redraw changed identity: %+v vs %+v", c, a)
	}
	// Kind is part of the fingerprint.
	pk := DetectOptions(optSnap(60, " Do you want to edit a.go?", " ❯ Yes", "   No"), -1, r)
	if len(pk) != 2 || pk[0].MenuFingerprint == a[0].MenuFingerprint {
		t.Errorf("pointer vs numbered share a fingerprint: %+v", pk)
	}
	// The choose plan carries the fingerprint on every step.
	for _, st := range PlanChoose(a, a[1], false) {
		if st.Expect.Fingerprint != a[0].MenuFingerprint {
			t.Errorf("step %q has fingerprint %q", st.Keys, st.Expect.Fingerprint)
		}
	}
	for _, st := range PlanChoose(pk, pk[1], false) {
		if st.Expect.Fingerprint != pk[0].MenuFingerprint {
			t.Errorf("arrow step %q has fingerprint %q", st.Keys, st.Expect.Fingerprint)
		}
	}
}

// ---- 4: pointer menus ----

func TestReviewPointerMenusNeedPlausibleShape(t *testing.T) {
	r := DefaultMenuRules()
	for _, p := range r.Pointers {
		if p == ">" {
			t.Error(`">" must not be a default pointer`)
		}
	}
	long := strings.Repeat("word ", 20)
	tests := []struct {
		name string
		rows []string
	}{
		{"blockquote with default pointers", []string{"> note one", "  text a", "  text b", "  text c"}},
		{"user echo then tool output", []string{"❯ run the tests", "  ⎿  ok", "  ⎿  fine", "  ⎿  done"}},
		{"bullet rows", []string{"❯ run it", "  ● one", "  ● two"}},
		{"sentence-long pointer label", []string{"Pick", "❯ " + long, "  b", "  c"}},
		{"prose follows the block", []string{"Pick", "❯ a", "  b", "", "Some trailing prose that is not a hint."}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectOptions(optSnap(120, tc.rows...), -1, r); got != nil {
				t.Fatalf("detected %+v", got)
			}
		})
	}
	// With ">" configured explicitly the blockquote shape is a menu again.
	cfg := r
	cfg.Pointers = []string{">"}
	if got := DetectOptions(optSnap(60, "Pick", "> a", "  b", "  c"), -1, cfg); len(got) != 3 {
		t.Errorf("configured > pointer: %+v", got)
	}
	// A menu followed by a hint row, and a framed menu, qualify.
	if got := DetectOptions(optSnap(60, "Pick", "❯ a", "  b", "", "  Esc to cancel"), -1, r); len(got) != 2 {
		t.Errorf("menu + hint row: %+v", got)
	}
	if got := DetectOptions(optSnap(60, "│ Pick       │", "│ ❯ a        │", "│   b        │", "│ more prose │", "╰────────────╯"), -1, r); len(got) != 2 {
		t.Errorf("framed menu: %+v", got)
	}
}

// ---- 5: input line vs menu rows ----

func TestReviewMenuRowIsNeverTheInputLine(t *testing.T) {
	w := 60
	rules := claudeRules()
	// Cursor parked right after the pointer of a numbered menu row.
	s := snapOf(w, " Do you want to proceed?", " ❯ 1. Yes", "   2. No")
	s.Cursor = Pos{X: 2, Y: 1}
	if r := Split(s, rules); r.HasInput {
		t.Errorf("numbered menu row taken as input: %+v", r.Input)
	}
	// Plain two-option pointer menu, cursor right after the pointer.
	s = snapOf(w, "Pick one", "❯ Yes", "  No")
	s.Cursor = Pos{X: 1, Y: 1}
	if r := Split(s, rules); r.HasInput {
		t.Errorf("pointer menu row taken as input: %+v", r.Input)
	}
	// Typed draft rule (1b): cursor on the draft's last row, siblings below.
	s = snapOf(w, "Pick one", "❯ Yes", "  No")
	s.Cursor = Pos{X: 5, Y: 1}
	if r := Split(s, rules); r.HasInput {
		t.Errorf("rule 1b: pointer menu row taken as input: %+v", r.Input)
	}
	// A real multi-row draft is still the input (continuation rows are not siblings).
	s = snapOf(w, rule(w), "❯ a long draft that continues", "  on the next row", rule(w))
	s.Cursor = Pos{X: 18, Y: 2}
	r := Split(s, rules)
	if !r.HasInput || r.Input.Row != 1 || r.Input.Text != "a long draft that continues\non the next row" {
		t.Errorf("multi-row draft lost: %+v", r.Input)
	}
}

// ---- 6: ghost text never in Input.Text ----

func TestReviewHiddenCursorInputHasNoGhost(t *testing.T) {
	w := 40
	rules := claudeRules()
	row := func(text string, faintFrom int) Row {
		r := mkRow(text, w)
		if faintFrom >= 0 {
			r = setFaint(r, faintFrom)
		}
		return r
	}
	s := &Snapshot{Cols: w, Rows: 3, CursorHidden: true}
	s.Screen = []Row{mkRow(rule(w), w), row("❯ Try \"fix lint errors\"", 2), mkRow(rule(w), w)}
	s.Screen[1].Cells[1] = Cell{Text: " ", Width: 1}
	r := Split(s, rules)
	if !r.HasInput || r.Input.Text != "" || r.Input.Ghost != `Try "fix lint errors"` || r.Input.State != InputSuggestion {
		t.Fatalf("text=%q ghost=%q state=%s", r.Input.Text, r.Input.Ghost, r.Input.State)
	}
	// Visible cursor parked on another row: same.
	s.CursorHidden, s.Cursor = false, Pos{X: 0, Y: 0}
	if r = Split(s, rules); r.Input.Text != "" && r.HasInput {
		t.Errorf("parked cursor: text=%q", r.Input.Text)
	}
	// A typed (non-faint) draft is kept.
	s = &Snapshot{Cols: w, Rows: 3, CursorHidden: true}
	s.Screen = []Row{mkRow(rule(w), w), row("❯ fix the bug", -1), mkRow(rule(w), w)}
	if r = Split(s, rules); r.Input.Text != "fix the bug" || r.Input.Ghost != "" {
		t.Errorf("draft text=%q ghost=%q", r.Input.Text, r.Input.Ghost)
	}
	// Codex shape (no style rule: everything right of the marker is ghost):
	// stripped without a pending draft, kept while a keystroke draft is pending.
	cr := codexRules()
	cr.InputBgColors = nil
	cr.PromptRegex = regexp.MustCompile(`(?m)^›( |$)`)
	cr.Ghost = GhostRules{RightOfCursor: true, PromptMarker: "›"}
	s = &Snapshot{Cols: w, Rows: 3, CursorHidden: true, Taken: time.Now()}
	s.Screen = []Row{mkRow(rule(w), w), mkRow("› Write tests for @filename", w), mkRow(rule(w), w)}
	if r = Split(s, cr); r.Input.Text != "" || r.Input.Ghost != "Write tests for @filename" {
		t.Errorf("codex hidden cursor: text=%q ghost=%q", r.Input.Text, r.Input.Ghost)
	}
	s.DraftSince = s.Taken.Add(-time.Minute)
	if r = Split(s, cr); r.Input.Text != "Write tests for @filename" || r.Input.State != InputDraft {
		t.Errorf("codex pending draft: text=%q state=%s", r.Input.Text, r.Input.State)
	}
}

// ---- 7: reflow guards ----

func TestReviewReflowKeepsCodeAndStructure(t *testing.T) {
	mk := func(cols int, rows ...string) string {
		return rowTexts(ReflowRows(rowsOf(cols, rows...), cols, ReflowOpts{Rules: DefaultRules()}))
	}
	tests := []struct {
		name string
		cols int
		rows []string
		want string
	}{
		{"indented code", 40, []string{"    x := computeSomething(a, b, c, d)", "    internationalization.Run()", "    return nil"},
			"    x := computeSomething(a, b, c, d)|    internationalization.Run()|    return nil"},
		{"full indented row then code", 40, []string{"    1234567890123456789012345678901234567890", "    foo()"},
			"    1234567890123456789012345678901234567890|    foo()"},
		{"code punctuation at line end", 30, []string{"func main() {", "internationalization.Run()", "}"},
			"func main() {|internationalization.Run()|}"},
		{"statement ending semicolon", 30, []string{"x := compute(a);", "next_statement_here()"},
			"x := compute(a);|next_statement_here()"},
		{"fenced block", 40, []string{"```", "plain words here that look like", "prose but are inside a fence", "```", "after"},
			"```|plain words here that look like|prose but are inside a fence|```|after"},
		{"next row is a bullet", 30, []string{"some prose that nearly fills", "- item two"}, "some prose that nearly fills|- item two"},
		{"next row starts with punctuation", 30, []string{"some prose that nearly fills", "|a|b|c|"}, "some prose that nearly fills||a|b|c|"},
		{"next row is numbered", 30, []string{"some prose that nearly fills", "2. second"}, "some prose that nearly fills|2. second"},
		{"narrow full rows are not autowrap", 10, []string{"0123456789", "next line"}, "0123456789|next line"},
		{"wide autowrap still joins", 30, []string{"abcdefghijklmnopqrstuvwxyz1234", "5678 tail"}, "abcdefghijklmnopqrstuvwxyz12345678 tail"},
		{"word wrap prose still joins", 36, []string{"the quick brown fox jumps over", "lazy-dog-again and so on"}, "the quick brown fox jumps over lazy-dog-again and so on"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mk(tc.cols, tc.rows...); got != tc.want {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// ---- 8: table rows stay intact in the transcript ----

func TestReviewTableRowsIntactInTranscript(t *testing.T) {
	w := 40
	rl := rule(w)
	s := snapOf(w,
		"⏺ Here is the table:",
		"┌──────┬──────┐",
		"│ name │ val  │",
		"│ a    │ 1    │",
		"└──────┴──────┘",
		"",
		rl, "❯ ", rl)
	s.Cursor = Pos{X: 2, Y: 7}
	r := Split(s, claudeRules())
	got := texts(r.Transcript)
	has := func(want string) bool {
		for _, g := range got {
			if g == want {
				return true
			}
		}
		return false
	}
	if !has("│ name │ val  │") || !has("│ a    │ 1    │") {
		t.Errorf("table rows lost their borders: %q", got)
	}
	// Dialog rows are still stripped.
	d := Split(snapOf(w, "╭────────────────────────────────────╮", "│ Do you want to proceed?            │", "│ ❯ 1. Yes                           │", "│   2. No                            │", "╰────────────────────────────────────╯"), claudeRules())
	if d.Dialog == nil {
		t.Fatal("no framed dialog")
	}
	for _, l := range d.Dialog.Lines {
		if strings.HasPrefix(l.Text, "│") {
			t.Errorf("dialog line keeps its border: %q", l.Text)
		}
	}
}

// ---- 9: yes/no ----

func TestReviewYesNoKeysAndPlacement(t *testing.T) {
	r := DefaultMenuRules()
	// Single-key mode: first letter whatever the prompt shows.
	got := DetectOptions(optSnap(40, "Proceed? (yes/no)"), -1, r)
	if len(got) != 2 || got[0].Keys != "y" || got[1].Keys != "n" || got[0].Submit {
		t.Fatalf("(yes/no) hidden cursor: %+v", got)
	}
	// Line mode: cursor after the prompt. (yes/no) needs the word + Enter.
	s := optSnap(40, "Proceed? (yes/no) ")
	s.CursorHidden, s.Cursor = false, Pos{X: 18, Y: 0}
	got = DetectOptions(s, -1, r)
	if len(got) != 2 || got[0].Keys != "yes" || got[1].Keys != "no" || !got[0].Submit {
		t.Fatalf("(yes/no) line mode: %+v", got)
	}
	s = optSnap(40, "Proceed? [y/N] ")
	s.CursorHidden, s.Cursor = false, Pos{X: 15, Y: 0}
	got = DetectOptions(s, -1, r)
	if len(got) != 2 || got[0].Keys != "y" || got[1].Keys != "n" || !got[0].Submit {
		t.Fatalf("[y/N] line mode: %+v", got)
	}
	// Placement: not the last content row, not the cursor row -> none.
	if got := DetectOptions(optSnap(40, "Old prompt? [y/N]", "● later output", "more output"), -1, r); got != nil {
		t.Errorf("stale prompt in transcript: %+v", got)
	}
	// Directly above the input box (rule skipped) -> found.
	rows := []string{"output", "Overwrite? [y/N]", rule(40), "❯ ", rule(40)}
	if got := DetectOptions(optSnap(40, rows...), 3, r); len(got) != 2 {
		t.Errorf("prompt above input: %+v", got)
	}
	// Quoted by a tool-output marker -> none.
	if got := DetectOptions(optSnap(40, "⎿ asks Overwrite? [y/N]"), -1, r); got != nil {
		t.Errorf("quoted prompt: %+v", got)
	}
	// The cursor row wins over a later row.
	s = optSnap(40, "Delete it? [y/n] ", "status line below")
	s.CursorHidden, s.Cursor = false, Pos{X: 17, Y: 0}
	if got := DetectOptions(s, -1, r); len(got) != 2 || !got[0].Submit {
		t.Errorf("cursor-row prompt: %+v", got)
	}
}

// ---- 11: labels from the full cell text ----

func TestReviewLabelsKeepCombiningMarksAndZWJ(t *testing.T) {
	r := DefaultMenuRules()
	s := optSnap(40, "Pick?", "❯ 1. cafe ok", "  2. team x")
	// One cell holds a whole grapheme cluster, like the emulator stores it.
	s.Screen[1].Cells[8] = Cell{Text: "e\u0301", Width: 1}
	s.Screen[2].Cells[5] = Cell{Text: "👨\u200d👩", Width: 1}
	got := DetectOptions(s, -1, r)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Label != "cafe\u0301 ok" {
		t.Errorf("combining mark dropped: %q", got[0].Label)
	}
	if got[1].Label != "👨\u200d👩eam x" {
		t.Errorf("ZWJ sequence cut: %q", got[1].Label)
	}
}

// ---- 12: hints ----

func TestReviewHintsRequireTo(t *testing.T) {
	r := DefaultMenuRules()
	if h := DetectHints(optSnap(60, "Then enter the value and save."), -1, r); len(h) != 0 {
		t.Errorf("prose matched as hint: %+v", h)
	}
	h := DetectHints(optSnap(60, "↑/↓ to select · Enter to confirm · Esc to cancel"), -1, r)
	if len(h) != 3 || h[0].Key != "↑/↓" || h[0].Action != "select" || h[2] != (Hint{"esc", "cancel"}) {
		t.Errorf("hints %+v", h)
	}
}
