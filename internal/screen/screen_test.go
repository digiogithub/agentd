package screen

import (
	"reflect"
	"testing"
)

func TestRowText(t *testing.T) {
	r := Row{Cells: []Cell{{Text: "a", Width: 1}, {Width: 1}, {Text: "漢", Width: 2}, {Width: 0}, {Width: 1}, {Width: 1}}}
	if got := r.Text(); got != "a 漢" {
		t.Fatalf("Text = %q", got)
	}
}

func TestAttr(t *testing.T) {
	a := Bold | Underline
	if !a.Has(Bold) || a.Has(Faint) {
		t.Fatal("Has")
	}
	if got := a.Names(); !reflect.DeepEqual(got, []string{"bold", "underline"}) {
		t.Fatalf("Names = %v", got)
	}
}

func TestCellBlank(t *testing.T) {
	if !(Cell{Width: 1}).Blank() || (Cell{Text: "x"}).Blank() || (Cell{Bg: "1"}).Blank() || (Cell{Attrs: Reverse}).Blank() {
		t.Fatal("Blank")
	}
}

func TestSnapshotText(t *testing.T) {
	s := &Snapshot{Screen: []Row{{Cells: []Cell{{Text: "hi"}}}, {}}}
	if got := s.Text(); got != "hi\n" {
		t.Fatalf("Text = %q", got)
	}
}

func TestSanitize(t *testing.T) {
	if got := Sanitize("a\x1b[31mb\u0085c\x7fd\n", 100); got != "a[31mbcd" {
		t.Fatalf("controls: %q", got)
	}
	if got := Sanitize("héllo wörld", 5); got != "héllo" {
		t.Fatalf("cap: %q", got)
	}
	if got := Sanitize("ok", 5); got != "ok" {
		t.Fatalf("clean: %q", got)
	}
}

func TestContinuation(t *testing.T) {
	if (Cell{Width: 1}).Continuation() || !(Cell{}).Continuation() {
		t.Fatal("Continuation")
	}
}
