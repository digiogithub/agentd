package screenfix

import (
	"reflect"
	"strings"
	"testing"

	"github.com/digiogithub/agentd/internal/screen"
)

func fixtureNames(t *testing.T) []string {
	t.Helper()
	names, err := Names(Dir())
	if err != nil || len(names) == 0 {
		t.Fatalf("no fixtures in %s: %v", Dir(), err)
	}
	return names
}

// Every committed fixture replays and matches its golden summary.
func TestFixtureGoldens(t *testing.T) {
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			f := MustLoad(t, name)
			Golden(t, name, Summarize(f.Replay()))
		})
	}
}

// Replay is deterministic, also when the log is fed in tiny chunks.
func TestReplayDeterministic(t *testing.T) {
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			f := MustLoad(t, name)
			a, b := Summarize(f.Replay()), Summarize(f.Replay())
			if !reflect.DeepEqual(a, b) {
				t.Fatal("two replays differ")
			}
			r := NewReplayer(f.Meta.Cols, f.Meta.Rows)
			defer r.Close()
			for i := 0; i < len(f.Log); i += 7 {
				r.Feed(f.Log[i:min(i+7, len(f.Log))])
			}
			if c := Summarize(r.Snapshot(-1)); !reflect.DeepEqual(a, c) {
				t.Fatalf("chunked replay differs:\n%v\n%v", a.Lines, c.Lines)
			}
		})
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	meta := Meta{Cols: 20, Rows: 3, Profile: "p", Description: "d", Frames: []int{3}}
	log := []byte("abc\r\n\x1b[2mfaint\x1b[22m\x1b]0;T\x07\x07")
	if err := Save(dir, "x", meta, log); err != nil {
		t.Fatal(err)
	}
	f, err := LoadFrom(dir, "x")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.Meta, meta) || string(f.Log) != string(log) {
		t.Fatalf("round trip mismatch: %+v", f)
	}
	s := f.Replay()
	if s.Cols != 20 || s.Rows != 3 || s.Title != "T" || s.Bells != 1 {
		t.Fatalf("snapshot: %+v", s)
	}
	if got := s.Lines(); got[0] != "abc" || got[1] != "faint" {
		t.Fatalf("lines %q", got)
	}
	if !s.Screen[1].Cells[0].Attrs.Has(screen.Faint) {
		t.Fatal("faint attribute lost")
	}
	var frames []string
	f.ReplayFrames(func(i int, snap *screen.Snapshot) { frames = append(frames, snap.Lines()[0]) })
	if len(frames) != 2 || frames[0] != "abc" {
		t.Fatalf("frames %q", frames)
	}
	if _, err := LoadFrom(dir, "missing"); err == nil {
		t.Fatal("missing fixture must fail")
	}
}

// Semantic spot checks that do not depend on the goldens.
func TestFixtureContent(t *testing.T) {
	menu := MustLoad(t, "fake-menu").Replay()
	if !strings.Contains(menu.Text(), "❯ Beta") || !menu.CursorHidden {
		t.Fatalf("menu:\n%s", menu.Text())
	}
	box := MustLoad(t, "fake-box-ghost").Replay()
	if !strings.Contains(box.Lines()[2], "❯ ") || box.Notifies != 0 {
		t.Fatalf("box:\n%s notifies=%d", box.Text(), box.Notifies)
	}
	ghost := false
	for _, c := range box.Screen[2].Cells {
		if c.Attrs.Has(screen.Faint) && c.Text != "" {
			ghost = true
		}
	}
	if !ghost {
		t.Fatal("no faint ghost text in the box")
	}
	if s := MustLoad(t, "fake-appcursor-menu").Replay(); !s.AppCursor || !strings.Contains(s.Text(), "❯ Beta") {
		t.Fatalf("appcursor: %v\n%s", s.AppCursor, s.Text())
	}
	if s := MustLoad(t, "fake-bell").Replay(); s.Bells != 1 {
		t.Fatalf("bells=%d", s.Bells)
	}
	if s := MustLoad(t, "claude-post-turn-suggestion").Replay(); s.Title != "\uFFFD Ping de prueba de agentd" /* U+2733 contains 0x9C: the string filter replaces it */ {
		t.Fatalf("title %q", s.Title)
	}
	// The same wrap capture at two sizes keeps the logical text.
	f := MustLoad(t, "fake-wrap-80")
	join := func(s *screen.Snapshot) string {
		// Whitespace removed: x/vt keeps no wrap flag, rows cannot be joined exactly.
		return strings.Join(strings.Fields(strings.Join(s.Lines(), " ")), "")
	}
	if a, b := join(f.ReplayAt(80, 24)), join(f.ReplayAt(120, 24)); a != b {
		t.Fatalf("logical text differs across widths:\n%s\n%s", a, b)
	}
}
