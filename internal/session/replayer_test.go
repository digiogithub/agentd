package session

import (
	"testing"

	"github.com/digiogithub/agentd/internal/screen"
)

func TestReplayerSnapshot(t *testing.T) {
	r := NewReplayer(10, 3)
	defer r.Close()
	r.Feed([]byte("\x1b]0;\xe2\x9c\xb3 hi\x07\x1b]7;file://h/tmp/x\x07\x07a\xe4\xb8\xadb\r\n\x1b[?2004h"))
	snap := r.Snapshot(screen.SnapOpts{})
	if snap.Title != "\ufffd hi" || snap.Cwd != "/tmp/x" || snap.Bells != 1 || !snap.BracketedPaste {
		t.Fatalf("state: %+v", snap)
	}
	if snap.OutputSeq != 1 || snap.State != screen.StateRunning || !snap.Taken.IsZero() {
		t.Fatalf("meta: %+v", snap)
	}
	c := snap.Screen[0].Cells
	if c[0].Text != "a" || c[1].Text != "中" || c[1].Width != 2 || c[2].Width != 0 || c[3].Text != "b" {
		t.Fatalf("cells: %+v", c[:4])
	}
}
