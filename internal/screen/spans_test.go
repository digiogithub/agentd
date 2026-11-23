package screen

import (
	"strings"
	"testing"
)

// styledRow builds a row from parts: text with "*" prefix = bold, "~" = fg
// 153 (inline code), "@" = fg 231 (a mark), plain otherwise.
func styledRow(parts ...string) Row {
	var r Row
	for _, p := range parts {
		var c Cell
		switch {
		case strings.HasPrefix(p, "*~"):
			p, c.Attrs, c.Fg = p[2:], Bold, "153"
		case strings.HasPrefix(p, "*"):
			p, c.Attrs = p[1:], Bold
		case strings.HasPrefix(p, "~"):
			p, c.Fg = p[1:], "153"
		case strings.HasPrefix(p, "@"):
			p, c.Fg = p[1:], "231"
		}
		for _, ch := range p {
			cc := c
			cc.Text, cc.Width = string(ch), 1
			if ch == ' ' {
				cc.Text = ""
			}
			r.Cells = append(r.Cells, cc)
		}
	}
	return r
}

func TestMarkupFromClaudeStyles(t *testing.T) {
	rows := []Row{
		styledRow("@●", " ", "*No por ahora.", " Chunking RAG no hace falta."),
		styledRow(""),
		styledRow("  ", "*Antes de RAG, más barato"),
		styledRow("  - Metadatos: ", "~organization_id", ", ", "~grant_call_id", ", página."),
		styledRow("  - Precio 2*3 con `x` y # no es título"),
		styledRow("  ", "~⎿  todo en color sin texto plano"),
		styledRow("  ---"),
		styledRow("  a_b <c> [d]"),
		styledRow("  - ", "*Guardar en ", "*~extractedConfig", "*: por regla", " y más"),
	}
	lines := ReflowRows(rows, 80, ReflowOpts{})
	var got []string
	for _, l := range lines {
		got = append(got, Markup(l))
	}
	want := []string{
		"● **No por ahora.** Chunking RAG no hace falta.",
		"",
		"  ### Antes de RAG, más barato",
		"  - Metadatos: `organization_id`, `grant_call_id`, página.",
		"  - Precio 2\\*3 con \\`x\\` y \\# no es título",
		"  ⎿  todo en color sin texto plano",
		"  \\---",
		"  a\\_b \\<c\\> \\[d\\]",
		"  - **Guardar en `extractedConfig`: por regla** y más",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("markup:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestSpansSurviveWrapJoin(t *testing.T) {
	rows := []Row{
		styledRow("  - uno dos tres cuatro cinco seis siete ocho nueve diez once doce trece"),
		styledRow("    catorce ", "~quince_x", " fin"),
	}
	rows[0].Wrapped = true
	lines := ReflowRows(rows, 74, ReflowOpts{})
	if len(lines) != 1 {
		t.Fatalf("lines %+v", lines)
	}
	if m := Markup(lines[0]); !strings.HasSuffix(m, "catorce `quince_x` fin") {
		t.Fatalf("markup %q", m)
	}
}

// A line with text in an ANSI palette colour is syntax-highlighted code; the
// theme colours of inline code and chrome ("#rrggbb") and bold are not.
func TestCodeLine(t *testing.T) {
	pal := func(s string) Row {
		r := styledRow("  ", "~"+s, " /tmp")
		for i := range r.Cells {
			if r.Cells[i].Fg != "" {
				r.Cells[i].Fg = "6"
			}
		}
		return r
	}
	theme := styledRow("  usa ", "~agentd", " aquí")
	for i := range theme.Cells {
		if theme.Cells[i].Fg != "" {
			theme.Cells[i].Fg = "#99ccff"
		}
	}
	lines := ReflowRows([]Row{pal("cd"), theme, styledRow("  ", "*Bash", "(ls)"), styledRow("  plain")}, 80, ReflowOpts{})
	want := []bool{true, false, false, false}
	for i, l := range lines {
		if CodeLine(l) != want[i] {
			t.Errorf("line %d %q: CodeLine = %v", i, l.Text, !want[i])
		}
	}
}
