package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/digiogithub/agentd/internal/backlog"
	"github.com/digiogithub/agentd/internal/mailbox"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/screen"
	"github.com/digiogithub/agentd/internal/session"
)

// Backlog notes (plan docs/plans/agentd/backlog-notes_plan.md): ops notes,
// note_add, note_update, note_rm and note_assign over internal/backlog. Humans
// on the socket and owner relay devices (relayGate) only; a session is
// forbidden. A note assigned to a session becomes a task "n<id>" with origin
// backlog in its list (tasks.go keeps it across tasks_set).

// noteTaskID is the id of the task a note becomes.
func noteTaskID(id int64) string { return fmt.Sprintf("n%d", id) }

// backlogStore returns the store, or not_ready when the daemon runs without one.
func (s *Server) backlogStore(op string) (*backlog.Store, error) {
	if s.Backlog == nil {
		return nil, proto.Errorf(proto.CodeNotReady, "%s: the backlog store is not available", op)
	}
	return s.Backlog, nil
}

// noteGate refuses sessions: agents do not manage the backlog.
func noteGate(op string, who caller) error {
	if who.session {
		return proto.Errorf(proto.CodeForbidden, "%s: the backlog is for humans, not for agent sessions", op)
	}
	return nil
}

// cleanMultiline sanitizes text line by line (screen.Sanitize drops every C0
// control, newlines included), keeping the newlines, and caps it to max runes.
func cleanMultiline(s string, max int) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(screen.Sanitize(l, max), " \t")
	}
	out := strings.TrimSpace(strings.Join(lines, "\n"))
	if utf8.RuneCountInString(out) > max {
		out = string([]rune(out)[:max])
	}
	return out
}

func cleanNoteTitle(op, t string) (string, error) {
	t = strings.TrimSpace(screen.Sanitize(t, proto.MaxNoteTitle))
	if t == "" {
		return "", proto.Errorf(proto.CodeInvalid, "%s: a note title is required (1..%d characters)", op, proto.MaxNoteTitle)
	}
	return t, nil
}

// noteErr maps store errors onto coded errors.
func noteErr(op string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, backlog.ErrNotFound):
		return proto.Errorf(proto.CodeNotFound, "%s: %v", op, err)
	case errors.Is(err, backlog.ErrInvalid), errors.Is(err, backlog.ErrFull):
		return proto.Errorf(proto.CodeInvalid, "%s: %v", op, err)
	}
	return err
}

func (s *Server) publishNote(n proto.Note, removed bool) {
	d := proto.NoteChangedData{ID: n.ID, State: n.State}
	if removed {
		d = proto.NoteChangedData{ID: n.ID, Removed: true}
	}
	s.publish(proto.EvNoteChanged, "", d)
}

// notesOp serves op notes.
func (s *Server) notesOp(ctx context.Context, who caller, a proto.NotesArgs) (proto.NotesResult, error) {
	const op = proto.OpNotes
	if err := noteGate(op, who); err != nil {
		return proto.NotesResult{}, err
	}
	for _, st := range a.States {
		if !slices.Contains(proto.NoteStates, st) {
			return proto.NotesResult{}, proto.Errorf(proto.CodeInvalid, "%s: invalid state %q (want one of %s)", op, st,
				strings.Join(proto.NoteStates, ", "))
		}
	}
	st, err := s.backlogStore(op)
	if err != nil {
		return proto.NotesResult{}, err
	}
	notes, err := st.List(ctx, a.States)
	if err != nil {
		return proto.NotesResult{}, err
	}
	d := backlog.Digest(notes)
	if a.SinceDigest != "" && a.SinceDigest == d {
		return proto.NotesResult{Notes: []proto.Note{}, Digest: d, NotModified: true}, nil
	}
	return proto.NotesResult{Notes: notes, Digest: d}, nil
}

// noteAdd serves op note_add.
func (s *Server) noteAdd(ctx context.Context, who caller, a proto.NoteAddArgs) (proto.Note, error) {
	const op = proto.OpNoteAdd
	if err := noteGate(op, who); err != nil {
		return proto.Note{}, err
	}
	title, err := cleanNoteTitle(op, a.Title)
	if err != nil {
		return proto.Note{}, err
	}
	st, err := s.backlogStore(op)
	if err != nil {
		return proto.Note{}, err
	}
	n, err := st.Add(ctx, title, cleanMultiline(a.Body, proto.MaxNoteBody))
	if err != nil {
		return proto.Note{}, noteErr(op, err)
	}
	s.Log.Info("note added", "id", n.ID, "via", who.via)
	s.publishNote(n, false)
	return n, nil
}

