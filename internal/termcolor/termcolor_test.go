package termcolor

import "testing"

func TestParse(t *testing.T) {
	ok := map[string]Spec{
		"8":        {Index: 8},
		" 244 ":    {Index: 244},
		"0":        {Index: 0},
		"#808080":  {Index: -1, RGB: 0x808080},
		"#ABCDEF":  {Index: -1, RGB: 0xabcdef},
		"#999":     {Index: -1, RGB: 0x999999},
		"#1e1e1e":  {Index: -1, RGB: 0x1e1e1e},
		"255":      {Index: 255},
		"#0000ff ": {Index: -1, RGB: 0x0000ff},
	}
	for in, want := range ok {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "256", "-1", "grey", "#12", "#gggggg", "#1234567", "0x80"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q): want error", in)
		}
	}
}

func TestMatch(t *testing.T) {
	specs, err := ParseAll([]string{"8", "#808080"})
	if err != nil {
		t.Fatal(err)
	}
	if !MatchIndex(specs, 8) || MatchIndex(specs, 7) {
		t.Fatal("MatchIndex")
	}
	if !MatchRGB(specs, 0x808080) || MatchRGB(specs, 8) {
		t.Fatal("MatchRGB")
	}
	if _, err := ParseAll([]string{"8", "bad"}); err == nil {
		t.Fatal("ParseAll should fail")
	}
	if s := (Spec{Index: -1, RGB: 0xff}).String(); s != "#0000ff" {
		t.Fatal(s)
	}
}
