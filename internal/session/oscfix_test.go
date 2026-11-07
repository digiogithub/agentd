package session

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"

	"github.com/digiogithub/agentd/internal/testutil"
)

func filtered(in string) string {
	var f stringFilter
	return string(f.filter(nil, []byte(in)))
}

func TestStringFilter(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"title with 0x9C rune", "\x1b]0;✳ Hi\x07x", "\x1b]0;� Hi\x07x"},
		{"title without 0x9C", "\x1b]0;⠐ Hi\x07", "\x1b]0;⠐ Hi\x07"},
		{"ground text untouched", "✻ Worked ✳ \x9c", "✻ Worked ✳ \x9c"},
		{"ESC backslash ends the string", "\x1b]2;a\x1b\\✳", "\x1b]2;a\x1b\\✳"},
		{"lone 8-bit ST ends the string", "\x1b]2;a\x9c✳", "\x1b]2;a\x9c✳"},
		{"DCS", "\x1bP✳\x1b\\", "\x1bP�\x1b\\"},
		{"BEL does not end a DCS", "\x1bPa\x07✳\x1b\\", "\x1bPa\x07�\x1b\\"},
		{"malformed rune with 0x9C", "\x1b]0;\xe2\x9cX\x07", "\x1b]0;�X\x07"},
		{"CSI is not a string", "\x1b[31m✳", "\x1b[31m✳"},
	} {
		if got := filtered(tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestStringFilterSplit: any split of the stream gives the same output.
func TestStringFilterSplit(t *testing.T) {
	in := []byte(testutil.ClaudePostTurnSuggestion + "\x1bP✳\x1b\\")
	want := filtered(string(in))
	for i := 0; i <= len(in); i++ {
		var f stringFilter
		out := f.filter(nil, in[:i])
		out = f.filter(out, in[i:])
		if !bytes.Equal(out, []byte(want)) {
			t.Fatalf("split at %d: %q", i, out)
		}
	}
}

// emuFiltered feeds out through stringFilter into a new emulator, like
// Session.handleOutput does.
func emuFiltered(t *testing.T, cols, rows int, out string) (*vt.Emulator, *string) {
	t.Helper()
	e := vt.NewEmulator(cols, rows)
	title := new(string)
	e.SetCallbacks(vt.Callbacks{Title: func(s string) { *title = s }})
	var f stringFilter
	if _, err := e.Write(f.filter(nil, []byte(out))); err != nil {
		t.Fatal(err)
	}
	return e, title
}

// TestClaudePostTurnLive reproduces the live rows: without the filter the
// title leaks into the input line (" Prueba de agentd", "agentd" after the
// suggestion); with it the suggestion is the only ghost text.
func TestClaudePostTurnLive(t *testing.T) {
	// The bug, for reference: the raw stream through x/vt.
	raw := emu(t, 60, 6, testutil.ClaudePostTurnSuggestion)
	if row := rowString(raw, 2, nil); !strings.HasSuffix(row, "¿Te llegó el pong? agentd") {
		t.Logf("x/vt no longer leaks OSC text (row %q): the filter may be unnecessary", row)
	}

	e, title := emuFiltered(t, 60, 6, testutil.ClaudePostTurnSuggestion)
	if *title != "� Ping de prueba de agentd" {
		t.Fatalf("title = %q", *title)
	}
	scr, ghost := filterScreen(e, ScreenOpts{HideGhost: true, Ghost: claudeRules})
	if ghost != testutil.ClaudePostTurnSuggestionText {
		t.Fatalf("ghost = %q\n%s", ghost, e.String())
	}
	if line := strings.Split(scr, "\n")[2]; line != "❯ " {
		t.Fatalf("filtered input line = %q", line)
	}

	e, title = emuFiltered(t, 60, 6, testutil.ClaudePostTurnEmpty)
	if *title != "� Prueba de agentd" {
		t.Fatalf("title = %q", *title)
	}
	scr, ghost = filterScreen(e, ScreenOpts{HideGhost: true, Ghost: claudeRules})
	if ghost != "" || strings.Split(scr, "\n")[2] != "❯ " {
		t.Fatalf("ghost %q, screen:\n%s", ghost, scr)
	}
}

// TestClaudeTypedDraftAfterTurn: text typed after a turn (default style,
// cursor after it) is not ghost, even after the suggestion was shown.
func TestClaudeTypedDraftAfterTurn(t *testing.T) {
	// Typing replaces the suggestion: Claude redraws the line with the draft.
	out := testutil.ClaudePostTurnSuggestion + "\x1b[3;3Hhola\x1b[K"
	e, _ := emuFiltered(t, 60, 6, out)
	scr, ghost := filterScreen(e, ScreenOpts{HideGhost: true, Ghost: claudeRules})
	if ghost != "" || strings.Split(scr, "\n")[2] != "❯ hola" {
		t.Fatalf("ghost %q, screen:\n%s", ghost, scr)
	}
	// Cursor moved back to the start of the draft (Home): still not ghost.
	e, _ = emuFiltered(t, 60, 6, out+"\x1b[3;3H")
	if _, ghost = filterScreen(e, ScreenOpts{HideGhost: true, Ghost: claudeRules}); ghost != "" {
		t.Fatalf("draft classified as ghost: %q", ghost)
	}
}

func TestRowCells(t *testing.T) {
	e, _ := emuFiltered(t, 60, 6, testutil.ClaudePostTurnSuggestion)
	cur, y, cells := rowCells(e, -1)
	if cur.X != 2 || cur.Y != 2 || y != 2 {
		t.Fatalf("cursor %v row %d", cur, y)
	}
	if len(cells) != 20 {
		t.Fatalf("cells = %d: %+v", len(cells), cells)
	}
	if c := cells[0]; c.X != 0 || c.Rune != "U+276F" || c.Fg != "" || len(c.Attrs) != 0 || c.Width != 1 {
		t.Fatalf("marker cell %+v", c)
	}
	if c := cells[1]; c.Rune != "U+00A0" {
		t.Fatalf("NBSP cell %+v", c)
	}
	if c := cells[2]; c.X != 2 || c.Content != "¿" || len(c.Attrs) != 1 || c.Attrs[0] != "faint" {
		t.Fatalf("ghost cell %+v", c)
	}
	_, _, rule := rowCells(e, 0)
	if len(rule) != 12 || rule[0].Fg != "244" {
		t.Fatalf("rule row %+v", rule)
	}
	e2 := emu(t, 10, 2, "\x1b[1;1H\x1b[1;3;4;7;38;2;153;153;153;48;5;16mA\x1b[0m")
	_, _, c := rowCells(e2, 0)
	if len(c) != 1 || c[0].Fg != "#999999" || c[0].Bg != "16" ||
		strings.Join(c[0].Attrs, ",") != "bold,italic,reverse,underline" {
		t.Fatalf("styled cell %+v", c)
	}
	if _, _, c := rowCells(e2, 5); c != nil {
		t.Fatalf("out of range: %+v", c)
	}
}
