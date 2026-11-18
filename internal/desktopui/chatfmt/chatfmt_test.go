package chatfmt

import (
	"reflect"
	"strings"
	"testing"
)

func tableOf(t *testing.T, text string) *Table {
	t.Helper()
	for _, b := range Split(text) {
		if b.Table != nil {
			return b.Table
		}
	}
	t.Fatalf("no table in %q: %+v", text, Split(text))
	return nil
}

func TestBoxWrappedCells(t *testing.T) {
	tb := tableOf(t, strings.Join([]string{
		"  ┌──────────┬───────────┐",
		"  │ Columna  │ Valor     │",
		"  ├──────────┼───────────┤",
		"  │ uno      │ texto que │",
		"  │          │ sigue     │",
		"  ├──────────┼───────────┤",
		"  │ dos      │ 2         │",
		"  └──────────┴───────────┘",
	}, "\n"))
	if !reflect.DeepEqual(tb.Header, []string{"Columna", "Valor"}) {
		t.Errorf("header %v", tb.Header)
	}
	want := [][]string{{"uno", "texto que sigue"}, {"dos", "2"}}
	if !reflect.DeepEqual(tb.Rows, want) {
		t.Errorf("rows %v", tb.Rows)
	}
}

func TestBoxNoRowSeparatorsAndMark(t *testing.T) {
	tb := tableOf(t, strings.Join([]string{
		"⏺ ┌────┬──────────┐",
		"  │ N  │ Nota     │",
		"  ├────┼──────────┤",
		"  │ a  │ primera  │",
		"  │    │ continua │",
		"  │ b  │ segunda  │",
		"  └────┴──────────┘",
	}, "\n"))
	want := [][]string{{"a", "primera continua"}, {"b", "segunda"}}
	if !reflect.DeepEqual(tb.Rows, want) || tb.Header[1] != "Nota" {
		t.Errorf("%+v", tb)
	}
}

func TestHeavyAndDouble(t *testing.T) {
	for _, s := range []string{
		"┏━━━┳━━━┓\n┃ a ┃ b ┃\n┣━━━╋━━━┫\n┃ 1 ┃ 2 ┃\n┗━━━┻━━━┛",
		"╔═══╦═══╗\n║ a ║ b ║\n╠═══╬═══╣\n║ 1 ║ 2 ║\n╚═══╩═══╝",
	} {
		tb := tableOf(t, s)
		if !reflect.DeepEqual(tb.Header, []string{"a", "b"}) || !reflect.DeepEqual(tb.Rows, [][]string{{"1", "2"}}) {
			t.Errorf("%+v", tb)
		}
	}
}

func TestBoxWideChars(t *testing.T) {
	tb := tableOf(t, "┌──────┬────┐\n│ 日本 │ ok │\n├──────┼────┤\n│ 你好 │ 🙂 │\n└──────┴────┘")
	if tb.Rows[0][0] != "你好" || tb.Header[0] != "日本" {
		t.Errorf("%+v", tb)
	}
}

func TestPipeTable(t *testing.T) {
	text := "Antes\n| a | b | c |\n|---|:--:|--:|\n| 1 | dos \\| x | 3 |\n| 4 | 5 |\nDespués"
	bl := Split(text)
	if len(bl) != 3 || bl[0].Text != "Antes" || bl[2].Text != "Después" {
		t.Fatalf("%+v", bl)
	}
	tb := bl[1].Table
	if !reflect.DeepEqual(tb.Align, []Align{AlignLeft, AlignCenter, AlignRight}) {
		t.Errorf("align %v", tb.Align)
	}
	if tb.Rows[0][1] != "dos | x" || !reflect.DeepEqual(tb.Rows[1], []string{"4", "5", ""}) {
		t.Errorf("rows %v", tb.Rows)
	}
}

func TestBrokenFallsBackToText(t *testing.T) {
	for _, s := range []string{
		"┌───┬───┐\n│ a │ b │\n│ c │\n└───┴───┘", // missing a bar
		"┌───┬───┐\n│ a │ b │ c │\n└───┴───┘",    // bars off the grid
		"| a | b |\n| 1 | 2 |",            // no separator row
		"| a | b |\n|---|\n| 1 | 2 |",     // separator width differs
		"| a | b |\n| x | y |\n|---|---|", // separator not second
		"just text | with a pipe |",
	} {
		for _, b := range Split(s) {
			if b.Table != nil {
				t.Errorf("parsed %q as %+v", s, b.Table)
			}
		}
		if got := Split(s); len(got) != 1 || got[0].Text != s {
			t.Errorf("text changed for %q: %+v", s, got)
		}
	}
}

func TestTrimURL(t *testing.T) {
	cases := map[string]string{
		"https://a.com/x).":                      "https://a.com/x",
		"https://en.wikipedia.org/wiki/Go_(x)":   "https://en.wikipedia.org/wiki/Go_(x)",
		"https://en.wikipedia.org/wiki/Go_(x)).": "https://en.wikipedia.org/wiki/Go_(x)",
		"http://a.com/?q=1,":                     "http://a.com/?q=1",
		"https://a.com/a]":                       "https://a.com/a",
		"https://a.com/\"'":                      "https://a.com/",
	}
	for in, want := range cases {
		if got := TrimURL(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestLinkify(t *testing.T) {
	got := Linkify("Mira (https://a.com/x), y http://b.org. fin ftp://no")
	var urls []string
	var joined string
	for _, s := range got {
		joined += s.Text
		if s.URL != "" {
			urls = append(urls, s.URL)
		}
	}
	if !reflect.DeepEqual(urls, []string{"https://a.com/x", "http://b.org"}) {
		t.Errorf("urls %v", urls)
	}
	if joined != "Mira (https://a.com/x), y http://b.org. fin ftp://no" {
		t.Errorf("segments do not rebuild the text: %q", joined)
	}
	if HasURL("sin enlaces https:// aquí") {
		t.Error("bare scheme is not a link")
	}
}

func TestUnmarkup(t *testing.T) {
	for in, want := range map[string]string{
		"**Total** `a_b` 2\\*3 \\[x\\]": "Total a_b 2*3 [x]",
		"### Título":                    "Título",
		"plain":                         "plain",
	} {
		if got := Unmarkup(in); got != want {
			t.Errorf("Unmarkup(%q) = %q, want %q", in, got, want)
		}
	}
}
