package fake

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/digiogithub/agentd/internal/desktopui/backend"
	"github.com/digiogithub/agentd/internal/proto"
)

// NoteAssignCall is one recorded NoteAssign.
type NoteAssignCall struct {
	HostID string
	Args   proto.NoteAssignArgs
}

// backlogState is the fake's backlog notes per host (f.mu guards it).
type backlogState struct {
	notes    map[string][]proto.Note
	next     int64
	assigned []NoteAssignCall
}

// seedNotes gives the first agentd hosts a few generic notes (f.mu held or
// not yet shared).
func (f *Fake) seedNotes(now time.Time) {
	f.backlog = &backlogState{notes: map[string][]proto.Note{}, next: 1}
	add := func(host, title, body string, ago time.Duration) {
		id := f.backlog.next
		f.backlog.next++
		at := now.Add(-ago)
		f.backlog.notes[host] = append(f.backlog.notes[host], proto.Note{ID: id, Title: title, Body: body, State: proto.NoteOpen, CreatedAt: at, UpdatedAt: at})
	}
	add("estudio", "Revisar las licencias de las dependencias", "Hacer una pasada por go.mod y anotar las que no sean permisivas.", 26*time.Hour)
	add("estudio", "Documentar el despliegue del relay", "", 5*time.Hour)
	add("estudio", "Ordenar la configuración de ejemplo", "Separar las claves por secciones\ny quitar las obsoletas.", 40*time.Minute)
	add("nube", "Rotar el token de alta", "", 3*time.Hour)
	// One note already handed to an agent and one finished.
	n := f.backlog.notes["estudio"]
	n[1].State, n[1].AssignedTo, n[1].TaskID = proto.NoteAssigned, "claude@webapp", "n2"
	at := now.Add(-2 * time.Hour)
	n[1].AssignedAt, n[1].UpdatedAt = &at, at
	add("estudio", "Añadir un ejemplo de cola de trabajo", "", 30*time.Hour)
	d := f.backlog.notes["estudio"]
	d[3].State, d[3].AssignedTo, d[3].TaskID = proto.NoteDone, "claude@docs", "n5"
	done := now.Add(-20 * time.Hour)
	d[3].AssignedAt, d[3].DoneAt, d[3].UpdatedAt = &done, &done, done
	// The matching task of the assigned note.
	for _, r := range f.sessions {
		if r.s.Name == "claude@webapp" && r.s.HostID == "estudio" {
			r.tasks = append(r.tasks, proto.Task{ID: "n2", Title: n[1].Title, Status: proto.TaskPending, Origin: proto.TaskOriginBacklog, UpdatedAt: at})
		}
	}
}

// sortedNotes orders a host's notes as op notes does: open (newest first),
// assigned (latest assignment first), done (latest first).
func sortedNotes(in []proto.Note) []proto.Note {
	out := slices.Clone(in)
	rank := func(s string) int {
		switch s {
		case proto.NoteOpen:
			return 0
		case proto.NoteAssigned:
			return 1
		}
		return 2
	}
	when := func(n proto.Note) time.Time {
		switch {
		case n.State == proto.NoteAssigned && n.AssignedAt != nil:
			return *n.AssignedAt
		case n.State == proto.NoteDone && n.DoneAt != nil:
			return *n.DoneAt
		}
		return n.CreatedAt
	}
	slices.SortStableFunc(out, func(a, b proto.Note) int {
		if ra, rb := rank(a.State), rank(b.State); ra != rb {
			return ra - rb
		}
		return when(b).Compare(when(a))
	})
	return out
}

// notesBoard returns the Notes/NotesErr of a host's board (f.mu held).
func (f *Fake) notesBoard(h backend.Host) ([]proto.Note, string) {
	if !h.Allows(backend.CapBacklog) {
		return nil, "este dispositivo no puede ver el backlog"
	}
	return sortedNotes(f.backlog.notes[h.ID]), ""
}

func (f *Fake) noteHostLocked(hostID string) (*hostRec, error) {
	h := f.hostLocked(hostID)
	if h == nil {
		return nil, proto.Errorf(proto.CodeNotFound, "no such host %q", hostID)
	}
	if !h.st.Host.Allows(backend.CapBacklog) {
		return nil, proto.Errorf(proto.CodeForbidden, "no backlog for this device")
	}
	return h, nil
}

func (f *Fake) noteIndex(hostID string, id int64) int {
	for i, n := range f.backlog.notes[hostID] {
		if n.ID == id {
			return i
		}
	}
	return -1
}

func checkNoteText(title, body string) error {
	if strings.TrimSpace(title) == "" {
		return proto.Errorf(proto.CodeInvalid, "empty title")
	}
	if utf8.RuneCountInString(title) > proto.MaxNoteTitle || utf8.RuneCountInString(body) > proto.MaxNoteBody {
		return proto.Errorf(proto.CodeInvalid, "note too long")
	}
	return nil
}

