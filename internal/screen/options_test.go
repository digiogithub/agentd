package screen

import (
	"strings"
	"testing"
)

// optSnap builds a Snapshot from text rows padded to cols. A row prefixed with
// "~" is drawn reverse-video (prefix removed).
func optSnap(cols int, rows ...string) *Snapshot {
	s := &Snapshot{Cols: cols, Rows: len(rows), CursorHidden: true}
	for _, t := range rows {
		rev := strings.HasPrefix(t, "~")
		t = strings.TrimPrefix(t, "~")
		var r Row
		for _, ru := range []rune(t) {
			c := Cell{Width: 1}
			if ru != ' ' {
				c.Text = string(ru)
			}
			if rev {
				c.Attrs = Reverse
			}
			r.Cells = append(r.Cells, c)
		}
		for len(r.Cells) < cols {
			r.Cells = append(r.Cells, Cell{Width: 1})
		}
		s.Screen = append(s.Screen, r)
	}
	return s
}

func labels(o []Option) []string {
	var out []string
	for _, x := range o {
		out = append(out, x.Label)
	}
	return out
}

func selectedIdx(o []Option) int {
	for i, x := range o {
		if x.Selected {
			return i
		}
	}
	return -1
}

var claudeDialog = []string{
	"  Bash command",
	"    rm -rf build",
	"",
	" Do you want to proceed?",
	" ❯ 1. Yes",
	"   2. Yes, and don't ask again for rm commands",
	"   3. No, and tell Claude what to do differently (esc)",
	"",
	"  Esc to cancel",
}

func TestDetectOptions(t *testing.T) {
	r := DefaultMenuRules()
	tests := []struct {
		name     string
		rows     []string
		input    int
		hidden   bool
		kind     string
		labels   []string
		selected int
		keys     []string
	}{
		{"claude permission dialog", claudeDialog, -1, true, KindNumbered,
			[]string{"Yes", "Yes, and don't ask again for rm commands", "No, and tell Claude what to do differently (esc)"}, 0, []string{"1", "2", "3"}},
		{"claude dialog in box border", []string{
			"│ Do you want to proceed?   │",
			"│ ❯ 1. Yes                  │",
			"│   2. No                   │",
		}, -1, true, KindNumbered, []string{"Yes", "No"}, 0, []string{"1", "2"}},
		{"codex pointer menu", []string{
			"  Allow command?",
			"  › Yes, proceed",
			"    No, cancel",
			"    Always allow",
		}, -1, true, KindPointer, []string{"Yes, proceed", "No, cancel", "Always allow"}, 0, nil},
		{"pointer on middle row", []string{
			"  Pick one",
			"    First",
			"  ▸ Second",
			"    Third",
		}, -1, true, KindPointer, []string{"First", "Second", "Third"}, 1, nil},
		{"selected by reverse style", []string{
			"Choose model",
			"  alpha",
			"~  beta  ",
			"  gamma",
		}, -1, true, KindPointer, []string{"alpha", "beta", "gamma"}, 1, nil},
		{"numbered selected by style", []string{
			"Which one?",
			"1. one",
			"~2. two",
			"3. three",
		}, -1, true, KindNumbered, []string{"one", "two", "three"}, 1, []string{"1", "2", "3"}},
		{"wrapped continuation label", []string{
			"Proceed?",
			"❯ 1. Yes, and remember this",
			"       choice for the session",
			"  2. No",
		}, -1, true, KindNumbered, []string{"Yes, and remember this choice for the session", "No"}, 0, []string{"1", "2"}},
		{"bracket numbers", []string{
			"Select an action?",
			"[1] build",
			"[2] test",
		}, -1, true, KindNumbered, []string{"build", "test"}, -1, []string{"1", "2"}},
		{"numbered list with paren", []string{
			"Which?",
			"❯ 1) a",
			"  2) b",
		}, -1, true, KindNumbered, []string{"a", "b"}, 0, []string{"1", "2"}},
		{"checkbox list", []string{
			"Select features",
			"❯ [x] lint",
			"  [ ] tests",
			"  [x] docs",
		}, -1, true, KindCheckbox, []string{"lint", "tests", "docs"}, 0, nil},
		{"yes/no default yes", []string{
			"output line",
			"Overwrite file? [Y/n] ",
		}, -1, true, KindYesNo, []string{"Yes", "No"}, 0, []string{"y", "n"}},
		{"yes/no plain", []string{
			"Continue (y/n)?",
		}, -1, true, KindYesNo, []string{"Yes", "No"}, -1, []string{"y", "n"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := optSnap(60, tc.rows...)
			s.CursorHidden = tc.hidden
			got := DetectOptions(s, tc.input, r)
			if strings.Join(labels(got), "|") != strings.Join(tc.labels, "|") {
				t.Fatalf("labels = %q, want %q", labels(got), tc.labels)
			}
			if got[0].Kind != tc.kind {
				t.Errorf("kind = %s, want %s", got[0].Kind, tc.kind)
			}
			if selectedIdx(got) != tc.selected {
				t.Errorf("selected = %d, want %d", selectedIdx(got), tc.selected)
			}
			for i, o := range got {
				if o.Index != i+1 || o.ID == "" || o.Confidence < r.MinConfidence {
					t.Errorf("option %d malformed: %+v", i, o)
				}
				if tc.keys != nil && o.Keys != tc.keys[i] {
					t.Errorf("keys[%d] = %q, want %q", i, o.Keys, tc.keys[i])
				}
			}
		})
	}
}

