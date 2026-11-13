package session_test

import (
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/screen"
	"github.com/digiogithub/agentd/internal/session"
	"github.com/digiogithub/agentd/internal/testutil"
)

// DEC 2026 tracking across PTY chunk boundaries: the BSU sequence is split in
// two writes, the update stays open for a while, a second complete BSU/ESU
// pair arrives inside one chunk.
func TestSyncUpdateTrackingAcrossChunks(t *testing.T) {
	script := `printf 'start'; sleep 0.3; printf '\033[?20'; sleep 0.3; printf '26hhalf'; sleep 1.2;` +
		` printf '\033[?2026ldone'; sleep 0.3; printf '\033[?2026hx\033[?2026ly'; sleep 30`
	s := start(t, session.Spec{Name: "sync", Cmd: []string{"sh", "-c", script}})
	waitScreen(t, s, "start")
	if s.InSyncUpdate() || s.SyncFrames() != 0 {
		t.Fatalf("before BSU: open=%v frames=%d", s.InSyncUpdate(), s.SyncFrames())
	}
	waitScreen(t, s, "half")
	if !s.InSyncUpdate() {
		t.Fatal("BSU split across chunks not tracked")
	}
	snap := s.Snapshot(screen.SnapOpts{})
	if !snap.SyncOpen || snap.SyncFrames != 0 || snap.SyncSince.IsZero() {
		t.Fatalf("snapshot open: %+v", snap)
	}
	waitScreen(t, s, "done")
	if s.InSyncUpdate() || s.SyncFrames() != 1 {
		t.Fatalf("after ESU: open=%v frames=%d", s.InSyncUpdate(), s.SyncFrames())
	}
	// Set and reset inside one chunk still count as a completed frame.
	testutil.WaitFor(t, 5*time.Second, func() bool { return s.SyncFrames() == 2 },
		"set+reset in one chunk should count a frame", func() string { return "" })
	snap = s.Snapshot(screen.SnapOpts{})
	if snap.SyncOpen || snap.SyncFrames != 2 {
		t.Fatalf("snapshot closed: %+v", snap)
	}
}

// A BSU that is never terminated stops being honoured after SyncTimeout.
func TestSyncUpdateTimeout(t *testing.T) {
	s := start(t, session.Spec{Name: "sync", Cmd: []string{"sh", "-c", `printf '\033[?2026hstuck'; sleep 30`}})
	waitScreen(t, s, "stuck")
	if !s.InSyncUpdate() {
		t.Fatal("open BSU not seen")
	}
	testutil.WaitFor(t, 5*time.Second, func() bool { return !s.InSyncUpdate() && !s.Snapshot(screen.SnapOpts{}).SyncOpen },
		"stale BSU should time out", func() string { return "" })
}
