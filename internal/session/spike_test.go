package session_test

import (
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"

	"github.com/digiogithub/agentd/internal/session"
)

// TestSpikeRealTUI is a manual probe, skipped unless AGENTD_SPIKE_CMD is set.
// It runs a real TUI in a session, optionally types keys, prints the emulated
// screen and checks that Redraw replayed into a fresh emulator reproduces it.
// See docs/spike-vt.md. Never point it at paid agent CLIs in automation.
//
//	AGENTD_SPIKE_CMD="htop" AGENTD_SPIKE_KEYS=$'\t' go test -run TestSpikeRealTUI -v ./internal/session/
func TestSpikeRealTUI(t *testing.T) {
	cmdline := os.Getenv("AGENTD_SPIKE_CMD")
	if cmdline == "" {
		t.Skip("set AGENTD_SPIKE_CMD to run")
	}
	settle := 2 * time.Second
	if d, err := time.ParseDuration(os.Getenv("AGENTD_SPIKE_SETTLE")); err == nil {
		settle = d
	}
	s := start(t, session.Spec{Name: "spike", Cmd: []string{"sh", "-c", cmdline}, Cols: 100, Rows: 30})
	time.Sleep(settle)
	if keys := os.Getenv("AGENTD_SPIKE_KEYS"); keys != "" {
		_, _ = s.Write([]byte(keys), true)
		time.Sleep(settle)
	}
	// Freeze the screen for the comparison: stop the child (SIGSTOP).
	_ = s.Signal(syscall.SIGSTOP)
	time.Sleep(200 * time.Millisecond)
	defer func() { _ = s.Signal(syscall.SIGCONT) }()

	x, y := s.Cursor()
	t.Logf("state=%s alt=%v cursor=%d,%d bracketedPaste=%v title=%q", s.State(), s.IsAltScreen(), x, y, s.BracketedPasteEnabled(), s.Title())
	t.Logf("screen:\n%s", s.Screen())

	emu := vt.NewEmulator(100, 30)
	go func() { _, _ = io.Copy(io.Discard, emu) }()
	_, _ = emu.Write(s.Redraw())
	if got, want := emu.String(), s.Screen(); got != want {
		t.Errorf("redraw replay differs:\n--- replay\n%s\n--- session\n%s", got, want)
	}
	if p := emu.CursorPosition(); p.X != x || p.Y != y {
		t.Errorf("redraw cursor %v vs %d,%d", p, x, y)
	}
	if n := strings.Count(s.ScreenANSI(), "\x1b["); n == 0 {
		t.Logf("note: no SGR in ScreenANSI")
	}
}
