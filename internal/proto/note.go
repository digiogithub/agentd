package proto

import "time"

// Backlog notes (plan docs/plans/agentd/backlog-notes_plan.md): ideas and
// future tasks the human writes down on a host and later assigns to a
// session. The daemon persists them in SQLite ($STATE/backlog.db). Only
// humans (and owner relay devices with the operate scope) read and write
// them; a session never does.

// Ops of the backlog.
const (
	OpNotes      = "notes"       // NotesArgs → NotesResult
	OpNoteAdd    = "note_add"    // NoteAddArgs → Note
	OpNoteUpdate = "note_update" // NoteUpdateArgs → Note
	OpNoteRm     = "note_rm"     // NoteIDArgs → Note (the removed note)
	OpNoteAssign = "note_assign" // NoteAssignArgs → NoteAssignResult
)

// EvNoteChanged is published (no session: host-wide, humans only) whenever a
// note is added, edited, assigned, completed or removed. Data NoteChangedData.
const EvNoteChanged = "note.changed"

// Note states.
const (
	NoteOpen     = "open"     // in the backlog, not assigned
	NoteAssigned = "assigned" // handed to a session as a task
	NoteDone     = "done"     // the session finished the task
)

// NoteStates lists every valid note state.
var NoteStates = []string{NoteOpen, NoteAssigned, NoteDone}

// Note limits.
const (
	MaxNotes     = 1000
	MaxNoteTitle = MaxTaskTitle // runes
	MaxNoteBody  = 4000         // runes
)

// TaskOriginBacklog marks a task the human assigned from the backlog
// (Task.Origin): tasks_set keeps it while it is open even when the agent's new
// list leaves it out.
const TaskOriginBacklog = "backlog"

// Note is one backlog item. ID is a monotonic integer, never reused. Title
// and Body are human-written, sanitized text. AssignedTo is the session and
// TaskID the id of the task created in its list ("n<ID>").
type Note struct {
	ID         int64      `json:"id"`
	Title      string     `json:"title"`
	Body       string     `json:"body,omitempty"`
	State      string     `json:"state"`
	AssignedTo string     `json:"assigned_to,omitempty"`
	TaskID     string     `json:"task_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	AssignedAt *time.Time `json:"assigned_at,omitempty"`
	DoneAt     *time.Time `json:"done_at,omitempty"`
}

// NotesArgs are the args of op notes. States filters (empty = every state).
// SinceDigest works like inbox: a matching digest answers not_modified.
type NotesArgs struct {
	States      []string `json:"states,omitempty"`
	SinceDigest string   `json:"since_digest,omitempty"`
}

// NotesResult answers op notes: open notes first (newest first), then
// assigned, then done.
type NotesResult struct {
	Notes       []Note `json:"notes"`
	Digest      string `json:"digest"`
	NotModified bool   `json:"not_modified,omitempty"`
}

// NoteAddArgs are the args of op note_add.
type NoteAddArgs struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}

// NoteUpdateArgs are the args of op note_update: nil fields are unchanged.
// State may only be set to "open" (take an assigned or done note back into
// the backlog; the session's task is left as it is).
type NoteUpdateArgs struct {
	ID    int64   `json:"id"`
	Title *string `json:"title,omitempty"`
	Body  *string `json:"body,omitempty"`
	State *string `json:"state,omitempty"`
}

// NoteIDArgs names one note.
type NoteIDArgs struct {
	ID int64 `json:"id"`
}

// NoteAssignArgs are the args of op note_assign: the note becomes a task
// ("n<ID>", origin backlog) of the running session Session with Status
// (pending, the default, or in_progress). Unless Notify is false the session
// also gets a mailbox message from the caller with the title and body, which
// the delivery loop types in.
type NoteAssignArgs struct {
	ID      int64  `json:"id"`
	Session string `json:"session"`
	Status  string `json:"status,omitempty"`
	Notify  *bool  `json:"notify,omitempty"`
}

// NoteAssignResult answers op note_assign.
type NoteAssignResult struct {
	Note      Note   `json:"note"`
	Task      Task   `json:"task"`
	MessageID int64  `json:"message_id,omitempty"` // the notice sent to the session
	Warning   string `json:"warning,omitempty"`
}

// NoteChangedData is the data of note.changed.
type NoteChangedData struct {
	ID      int64  `json:"id"`
	State   string `json:"state,omitempty"`
	Removed bool   `json:"removed,omitempty"`
}
