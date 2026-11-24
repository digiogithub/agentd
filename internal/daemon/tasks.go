package daemon

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/screen"
	"github.com/digiogithub/agentd/internal/session"
)

// Task lists (plan docs/plans/agentd/dashboard_plan.md, "Contract > Tasks"):
// an agent reports its plan with tasks_set / task_update; the daemon keeps it
// per session in memory (dropped by rm, readable after exit), serves it with
// op tasks, summarizes it in SessionInfo.tasks and publishes task.changed.

var taskIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// taskList is the task bookkeeping of one session, guarded by entry.mu.
type taskList struct {
	tasks []proto.Task
	rev   uint64 // bumps only when the list changes; 0 = never set
}

func sameTask(a, b proto.Task) bool {
	return a.ID == b.ID && a.Title == b.Title && a.Status == b.Status && a.Note == b.Note && a.Origin == b.Origin
}

// keepsBacklog reports whether a backlog task survives a tasks_set that leaves
// it out: it is still open.
func keepsBacklog(t proto.Task) bool {
	return t.Origin == proto.TaskOriginBacklog &&
		(t.Status == proto.TaskPending || t.Status == proto.TaskInProgress || t.Status == proto.TaskBlocked)
}

func cleanTitle(op, t string) (string, error) {
	t = strings.TrimSpace(screen.Sanitize(t, proto.MaxTaskTitle))
	if t == "" {
		return "", proto.Errorf(proto.CodeInvalid, "%s: a task title is required (1..%d characters)", op, proto.MaxTaskTitle)
	}
	return t, nil
}

func cleanNote(n string) string { return strings.TrimSpace(screen.Sanitize(n, proto.MaxTaskNote)) }

func checkStatus(op, st string) error {
	if !slices.Contains(proto.TaskStatuses, st) {
		return proto.Errorf(proto.CodeInvalid, "%s: invalid status %q (want one of %s)", op, st,
			strings.Join(proto.TaskStatuses, ", "))
	}
	return nil
}

// normalizeTasks validates and cleans a tasks_set list and assigns the
// missing ids (t1, t2... first free).
func normalizeTasks(in []proto.Task) ([]proto.Task, error) {
	const op = "tasks_set"
	if len(in) > proto.MaxTasks {
		return nil, proto.Errorf(proto.CodeInvalid, "%s: at most %d tasks (got %d)", op, proto.MaxTasks, len(in))
	}
	out := make([]proto.Task, len(in))
	used := map[string]bool{}
	for i, t := range in {
		if t.ID != "" {
			if !taskIDRE.MatchString(t.ID) {
				return nil, proto.Errorf(proto.CodeInvalid, "%s: invalid task id %q (want [A-Za-z0-9._-]{1,64})", op, t.ID)
			}
			if used[t.ID] {
				return nil, proto.Errorf(proto.CodeInvalid, "%s: duplicate task id %q", op, t.ID)
			}
			used[t.ID] = true
		}
		title, err := cleanTitle(op, t.Title)
		if err != nil {
			return nil, err
		}
		st := t.Status
		if st == "" {
			st = proto.TaskPending
		}
		if err := checkStatus(op, st); err != nil {
			return nil, err
		}
		out[i] = proto.Task{ID: t.ID, Title: title, Status: st, Note: cleanNote(t.Note)}
	}
	n := 1
	for i := range out {
		if out[i].ID != "" {
			continue
		}
		for used[fmt.Sprintf("t%d", n)] {
			n++
		}
		out[i].ID = fmt.Sprintf("t%d", n)
		used[out[i].ID] = true
	}
	return out, nil
}

// summarize returns the counts and the title of the first in_progress task.
func summarize(ts []proto.Task) (proto.TaskCounts, string) {
	var c proto.TaskCounts
	cur := ""
	for _, t := range ts {
		switch t.Status {
		case proto.TaskPending:
			c.Pending++
		case proto.TaskInProgress:
			c.InProgress++
			if cur == "" {
				cur = t.Title
			}
		case proto.TaskBlocked:
			c.Blocked++
		case proto.TaskDone:
			c.Done++
		case proto.TaskCancelled:
			c.Cancelled++
		}
	}
	return c, cur
}