// NoteAdd implements backend.Backend.
func (f *Fake) NoteAdd(ctx context.Context, hostID, title, body string) (*proto.Note, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.noteHostLocked(hostID); err != nil {
		return nil, err
	}
	if err := checkNoteText(title, body); err != nil {
		return nil, err
	}
	now := time.Now()
	n := proto.Note{ID: f.backlog.next, Title: strings.TrimSpace(title), Body: body, State: proto.NoteOpen, CreatedAt: now, UpdatedAt: now}
	f.backlog.next++
	f.backlog.notes[hostID] = append(f.backlog.notes[hostID], n)
	f.publishLocked()
	return &n, nil
}

// NoteUpdate implements backend.Backend.
func (f *Fake) NoteUpdate(ctx context.Context, hostID string, args proto.NoteUpdateArgs) (*proto.Note, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.noteHostLocked(hostID); err != nil {
		return nil, err
	}
	i := f.noteIndex(hostID, args.ID)
	if i < 0 {
		return nil, proto.Errorf(proto.CodeNotFound, "no such note %d", args.ID)
	}
	n := f.backlog.notes[hostID][i]
	if args.Title != nil {
		n.Title = strings.TrimSpace(*args.Title)
	}
	if args.Body != nil {
		n.Body = *args.Body
	}
	if err := checkNoteText(n.Title, n.Body); err != nil {
		return nil, err
	}
	if args.State != nil {
		if *args.State != proto.NoteOpen {
			return nil, proto.Errorf(proto.CodeInvalid, "state may only be %q", proto.NoteOpen)
		}
		n.State, n.AssignedTo, n.TaskID, n.AssignedAt, n.DoneAt = proto.NoteOpen, "", "", nil, nil
	}
	n.UpdatedAt = time.Now()
	f.backlog.notes[hostID][i] = n
	f.publishLocked()
	return &n, nil
}

// NoteRemove implements backend.Backend.
func (f *Fake) NoteRemove(ctx context.Context, hostID string, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.noteHostLocked(hostID); err != nil {
		return err
	}
	i := f.noteIndex(hostID, id)
	if i < 0 {
		return proto.Errorf(proto.CodeNotFound, "no such note %d", id)
	}
	f.backlog.notes[hostID] = slices.Delete(f.backlog.notes[hostID], i, i+1)
	f.publishLocked()
	return nil
}

// NoteAssign implements backend.Backend: the note becomes the task n<id>
// (origin backlog) of the session's task list.
func (f *Fake) NoteAssign(ctx context.Context, hostID string, args proto.NoteAssignArgs) (*proto.NoteAssignResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.backlog.assigned = append(f.backlog.assigned, NoteAssignCall{HostID: hostID, Args: args})
	if _, err := f.noteHostLocked(hostID); err != nil {
		return nil, err
	}
	i := f.noteIndex(hostID, args.ID)
	if i < 0 {
		return nil, proto.Errorf(proto.CodeNotFound, "no such note %d", args.ID)
	}
	status := args.Status
	switch status {
	case "":
		status = proto.TaskPending
	case proto.TaskPending, proto.TaskInProgress:
	default:
		return nil, proto.Errorf(proto.CodeInvalid, "status must be pending or in_progress")
	}
	var rec *sessRec
	for _, r := range f.sessions {
		if r.s.HostID == hostID && r.s.Source == backend.SourceAgentd && r.s.Name == args.Session {
			rec = r
		}
	}
	if rec == nil {
		return nil, proto.Errorf(proto.CodeNotFound, "no such session %q", args.Session)
	}
	if rec.s.Status == "exited" {
		return nil, proto.Errorf(proto.CodeExited, "session %q has exited", args.Session)
	}
	n := f.backlog.notes[hostID][i]
	if n.State == proto.NoteDone {
		return nil, proto.Errorf(proto.CodeInvalid, "note %d is done", n.ID)
	}
	now := time.Now()
	body := []rune(n.Body)
	if len(body) > 500 {
		body = body[:500]
	}
	task := proto.Task{ID: fmt.Sprintf("n%d", n.ID), Title: n.Title, Note: string(body), Status: status, Origin: proto.TaskOriginBacklog, UpdatedAt: now}
	replaced := false
	for j, t := range rec.tasks {
		if t.ID == task.ID {
			rec.tasks[j], replaced = task, true
		}
	}
	if !replaced {
		rec.tasks = append(rec.tasks, task)
	}
	n.State, n.AssignedTo, n.TaskID, n.AssignedAt, n.UpdatedAt = proto.NoteAssigned, args.Session, task.ID, &now, now
	f.backlog.notes[hostID][i] = n
	f.publishLocked()
	return &proto.NoteAssignResult{Note: n, Task: task}, nil
}

// NoteAssigns returns the recorded NoteAssign calls (tests).
func (f *Fake) NoteAssigns() []NoteAssignCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.backlog.assigned)
}

// Notes returns a host's notes in the order op notes answers (tests).
func (f *Fake) Notes(hostID string) []proto.Note {
	f.mu.Lock()
	defer f.mu.Unlock()
	return sortedNotes(f.backlog.notes[hostID])
}
