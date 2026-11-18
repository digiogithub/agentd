package screen

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// pandoLike is a 100-column screen: a tab bar, a chat on the left, an info
// panel on the right behind a full-height divider, an input box and a footer
// that spans the whole width.
func pandoLike() *Snapshot {
	const w, split = 100, 70
	row := func(left, right string) string {
		return fmt.Sprintf("%-*s│ %s", split, left, right)
	}
	lines := []string{
		"Pando  Chat  Editor  Editor+Chat" + strings.Repeat(" ", 40) + "v1.5.5",
		row("", ""),
		row("> revisa los cambios", "https://example.org/pando"),
		row("", ""),
		row("* He mirado el diff y todo parece correcto.", "cwd: /work/pando"),
		row("", ""),
		row("  Segunda linea de la respuesta.", "Session: demo"),
		row("", "Sandbox: off"),
		row("", ""),
		row("", "LSP Configuration"),
		row("", "- gopls"),
		row("", ""),
		row("press enter to send", ""),
		row("> ", ""),
		"ctrl+h help" + strings.Repeat(" ", 60) + "No diagnostics",
	}
	return snapOf(w, lines...)
}

func pandoRules() Rules {
	r := DefaultRules()
	r.Ghost = GhostRules{Faint: true}
	r.HeaderRegex = regexp.MustCompile(`\bChat\b.*\bEditor\+Chat\b`)
	r.FooterRegex = regexp.MustCompile(`ctrl\+h help`)
	return r
}

func TestSplitCutsSidePanelAndHeader(t *testing.T) {
	reg := Split(pandoLike(), pandoRules())
	var text []string
	for _, l := range reg.Transcript {
		text = append(text, l.Text)
	}
	joined := strings.Join(text, "\n")
	for _, want := range []string{"revisa los cambios", "He mirado el diff", "Segunda linea"} {
		if !strings.Contains(joined, want) {
			t.Errorf("transcript lacks %q:\n%s", want, joined)
		}
	}
	for _, bad := range []string{"cwd:", "Session:", "gopls", "Editor+Chat", "│", "https://"} {
		if strings.Contains(joined, bad) {
			t.Errorf("transcript still has %q:\n%s", bad, joined)
		}
	}
	if len(reg.Header) != 1 || !strings.Contains(reg.Header[0], "Editor+Chat") {
		t.Errorf("header = %q", reg.Header)
	}
	side := strings.Join(reg.Side, "|")
	for _, want := range []string{"https://example.org/pando", "cwd: /work/pando", "Session: demo", "LSP Configuration", "- gopls"} {
		if !strings.Contains(side, want) {
			t.Errorf("side lacks %q: %q", want, side)
		}
	}
	// The full-width status bar is kept whole, in the footer.
	if len(reg.Footer) == 0 || !strings.Contains(strings.Join(reg.Footer, " "), "No diagnostics") {
		t.Errorf("footer = %q", reg.Footer)
	}
}

func TestNoSidePanelWithoutFullHeightDivider(t *testing.T) {
	// A table in the middle of the transcript is not a side panel.
	s := snapOf(100,
		"intro text",
		"│ a │ b │",
		"│ 1 │ 2 │",
		"more text",
		"",
		"",
		"",
		"",
	)
	if _, ok := findSideColumn(s); ok {
		t.Error("table taken for a side panel")
	}
	if reg := Split(s, DefaultRules()); len(reg.Side) != 0 {
		t.Errorf("side = %q", reg.Side)
	}
}