func TestDetectOptionsNone(t *testing.T) {
	r := DefaultMenuRules()
	idle := []string{
		"⏺ Done.",
		"",
		"────────────────────────",
		"❯ ",
		"────────────────────────",
		"  ? for shortcuts",
	}
	prose := []string{
		"Here is how to do it:",
		"1. install the tool",
		"2. run the tool",
		"3. enjoy",
	}
	proseQ := []string{
		"Plan summary.",
		"1. install the tool",
		"2. run the tool",
	}
	echoWrap := []string{
		"❯ please fix the failing test in",
		"  the parser package",
		"⏺ Sure.",
	}
	tests := []struct {
		name   string
		rows   []string
		input  int
		hidden bool
	}{
		{"claude idle input, row known", idle, 3, false},
		{"claude idle input, row unknown, cursor visible", idle, -1, false},
		{"claude idle input, row unknown, cursor hidden", idle, -1, true},
		{"ghost text suggestion with footer sibling", []string{"❯ Try \"fix lint errors\"", "  ? for shortcuts"}, -1, false},
		{"numbered prose without question", prose, -1, true},
		{"numbered prose, plan only", proseQ, -1, true},
		{"echoed wrapped prompt, cursor visible", echoWrap, -1, false},
		{"list not starting at 1", []string{"Pick?", "2. a", "3. b"}, -1, true},
		{"single numbered", []string{"Pick?", "1. only"}, -1, true},
		{"markdown task list", []string{"Todo?", "- [ ] a", "- [x] b"}, -1, true},
		{"yes/no typed in input row", []string{"❯ rename (y/n)"}, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := optSnap(60, tc.rows...)
			s.CursorHidden = tc.hidden
			if tc.name == "claude idle input, row unknown, cursor visible" {
				s.Cursor = Pos{X: 2, Y: 3}
			}
			if got := DetectOptions(s, tc.input, r); got != nil {
				t.Fatalf("got options %+v, want none", got)
			}
		})
	}
}

func TestPointerNeedsSiblingsAndHiddenCursor(t *testing.T) {
	r := DefaultMenuRules()
	rows := []string{"Pick", "❯ a", "  b"}
	s := optSnap(30, rows...)
	s.CursorHidden = false // visible cursor elsewhere, 2 rows: 0.5 < min
	s.Cursor = Pos{Y: 0}
	if got := DetectOptions(s, -1, r); got != nil {
		t.Errorf("2-row menu with visible cursor: %+v", got)
	}
	s.CursorHidden = true
	if got := DetectOptions(s, -1, r); len(got) != 2 {
		t.Errorf("hidden cursor: %+v", got)
	}
	// pointer row without siblings
	if got := DetectOptions(optSnap(30, "❯ lonely"), -1, r); got != nil {
		t.Errorf("lonely pointer: %+v", got)
	}
}

