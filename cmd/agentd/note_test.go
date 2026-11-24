package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/proto"
)

type fakeNotes struct {
	states  []string
	added   [2]string
	update  proto.NoteUpdateArgs
	rmID    int64
	assign  proto.NoteAssignArgs
	assigns proto.NoteAssignResult
	err     error
}

func (f *fakeNotes) Notes(states []string, _ string) (proto.NotesResult, error) {
	f.states = states
	return proto.NotesResult{Notes: []proto.Note{
		{ID: 2, Title: "open one", State: proto.NoteOpen, CreatedAt: time.Now()},
		{ID: 1, Title: "taken", State: proto.NoteAssigned, AssignedTo: "codex@x", CreatedAt: time.Now()},
	}}, f.err
}
func (f *fakeNotes) NoteAdd(title, body string) (proto.Note, error) {
	f.added = [2]string{title, body}
	return proto.Note{ID: 5}, f.err
}
func (f *fakeNotes) NoteUpdate(a proto.NoteUpdateArgs) (proto.Note, error) {
	f.update = a
	return proto.Note{ID: a.ID, State: proto.NoteOpen}, f.err
}
func (f *fakeNotes) NoteRm(id int64) (proto.Note, error) {
	f.rmID = id
	return proto.Note{ID: id}, f.err
}
func (f *fakeNotes) NoteAssign(a proto.NoteAssignArgs) (proto.NoteAssignResult, error) {
	f.assign = a
	return f.assigns, f.err
}

func noteCmdWith(f *fakeNotes) *cobra.Command {
	return newNoteCmd(func() (noteAPI, func(), error) { return f, func() {}, nil })
}

func TestNoteAddAndStdinBody(t *testing.T) {
	f := &fakeNotes{}
	out, err := execCmd(t, noteCmdWith(f), "add", "write", "docs", "--body", "line1")
	if err != nil || !strings.Contains(out, "note #5 added") || f.added != [2]string{"write docs", "line1"} {
		t.Fatalf("%v %q %v", err, out, f.added)
	}
	c := noteCmdWith(f)
	c.SetIn(strings.NewReader("from\nstdin\n"))
	if _, err := execCmd(t, c, "add", "t", "--body", "-"); err != nil || f.added[1] != "from\nstdin\n" {
		t.Fatalf("%v %q", err, f.added)
	}
}

func TestNoteLs(t *testing.T) {
	f := &fakeNotes{}
	out, err := execCmd(t, noteCmdWith(f), "ls")
	if err != nil || len(f.states) != 2 || !strings.Contains(out, "assigned -> codex@x") || !strings.Contains(out, "open one") {
		t.Fatalf("%v %q %v", err, out, f.states)
	}
	if _, err := execCmd(t, noteCmdWith(f), "ls", "--all", "--json"); err != nil || f.states != nil {
		t.Fatalf("--all: %v %v", err, f.states)
	}
}

func TestNoteEditRmAssign(t *testing.T) {
	f := &fakeNotes{}
	if _, err := execCmd(t, noteCmdWith(f), "edit", "3", "--title", "T", "--reopen"); err != nil ||
		f.update.ID != 3 || *f.update.Title != "T" || f.update.Body != nil || *f.update.State != proto.NoteOpen {
		t.Fatalf("edit: %v %+v", err, f.update)
	}
	if _, err := execCmd(t, noteCmdWith(f), "edit", "3"); err == nil {
		t.Fatal("edit without changes accepted")
	}
	if out, err := execCmd(t, noteCmdWith(f), "rm", "#4"); err != nil || f.rmID != 4 || !strings.Contains(out, "removed") {
		t.Fatalf("rm: %v %q", err, out)
	}
	f.assigns = proto.NoteAssignResult{Note: proto.Note{ID: 6}, Task: proto.Task{ID: "n6", Status: "in_progress"}, MessageID: 9}
	out, err := execCmd(t, noteCmdWith(f), "assign", "6", "codex@x", "--start", "--no-notify")
	if err != nil || f.assign.Status != proto.TaskInProgress || f.assign.Notify == nil || *f.assign.Notify ||
		!strings.Contains(out, "task n6") || !strings.Contains(out, "message #9") {
		t.Fatalf("assign: %v %q %+v", err, out, f.assign)
	}
}

func TestNoteCmdErrors(t *testing.T) {
	for _, bad := range [][]string{{"add"}, {"rm"}, {"rm", "x"}, {"rm", "0"}, {"assign", "1"}, {"edit"}} {
		if _, err := execCmd(t, noteCmdWith(&fakeNotes{}), bad...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if _, err := execCmd(t, noteCmdWith(&fakeNotes{err: errors.New("boom")}), "rm", "1"); err == nil {
		t.Fatal("error not propagated")
	}
}
