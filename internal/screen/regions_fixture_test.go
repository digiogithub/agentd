package screen_test

import (
	"regexp"
	"testing"

	"github.com/digiogithub/agentd/internal/screen"
	"github.com/digiogithub/agentd/internal/testutil"
	"github.com/digiogithub/agentd/internal/testutil/screenfix"
)

func claudeRules() screen.Rules {
	r := screen.DefaultRules()
	r.Ghost = screen.GhostRules{Faint: true, RightOfCursor: true, PromptMarker: "❯"}
	r.PromptRegex = regexp.MustCompile(`(?m)^❯([ \x{a0}]|$)`)
	r.Menu.Pointers = []string{"❯"}
	return r
}

func TestSplitFixtureClaudePostTurn(t *testing.T) {
	s := screenfix.MustLoad(t, "claude-post-turn-suggestion").Replay()
	r := screen.Split(s, claudeRules())
	if !r.HasInput || r.Input.Row != 2 || r.Input.Text != "" || r.Input.Ghost != testutil.ClaudePostTurnSuggestionText || r.Input.State != screen.InputSuggestion {
		t.Fatalf("input %+v", r.Input)
	}
	if r.Of[0] != screen.RegionRule || r.Of[3] != screen.RegionRule || r.Of[2] != screen.RegionInput {
		t.Fatalf("Of %v", r.Of)
	}
	e := screenfix.MustLoad(t, "claude-post-turn-empty").Replay()
	r = screen.Split(e, claudeRules())
	if !r.HasInput || r.Input.Ghost != "" || r.Input.State != screen.InputEmpty {
		t.Fatalf("empty input %+v", r.Input)
	}
}

func TestSplitFixtureFakeAgent(t *testing.T) {
	// fake-box-ghost: "❯ Try ..." between two rules, footer below.
	s := screenfix.MustLoad(t, "fake-box-ghost").Replay()
	r := screen.Split(s, claudeRules())
	if !r.HasInput || r.Input.Top != 2 || r.Input.Bottom != 2 || r.Input.Ghost != `Try "fix lint errors"` || len(r.Footer) != 1 || r.Footer[0] != "? for shortcuts" {
		t.Fatalf("box: %+v footer %q", r.Input, r.Footer)
	}
	// fake-dialog: framed dialog with numbered options, hidden cursor.
	s = screenfix.MustLoad(t, "fake-dialog").Replay()
	r = screen.Split(s, claudeRules())
	if r.HasInput || r.Dialog == nil || r.Dialog.Question != "Do you want to proceed?" || len(r.Dialog.Options) != 3 {
		t.Fatalf("dialog: %+v", r.Dialog)
	}
	// fake-menu: pointer menu is a dialog, not an input line.
	s = screenfix.MustLoad(t, "fake-menu").Replay()
	r = screen.Split(s, claudeRules())
	if r.HasInput || r.Dialog == nil || len(r.Dialog.Options) != 3 {
		t.Fatalf("menu: input=%v dialog=%+v", r.HasInput, r.Dialog)
	}
}

// The same wrapped fakeagent output at two sizes reflows to the same text.
func TestReflowFixtureWrapSizes(t *testing.T) {
	a := screen.Reflow(screenfix.MustLoad(t, "fake-wrap-80").Replay(), screen.ReflowOpts{Rules: screen.DefaultRules()})
	b := screen.Reflow(screenfix.MustLoad(t, "fake-wrap-120").Replay(), screen.ReflowOpts{Rules: screen.DefaultRules()})
	t.Logf("80: %+v", a)
	t.Logf("120: %+v", b)
	if len(a) != len(b) {
		t.Fatalf("%d vs %d lines", len(a), len(b))
	}
	for i := range a {
		if a[i].Text != b[i].Text {
			t.Errorf("line %d: %q vs %q", i, a[i].Text, b[i].Text)
		}
	}
}
