package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/proto"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func titles(ts []proto.Task) string {
	var p []string
	for _, t := range ts {
		p = append(p, t.ID+":"+t.Status+":"+t.Title)
	}
	return strings.Join(p, "|")
}

func TestSyncTodoWriteStableIDs(t *testing.T) {
	first, ok := syncTasks(fixture(t, "hook_todowrite.json"), nil)
	if !ok || len(first) != 3 || first[0].Status != "done" || first[1].Status != "in_progress" || first[2].Status != "pending" {
		t.Fatalf("first = %s", titles(first))
	}
	second, ok := syncTasks(fixture(t, "hook_todowrite_status.json"), first)
	if !ok || len(second) != 3 || second[1].Status != "done" || second[2].Status != "in_progress" {
		t.Fatalf("second = %s", titles(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID || !strings.HasPrefix(first[i].ID, "td-") {
			t.Fatalf("ids not stable: %s vs %s", titles(first), titles(second))
		}
	}
	// Same payload again: nothing changed.
	if _, ok := syncTasks(fixture(t, "hook_todowrite_status.json"), second); ok {
		t.Fatal("identical list reported as changed")
	}
	// Duplicate texts get distinct ids.
	dup, _ := syncTasks([]byte(`{"tool_name":"TodoWrite","tool_input":{"todos":[{"content":"a","status":"pending"},{"content":"a","status":"pending"}]}}`), nil)
	if len(dup) != 2 || dup[0].ID == dup[1].ID {
		t.Fatalf("dup = %s", titles(dup))
	}
	// An emptied list clears.
	cleared, ok := syncTasks([]byte(`{"tool_name":"TodoWrite","tool_input":{"todos":[]}}`), first)
	if !ok || len(cleared) != 0 {
		t.Fatalf("clear = %v %v", cleared, ok)
	}
}

func TestSyncTaskCreateUpdate(t *testing.T) {
	l, ok := syncTasks(fixture(t, "hook_taskcreate.json"), nil)
	if !ok || titles(l) != "c1:pending:Implement login" {
		t.Fatalf("create = %s", titles(l))
	}
	l, ok = syncTasks(fixture(t, "hook_taskupdate.json"), l)
	if !ok || titles(l) != "c1:in_progress:Implement login" {
		t.Fatalf("update = %s", titles(l))
	}
	if _, ok := syncTasks(fixture(t, "hook_taskupdate.json"), l); ok {
		t.Fatal("repeat update reported as changed")
	}
	// Unknown task id: ignored.
	if _, ok := syncTasks(fixture(t, "hook_taskupdate.json"), nil); ok {
		t.Fatal("update of an unknown task applied")
	}
	l, ok = syncTasks(fixture(t, "hook_taskupdate_deleted.json"), l)
	if !ok || len(l) != 0 {
		t.Fatalf("delete = %s", titles(l))
	}
}

func TestSyncIgnoresJunk(t *testing.T) {
	for _, in := range []string{"", "not json", "{}", `{"tool_name":"TodoWrite","tool_input":"x"}`,
		`{"hook_event_name":"PreToolUse","tool_name":"TodoWrite","tool_input":{"todos":[]}}`} {
		if _, ok := syncTasks([]byte(in), nil); ok {
			t.Errorf("%q applied", in)
		}
	}
	if _, ok := syncTasks(fixture(t, "hook_bash.json"), nil); ok {
		t.Fatal("Bash applied")
	}
}

func runSync(t *testing.T, payload []byte) string {
	t.Helper()
	cmd := taskSyncCmd()
	cmd.SetIn(strings.NewReader(string(payload)))
	out, err := execCmd(t, cmd, "--from", "claude")
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	return out
}

func TestTaskSyncNoDaemonNeverStarts(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "agentd.sock")
	t.Setenv(config.EnvSocket, sock)
	t.Setenv(config.EnvStateDir, filepath.Join(dir, "state"))
	if out := runSync(t, fixture(t, "hook_todowrite.json")); out != "" {
		t.Fatalf("output %q", out)
	}
	if _, err := os.Stat(sock); err == nil {
		t.Fatal("a daemon was started")
	}
}

func TestTaskSyncEndToEnd(t *testing.T) {
	sock := serveTestDaemon(t)
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Run(proto.RunArgs{Name: "sh@sync", Cmd: []string{"sh", "-c", `printf 'TOK=%s\n' "$AGENTD_TOKEN"; sleep 60`}}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Kill("sh@sync", "KILL") }()
	var token string
	for i := 0; i < 100 && token == ""; i++ {
		txt, _ := c.Dump(proto.DumpArgs{Name: "sh@sync"})
		if _, rest, ok := strings.Cut(txt, "TOK="); ok {
			if f := strings.Fields(rest); len(f) > 0 && len(f[0]) == 64 {
				token = f[0]
			}
		}
		if token == "" {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if token == "" {
		t.Fatal("no session token")
	}

	// Outside the session (human): nothing is written.
	if out := runSync(t, fixture(t, "hook_todowrite.json")); out != "" {
		t.Fatalf("human output %q", out)
	}
	if r, _ := c.Tasks("sh@sync"); len(r.Tasks) != 0 {
		t.Fatalf("human wrote tasks: %s", titles(r.Tasks))
	}

	// As the session: the list follows the hook payloads, silently.
	t.Setenv("AGENTD_ID", "sh@sync")
	t.Setenv("AGENTD_TOKEN", token)
	for _, f := range []string{"hook_todowrite.json", "hook_bash.json", "garbage"} {
		payload := []byte("garbage")
		if f != "garbage" {
			payload = fixture(t, f)
		}
		if out := runSync(t, payload); out != "" {
			t.Fatalf("%s: output %q", f, out)
		}
	}
	r, err := c.Tasks("sh@sync")
	if err != nil || len(r.Tasks) != 3 || r.Tasks[1].Status != "in_progress" {
		t.Fatalf("tasks = %s, %v", titles(r.Tasks), err)
	}
	runSync(t, fixture(t, "hook_todowrite_status.json"))
	r2, _ := c.Tasks("sh@sync")
	if len(r2.Tasks) != 3 || r2.Tasks[2].Status != "in_progress" || r2.Tasks[0].ID != r.Tasks[0].ID {
		t.Fatalf("after status change: %s", titles(r2.Tasks))
	}
	// Incremental tools append to the list.
	runSync(t, fixture(t, "hook_taskcreate.json"))
	runSync(t, fixture(t, "hook_taskupdate.json"))
	r3, _ := c.Tasks("sh@sync")
	if len(r3.Tasks) != 4 || r3.Tasks[3].ID != "c1" || r3.Tasks[3].Status != "in_progress" {
		t.Fatalf("after create/update: %s", titles(r3.Tasks))
	}
}

func TestHooksInstallPrint(t *testing.T) {
	out, err := execCmd(t, hooksCmd(), "install", "--print")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"PostToolUse"`, `"matcher": "TodoWrite|TaskCreate|TaskUpdate"`, "task sync --from claude", "agentd_tasks_set", "agentd_task_update", "agentd_ask"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if _, err := execCmd(t, hooksCmd(), "install"); err == nil {
		t.Fatal("install without --print accepted")
	}
}
