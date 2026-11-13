package daemon

import (
	"slices"
	"testing"

	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/screen"
)

func TestResolveKeysTable(t *testing.T) {
	want := map[string]string{
		"enter": "\r", "esc": "\x1b", "tab": "\t", "shift-tab": "\x1b[Z",
		"up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
		"home": "\x1b[H", "end": "\x1b[F", "pgup": "\x1b[5~", "pgdown": "\x1b[6~",
		"backspace": "\x7f", "delete": "\x1b[3~", "ctrl-c": "\x03", "ctrl-d": "\x04", "ctrl-l": "\x0c",
		"space": " ", "y": "y", "n": "n", "0": "0", "5": "5", "9": "9",
	}
	for name, b := range want {
		ks, err := resolveKeys([]string{name}, false, "")
		if err != nil || string(ks[0].bytes) != b {
			t.Errorf("%s = %q, %v; want %q", name, ks[0].bytes, err, b)
		}
	}
	// SS3 in application cursor mode.
	for name, b := range map[string]string{"up": "\x1bOA", "down": "\x1bOB", "right": "\x1bOC", "left": "\x1bOD", "esc": "\x1b", "pgup": "\x1b[5~"} {
		ks, _ := resolveKeys([]string{name}, true, "")
		if string(ks[0].bytes) != b {
			t.Errorf("appcursor %s = %q, want %q", name, ks[0].bytes, b)
		}
	}
	// enter honours the profile submit sequence.
	if ks, _ := resolveKeys([]string{"enter"}, false, "\x1b\r"); string(ks[0].bytes) != "\x1b\r" {
		t.Errorf("enter with custom submit = %q", ks[0].bytes)
	}
	// Printable keys are never marked as human input (no phantom draft).
	for _, n := range []string{"y", "n", "1", "space", "backspace", "tab"} {
		if ks, _ := resolveKeys([]string{n}, false, ""); ks[0].human {
			t.Errorf("%s marked human", n)
		}
	}
	for _, bad := range [][]string{nil, {"nope"}, {"ctrl-z"}, {"\x1b"}, {"Enter"}, {"up", ""}, make([]string, 33)} {
		if _, err := resolveKeys(bad, false, ""); err == nil {
			t.Errorf("resolveKeys(%q) accepted", bad)
		}
	}
	if !slices.IsSorted(KeyNames()) || len(KeyNames()) != 20+10 {
		t.Errorf("KeyNames = %v", KeyNames())
	}
}

func TestMenuTrack(t *testing.T) {
	var m menuTrack
	m.note(1, "A")
	m.note(2, "A") // pointer moved, same menu instance
	m.note(3, "")  // menu gone
	m.note(4, "B") // an identical-looking menu came back: another instance
	i1, _ := m.instanceAt(1)
	i2, _ := m.instanceAt(2)
	i4, _ := m.instanceAt(4)
	if i1 != i2 || i1 == i4 {
		t.Errorf("instances = %q %q %q", i1, i2, i4)
	}
	for r := uint64(10); r < 10+menuHistory+10; r++ {
		m.note(r, "")
	}
	if _, ok := m.instanceAt(1); ok {
		t.Error("old revision not evicted")
	}
}

func TestSatisfiedRequiresInstance(t *testing.T) {
	opts := []screen.Option{{ID: "o1", MenuFingerprint: "mA", Selected: true}}
	exp := screen.Expect{SelectedID: "o1", Gone: true, Fingerprint: "mA", Instance: "i1"}
	if !satisfied(exp, opts, "i1", false) {
		t.Error("same instance, pointer on target: should hold")
	}
	// Same fingerprint and same selected id, but another instance: that is a
	// different dialog, so SelectedID must not hold; Gone does (the instance
	// the plan was made for is no longer on screen).
	exp2 := screen.Expect{SelectedID: "o1", Fingerprint: "mA", Instance: "i1"}
	if satisfied(exp2, opts, "i2", false) {
		t.Error("another instance satisfied SelectedID")
	}
	if !satisfied(screen.Expect{Gone: true, Fingerprint: "mA", Instance: "i1"}, opts, "i2", false) {
		t.Error("another instance should count as gone")
	}
}

func TestStableTitle(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"✳ fix the bug", "fix the bug"}, {"⠋ fix the bug", "fix the bug"}, {"⠙ fix the bug", "fix the bug"},
		{"✶ fix the bug", "fix the bug"}, {"fix the bug", "fix the bug"}, {"", ""},
	} {
		if got := stableTitle(c.in, ""); got != c.want {
			t.Errorf("stableTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Review 8: a title that only animates its spinner glyph does not change the
// revision fingerprint; a real title change does.
func TestFingerprintIgnoresTitleSpinner(t *testing.T) {
	mk := func(title string) [32]byte {
		return fingerprint(proto.View{Status: "working", Title: title}, nil, 3, "")
	}
	if mk("⠋ fix lint") != mk("⠙ fix lint") || mk("✳ fix lint") != mk("✶ fix lint") || mk("✳ fix lint") != mk("fix lint") {
		t.Error("spinner-only title change moved the fingerprint")
	}
	if mk("✳ fix lint") == mk("✳ fix tests") {
		t.Error("a real title change must move the fingerprint")
	}
}
