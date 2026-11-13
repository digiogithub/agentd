package screen

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/termcolor"
)

func claudeRules() Rules {
	r := DefaultRules()
	r.Ghost = GhostRules{Faint: true, RightOfCursor: true, PromptMarker: "❯"}
	r.PromptRegex = regexp.MustCompile(`(?m)^❯([ \x{a0}]|$)`)
	r.FooterRegex = regexp.MustCompile(`(?i)for shortcuts`)
	r.BusyRegex = regexp.MustCompile(`(?i)esc to interrupt`)
	r.SpinnerChars = "✻✳✢✶✽·*"
	r.Menu.Pointers = []string{"❯"}
	return r
}

func codexRules() Rules {
	r := DefaultRules()
	r.Ghost = GhostRules{Faint: true, RightOfCursor: true, PromptMarker: "›"}
	r.InputBgColors = []termcolor.Spec{{Index: -1, RGB: 0x1e1e1e}}
	r.Menu.Pointers = []string{"›"}
	return r
}

func rule(w int) string { return strings.Repeat("─", w) }

// claudeScreen builds a 60-col Claude-like screen: transcript, status line,
// rule, input row, rule, footer.
func claudeScreen(input string, faintFrom int, cursorX int) *Snapshot {
	w := 60
	s := snapOf(w,
		"⏺ Done. The tests pass and the change is ready for review.",
		"",
		"✻ Thinking… (12s · esc to interrupt)",
		rule(w),
		input,
		rule(w),
		"  ? for shortcuts",
		"",
	)
	if faintFrom >= 0 {
		s.Screen[4] = setFaint(s.Screen[4], faintFrom)
	}
	s.Cursor = Pos{X: cursorX, Y: 4}
	return s
}

func TestSplitClaudeGhost(t *testing.T) {
	s := claudeScreen("❯ Try \"fix lint errors\"", 2, 2)
	r := Split(s, claudeRules())
	if !r.HasInput || r.Input.Row != 4 || r.Input.Top != 4 || r.Input.Bottom != 4 {
		t.Fatalf("input %+v", r.Input)
	}
	if r.Input.Text != "" || r.Input.Ghost != `Try "fix lint errors"` || r.Input.State != InputSuggestion || r.Input.Draft || !r.Input.Marker {
		t.Fatalf("input %+v", r.Input)
	}
	wantOf := []Region{RegionTranscript, RegionTranscript, RegionStatus, RegionRule, RegionInput, RegionRule, RegionFooter, RegionFooter}
	if !reflect.DeepEqual(r.Of, wantOf) {
		t.Fatalf("Of = %v", r.Of)
	}
	if !reflect.DeepEqual(r.Footer, []string{"? for shortcuts"}) {
		t.Fatalf("footer %q", r.Footer)
	}
	if got := texts(r.Status); !reflect.DeepEqual(got, []string{"✻ Thinking… (12s · esc to interrupt)"}) || r.Status[0].Role != RoleStatus {
		t.Fatalf("status %+v", r.Status)
	}
	if got := texts(r.Transcript); !reflect.DeepEqual(got, []string{"⏺ Done. The tests pass and the change is ready for review."}) {
		t.Fatalf("transcript %q", got)
	}
	if r.Dialog != nil {
		t.Fatalf("dialog %+v", r.Dialog)
	}
}

func TestSplitClaudeDraftAndMultiRow(t *testing.T) {
	s := claudeScreen("❯ fix the", -1, 10)
	s.DraftSince = time.Now().Add(-time.Minute)
	s.Taken = time.Now()
	r := Split(s, claudeRules())
	if r.Input.Text != "fix the" || r.Input.State != InputDraft || !r.Input.Draft {
		t.Fatalf("input %+v", r.Input)
	}
	s.DraftSince = time.Now().Add(-time.Hour)
	if r = Split(s, claudeRules()); r.Input.State != InputEmpty || r.Input.Draft {
		t.Fatalf("stale draft: %+v", r.Input)
	}

	// Two-row draft inside the rules.
	w := 40
	m := snapOf(w, "answer", rule(w), "❯ first row of the draft", "  second row", rule(w), "  ? for shortcuts")
	m.Cursor = Pos{X: 14, Y: 3}
	r = Split(m, claudeRules())
	if r.Input.Top != 2 || r.Input.Bottom != 3 || r.Input.Text != "first row of the draft\nsecond row" {
		t.Fatalf("input %+v", r.Input)
	}
}

