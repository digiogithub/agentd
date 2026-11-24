package fake_test

import (
	"context"
	"testing"

	"github.com/digiogithub/agentd/internal/desktopui/backend/fake"
	"github.com/digiogithub/agentd/internal/proto"
)

func TestFakeNotesLifecycle(t *testing.T) {
	f := fake.New()
	ctx := context.Background()
	n, err := f.NoteAdd(ctx, "estudio", "Probar la cola", "con dos agentes")
	if err != nil || n.State != proto.NoteOpen || n.ID == 0 {
		t.Fatalf("add %+v %v", n, err)
	}
	if _, err := f.NoteAdd(ctx, "estudio", "  ", ""); proto.CodeOf(err) != proto.CodeInvalid {
		t.Errorf("blank title: %v", err)
	}
	title := "Probar la cola v2"
	if u, err := f.NoteUpdate(ctx, "estudio", proto.NoteUpdateArgs{ID: n.ID, Title: &title}); err != nil || u.Title != title {
		t.Fatalf("update %+v %v", u, err)
	}
	res, err := f.NoteAssign(ctx, "estudio", proto.NoteAssignArgs{ID: n.ID, Session: "pando@agentd", Status: proto.TaskInProgress})
	if err != nil || res.Task.ID != "n"+itoa(n.ID) || res.Task.Origin != proto.TaskOriginBacklog || res.Note.State != proto.NoteAssigned {
		t.Fatalf("assign %+v %v", res, err)
	}
	if calls := f.NoteAssigns(); len(calls) != 1 || calls[0].Args.Session != "pando@agentd" {
		t.Errorf("calls %+v", calls)
	}
	// The board snapshot carries the notes (assigned after the open ones).
	var hb = <-f.WatchBoard(ctx)
	found := false
	for _, b := range hb {
		if b.HostID == "estudio" {
			found = len(b.Notes) > 0 && b.NotesErr == ""
		}
	}
	if !found {
		t.Errorf("board without notes")
	}
	if _, err := f.NoteAssign(ctx, "estudio", proto.NoteAssignArgs{ID: n.ID, Session: "no@existe"}); proto.CodeOf(err) != proto.CodeNotFound {
		t.Errorf("unknown session: %v", err)
	}
	open := proto.NoteOpen
	if u, err := f.NoteUpdate(ctx, "estudio", proto.NoteUpdateArgs{ID: n.ID, State: &open}); err != nil || u.State != proto.NoteOpen || u.AssignedTo != "" {
		t.Fatalf("reopen %+v %v", u, err)
	}
	if err := f.NoteRemove(ctx, "estudio", n.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.NoteRemove(ctx, "estudio", n.ID); proto.CodeOf(err) != proto.CodeNotFound {
		t.Errorf("second remove: %v", err)
	}
}

func itoa(n int64) string {
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}
