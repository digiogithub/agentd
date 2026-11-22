package daemon_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/relayclient"
	"github.com/digiogithub/agentd/internal/testutil"
)

func ptr(s string) *string { return &s }

func taskChanged(t *testing.T, sub *client.Subscription) (proto.Event, proto.TaskChangedData) {
	t.Helper()
	ev := nextKind(t, sub, proto.EvTaskChanged)
	var d proto.TaskChangedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		t.Fatal(err)
	}
	return ev, d
}

// A session reports its plan; revisions bump only on change; task.changed and
// SessionInfo.tasks follow; humans read but never write.
func TestTasksSetUpdateAndEvents(t *testing.T) {
	e := startDaemon(t)
	c := e.dial(t)
	mustRun(t, c, "ag", "cat")
	mustRun(t, c, "other", "cat")
	sub := subscribe(t, e.sock, proto.SubscribeArgs{Kinds: []string{"task"}})
	expectHello(t, sub, e)
	as := dialAs(t, e, "ag")
	// A peer session's subscription sees only its own task events.
	otherSub, err := dialAs(t, e, "other").Subscribe(proto.SubscribeArgs{Kinds: []string{"task"}}, 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = otherSub.Close() })
	expectHello(t, otherSub, e)

	if si := findSession(t, c, "ag"); si.Tasks != nil {
		t.Fatalf("tasks before any report: %+v", si.Tasks)
	}
	res, err := as.TasksSet([]proto.Task{
		{Title: "read the code"},
		{ID: "t1", Title: "write\x1b[31m tests\n", Status: proto.TaskInProgress},
		{Title: "ship it", Note: "after review"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Revision != 1 || len(res.Tasks) != 3 {
		t.Fatalf("set = %+v", res)
	}
	// Explicit t1 is taken: the auto ids are the first free ones.
	if res.Tasks[0].ID != "t2" || res.Tasks[1].ID != "t1" || res.Tasks[2].ID != "t3" {
		t.Fatalf("ids = %+v", res.Tasks)
	}
	if res.Tasks[1].Title != "write[31m tests" || res.Tasks[0].Status != proto.TaskPending || res.Tasks[0].UpdatedAt.IsZero() {
		t.Fatalf("cleaned tasks = %+v", res.Tasks)
	}
	ev, d := taskChanged(t, sub)
	if ev.Session != "ag" || d.Revision != 1 || d.Counts.Pending != 2 || d.Counts.InProgress != 1 ||
		d.Current != "write[31m tests" || len(d.Changed) != 3 || d.Changed[0].From != "" {
		t.Fatalf("task.changed = %+v %+v", ev, d)
	}

	// Same list again: idempotent, no revision, no event.
	same := []proto.Task{}
	for _, tk := range res.Tasks {
		same = append(same, proto.Task{ID: tk.ID, Title: tk.Title, Status: tk.Status, Note: tk.Note})
	}
	again, err := as.TasksSet(same)
	if err != nil || again.Revision != 1 || !again.Tasks[0].UpdatedAt.Equal(res.Tasks[0].UpdatedAt) {
		t.Fatalf("idempotent set = %+v %v", again, err)
	}
	if up, err := as.TaskUpdate(proto.TaskUpdateArgs{ID: "t1", Status: ptr(proto.TaskInProgress)}); err != nil || up.Revision != 1 {
		t.Fatalf("no-op update = %+v %v", up, err)
	}

	// One task changes.
	up, err := as.TaskUpdate(proto.TaskUpdateArgs{ID: "t1", Status: ptr(proto.TaskBlocked), Note: ptr("need a decision")})
	if err != nil || up.Revision != 2 || up.Tasks[1].Status != proto.TaskBlocked || up.Tasks[1].Note != "need a decision" {
		t.Fatalf("update = %+v %v", up, err)
	}
	_, d = taskChanged(t, sub)
	if d.Revision != 2 || d.Current != "" || d.Counts.Blocked != 1 || len(d.Changed) != 1 ||
		d.Changed[0] != (proto.TaskChange{ID: "t1", Title: "write[31m tests", Status: proto.TaskBlocked, From: proto.TaskInProgress}) {
		t.Fatalf("update event = %+v", d)
	}
	si := findSession(t, c, "ag")
	if si.Tasks == nil || si.Tasks.Revision != 2 || si.Tasks.Counts.Blocked != 1 || si.Tasks.Counts.Pending != 2 {
		t.Fatalf("ls tasks = %+v", si.Tasks)
	}

	// Errors.
	for name, err := range map[string]error{
		"unknown id": func() error { _, err := as.TaskUpdate(proto.TaskUpdateArgs{ID: "zz", Status: ptr("done")}); return err }(),
		"bad status": func() error {
			_, err := as.TaskUpdate(proto.TaskUpdateArgs{ID: "t1", Status: ptr("later")})
			return err
		}(),
		"empty title": func() error {
			_, err := as.TasksSet([]proto.Task{{Title: " \x07 "}})
			return err
		}(),
		"bad id": func() error { _, err := as.TasksSet([]proto.Task{{ID: "a b", Title: "x"}}); return err }(),
		"dup id": func() error {
			_, err := as.TasksSet([]proto.Task{{ID: "a", Title: "x"}, {ID: "a", Title: "y"}})
			return err
		}(),
		"too many": func() error { _, err := as.TasksSet(make([]proto.Task, proto.MaxTasks+1)); return err }(),
	} {
		want := proto.CodeInvalid
		if name == "unknown id" {
			want = proto.CodeNotFound
		}
		if client.ErrorCode(err) != want {
			t.Errorf("%s: %v (want %s)", name, err, want)
		}
	}
	// Humans read any list, never write.
	if _, err := c.TasksSet([]proto.Task{{Title: "x"}}); client.ErrorCode(err) != proto.CodeForbidden {
		t.Errorf("human tasks_set: %v", err)
	}
	if _, err := c.TaskUpdate(proto.TaskUpdateArgs{ID: "t1", Status: ptr("done")}); client.ErrorCode(err) != proto.CodeForbidden {
		t.Errorf("human task_update: %v", err)
	}
	got, err := c.Tasks("ag")
	if err != nil || got.Name != "ag" || got.Revision != 2 || len(got.Tasks) != 3 {
		t.Fatalf("human tasks = %+v %v", got, err)
	}
	// A session reads only itself (no agent_view).
	if _, err := dialAs(t, e, "other").Tasks("ag"); client.ErrorCode(err) != proto.CodeForbidden {
		t.Errorf("cross-session tasks: %v", err)
	}
	if r, err := dialAs(t, e, "other").Tasks("other"); err != nil || r.Revision != 0 || len(r.Tasks) != 0 {
		t.Errorf("own empty tasks: %+v %v", r, err)
	}
	if _, err := dialAs(t, e, "other").TasksSet([]proto.Task{{Title: "mine"}}); err != nil {
		t.Fatal(err)
	}
	if ev, d := taskChanged(t, otherSub); ev.Session != "other" || d.Revision != 1 {
		t.Fatalf("peer subscription got %+v %+v, want only its own task.changed", ev, d)
	}
	if _, err := c.Tasks("nope"); client.ErrorCode(err) != proto.CodeNotFound {
		t.Errorf("unknown session: %v", err)
	}

	// Still readable after exit, gone after rm.
	if err := c.Kill("ag", "KILL"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, 5*time.Second, func() bool { return findSession(t, c, "ag").State == "exited" }, "ag exited", nil)
	if r, err := c.Tasks("ag"); err != nil || r.Revision != 2 {
		t.Fatalf("tasks after exit = %+v %v", r, err)
	}
	if err := c.Rm("ag"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Tasks("ag"); client.ErrorCode(err) != proto.CodeNotFound {
		t.Fatalf("tasks after rm: %v", err)
	}
	mustRun(t, c, "ag", "cat")
	if r, err := c.Tasks("ag"); err != nil || r.Revision != 0 || len(r.Tasks) != 0 {
		t.Fatalf("recreated session tasks = %+v %v", r, err)
	}
}

// task.changed is a session event: another session does not see it, the
// owner does; agent_view lifts the read restriction.
func TestTasksVisibility(t *testing.T) {
	cfg := config.Default()
	cfg.Security.AgentView = true
	e := startDaemonWith(t, cfg)
	c := e.dial(t)
	mustRun(t, c, "a", "cat")
	mustRun(t, c, "b", "cat")
	if _, err := dialAs(t, e, "a").TasksSet([]proto.Task{{Title: "x"}}); err != nil {
		t.Fatal(err)
	}
	if r, err := dialAs(t, e, "b").Tasks("a"); err != nil || len(r.Tasks) != 1 {
		t.Fatalf("agent_view cross read = %+v %v", r, err)
	}
	if _, err := dialAs(t, e, "b").TasksSet(nil); err != nil {
		t.Fatalf("empty list: %v", err)
	}
	if si := findSession(t, c, "b"); si.Tasks == nil || si.Tasks.Revision != 1 {
		t.Fatalf("empty list summary = %+v", si.Tasks)
	}
}

// Relay devices read task lists (session globs applied) and never write.
func TestTasksOverRelay(t *testing.T) {
	e, _ := relayEnv(t)
	c := e.dial(t)
	mustRun(t, c, "codex@web", "cat")
	mustRun(t, c, "claude@api", "cat")
	if _, err := dialAs(t, e, "codex@web").TasksSet([]proto.Task{{Title: "plan"}}); err != nil {
		t.Fatal(err)
	}
	tg, _ := pairDevice(t, c, proto.RelayArgs{Scope: "admin", Sessions: []string{"codex@*"}}, true)
	d := &relayclient.Dialer{Target: tg}
	defer func() { _ = d.Close() }()
	rc := remoteClient(t, d)
	if r, err := rc.Tasks("codex@web"); err != nil || len(r.Tasks) != 1 {
		t.Fatalf("relay tasks = %+v %v", r, err)
	}
	if _, err := rc.Tasks("claude@api"); client.ErrorCode(err) != proto.CodeNotFound {
		t.Errorf("relay tasks outside the filter: %v", err)
	}
	if _, err := rc.TasksSet([]proto.Task{{Title: "x"}}); client.ErrorCode(err) != proto.CodeForbidden {
		t.Errorf("relay tasks_set: %v", err)
	}
	if _, err := rc.TaskUpdate(proto.TaskUpdateArgs{ID: "t1", Status: ptr("done")}); client.ErrorCode(err) != proto.CodeForbidden {
		t.Errorf("relay task_update: %v", err)
	}
}
