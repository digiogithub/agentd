package daemon_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/testutil"
)

// Turns record how they ended and who started them: the program on its own,
// then a person typing on an attached terminal.
func TestTurnRecordsOriginAndEvent(t *testing.T) {
	e := startViewServer(t, "")
	c := e.dial(t)
	sub := subscribe(t, e.sock, proto.SubscribeArgs{Kinds: []string{"turn"}})
	expectHello(t, sub, e)
	work := `for i in 1 2 3 4 5 6; do echo step $i; sleep 0.15; done`
	runView(t, c, "tr", "fake", "sh", "-c",
		"echo ready; sleep 3.6; "+work+"; while read l; do "+work+"; done")

	var rec proto.TurnRecord
	ev := nextKind(t, sub, proto.EvTurnCompleted)
	if json.Unmarshal(ev.Data, &rec) != nil || ev.Session != "tr" || rec.Turn != 1 || rec.Outcome != "completed" || rec.Origin != "program" {
		t.Fatalf("first turn = %+v", rec)
	}
	if rec.StartedAt.IsZero() || !rec.EndedAt.After(rec.StartedAt) {
		t.Fatalf("turn times = %+v", rec)
	}

	_, w := attachWith(t, e, proto.AttachArgs{Name: "tr", Cols: 80, Rows: 24})
	time.Sleep(200 * time.Millisecond)
	_ = w.Data([]byte("go\r"))
	_ = json.Unmarshal(nextKind(t, sub, proto.EvTurnCompleted).Data, &rec)
	if rec.Turn != 2 || rec.Origin != "human" {
		t.Fatalf("second turn = %+v", rec)
	}
	var turns []proto.TurnRecord
	testutil.WaitFor(t, timeout, func() bool {
		var err error
		turns, err = c.Turns("tr")
		return err == nil && len(turns) == 2
	}, "two turn records", func() string { return fmt.Sprint(turns) })
	if as := dialAs(t, e, "tr"); as != nil {
		if _, err := as.Turns("tr"); err != nil {
			t.Fatalf("own turns: %v", err)
		}
	}
}
