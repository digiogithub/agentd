package daemon

import "testing"

func TestSplitTermReplies(t *testing.T) {
	cases := []struct {
		name, in, input, replies string
	}{
		{"plain", "hello\r", "hello\r", ""},
		{"cpr", "\x1b[12;40R", "", "\x1b[12;40R"},
		{"decxcpr", "\x1b[?12;40;1R", "", "\x1b[?12;40;1R"},
		{"da1", "\x1b[?62;22c", "", "\x1b[?62;22c"},
		{"da2", "\x1b[>1;10;0c", "", "\x1b[>1;10;0c"},
		{"decrpm", "\x1b[?2004;1$y", "", "\x1b[?2004;1$y"},
		{"ansi rpm", "\x1b[4;2$y", "", "\x1b[4;2$y"},
		{"kitty flags", "\x1b[?0u", "", "\x1b[?0u"},
		{"osc11 bel", "\x1b]11;rgb:1e1e/1e1e/1e1e\x07", "", "\x1b]11;rgb:1e1e/1e1e/1e1e\x07"},
		{"osc10 st", "\x1b]10;rgb:ffff/ffff/ffff\x1b\\", "", "\x1b]10;rgb:ffff/ffff/ffff\x1b\\"},
		{"osc4", "\x1b]4;1;rgb:cd/00/00\x07", "", "\x1b]4;1;rgb:cd/00/00\x07"},
		{"dcs xtversion", "\x1bP>|kitty(0.35)\x1b\\", "", "\x1bP>|kitty(0.35)\x1b\\"},
		{"mixed", "ab\x1b[5;1Rcd\x1b[?1;2c\r", "abcd\r", "\x1b[5;1R\x1b[?1;2c"},
		{"cursor keys", "\x1b[A\x1b[B\x1bOC", "\x1b[A\x1b[B\x1bOC", ""},
		{"focus", "\x1b[I\x1b[O", "\x1b[I\x1b[O", ""},
		{"mouse", "\x1b[<0;10;5M", "\x1b[<0;10;5M", ""},
		{"bracketed paste", "\x1b[200~text\x1b[201~", "\x1b[200~text\x1b[201~", ""},
		{"alt key", "\x1bx", "\x1bx", ""},
		{"lone esc", "\x1b", "\x1b", ""},
		{"truncated csi", "a\x1b[12;4", "a\x1b[12;4", ""},
		{"unterminated osc", "\x1b]11;rgb:00", "\x1b]11;rgb:00", ""},
		{"osc title not reply", "\x1b]0;title\x07", "\x1b]0;title\x07", ""},
		{"F-key R without semicolon", "\x1b[2R", "\x1b[2R", ""},
		{"DA query is not reply", "\x1b[c", "\x1b[c", ""},
		{"dsr ok", "\x1b[0n", "", "\x1b[0n"},
		{"color scheme 2031", "\x1b[?997;1n", "", "\x1b[?997;1n"},
		{"xtwinops size", "\x1b[8;50;160t", "", "\x1b[8;50;160t"},
		{"xtwinops pixels", "\x1b[4;800;1280t", "", "\x1b[4;800;1280t"},
	}
	for _, c := range cases {
		in, rep := splitTermReplies([]byte(c.in))
		if string(in) != c.input || string(rep) != c.replies {
			t.Errorf("%s: split(%q) = (%q, %q), want (%q, %q)", c.name, c.in, in, rep, c.input, c.replies)
		}
	}
}

func TestAmbientOnly(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"focus in", "\x1b[I", true},
		{"focus out", "\x1b[O", true},
		{"focus pair", "\x1b[O\x1b[I", true},
		{"sgr motion no button", "\x1b[<35;10;5M", true},
		{"sgr motion with shift", "\x1b[<39;10;5M", true},
		{"sgr wheel up", "\x1b[<64;10;5M", true},
		{"sgr wheel down", "\x1b[<65;3;4M", true},
		{"several motions", "\x1b[<35;10;5M\x1b[<35;11;5M\x1b[<35;12;6M", true},
		{"urxvt motion", "\x1b[67;10;5M", true},
		{"x10 motion", "\x1b[M" + string([]byte{32 + 35, 32 + 10, 32 + 5}), true},
		{"x10 press", "\x1b[M" + string([]byte{32, 32 + 10, 32 + 5}), false},
		{"sgr press", "\x1b[<0;10;5M", false},
		{"sgr release", "\x1b[<0;10;5m", false},
		{"sgr drag", "\x1b[<32;10;5M", false},
		{"key", "a", false},
		{"motion then key", "\x1b[<35;10;5Mx", false},
		{"cursor key", "\x1b[A", false},
		{"enter", "\r", false},
		{"truncated", "\x1b[<35;10", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		if got := ambientOnly([]byte(c.in)); got != c.want {
			t.Errorf("%s: ambientOnly(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}
