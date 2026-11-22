package fake

import (
	"context"
	"testing"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

func TestBoardAndReply(t *testing.T) {
	f := New()
	f.Delay = 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := f.WatchBoard(ctx)
	bs := <-ch
	var hosts []string
	for _, b := range bs {
		hosts = append(hosts, b.HostID)
	}
	// Online hosts with agentd only, ordered by id.
	if len(hosts) != 3 || hosts[0] != "estudio" || hosts[1] != "nube" || hosts[2] != "vps-hetzner" {
		t.Fatalf("board hosts %v", hosts)
	}
	var pando *proto.BoardSession
	for i, s := range bs[0].Board.Sessions {
		if s.Name == "pando@agentd" {
			pando = &bs[0].Board.Sessions[i]
		}
	}
	if pando == nil || len(pando.Tasks) != 7 || len(pando.Questions) != 1 || pando.Questions[0].ID != QuestionID {
		t.Fatalf("pando board %+v", pando)
	}
	if _, err := f.Reply(ctx, "estudio", QuestionID, "Cambio aparte", true); err != nil {
		t.Fatal(err)
	}
	bs = <-ch
	for _, s := range bs[0].Board.Sessions {
		if s.Name == "pando@agentd" && len(s.Questions) != 0 {
			t.Errorf("question still open: %+v", s.Questions)
		}
	}
	if r := f.Replies(); len(r) != 1 || r[0].Body != "Cambio aparte" {
		t.Errorf("replies %+v", r)
	}
	// A view-only relay device has no mailbox.
	f.SetRelayScope(RelayHost.ID, backend.ScopeView)
	id := f.Ask("claude@landing", "¿Publico?")
	if _, err := f.Reply(ctx, RelayHost.ID, id, "Sí", true); proto.CodeOf(err) != proto.CodeForbidden {
		t.Errorf("reply from a view device: %v", err)
	}
}
