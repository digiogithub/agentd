package screenfix

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digiogithub/agentd/internal/screen"
)

// The -update flag is registered once per test binary; if another package
// already defined "update" it is reused.
func init() {
	if flag.Lookup("update") == nil {
		flag.Bool("update", false, "rewrite golden files")
	}
}

func updating() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

// Golden compares got, marshalled as indented JSON, with
// <Dir()>/<name>.golden.json. With `-update` the file is (re)written.
func Golden(t testing.TB, name string, got any) {
	t.Helper()
	GoldenIn(t, Dir(), name, got)
}

// GoldenIn is Golden with an explicit directory.
func GoldenIn(t testing.TB, dir, name string, got any) {
	t.Helper()
	raw, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("golden %s: marshal: %v", name, err)
	}
	raw = append(raw, '\n')
	path := filepath.Join(dir, name+".golden.json")
	if updating() {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s: %v (run with -update to create it)", name, err)
	}
	if !bytes.Equal(want, raw) {
		t.Fatalf("golden %s mismatch (run with -update to accept)\n--- want\n%s\n--- got\n%s", name, want, raw)
	}
}

// Span is a run of identically styled non-default cells on one row.
type Span struct {
	Row   int      `json:"row"`
	Col   int      `json:"col"`
	Text  string   `json:"text"`
	Attrs []string `json:"attrs,omitempty"`
	Fg    string   `json:"fg,omitempty"`
	Bg    string   `json:"bg,omitempty"`
}

// Summary is a compact, JSON-friendly digest of a Snapshot for goldens:
// terminal state, plain-text lines (trailing blank rows trimmed) and the
// styled runs (faint, bold, reverse, colours) that ghost/pointer detection
// depends on. Timestamps are not included.
type Summary struct {
	Cols, Rows     int
	CursorX        int
	CursorY        int
	CursorHidden   bool
	AltScreen      bool
	Title          string `json:",omitempty"`
	AppCursor      bool
	BracketedPaste bool
	Bells          uint64
	Notifies       uint64
	NotifyBody     string   `json:",omitempty"`
	ScrollbackLen  int      `json:",omitempty"`
	Lines          []string // visible rows, trailing blank rows trimmed
	Styled         []Span   `json:",omitempty"`
}

// Summarize digests a Snapshot.
func Summarize(s *screen.Snapshot) Summary {
	sum := Summary{
		Cols: s.Cols, Rows: s.Rows, CursorX: s.Cursor.X, CursorY: s.Cursor.Y,
		CursorHidden: s.CursorHidden, AltScreen: s.AltScreen, Title: s.Title,
		AppCursor: s.AppCursor, BracketedPaste: s.BracketedPaste,
		Bells: s.Bells, Notifies: s.Notifies, NotifyBody: s.LastNotify.Body,
		ScrollbackLen: s.ScrollbackLen, Lines: s.Lines(),
	}
	for len(sum.Lines) > 0 && sum.Lines[len(sum.Lines)-1] == "" {
		sum.Lines = sum.Lines[:len(sum.Lines)-1]
	}
	for y, row := range s.Screen {
		var cur *Span
		flush := func() {
			if cur != nil {
				sum.Styled = append(sum.Styled, *cur)
				cur = nil
			}
		}
		for x, c := range row.Cells {
			if c.Attrs == 0 && c.Fg == "" && c.Bg == "" {
				flush()
				continue
			}
			txt := c.Text
			if txt == "" {
				txt = " "
			}
			names := c.Attrs.Names()
			if cur != nil && cur.Fg == c.Fg && cur.Bg == c.Bg && strings.Join(cur.Attrs, ",") == strings.Join(names, ",") && cur.Col+len([]rune(cur.Text)) == x {
				cur.Text += txt
				continue
			}
			flush()
			cur = &Span{Row: y, Col: x, Text: txt, Attrs: names, Fg: c.Fg, Bg: c.Bg}
		}
		flush()
	}
	return sum
}