// noteUpdate serves op note_update.
func (s *Server) noteUpdate(ctx context.Context, who caller, a proto.NoteUpdateArgs) (proto.Note, error) {
	const op = proto.OpNoteUpdate
	if err := noteGate(op, who); err != nil {
		return proto.Note{}, err
	}
	var title, body *string
	if a.Title != nil {
		t, err := cleanNoteTitle(op, *a.Title)
		if err != nil {
			return proto.Note{}, err
		}
		title = &t
	}
	if a.Body != nil {
		b := cleanMultiline(*a.Body, proto.MaxNoteBody)
		body = &b
	}
	reopen := false
	if a.State != nil {
		if *a.State != proto.NoteOpen {
			return proto.Note{}, proto.Errorf(proto.CodeInvalid, "%s: state may only be set to %q (got %q)", op, proto.NoteOpen, *a.State)
		}
		reopen = true
	}
	st, err := s.backlogStore(op)
	if err != nil {
		return proto.Note{}, err
	}
	n, err := st.Update(ctx, a.ID, title, body, reopen)
	if err != nil {
		return proto.Note{}, noteErr(op, err)
	}
	s.publishNote(n, false)
	return n, nil
}

// noteRm serves op note_rm.
func (s *Server) noteRm(ctx context.Context, who caller, a proto.NoteIDArgs) (proto.Note, error) {
	const op = proto.OpNoteRm
	if err := noteGate(op, who); err != nil {
		return proto.Note{}, err
	}
	st, err := s.backlogStore(op)
	if err != nil {
		return proto.Note{}, err
	}
	n, err := st.Remove(ctx, a.ID)
	if err != nil {
		return proto.Note{}, noteErr(op, err)
	}
	s.Log.Info("note removed", "id", n.ID, "via", who.via)
	s.publishNote(n, true)
	return n, nil
}

// noteTask builds the task a note becomes.
func noteTask(n proto.Note, status string, now time.Time) proto.Task {
	flat := strings.Join(strings.Fields(strings.ReplaceAll(n.Body, "\n", " ")), " ")
	return proto.Task{ID: noteTaskID(n.ID), Title: n.Title, Status: status,
		Note: cleanNote(flat), Origin: proto.TaskOriginBacklog, UpdatedAt: now}
}

// putTaskLocked appends t to e's list or replaces the task with the same id
// (e.mu held). It reports whether anything changed; a full list is invalid.
func (s *Server) putTaskLocked(name string, e *entry, t proto.Task) (bool, error) {
	cur := e.tasks.tasks
	i := slices.IndexFunc(cur, func(x proto.Task) bool { return x.ID == t.ID })
	next := slices.Clone(cur)
	ch := proto.TaskChange{ID: t.ID, Title: t.Title, Status: t.Status}
	if i >= 0 {
		if sameTask(cur[i], t) {
			return false, nil
		}
		if cur[i].Status != t.Status {
			ch.From = cur[i].Status
		}
		next[i] = t
	} else {
		if len(cur) >= proto.MaxTasks {
			return false, proto.Errorf(proto.CodeInvalid, "note_assign: the task list of %q is full (%d tasks)", name, proto.MaxTasks)
		}
		next = append(next, t)
	}
	s.commitTasksLocked(name, e, next, []proto.TaskChange{ch})
	return true, nil
}

