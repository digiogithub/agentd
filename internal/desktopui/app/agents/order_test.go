package agents

import (
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/desktopui/app"
	"github.com/digiogithub/agentd/internal/desktopui/backend"
)

// The order must not depend on status, attention or activity.
func TestOrderIgnoresLiveData(t *testing.T) {
	mk := func(name, status string, ago time.Duration) entry {
		s := backend.Session{HostID: "h", Name: name, Status: status, Updated: time.Now().Add(-ago)}
		return entry{s: s, g: app.GroupOf(s, nil)}
	}
	names := func(es []entry) (out []string) {
		for _, e := range es {
			out = append(out, e.s.Name)
		}
		return
	}
	a := []entry{mk("b", "idle", 0), mk("c", "waiting", time.Hour), mk("a", "working", 2*time.Hour)}
	b := []entry{mk("b", "waiting", time.Hour), mk("c", "working", 0), mk("a", "idle", 5*time.Hour)}
	sortEntries(a)
	sortEntries(b)
	for i, want := range []string{"a", "b", "c"} {
		if names(a)[i] != want || names(b)[i] != want {
			t.Fatalf("a=%v b=%v", names(a), names(b))
		}
	}
}