// taskSummary is SessionInfo.tasks (nil when the session never set a list).
func (e *entry) taskSummary() *proto.TaskSummary {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tasks.rev == 0 {
		return nil
	}
	c, cur := summarize(e.tasks.tasks)
	return &proto.TaskSummary{Revision: e.tasks.rev, Counts: c, Current: cur}
}

// tasksResultLocked copies the list (e.mu held).
func (e *entry) tasksResultLocked() proto.TasksResult {
	return proto.TasksResult{Tasks: append([]proto.Task{}, e.tasks.tasks...), Revision: e.tasks.rev}
}

// commitTasksLocked installs next as the new revision and publishes
// task.changed (e.mu held, so concurrent writes publish in apply order).
func (s *Server) commitTasksLocked(name string, e *entry, next []proto.Task, changed []proto.TaskChange) {
	e.tasks.tasks = next
	e.tasks.rev++
	c, cur := summarize(next)
	if changed == nil {
		changed = []proto.TaskChange{}
	}
	s.publish(proto.EvTaskChanged, name, proto.TaskChangedData{Revision: e.tasks.rev, Counts: c, Current: cur, Changed: changed})
}

// taskWriter returns the entry of the calling session for a task write: the
// caller must be a running session (humans and relay devices only read).
func (s *Server) taskWriter(op string, who caller) (*entry, error) {
	if !who.session {
		return nil, proto.Errorf(proto.CodeForbidden,
			"%s: only an agent session writes its task list (humans read it with op tasks)", op)
	}
	e, err := s.lookup(who.id)
	if err != nil {
		return nil, err
	}
	if e.sess.State() == session.StateExited {
		return nil, proto.Errorf(proto.CodeExited, "%s: session %q has exited", op, who.id)
	}
	return e, nil
}

// tasksSet serves op tasks_set: the caller's list is replaced (idempotent).
func (s *Server) tasksSet(who caller, a proto.TasksSetArgs) (proto.TasksResult, error) {
	e, err := s.taskWriter(proto.OpTasksSet, who)
	if err != nil {
		return proto.TasksResult{}, err
	}
	next, err := normalizeTasks(a.Tasks)
	if err != nil {
		return proto.TasksResult{}, err
	}
	now := time.Now()
	res, done, err := s.tasksSetLocked(who.id, e, next, now)
	if err != nil {
		return proto.TasksResult{}, err
	}
	s.backlogDone(who.id, done) // store I/O, outside e.mu
	return res, nil
}

// tasksSetLocked applies a normalized list under e.mu. It returns the ids of
// the backlog tasks that became done.
func (s *Server) tasksSetLocked(name string, e *entry, next []proto.Task, now time.Time) (proto.TasksResult, []string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	prev := e.tasks.tasks
	byID := make(map[string]proto.Task, len(prev))
	for _, t := range prev {
		byID[t.ID] = t
	}
	// Origin is the daemon's: kept for matching ids (input is ignored), and an
	// open backlog task the agent left out stays after its own tasks.
	inNext := make(map[string]bool, len(next))
	for i, t := range next {
		inNext[t.ID] = true
		next[i].Origin = byID[t.ID].Origin
	}
	for _, t := range prev {
		if keepsBacklog(t) && !inNext[t.ID] {
			next = append(next, t)
		}
	}
	if len(next) > proto.MaxTasks {
		return proto.TasksResult{}, nil, proto.Errorf(proto.CodeInvalid,
			"tasks_set: at most %d tasks including the open backlog tasks the list leaves out (got %d)", proto.MaxTasks, len(next))
	}
	if e.tasks.rev > 0 && slices.EqualFunc(prev, next, sameTask) {
		return e.tasksResultLocked(), nil, nil
	}
	var changed []proto.TaskChange
	var done []string
	for i, t := range next {
		old, ok := byID[t.ID]
		switch {
		case !ok:
			next[i].UpdatedAt = now
			changed = append(changed, proto.TaskChange{ID: t.ID, Title: t.Title, Status: t.Status})
		case sameTask(old, t):
			next[i].UpdatedAt = old.UpdatedAt
		default:
			next[i].UpdatedAt = now
			ch := proto.TaskChange{ID: t.ID, Title: t.Title, Status: t.Status}
			if old.Status != t.Status {
				ch.From = old.Status
				if t.Origin == proto.TaskOriginBacklog && t.Status == proto.TaskDone {
					done = append(done, t.ID)
				}
			}
			changed = append(changed, ch)
		}
	}
	s.commitTasksLocked(name, e, next, changed)
	return e.tasksResultLocked(), done, nil
}

