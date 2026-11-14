package daemon

import (
	"testing"
	"time"
)

func TestTurnOutcomeAndOrigin(t *testing.T) {
	e := &entry{}
	t0 := time.Now()
	e.noteInput(OriginAct, false)
	e.turnStarted(t0)
	if r := e.turnEnded(1, "idle", "done", t0.Add(time.Second)); r.Origin != OriginAct || r.Outcome != "completed" || r.Message != "done" {
		t.Fatalf("act turn = %+v", r)
	}
	e.turnStarted(time.Now())
	e.noteInput(OriginHuman, true) // Ctrl-C during the turn
	if r := e.turnEnded(2, "idle", "", time.Now()); r.Outcome != "aborted" {
		t.Fatalf("interrupted turn = %+v", r)
	}
	e.turnStarted(time.Now())
	if r := e.turnEnded(3, "exited", "", time.Now()); r.Outcome != "exited" {
		t.Fatalf("exit = %+v", r)
	}
	for i := 0; i < MaxTurnRecords+5; i++ {
		e.turnStarted(time.Now())
		e.turnEnded(uint64(10+i), "idle", "", time.Now())
	}
	if n := len(e.turnRecords()); n != MaxTurnRecords {
		t.Fatalf("records kept = %d", n)
	}
}