func TestSplitClaudeIdleEmpty(t *testing.T) {
	r := Split(claudeScreen("❯ ", -1, 2), claudeRules())
	if !r.HasInput || r.Input.Text != "" || r.Input.Ghost != "" || r.Input.State != InputEmpty {
		t.Fatalf("input %+v", r.Input)
	}
}

func TestSplitCodexBackground(t *testing.T) {
	w := 50
	s := snapOf(w, "• Ran the tests", "", "› Ask Codex to do anything", "", "  ⏎ send   ctrl+c quit", "")
	for _, y := range []int{2, 3} {
		s.Screen[y] = setBg(s.Screen[y], "#1e1e1e")
	}
	s.Screen[2] = setFaint(s.Screen[2], 2)
	s.Cursor = Pos{X: 2, Y: 2}
	r := Split(s, codexRules())
	if !r.HasInput || r.Input.Top != 2 || r.Input.Bottom != 3 || r.Input.Ghost != "Ask Codex to do anything" || r.Input.Text != "" {
		t.Fatalf("input %+v", r.Input)
	}
	if !reflect.DeepEqual(r.Footer, []string{"⏎ send   ctrl+c quit"}) {
		t.Fatalf("footer %q", r.Footer)
	}
	if got := texts(r.Transcript); !reflect.DeepEqual(got, []string{"• Ran the tests"}) {
		t.Fatalf("transcript %q", got)
	}

	// Cursor away from the marker: still found through the background.
	s.Cursor = Pos{X: 10, Y: 3}
	s.Screen[2] = mkRow("  draft", w)
	s.Screen[2] = setBg(s.Screen[2], "#1e1e1e")
	if r = Split(s, codexRules()); !r.HasInput || r.Input.Row != 3 {
		t.Fatalf("bg input %+v", r.Input)
	}
}

func TestSplitNoMarkerBottomThird(t *testing.T) {
	s := snapOf(20, "out", "", "", "", "", "$ ls")
	s.Cursor = Pos{X: 4, Y: 5}
	r := Split(s, DefaultRules())
	if !r.HasInput || r.Input.Text != "$ ls" {
		t.Fatalf("%+v", r.Input)
	}
	s.Cursor = Pos{X: 4, Y: 0}
	if r = Split(s, DefaultRules()); r.HasInput {
		t.Fatalf("cursor at top must not be input: %+v", r.Input)
	}
}

func TestSplitNumberedDialogHiddenCursor(t *testing.T) {
	w := 60
	s := snapOf(w,
		"⏺ Bash(rm -rf build)",
		"",
		rule(w),
		" Do you want to proceed?",
		" ❯ 1. Yes",
		"   2. Yes, and don't ask again",
		"   3. No",
		"",
		" Esc to cancel",
	)
	s.CursorHidden = true
	s.Cursor = Pos{X: 0, Y: 8}
	r := Split(s, claudeRules())
	if r.HasInput {
		t.Fatalf("menu pointer row taken as input: %+v", r.Input)
	}
	if r.Dialog == nil || r.Dialog.First != 3 || r.Dialog.Last != 6 || r.Dialog.Question != "Do you want to proceed?" || len(r.Dialog.Options) != 3 {
		t.Fatalf("dialog %+v", r.Dialog)
	}
	if r.Of[4] != RegionDialog || r.Of[2] != RegionRule || r.Of[0] != RegionTranscript {
		t.Fatalf("Of %v", r.Of)
	}
	if got := texts(r.Transcript); !reflect.DeepEqual(got, []string{"⏺ Bash(rm -rf build)", "", " Esc to cancel"}[:1]) && len(got) < 1 {
		t.Fatalf("transcript %q", got)
	}
}

