package session

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"

	"github.com/digiogithub/agentd/internal/termcolor"
)

// emu returns an emulator that has processed out.
func emu(t *testing.T, cols, rows int, out string) *vt.Emulator {
	t.Helper()
	// The fixtures contain no terminal queries, so the emulator never writes
	// a reply (which would block on its unbuffered pipe).
	e := vt.NewEmulator(cols, rows)
	if _, err := e.Write([]byte(out)); err != nil {
		t.Fatal(err)
	}
	return e
}

var (
	claudeRules = GhostRules{Faint: true, RightOfCursor: true, PromptMarker: "❯"}
	codexRules  = GhostRules{Faint: true, RightOfCursor: true, PromptMarker: "›"}
)

// Byte sequences reproduce the captures in docs/ghost-text.md.
const (
	claudeIdle = "\x1b[38;2;136;136;136m" + "────────────\r\n" +
		"\x1b[39m❯ \x1b[2mTry \"how does <filepath> work?\"\x1b[22m\r\n" +
		"\x1b[38;2;136;136;136m────────────\x1b[39m" +
		"\x1b[2;3H" // cursor on the first ghost cell (row 2, col 3; 1-based)
	codexIdle = "\x1b[2;1H\x1b[39;48;2;30;30;30m\x1b[1m›\x1b[22m \x1b[2mAsk Codex to do anything\x1b[22m\x1b[K\x1b[0m" +
		"\x1b[1;3H\x1b[2m(v0.166.0) faint help text\x1b[22m" +
		"\x1b[2;3H"
)

func TestFilterScreenClaude(t *testing.T) {
	e := emu(t, 60, 4, claudeIdle)
	raw, ghost := filterScreen(e, ScreenOpts{Ghost: claudeRules})
	if !strings.Contains(raw, `Try "how does <filepath> work?"`) {
		t.Fatalf("raw screen should keep the ghost:\n%s", raw)
	}
	if ghost != `Try "how does <filepath> work?"` {
		t.Fatalf("ghost = %q", ghost)
	}
	scr, ghost2 := filterScreen(e, ScreenOpts{HideGhost: true, Ghost: claudeRules})
	if ghost2 != ghost {
		t.Fatalf("ghost differs: %q vs %q", ghost2, ghost)
	}
	lines := strings.Split(scr, "\n")
	if lines[1] != "❯ " {
		t.Fatalf("input line = %q\n%s", lines[1], scr)
	}
	if lines[0] != "────────────" || lines[2] != "────────────" {
		t.Fatalf("other rows changed:\n%s", scr)
	}
	if strings.Contains(scr, "Try") {
		t.Fatalf("ghost visible:\n%s", scr)
	}
}

func TestFilterScreenCodexKeepsOtherFaintText(t *testing.T) {
	e := emu(t, 60, 4, codexIdle)
	scr, ghost := filterScreen(e, ScreenOpts{HideGhost: true, Ghost: codexRules})
	if ghost != "Ask Codex to do anything" {
		t.Fatalf("ghost = %q", ghost)
	}
	lines := strings.Split(scr, "\n")
	if lines[1] != "›" {
		t.Fatalf("input line = %q", lines[1])
	}
	if !strings.Contains(lines[0], "faint help text") {
		t.Fatalf("faint text outside the input line must stay:\n%s", scr)
	}
}

func TestFilterScreenTypedTextIsNotGhost(t *testing.T) {
	// The human typed "fix it" (normal intensity); the cursor is at its end.
	e := emu(t, 40, 3, "\x1b[2;1H❯ fix it")
	scr, ghost := filterScreen(e, ScreenOpts{HideGhost: true, Ghost: claudeRules})
	if ghost != "" || !strings.Contains(scr, "❯ fix it") {
		t.Fatalf("ghost %q, screen:\n%s", ghost, scr)
	}
	// Cursor moved back to the start of the typed text (Home): text right of
	// the cursor is not faint, so it is not ghost either.
	e = emu(t, 40, 3, "\x1b[2;1H❯ fix it\x1b[2;3H")
	if _, ghost := filterScreen(e, ScreenOpts{Ghost: claudeRules}); ghost != "" {
		t.Fatalf("typed text classified as ghost: %q", ghost)
	}
	// Without style rules, position alone decides.
	pos := GhostRules{RightOfCursor: true, PromptMarker: "❯"}
	if _, ghost := filterScreen(e, ScreenOpts{Ghost: pos}); ghost != "fix it" {
		t.Fatalf("position-only ghost = %q", ghost)
	}
}

