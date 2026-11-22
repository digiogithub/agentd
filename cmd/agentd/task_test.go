package main

import (
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
)

func TestParseTaskInput(t *testing.T) {
	got, err := parseTaskInput(strings.NewReader("\n  read the code \n\nwrite tests\n"))
	if err != nil || len(got) != 2 || got[0] != (proto.Task{Title: "read the code"}) || got[1].Title != "write tests" {
		t.Fatalf("lines = %+v %v", got, err)
	}
	got, err = parseTaskInput(strings.NewReader(` [{"id":"a","title":"x","status":"done"},{"title":"y","note":"n"}]`))
	if err != nil || len(got) != 2 || got[0] != (proto.Task{ID: "a", Title: "x", Status: "done"}) || got[1].Note != "n" {
		t.Fatalf("json = %+v %v", got, err)
	}
	if _, err := parseTaskInput(strings.NewReader(`[{"title":`)); err == nil {
		t.Fatal("broken JSON accepted")
	}
	if got, err := parseTaskInput(strings.NewReader("")); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty = %#v %v", got, err)
	}
}

func TestPrintTasks(t *testing.T) {
	var b strings.Builder
	_ = printTasks(&b, proto.TasksResult{})
	if b.String() != "no task list reported\n" {
		t.Fatalf("empty: %q", b.String())
	}
	b.Reset()
	_ = printTasks(&b, proto.TasksResult{Revision: 3, Tasks: []proto.Task{
		{ID: "t1", Title: "ship", Status: "blocked", Note: "waiting for review", UpdatedAt: time.Now()},
	}})
	out := b.String()
	for _, want := range []string{"ID", "t1", "blocked", "ship (waiting for review)", "revision 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestTaskCmdArgs(t *testing.T) {
	for _, bad := range [][]string{{"start"}, {"done", "a", "b"}, {"block"}, {"add"}, {"set", "x"}, {"ls", "a", "b"}} {
		if _, err := execCmd(t, taskCmd(), bad...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