func TestSplitFramedDialogAndFramedInput(t *testing.T) {
	w := 40
	frame := func(inner ...string) []string {
		out := []string{"╭" + rule(w-2) + "╮"}
		for _, l := range inner {
			out = append(out, "│ "+l+strings.Repeat(" ", w-4-len([]rune(l)))+" │")
		}
		return append(out, "╰"+rule(w-2)+"╯")
	}
	lines := append([]string{"output line"}, frame("Allow this edit?", "", "  1. Yes", "❯ 2. No")...)
	s := snapOf(w, lines...)
	s.CursorHidden = true
	r := Split(s, claudeRules())
	if r.Dialog == nil || r.Dialog.First != 1 || r.Dialog.Last != 6 || r.Dialog.Question != "Allow this edit?" || len(r.Dialog.Options) != 2 {
		t.Fatalf("dialog %+v", r.Dialog)
	}
	if got := texts(r.Transcript); !reflect.DeepEqual(got, []string{"output line"}) {
		t.Fatalf("transcript %q", got)
	}

	// The input box itself drawn as a frame is not a dialog.
	lines = append([]string{"output line"}, frame("❯ hello")...)
	s = snapOf(w, lines...)
	s.Cursor = Pos{X: 4, Y: 2}
	r = Split(s, claudeRules())
	if r.Dialog != nil || !r.HasInput || r.Input.Text != "hello" || r.Input.Top != 2 || r.Input.Bottom != 2 {
		t.Fatalf("framed input: %+v dialog %+v", r.Input, r.Dialog)
	}
}

func TestSplitFooterWithoutBoxAndWideTranscript(t *testing.T) {
	s := snapOf(40, "中文 line", "", "  ? for shortcuts", "")
	s.CursorHidden = true
	r := Split(s, claudeRules())
	if !reflect.DeepEqual(r.Footer, []string{"? for shortcuts"}) || r.Of[2] != RegionFooter {
		t.Fatalf("footer %q Of %v", r.Footer, r.Of)
	}
	if got := texts(r.Transcript); !reflect.DeepEqual(got, []string{"中文 line"}) {
		t.Fatalf("transcript %q", got)
	}
}

func TestSplitStatusOnlyDirectlyAboveBox(t *testing.T) {
	w := 40
	s := snapOf(w, "* a bullet that looks like a spinner", "plain text", "✻ Working (esc to interrupt)", rule(w), "❯ ", rule(w))
	s.Cursor = Pos{X: 2, Y: 4}
	r := Split(s, claudeRules())
	if r.Of[0] != RegionTranscript || r.Of[2] != RegionStatus || len(r.Status) != 1 {
		t.Fatalf("Of %v", r.Of)
	}
}

func TestSplitScrollbackIsTranscript(t *testing.T) {
	w := 40
	s := snapOf(w, "current", rule(w), "❯ ", rule(w))
	s.Scrollback = rowsOf(w, "older line", "")
	s.Cursor = Pos{X: 2, Y: 2}
	r := Split(s, claudeRules())
	if got := texts(r.Transcript); !reflect.DeepEqual(got, []string{"older line", "", "current"}) {
		t.Fatalf("transcript %q", got)
	}
	s.AltScreen = true
	if got := texts(Split(s, claudeRules()).Transcript); !reflect.DeepEqual(got, []string{"current"}) {
		t.Fatalf("alt transcript %q", got)
	}
}

func TestRuleRowDetection(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{rule(40), true},
		{"──── section ────", false}, // spans < 50 % and is < 80 % rule glyphs
		{"━━━━━━━━━━━━━━━━━━━━━━━━", true},
		{"short ──", false},
	}
	for _, c := range cases {
		if got := isRuleRow(mkRow(c.text, 40), 40, DefaultRuleChars); got != c.want {
			t.Errorf("%q: %v", c.text, got)
		}
	}
}