func TestInputRowExcluded(t *testing.T) {
	r := DefaultMenuRules()
	rows := []string{"Q?", "❯ 1. a", "  2. b", "❯ 3. typed"}
	s := optSnap(30, rows...)
	got := DetectOptions(s, 3, r)
	if len(got) != 2 {
		t.Fatalf("input row must not extend the menu: %+v", got)
	}
	// Without pointer or style, a list right above a live input row is prose
	// (regression: used to be detected with confidence 0.7).
	if got := DetectOptions(optSnap(30, "Q?", "  1. a", "  2. b", "❯ 3. typed"), 3, r); got != nil {
		t.Fatalf("unstyled list above input row: %+v", got)
	}
}

func TestStableIDs(t *testing.T) {
	r := DefaultMenuRules()
	a := DetectOptions(optSnap(60, claudeDialog...), -1, r)
	// Redraw: pointer moved, dialog shifted down, narrower terminal re-wraps a label.
	moved := []string{
		"", "",
		"  Bash command",
		"    rm -rf build",
		"",
		" Do you want to proceed?",
		"   1. Yes",
		" ❯ 2. Yes, and don't ask again for",
		"       rm commands",
		"   3. No, and tell Claude what to do",
		"       differently (esc)",
	}
	b := DetectOptions(optSnap(40, moved...), -1, r)
	if len(a) != 3 || len(b) != 3 {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Errorf("id %d changed: %s vs %s", i, a[i].ID, b[i].ID)
		}
	}
	if selectedIdx(b) != 1 {
		t.Errorf("pointer move not seen: %d", selectedIdx(b))
	}
	other := DetectOptions(optSnap(60, "Q?", "❯ 1. Yes", "  2. Maybe"), -1, r)
	if other[0].ID == a[0].ID {
		t.Error("different menus share an id for the same label")
	}
}

func TestWideCharContinuationCells(t *testing.T) {
	r := DefaultMenuRules()
	s := optSnap(30, "Q?", "❯ 1. 好", "  2. no")
	// Insert a Width:0 continuation cell after 好 (index == column).
	row := s.Screen[1]
	i := 6
	row.Cells[i].Width = 2
	row.Cells = append(row.Cells[:i+1], append([]Cell{{Width: 0}}, row.Cells[i+1:]...)...)
	row.Cells = row.Cells[:30]
	s.Screen[1] = row
	got := DetectOptions(s, -1, r)
	if len(got) != 2 || got[0].Label != "好" {
		t.Fatalf("got %+v", got)
	}
}

func TestHints(t *testing.T) {
	s := optSnap(60, claudeDialog...)
	h := DetectHints(s, -1, DefaultMenuRules())
	if len(h) != 1 || h[0] != (Hint{"esc", "cancel"}) {
		t.Errorf("hints = %+v", h)
	}
	s = optSnap(60, "Enter to confirm · Esc to cancel")
	h = DetectHints(s, -1, DefaultMenuRules())
	if len(h) != 2 || h[0].Key != "enter" || h[0].Action != "confirm" {
		t.Errorf("hints = %+v", h)
	}
}

func TestYesNoSubmitAndDefault(t *testing.T) {
	r := DefaultMenuRules()
	s := optSnap(40, "Remove files? [y/N] ")
	s.CursorHidden = false
	s.Cursor = Pos{X: 20, Y: 0}
	got := DetectOptions(s, -1, r)
	if len(got) != 2 || !got[0].Submit || got[0].Selected || !got[1].Selected {
		t.Fatalf("got %+v", got)
	}
	s.CursorHidden = true
	got = DetectOptions(s, -1, r)
	if got[0].Submit {
		t.Error("submit without cursor on the prompt")
	}
}

