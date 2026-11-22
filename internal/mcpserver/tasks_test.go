package mcpserver_test

import (
	"testing"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/mcpserver"
)

func TestTaskTools(t *testing.T) {
	sock := startDaemon(t)
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	a := connect(t, sock, runWithToken(t, c, "fake@a", t.TempDir(), "codex"))

	var out mcpserver.TasksOutput
	if res := call(t, a, "agentd_tasks_set", map[string]any{"tasks": []any{
		map[string]any{"title": "explore"},
		map[string]any{"id": "impl", "title": "implement", "status": "in_progress"},
	}}, &out); res.IsError {
		t.Fatalf("tasks_set: %s", errText(res))
	}
	if out.Revision != 1 || len(out.Tasks) != 2 || out.Tasks[0].ID != "t1" || out.Tasks[1].Status != "in_progress" {
		t.Fatalf("tasks_set = %+v", out)
	}
	out = mcpserver.TasksOutput{}
	if res := call(t, a, "agentd_task_update", map[string]any{"id": "impl", "status": "blocked", "note": "need creds"}, &out); res.IsError {
		t.Fatalf("task_update: %s", errText(res))
	}
	if out.Revision != 2 || out.Tasks[1].Status != "blocked" || out.Tasks[1].Note != "need creds" {
		t.Fatalf("task_update = %+v", out)
	}
	if res := call(t, a, "agentd_task_update", map[string]any{"id": "nope", "status": "done"}, nil); !res.IsError {
		t.Fatal("update of an unknown task succeeded")
	}
	if res := call(t, a, "agentd_task_update", map[string]any{"id": "impl"}, nil); !res.IsError {
		t.Fatal("update without status or note succeeded")
	}
	got, err := c.Tasks("fake@a")
	if err != nil || got.Revision != 2 || len(got.Tasks) != 2 {
		t.Fatalf("daemon list = %+v %v", got, err)
	}
}