// taskUpdate serves op task_update: one task of the caller's list changes.
func (s *Server) taskUpdate(who caller, a proto.TaskUpdateArgs) (proto.TasksResult, error) {
	const op = proto.OpTaskUpdate
	e, err := s.taskWriter(op, who)
	if err != nil {
		return proto.TasksResult{}, err
	}
	var title, note string
	if a.Title != nil {
		if title, err = cleanTitle(op, *a.Title); err != nil {
			return proto.TasksResult{}, err
		}
	}
	if a.Note != nil {
		note = cleanNote(*a.Note)
	}
	if a.Status != nil {
		if err := checkStatus(op, *a.Status); err != nil {
			return proto.TasksResult{}, err
		}
	}
	res, done, err := s.taskUpdateLocked(who.id, e, a, title, note)
	if err != nil {
		return proto.TasksResult{}, err
	}
	if done {
		s.backlogDone(who.id, []string{a.ID}) // store I/O, outside e.mu
	}
	return res, nil
}

// taskUpdateLocked applies one task_update under e.mu; done reports that a
// backlog task became done.
func (s *Server) taskUpdateLocked(name string, e *entry, a proto.TaskUpdateArgs, title, note string) (proto.TasksResult, bool, error) {
	const op = proto.OpTaskUpdate
	e.mu.Lock()
	defer e.mu.Unlock()
	i := slices.IndexFunc(e.tasks.tasks, func(t proto.Task) bool { return t.ID == a.ID })
	if i < 0 {
		return proto.TasksResult{}, false, proto.Errorf(proto.CodeNotFound, "%s: no task %q", op, a.ID)
	}
	old := e.tasks.tasks[i]
	t := old
	if a.Title != nil {
		t.Title = title
	}
	if a.Note != nil {
		t.Note = note
	}
	if a.Status != nil {
		t.Status = *a.Status
	}
	if sameTask(old, t) {
		return e.tasksResultLocked(), false, nil
	}
	t.UpdatedAt = time.Now()
	next := slices.Clone(e.tasks.tasks)
	next[i] = t
	ch := proto.TaskChange{ID: t.ID, Title: t.Title, Status: t.Status}
	if old.Status != t.Status {
		ch.From = old.Status
	}
	s.commitTasksLocked(name, e, next, []proto.TaskChange{ch})
	done := old.Origin == proto.TaskOriginBacklog && old.Status != proto.TaskDone && t.Status == proto.TaskDone
	return e.tasksResultLocked(), done, nil
}

// tasksOp serves op tasks: humans read any session, a session itself unless
// [security] agent_view (same rule as view and turns).
func (s *Server) tasksOp(who caller, a proto.NameArgs) (proto.TasksResult, error) {
	if who.session && who.id != a.Name && !s.Cfg.Security.AgentView {
		return proto.TasksResult{}, proto.Errorf(proto.CodeForbidden,
			"tasks: session %q may not read session %q (set [security] agent_view = true to allow)", who.id, a.Name)
	}
	e, err := s.lookup(a.Name)
	if err != nil {
		return proto.TasksResult{}, err
	}
	e.mu.Lock()
	res := e.tasksResultLocked()
	e.mu.Unlock()
	res.Name = a.Name
	return res, nil
}
