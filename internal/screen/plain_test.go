package screen

import "testing"

func TestPlainText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"plain untouched", "hello\nworld\n", "hello\nworld\n"},
		{"box", "╭──────────╮\n│ hello    │\n│ world    │\n╰──────────╯\n", "hello\nworld\n"},
		{"titled border", "╭─ Answer ─────╮\n│ text here    │\n╰──────────────╯\n", "Answer\ntext here\n"},
		{"heavy and double", "┏━━━┓\n┃ a ┃\n┗━━━┛\n╔═══╗\n║ b ║\n╚═══╝\n", "a\n\nb\n"},
		{"blocks", "▄▄▄▄▄▄\n█ hi █\n▀▀▀▀▀▀\n", "hi\n"},
		{"ascii kept", "| a | b |\n|---|---|\n+--+\n", "| a | b |\n|---|---|\n+--+\n"},
		{"inner glyph kept", "│ a │ b │\n", "a │ b\n"},
		{"indent kept relative", "│ top    │\n│   deep │\n", "top\n  deep\n"},
		{"blank collapse", "a\n\n\n\nb\n\n\n", "a\n\nb\n"},
		{"trailing blanks", "a   \nb\t\n", "a\nb\n"},
		{"indent of unframed text", "    code\n      more\n", "code\n  more\n"},
		{"side panel", "main a      │ side 1\nmain b      │ side 2\n            │\nmain c      │ side 3\n", "main a\nmain b\n\nmain c\n\nside 1\nside 2\n\nside 3\n"},
		{"crlf", "│ a │\r\n│ b │\r\n", "a\nb\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PlainText(c.in); got != c.want {
				t.Errorf("PlainText(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
