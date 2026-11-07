package session_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/session"
	"github.com/digiogithub/agentd/internal/testutil"
)

// TestGhostFakeAgent: the faint suggestion is hidden by ScreenFiltered, never
// counts as a human draft, and disappears once a human types.
func TestGhostFakeAgent(t *testing.T) {
	bin := testutil.BuildFakeAgent(t)
	s := start(t, session.Spec{Name: "g", Cmd: []string{bin, "-ghost", "Run the tests"}, Cols: 80, Rows: 10})
	waitScreen(t, s, "Run the tests")
	opts := session.ScreenOpts{HideGhost: true, Ghost: session.GhostRules{Faint: true, RightOfCursor: true, PromptMarker: ">"}}

	scr, ghost := s.ScreenFiltered(opts)
	if ghost != "Run the tests" || strings.Contains(scr, "Run the tests") {
		t.Fatalf("ghost %q, filtered screen:\n%s", ghost, scr)
	}
	if !strings.Contains(scr, "\n>") {
		t.Fatalf("prompt missing from filtered screen:\n%s", scr)
	}
	if g := s.GhostText(opts.Ghost); g != "Run the tests" {
		t.Fatalf("GhostText = %q", g)
	}
	if raw, _ := s.ScreenFiltered(session.ScreenOpts{Ghost: opts.Ghost}); raw != s.Screen() {
		t.Fatalf("HideGhost=false should return the raw screen")
	}
	if !s.HumanDraftSince().IsZero() {
		t.Fatal("ghost text must never count as a human draft")
	}
	if sb, g := s.ScrollbackFiltered(opts); g != ghost || strings.Contains(sb, "Run the tests") {
		t.Fatalf("scrollback ghost %q:\n%s", g, sb)
	}

	if _, err := s.Write([]byte("x"), true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		scr, ghost = s.ScreenFiltered(opts)
		if ghost == "" && strings.Contains(scr, "> x") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ghost %q after typing, screen:\n%s", ghost, scr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Contains(s.Screen(), "Run the tests") {
		t.Fatalf("ghost still on the raw screen:\n%s", s.Screen())
	}
	if s.HumanDraftSince().IsZero() {
		t.Fatal("typed text is a draft")
	}
}

// TestClaudePostTurnFixtureSession: the live Claude Code bytes through a real
// session (PTY → filter → emulator): the title does not leak into the input
// line and the post-turn suggestion is the only ghost text.
func TestClaudePostTurnFixtureSession(t *testing.T) {
	f := filepath.Join(t.TempDir(), "turn.bin")
	if err := os.WriteFile(f, []byte(testutil.ClaudePostTurnSuggestion), 0o600); err != nil {
		t.Fatal(err)
	}
	s := start(t, session.Spec{Name: "c", Cmd: []string{"sh", "-c", `cat "$0"; exec sleep 60`, f}, Cols: 60, Rows: 6})
	waitScreen(t, s, testutil.ClaudePostTurnSuggestionText)
	rules := session.GhostRules{Faint: true, RightOfCursor: true, PromptMarker: "❯"}
	scr, ghost := s.ScreenFiltered(session.ScreenOpts{HideGhost: true, Ghost: rules})
	if ghost != testutil.ClaudePostTurnSuggestionText || strings.Contains(scr, "agentd") {
		t.Fatalf("ghost %q, screen:\n%s", ghost, scr)
	}
	if s.Title() != "� Ping de prueba de agentd" {
		t.Fatalf("title %q", s.Title())
	}
	cur, y, cells := s.RowCells(-1)
	if cur.X != 2 || y != 2 || len(cells) != 20 {
		t.Fatalf("cursor %v row %d cells %+v", cur, y, cells)
	}
}
