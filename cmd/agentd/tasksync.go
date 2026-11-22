package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/client"
	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/proto"
)

// syncTimeout bounds the whole `task sync` run: it sits in an agent's hook
// chain and must never stall it.
const syncTimeout = 3 * time.Second

// maxHookPayload caps the hook JSON read from stdin.
const maxHookPayload = 4 << 20

// taskSyncCmd is `agentd task sync`: a harness hook feeds it one tool-call
// payload and the calling session's task list follows the harness's own todo
// list. It never prints to stdout (hook output can reach the model), never
// autostarts the daemon and never fails the hook chain: every problem is a
// silent exit 0 (stderr only with --verbose).
func taskSyncCmd() *cobra.Command {
	var from string
	var verbose bool
	cmd := &cobra.Command{
		Use:   "sync [--from claude]",
		Short: "Mirror a harness todo list into this session's task list (hook mode)",
		Long: `Read one Claude Code PostToolUse hook payload from stdin and update this
session's task list (see 'agentd hooks install --print' for the settings
snippet). Handles TodoWrite (whole list) and TaskCreate / TaskUpdate (one task).
Task ids are derived from the harness (TodoWrite: "td-" + a hash of the todo's
text, so a status change keeps the id; TaskCreate/TaskUpdate: "c" + the
harness task id). Unknown tools, malformed input, no daemon or not running
inside an agentd session: exit 0, no output. The daemon is never started.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if from != "claude" {
				return fmt.Errorf("unknown --from %q (want claude)", from)
			}
			data, _ := io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxHookPayload))
			done := make(chan error, 1)
			go func() { done <- runTaskSync(data) }()
			select {
			case err := <-done:
				logSync(verbose, err)
			case <-time.After(syncTimeout):
				logSync(verbose, fmt.Errorf("timed out after %s", syncTimeout))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "claude", "payload format (claude)")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "report problems on stderr")
	return cmd
}

func logSync(verbose bool, err error) {
	if verbose && err != nil {
		fmt.Fprintln(os.Stderr, "agentd task sync:", err)
	}
}

// runTaskSync applies one payload. A nil error also covers "nothing to do".
func runTaskSync(data []byte) error {
	if _, ok := parseHook(data); !ok {
		return nil // unrelated tool call: never touch the daemon
	}
	c, err := client.Dial(config.SocketPath()) // never DialAuto: no autostart
	if err != nil {
		return err
	}
	defer c.Close()
	who, err := c.Whoami()
	if err != nil || !who.Session {
		return err
	}
	cur, err := c.Tasks(who.Identity)
	if err != nil {
		return err
	}
	next, changed := syncTasks(data, cur.Tasks)
	if !changed {
		return nil
	}
	_, err = c.TasksSet(next)
	return err
}

// hookPayload is the part of a Claude Code PostToolUse payload we read.
type hookPayload struct {
	Event    string          `json:"hook_event_name"`
	Tool     string          `json:"tool_name"`
	Input    json.RawMessage `json:"tool_input"`
	Response json.RawMessage `json:"tool_response"`
}

func parseHook(data []byte) (hookPayload, bool) {
	var p hookPayload
	if json.Unmarshal(data, &p) != nil {
		return p, false
	}
	if p.Event != "" && p.Event != "PostToolUse" {
		return p, false
	}
	switch p.Tool {
	case "TodoWrite", "TaskCreate", "TaskUpdate":
		return p, true
	}
	return p, false
}

// syncTasks maps the payload onto the current list and reports whether it
// changed. Anything it does not understand leaves the list alone.
func syncTasks(data []byte, cur []proto.Task) ([]proto.Task, bool) {
	p, ok := parseHook(data)
	if !ok {
		return nil, false
	}
	var next []proto.Task
	switch p.Tool {
	case "TodoWrite":
		next, ok = syncTodoWrite(p)
	case "TaskCreate":
		next, ok = syncTaskCreate(p, cur)
	case "TaskUpdate":
		next, ok = syncTaskUpdate(p, cur)
	}
	if !ok {
		return nil, false
	}
	if len(next) > proto.MaxTasks {
		next = next[:proto.MaxTasks]
	}
	return next, !sameTasks(cur, next)
}

func sameTasks(a, b []proto.Task) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Title != b[i].Title || a[i].Status != b[i].Status || a[i].Note != b[i].Note {
			return false
		}
	}
	return true
}

// mapHookStatus maps a Claude Code status to a task status ("" = unknown).
func mapHookStatus(s string) string {
	switch s {
	case "pending":
		return proto.TaskPending
	case "in_progress":
		return proto.TaskInProgress
	case "completed":
		return proto.TaskDone
	}
	return ""
}

func clipTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > proto.MaxTaskTitle {
		s = string(r[:proto.MaxTaskTitle-1]) + "…"
	}
	return s
}

// hashID is "<prefix>-" + 8 hex of SHA-1 over the text: stable while the text
// is, whatever the status or position.
func hashID(prefix, text string) string {
	h := sha1.Sum([]byte(strings.Join(strings.Fields(text), " ")))
	return prefix + "-" + hex.EncodeToString(h[:4])
}

// TodoWrite: tool_input.todos = [{content, status, activeForm}], the whole list.
func syncTodoWrite(p hookPayload) ([]proto.Task, bool) {
	var in struct {
		Todos []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
	}
	if json.Unmarshal(p.Input, &in) != nil || in.Todos == nil {
		return nil, false
	}
	out := make([]proto.Task, 0, len(in.Todos))
	seen := map[string]int{}
	for _, t := range in.Todos {
		title := clipTitle(t.Content)
		if title == "" {
			continue
		}
		st := mapHookStatus(t.Status)
		if st == "" {
			st = proto.TaskPending
		}
		id := hashID("td", title)
		if seen[id]++; seen[id] > 1 { // identical texts: -2, -3...
			id += "-" + strconv.Itoa(seen[id])
		}
		out = append(out, proto.Task{ID: id, Title: title, Status: st})
	}
	return out, true
}

// flexString reads a JSON string or number.
func flexString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

// taskID makes a valid task id ([A-Za-z0-9._-]{1,64}) from a harness id.
func taskID(harnessID string) string {
	var b strings.Builder
	for _, r := range harnessID {
		if r == '.' || r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	id := "c" + b.String()
	if len(id) > 64 {
		id = id[:64]
	}
	return id
}

// TaskCreate: tool_input {subject, description, activeForm?}; the assigned id
// is in tool_response.task.id (TaskCreateOutput). Without it the id falls back
// to a hash of the subject (later TaskUpdates then cannot find the task).
func syncTaskCreate(p hookPayload, cur []proto.Task) ([]proto.Task, bool) {
	var in struct {
		Subject string `json:"subject"`
	}
	if json.Unmarshal(p.Input, &in) != nil {
		return nil, false
	}
	title := clipTitle(in.Subject)
	if title == "" {
		return nil, false
	}
	var resp struct {
		Task struct {
			ID json.RawMessage `json:"id"`
		} `json:"task"`
	}
	_ = json.Unmarshal(p.Response, &resp)
	id := taskID(flexString(resp.Task.ID))
	if id == "" {
		id = hashID("tc", title)
	}
	next := append([]proto.Task(nil), cur...)
	for i := range next {
		if next[i].ID == id {
			next[i].Title = title
			return next, true
		}
	}
	return append(next, proto.Task{ID: id, Title: title, Status: proto.TaskPending}), true
}

// TaskUpdate: tool_input {taskId, status?, subject?, ...}; status "deleted"
// removes the task. Unknown tasks are ignored.
func syncTaskUpdate(p hookPayload, cur []proto.Task) ([]proto.Task, bool) {
	var in map[string]json.RawMessage
	if json.Unmarshal(p.Input, &in) != nil {
		return nil, false
	}
	var hid json.RawMessage
	for _, k := range []string{"taskId", "id", "task_id"} { // Claude Code repairs these aliases
		if v, ok := in[k]; ok {
			hid = v
			break
		}
	}
	id := taskID(flexString(hid))
	idx := -1
	for i, t := range cur {
		if id != "" && t.ID == id {
			idx = i
		}
	}
	if idx < 0 {
		return nil, false
	}
	next := append([]proto.Task(nil), cur...)
	var status, subject string
	_ = json.Unmarshal(in["status"], &status)
	_ = json.Unmarshal(in["subject"], &subject)
	if status == "deleted" {
		return append(next[:idx], next[idx+1:]...), true
	}
	if st := mapHookStatus(status); st != "" {
		next[idx].Status = st
	}
	if t := clipTitle(subject); t != "" {
		next[idx].Title = t
	}
	return next, true
}
