package chatfmt

import (
	"reflect"
	"testing"
)

func TestReflow(t *testing.T) {
	got := Reflow("  Primera línea del párrafo que el\n  terminal cortó aquí.\n\n  Pasos:\n  - uno\n    sigue\n  2. dos\n\n\n")
	want := []string{
		"Primera línea del párrafo que el terminal cortó aquí.",
		"Pasos:\n- uno sigue\n2. dos",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := Reflow(" \n\n"); got != nil {
		t.Errorf("blank: %q", got)
	}
}
