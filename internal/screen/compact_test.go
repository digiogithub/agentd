package screen

import "testing"

func TestCompactTools(t *testing.T) {
	in := "hello\n\n- View: a.go (limit=2)\n    import (\n        \"fmt\"\n- Grep: foo\n   Found 1 file\n   x: y\n\n~ Reasoning title\n   long thought\n   more\n\n⏳ Thinking...\nanswer\n"
	want := "hello\n\n- View: a.go (limit=2)\n   … 2 lines\n- Grep: foo\n   … 2 lines\n\n~ Reasoning title\n\nanswer\n"
	if got := CompactTools(in); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got := CompactTools("- note: keep\n- kb_search: q\n   r1\n"); got != "- note: keep\n- kb_search: q\n   … 1 lines\n" {
		t.Errorf("lowercase: %q", got)
	}
	if got := CompactTools("plain\ntext\n"); got != "plain\ntext\n" {
		t.Errorf("passthrough: %q", got)
	}
}