func TestPlanChoose(t *testing.T) {
	r := DefaultMenuRules()
	num := DetectOptions(optSnap(60, claudeDialog...), -1, r)
	steps := PlanChoose(num, num[2], false)
	if len(steps) != 2 || steps[0].Keys != "3" || !steps[0].Expect.Gone || steps[0].Expect.SelectedID != num[2].ID || !steps[1].IfNotGone || steps[1].Keys != "\r" {
		t.Errorf("numbered: %+v", steps)
	}
	// select: the digit only, never an Enter afterwards.
	if got := PlanChooseKeys(num, num[2], false, ChooseKeys{DigitAction: "select"}); len(got) != 1 ||
		got[0].Keys != "3" || !got[0].Expect.Gone || got[0].Expect.SelectedID != "" || got[0].IfNotGone {
		t.Errorf("digit select: %+v", got)
	}
	if got := PlanChooseKeys(num, num[2], false, ChooseKeys{DigitAction: "move"}); len(got) != 2 || !got[1].IfNotGone {
		t.Errorf("digit move: %+v", got)
	}
	if got := PlanChooseKeys(num, num[2], false, ChooseKeys{Select: "arrows", DigitAction: "select"}); len(got) != 3 {
		t.Errorf("arrows beat digit select: %+v", got)
	}
	if got := PlanChooseKeys(num, num[2], false, ChooseKeys{Select: "arrows"}); len(got) != 3 ||
		got[0].Keys != "\x1b[B" || got[1].Keys != "\x1b[B" || got[2].Keys != "\r" || got[0].Expect.SelectedID != num[1].ID {
		t.Errorf("forced arrows: %+v", got)
	}

	ptr := DetectOptions(optSnap(40, "  Pick one", "    First", "  ▸ Second", "    Third"), -1, r)
	up := PlanChoose(ptr, ptr[0], false)
	if len(up) != 2 || up[0].Keys != "\x1b[A" || up[1].Keys != "\r" || !up[1].Expect.Gone {
		t.Errorf("up: %+v", up)
	}
	dn := PlanChoose(ptr, ptr[2], true)
	if len(dn) != 2 || dn[0].Keys != "\x1bOB" {
		t.Errorf("ss3 down: %+v", dn)
	}
	same := PlanChoose(ptr, ptr[1], false)
	if len(same) != 1 || same[0].Keys != "\r" {
		t.Errorf("already selected: %+v", same)
	}

	// No known selection and no digit: not navigable.
	noSel := DetectOptions(optSnap(40, "Pick?", "[1] a", "[2] b"), -1, r)
	noSel[0].Keys, noSel[1].Keys = "", ""
	if PlanChoose(noSel, noSel[1], false) != nil {
		t.Error("expected nil plan without selection")
	}
	if PlanChoose(ptr, Option{ID: "nope"}, false) != nil {
		t.Error("expected nil plan for unknown target")
	}

	yn := DetectOptions(optSnap(40, "Go on? [Y/n]"), -1, r)
	if p := PlanChoose(yn, yn[1], false); len(p) != 1 || p[0].Keys != "n" {
		t.Errorf("yesno: %+v", p)
	}
	yn[1].Submit = true
	if p := PlanChoose(yn, yn[1], false); len(p) != 2 || p[1].Keys != "\r" {
		t.Errorf("yesno submit: %+v", p)
	}

	cb := DetectOptions(optSnap(40, "Select", "❯ [x] a", "  [ ] b", "  [ ] c"), -1, r)
	p := PlanChoose(cb, cb[2], false)
	if len(p) != 3 || p[2].Keys != " " || p[2].Expect.Checked == nil || !*p[2].Expect.Checked {
		t.Errorf("checkbox: %+v", p)
	}
}

// Review 1b: the context hash tells apart two dialogs with the same question
// and labels but different rows above the question, and ignores the pointer.
func TestMenuContext(t *testing.T) {
	r := DefaultMenuRules()
	mk := func(cmd, ptr string) (*Snapshot, []Option) {
		s := optSnap(60, "Command: "+cmd, "", "Run it?", ptr+" 1. Yes", "  2. No")
		return s, DetectOptions(s, -1, r)
	}
	sa, oa := mk("rm a", "❯")
	sb, ob := mk("rm b", "❯")
	if len(oa) == 0 || oa[0].MenuFingerprint != ob[0].MenuFingerprint {
		t.Fatalf("premise: same fingerprint expected: %+v %+v", oa, ob)
	}
	ca, cb := MenuContext(sa, oa), MenuContext(sb, ob)
	if ca == "" || ca == cb {
		t.Errorf("context a=%q b=%q", ca, cb)
	}
	sp := optSnap(60, "Command: rm a", "", "Run it?", "  1. Yes", "❯ 2. No")
	if got := MenuContext(sp, DetectOptions(sp, -1, r)); got != ca {
		t.Errorf("a moved pointer changed the context: %q vs %q", got, ca)
	}
	if MenuContext(sa, nil) != "" {
		t.Error("no options: empty context")
	}
}
