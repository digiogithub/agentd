package session

import "testing"

func TestStripControls(t *testing.T) {
	cases := map[string]string{
		"A\x1b[20\x1b[201~1~B":  "A[20[201~1~B",
		"a\tb\nc\rd":            "a\tb\nc\rd",
		"x\x00\x07\x08\x7fy":    "xy",
		"\x1b]0;pwn\x07ok":      "]0;pwnok",
		"c1\u009b31mred":        "c131mred",
		"ñandú — ok":            "ñandú — ok",
		"bad\xffutf8":           "badutf8",
		"\x1b[200~in\x1b[201~x": "[200~in[201~x",
	}
	for in, want := range cases {
		if got := StripControls(in); got != want {
			t.Errorf("StripControls(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPasteSafe(t *testing.T) {
	cases := map[string]string{
		"A\x1b[20\x1b[201~1~B":             "AB",
		"first\x1b[201~ line":              "first line",
		"x\x1b[200~\x1b[201~y\x1b]0;t\x07": "xy]0;t",
		"\x1b[2\x1b[201~01~":               "",
	}
	for in, want := range cases {
		if got := PasteSafe(in); got != want {
			t.Errorf("PasteSafe(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDraftEffect(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"h", draftSet},
		{"hello\r", draftClear},
		{"\r", draftClear},
		{"\n", draftClear},
		{"abc\x03", draftClear},
		{"run\rnext", draftSet},
		{"\x1b[A", draftNone},        // cursor up
		{"\x1bOB", draftNone},        // SS3 cursor down
		{"\x1b[I", draftNone},        // focus in
		{"\x1b[<0;10;5M", draftNone}, // SGR mouse
		{"\x1b]11;rgb:0/0/0\x07", draftNone},
		{"\x1b[200~pasted\x1b[201~", draftSet},
		{"x\x1b[D", draftSet},
		{"\x7f", draftSet},
		{"", draftNone},
	}
	for _, c := range cases {
		if got := draftEffect([]byte(c.in)); got != c.want {
			t.Errorf("draftEffect(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
