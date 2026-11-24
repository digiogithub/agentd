package daemon_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/relayclient"
)

func noteChanged(t *testing.T, sub *client.Subscription) proto.NoteChangedData {
	t.Helper()
	ev := nextKind(t, sub, proto.EvNoteChanged)
	if ev.Session != "" {
		t.Fatalf("note.changed carries a session: %+v", ev)
	}
	var d proto.NoteChangedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestNotesCRUDAndEvents(t *testing.T) {
	e := startDaemon(t)
	c := e.dial(t)
	mustRun(t, c, "ag", "cat")
	sub := subscribe(t, e.sock, proto.SubscribeArgs{Kinds: []string{"note"}})
	expectHello(t, sub, e)
	// A session never sees note events, even with the agent's own subscription.
	asSub, err := dialAs(t, e, "ag").Subscribe(proto.SubscribeArgs{Kinds: []string{"note"}}, 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = asSub.Close() })
	expectHello(t, asSub, e)

	n, err := c.NoteAdd("  write\x1b[31m docs ", "line one\x1b[0m\r\nline two\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if n.ID != 1 || n.Title != "write[31m docs" || n.Body != "line one[0m\nline two" || n.State != proto.NoteOpen {
		t.Fatalf("add = %+v", n)
	}
	if d := noteChanged(t, sub); d.ID != 1 || d.State != proto.NoteOpen {
		t.Fatalf("event = %+v", d)
	}
	if _, err := c.NoteAdd("   ", ""); client.ErrorCode(err) != proto.CodeInvalid {
		t.Errorf("blank title: %v", err)
	}
	if _, err := c.NoteAdd("x", strings.Repeat("é", proto.MaxNoteBody+50)); err != nil {
		t.Fatal(err)
	}
	res, err := c.Notes(nil, "")
	if err != nil || len(res.Notes) != 2 || res.Notes[0].ID != 2 || res.Digest == "" {
		t.Fatalf("notes = %+v %v", res, err)
	}
	if got := []rune(res.Notes[0].Body); len(got) != proto.MaxNoteBody {
		t.Errorf("body not capped: %d", len(got))
	}
	if nm, err := c.Notes(nil, res.Digest); err != nil || !nm.NotModified || len(nm.Notes) != 0 {
		t.Fatalf("not modified = %+v %v", nm, err)
	}
	if _, err := c.Notes([]string{"bogus"}, ""); client.ErrorCode(err) != proto.CodeInvalid {
		t.Errorf("bad state: %v", err)
	}
	title := "renamed"
	if u, err := c.NoteUpdate(proto.NoteUpdateArgs{ID: 1, Title: &title}); err != nil || u.Title != "renamed" || u.Body == "" {
		t.Fatalf("update = %+v %v", u, err)
	}
	done := proto.NoteDone
	if _, err := c.NoteUpdate(proto.NoteUpdateArgs{ID: 1, State: &done}); client.ErrorCode(err) != proto.CodeInvalid {
		t.Errorf("state done: %v", err)
	}
	if _, err := c.NoteUpdate(proto.NoteUpdateArgs{ID: 99, Title: &title}); client.ErrorCode(err) != proto.CodeNotFound {
		t.Errorf("unknown id: %v", err)
	}
	if rm, err := c.NoteRm(2); err != nil || rm.ID != 2 {
		t.Fatalf("rm = %+v %v", rm, err)
	}
	var sawRemoved bool
	for i := 0; i < 4 && !sawRemoved; i++ {
		d := noteChanged(t, sub)
		sawRemoved = d.ID == 2 && d.Removed
	}
	if !sawRemoved {
		t.Error("no removed event")
	}
	select {
	case ev, ok := <-asSub.Events:
		if ok && ev.Kind == proto.EvNoteChanged {
			t.Errorf("session saw %+v", ev)
		}
	case <-time.After(200 * time.Millisecond):
	}
}

func TestNotesRefuseSessions(t *testing.T) {
	e := startDaemon(t)
	c := e.dial(t)
	mustRun(t, c, "ag", "cat")
	as := dialAs(t, e, "ag")
	if _, err := as.Notes(nil, ""); client.ErrorCode(err) != proto.CodeForbidden {
		t.Errorf("notes: %v", err)
	}
	if _, err := as.NoteAdd("x", ""); client.ErrorCode(err) != proto.CodeForbidden {
		t.Errorf("note_add: %v", err)
	}
	if _, err := as.NoteRm(1); client.ErrorCode(err) != proto.CodeForbidden {
		t.Errorf("note_rm: %v", err)
	}
	if _, err := as.NoteUpdate(proto.NoteUpdateArgs{ID: 1}); client.ErrorCode(err) != proto.CodeForbidden {
		t.Errorf("note_update: %v", err)
	}
	if _, err := as.NoteAssign(proto.NoteAssignArgs{ID: 1, Session: "ag"}); client.ErrorCode(err) != proto.CodeForbidden {
		t.Errorf("note_assign: %v", err)
	}
}

func TestNoteAssignCreatesTaskAndMessage(t *testing.T) {
	e := startDaemon(t)
	c := e.dial(t)
	mustRun(t, c, "ag", "cat")
	as := dialAs(t, e, "ag")
	sub := subscribe(t, e.sock, proto.SubscribeArgs{Kinds: []string{"task", "mail", "note"}})
	expectHello(t, sub, e)

	if _, err := as.TasksSet([]proto.Task{{Title: "own work"}}); err != nil {
		t.Fatal(err)
	}
	n, _ := c.NoteAdd("fix flaky test", "see CI run 42\nit fails on arm64")
	res, err := c.NoteAssign(proto.NoteAssignArgs{ID: n.ID, Session: "ag", Status: proto.TaskInProgress})
	if err != nil {
		t.Fatal(err)
	}
	if res.Note.State != proto.NoteAssigned || res.Note.AssignedTo != "ag" || res.Note.TaskID != "n1" ||
		res.Task.ID != "n1" || res.Task.Origin != proto.TaskOriginBacklog || res.Task.Status != proto.TaskInProgress ||
		res.Task.Note != "see CI run 42 it fails on arm64" || res.MessageID == 0 || res.Warning != "" {
		t.Fatalf("assign = %+v", res)
	}
	tl, _ := c.Tasks("ag")
	if len(tl.Tasks) != 2 || tl.Tasks[1].ID != "n1" || tl.Tasks[1].Origin != proto.TaskOriginBacklog {
		t.Fatalf("tasks = %+v", tl)
	}
	taskChanged(t, sub)
	// the message is in the session's inbox
	in, err := as.Inbox("", true)
	if err != nil || len(in) != 1 || !strings.Contains(in[0].Subject, "Backlog task n1: fix flaky test") ||
		!strings.Contains(in[0].Body, "it fails on arm64") || !strings.Contains(in[0].Body, `"n1"`) {
		t.Fatalf("inbox = %+v %v", in, err)
	}
	if !strings.HasPrefix(in[0].Sender, "human:") {
		t.Errorf("sender = %q", in[0].Sender)
	}
	nextKind(t, sub, proto.EvMailQueued)

	// no-notify assigns without a message
	n2, _ := c.NoteAdd("second", "")
	f := false
	r2, err := c.NoteAssign(proto.NoteAssignArgs{ID: n2.ID, Session: "ag", Notify: &f})
	if err != nil || r2.MessageID != 0 || r2.Task.Status != proto.TaskPending {
		t.Fatalf("no-notify = %+v %v", r2, err)
	}
	// errors
	if _, err := c.NoteAssign(proto.NoteAssignArgs{ID: n2.ID, Session: "nobody"}); client.ErrorCode(err) != proto.CodeNotFound {
		t.Errorf("unknown session: %v", err)
	}
	if _, err := c.NoteAssign(proto.NoteAssignArgs{ID: 99, Session: "ag"}); client.ErrorCode(err) != proto.CodeNotFound {
		t.Errorf("unknown note: %v", err)
	}
	if _, err := c.NoteAssign(proto.NoteAssignArgs{ID: n2.ID, Session: "ag", Status: "done"}); client.ErrorCode(err) != proto.CodeInvalid {
		t.Errorf("bad status: %v", err)
	}
	// reopen then reassign is allowed
	open := proto.NoteOpen
	if u, err := c.NoteUpdate(proto.NoteUpdateArgs{ID: n2.ID, State: &open}); err != nil || u.State != proto.NoteOpen || u.AssignedTo != "" {
		t.Fatalf("reopen = %+v %v", u, err)
	}
}

func TestNoteAssignFullListAndExited(t *testing.T) {
	e := startDaemon(t)
	c := e.dial(t)
	mustRun(t, c, "ag", "cat")
	as := dialAs(t, e, "ag")
	var many []proto.Task
	for i := 0; i < proto.MaxTasks; i++ {
		many = append(many, proto.Task{Title: "t"})
	}
	if _, err := as.TasksSet(many); err != nil {
		t.Fatal(err)
	}
	n, _ := c.NoteAdd("x", "")
	if _, err := c.NoteAssign(proto.NoteAssignArgs{ID: n.ID, Session: "ag"}); client.ErrorCode(err) != proto.CodeInvalid {
		t.Fatalf("full list: %v", err)
	}
	if got, _ := c.Notes([]string{proto.NoteOpen}, ""); len(got.Notes) != 1 {
		t.Fatalf("note must stay open: %+v", got)
	}
}

// A backlog task survives tasks_set, keeps its origin, and completing it
// completes the note (via tasks_set and via task_update).
func TestBacklogTaskSurvivesTasksSetAndCompletes(t *testing.T) {
	e := startDaemon(t)
	c := e.dial(t)
	mustRun(t, c, "ag", "cat")
	as := dialAs(t, e, "ag")
	sub := subscribe(t, e.sock, proto.SubscribeArgs{Kinds: []string{"note"}})
	expectHello(t, sub, e)

	na, _ := c.NoteAdd("A", "")
	nb, _ := c.NoteAdd("B", "")
	f := false
	for _, n := range []proto.Note{na, nb} {
		if _, err := c.NoteAssign(proto.NoteAssignArgs{ID: n.ID, Session: "ag", Notify: &f}); err != nil {
			t.Fatal(err)
		}
	}
	// The agent rewrites its list without the backlog tasks, and tries to forge origin.
	res, err := as.TasksSet([]proto.Task{{ID: "x", Title: "mine", Origin: proto.TaskOriginBacklog}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tasks) != 3 || res.Tasks[0].ID != "x" || res.Tasks[0].Origin != "" ||
		res.Tasks[1].ID != "n1" || res.Tasks[2].ID != "n2" || res.Tasks[1].Origin != proto.TaskOriginBacklog {
		t.Fatalf("tasks = %+v", res.Tasks)
	}
	// Resending without origin is not a change (no spurious revision).
	again, err := as.TasksSet([]proto.Task{{ID: "x", Title: "mine"}, {ID: "n1", Title: "A"}, {ID: "n2", Title: "B"}})
	if err != nil || again.Revision != res.Revision {
		t.Fatalf("resend: rev %d -> %d %v", res.Revision, again.Revision, err)
	}
	// Done via tasks_set.
	if _, err := as.TasksSet([]proto.Task{{ID: "x", Title: "mine"}, {ID: "n1", Title: "A", Status: proto.TaskDone}, {ID: "n2", Title: "B"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Notes(nil, "")
	states := map[int64]string{}
	for _, n := range got.Notes {
		states[n.ID] = n.State
	}
	if states[na.ID] != proto.NoteDone || states[nb.ID] != proto.NoteAssigned {
		t.Fatalf("states = %v", states)
	}
	var sawDone bool
	for i := 0; i < 6 && !sawDone; i++ {
		d := noteChanged(t, sub)
		sawDone = d.ID == na.ID && d.State == proto.NoteDone
	}
	if !sawDone {
		t.Error("no note.changed done")
	}
	// Done via task_update; origin kept.
	up, err := as.TaskUpdate(proto.TaskUpdateArgs{ID: "n2", Status: ptr(proto.TaskDone)})
	if err != nil || up.Tasks[2].Origin != proto.TaskOriginBacklog {
		t.Fatalf("update = %+v %v", up, err)
	}
	got, _ = c.Notes([]string{proto.NoteDone}, "")
	if len(got.Notes) != 2 {
		t.Fatalf("done notes = %+v", got.Notes)
	}
	// A done backlog task the agent leaves out is dropped.
	res, err = as.TasksSet([]proto.Task{{ID: "x", Title: "mine"}})
	if err != nil || len(res.Tasks) != 1 {
		t.Fatalf("drop done = %+v %v", res, err)
	}
	// Overflow: kept backlog tasks count towards the limit.
	n3, _ := c.NoteAdd("C", "")
	if _, err := c.NoteAssign(proto.NoteAssignArgs{ID: n3.ID, Session: "ag", Notify: &f}); err != nil {
		t.Fatal(err)
	}
	var full []proto.Task
	for i := 0; i < proto.MaxTasks; i++ {
		full = append(full, proto.Task{Title: "t"})
	}
	if _, err := as.TasksSet(full); client.ErrorCode(err) != proto.CodeInvalid {
		t.Errorf("overflow: %v", err)
	}
}

func TestNotesOverRelayGate(t *testing.T) {
	e, _ := relayEnv(t)
	c := e.dial(t)
	mustRun(t, c, "codex@web", "cat")
	n, _ := c.NoteAdd("from host", "")

	check := func(name string, args proto.RelayArgs, wantAllowed bool) {
		t.Helper()
		tg, _ := pairDevice(t, c, args, true)
		d := &relayclient.Dialer{Target: tg}
		defer func() { _ = d.Close() }()
		rc := remoteClient(t, d)
		_, err := rc.Notes(nil, "")
		if wantAllowed && err != nil {
			t.Errorf("%s: notes: %v", name, err)
		}
		if !wantAllowed && client.ErrorCode(err) != proto.CodeForbidden {
			t.Errorf("%s: notes: %v", name, err)
		}
		if wantAllowed {
			if _, err := rc.NoteAdd("from phone", ""); err != nil {
				t.Errorf("%s: note_add: %v", name, err)
			}
			if _, err := rc.NoteAssign(proto.NoteAssignArgs{ID: n.ID, Session: "codex@web"}); err != nil {
				t.Errorf("%s: note_assign: %v", name, err)
			}
			if _, err := rc.NoteRm(n.ID); err != nil {
				t.Errorf("%s: note_rm: %v", name, err)
			}
		} else if _, err := rc.NoteAdd("x", ""); client.ErrorCode(err) != proto.CodeForbidden {
			t.Errorf("%s: note_add: %v", name, err)
		}
	}
	check("view", proto.RelayArgs{Scope: "view"}, false)
	check("limited operate", proto.RelayArgs{Scope: "operate", Sessions: []string{"codex@*"}}, false)
	check("guest", proto.RelayArgs{Scope: "operate", Guest: true, Sessions: []string{"codex@*"}}, false)
	check("owner operate", proto.RelayArgs{Scope: "operate"}, true)
}