// noteAssign serves op note_assign.
func (s *Server) noteAssign(ctx context.Context, who caller, a proto.NoteAssignArgs) (proto.NoteAssignResult, error) {
	const op = proto.OpNoteAssign
	if err := noteGate(op, who); err != nil {
		return proto.NoteAssignResult{}, err
	}
	status := a.Status
	if status == "" {
		status = proto.TaskPending
	}
	if status != proto.TaskPending && status != proto.TaskInProgress {
		return proto.NoteAssignResult{}, proto.Errorf(proto.CodeInvalid, "%s: status must be %q or %q (got %q)", op,
			proto.TaskPending, proto.TaskInProgress, a.Status)
	}
	st, err := s.backlogStore(op)
	if err != nil {
		return proto.NoteAssignResult{}, err
	}
	if who.dev != nil && !who.dev.allows(a.Session) {
		return proto.NoteAssignResult{}, proto.Errorf(proto.CodeNotFound, "no such session %q", a.Session)
	}
	e, err := s.lookup(a.Session)
	if err != nil {
		return proto.NoteAssignResult{}, err
	}
	if e.sess.State() == session.StateExited {
		return proto.NoteAssignResult{}, proto.Errorf(proto.CodeExited, "%s: session %q has exited", op, a.Session)
	}
	prev, err := st.Get(ctx, a.ID)
	if err != nil {
		return proto.NoteAssignResult{}, noteErr(op, err)
	}
	if prev.State == proto.NoteDone {
		return proto.NoteAssignResult{}, proto.Errorf(proto.CodeInvalid, "%s: note #%d is done (reopen it first)", op, a.ID)
	}
	// Room for the task, checked before any write.
	tid := noteTaskID(a.ID)
	e.mu.Lock()
	full := len(e.tasks.tasks) >= proto.MaxTasks &&
		!slices.ContainsFunc(e.tasks.tasks, func(x proto.Task) bool { return x.ID == tid })
	e.mu.Unlock()
	if full {
		return proto.NoteAssignResult{}, proto.Errorf(proto.CodeInvalid, "%s: the task list of %q is full (%d tasks)", op, a.Session, proto.MaxTasks)
	}

	now := time.Now()
	n, err := st.Assign(ctx, a.ID, a.Session, tid, now)
	if err != nil {
		return proto.NoteAssignResult{}, noteErr(op, err)
	}
	task := noteTask(n, status, now)
	e.mu.Lock()
	_, err = s.putTaskLocked(a.Session, e, task)
	e.mu.Unlock()
	if err != nil { // the list filled up in between: undo the assignment
		if prev.State == proto.NoteOpen {
			_, _ = st.Update(ctx, a.ID, nil, nil, true)
		}
		return proto.NoteAssignResult{}, err
	}
	s.Log.Info("note assigned", "id", n.ID, "session", a.Session, "status", status, "via", who.via)
	s.publishNote(n, false)

	res := proto.NoteAssignResult{Note: n, Task: task}
	if a.Notify != nil && !*a.Notify {
		return res, nil
	}
	if !s.Cfg.Allowed(a.Session, who.id) {
		res.Warning = fmt.Sprintf("assigned, but %q is not allowed to message %q (acl): no notice sent", who.id, a.Session)
		return res, nil
	}
	m, err := s.Store.Send(ctx, mailbox.Message{
		Sender: who.id, Recipient: a.Session,
		Subject: fmt.Sprintf("Backlog task %s: %s", tid, n.Title),
		Body:    noteMessageBody(n, tid),
	})
	if err != nil {
		s.Log.Warn("note notice not stored", "id", n.ID, "err", err)
		res.Warning = "assigned, but the mailbox notice could not be stored: " + err.Error()
		return res, nil
	}
	res.MessageID = m.ID
	s.Log.Info("message queued", "id", m.ID, "from", m.Sender, "to", m.Recipient, "via", who.via)
	s.publishMailKind(proto.EvMailQueued, m.ID, m.Sender, m.Recipient, m.Kind)
	s.kick(m.Recipient)
	return res, nil
}

func noteMessageBody(n proto.Note, tid string) string {
	var b strings.Builder
	b.WriteString(n.Title)
	if n.Body != "" {
		b.WriteString("\n\n")
		b.WriteString(n.Body)
	}
	fmt.Fprintf(&b, "\n\nThis task is already in your task list as %q: keep that id when you rewrite the list.", tid)
	return b.String()
}

// backlogDone marks the notes assigned to (session, taskID) done and
// publishes note.changed. Called after e.mu is released (store I/O).
func (s *Server) backlogDone(session string, taskIDs []string) {
	if s.Backlog == nil || len(taskIDs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	for _, tid := range taskIDs {
		notes, err := s.Backlog.MarkDone(ctx, session, tid, time.Now())
		if err != nil {
			s.Log.Warn("mark note done", "session", session, "task", tid, "err", err)
			continue
		}
		for _, n := range notes {
			s.publishNote(n, false)
		}
	}
}