func TestFilterScreenMarkerRequired(t *testing.T) {
	// Faint text right of the cursor, but the cursor is not after the marker.
	e := emu(t, 40, 3, "\x1b[2;1H❯ abc\x1b[2mdef\x1b[22m\x1b[2;6H")
	if _, ghost := filterScreen(e, ScreenOpts{Ghost: claudeRules}); ghost != "" {
		t.Fatalf("ghost = %q", ghost)
	}
	// Without the marker requirement, the faint suffix is ghost (inline
	// completion style).
	if _, ghost := filterScreen(e, ScreenOpts{Ghost: GhostRules{Faint: true}}); ghost != "def" {
		t.Fatalf("ghost = %q", ghost)
	}
}

func TestFilterScreenColors(t *testing.T) {
	out := "\x1b[2;1H> \x1b[38;5;244msuggested\x1b[39m \x1b[38;2;128;128;128mgrey\x1b[39m \x1b[90mbright\x1b[39m\x1b[2;3H"
	e := emu(t, 60, 3, out)
	specs, err := termcolor.ParseAll([]string{"244", "#808080", "8"})
	if err != nil {
		t.Fatal(err)
	}
	r := GhostRules{Colors: specs, RightOfCursor: true, PromptMarker: ">"}
	scr, ghost := filterScreen(e, ScreenOpts{HideGhost: true, Ghost: r})
	if ghost != "suggested grey bright" {
		t.Fatalf("ghost = %q", ghost)
	}
	if strings.Split(scr, "\n")[1] != ">" {
		t.Fatalf("screen:\n%s", scr)
	}
	// Only one colour configured: the other cells stay.
	r.Colors = specs[:1]
	scr, ghost = filterScreen(e, ScreenOpts{HideGhost: true, Ghost: r})
	if ghost != "suggested" || strings.Split(scr, "\n")[1] != ">"+strings.Repeat(" ", 11)+"grey bright" {
		t.Fatalf("ghost %q line %q", ghost, strings.Split(scr, "\n")[1])
	}
}

func TestFilterScreenWideAndDisabled(t *testing.T) {
	e := emu(t, 40, 2, "› \x1b[2m日本 ok\x1b[22m\x1b[1;3H")
	scr, ghost := filterScreen(e, ScreenOpts{HideGhost: true, Ghost: codexRules})
	if ghost != "日本 ok" || strings.TrimRight(scr, "\n") != "›" {
		t.Fatalf("ghost %q screen %q", ghost, scr)
	}
	// No rules: nothing is ghost.
	raw, ghost := filterScreen(e, ScreenOpts{HideGhost: true})
	if ghost != "" || !strings.Contains(raw, "日本 ok") {
		t.Fatalf("disabled rules: ghost %q screen %q", ghost, raw)
	}
}

// ghostCells must reject without a visible cell at or right of the cursor and
// for a cursor outside the screen, before copying the row; real ghost is still
// found.
func TestGhostCellsCheapRejection(t *testing.T) {
	e := emu(t, 20, 3, "\x1b[1;1H❯ typed")
	if got := ghostCells(e, uv.Position{X: 9, Y: 0}, claudeRules); got != nil {
		t.Errorf("nothing right of the cursor: %v", got)
	}
	if got := ghostCells(e, uv.Position{X: 20, Y: 0}, claudeRules); got != nil {
		t.Errorf("cursor outside: %v", got)
	}
	if got := ghostCells(e, uv.Position{X: -1, Y: 0}, claudeRules); got != nil {
		t.Errorf("negative cursor: %v", got)
	}
	e = emu(t, 20, 3, "\x1b[1;1H❯ \x1b[2mTry it\x1b[0m\x1b[1;3H")
	if got := ghostCells(e, e.CursorPosition(), claudeRules); len(got) == 0 {
		t.Error("faint ghost after the marker not found")
	}
}
