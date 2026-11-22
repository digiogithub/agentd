package proto

import "time"

// Task list of a session (ops tasks_set, task_update, tasks; event
// task.changed). Only the session itself writes its list; the daemon keeps it
// in memory until the session is removed.

// Task statuses.
const (
	TaskPending    = "pending"
	TaskInProgress = "in_progress"
	TaskBlocked    = "blocked"
	TaskDone       = "done"
	TaskCancelled  = "cancelled"
)

// TaskStatuses lists every valid task status.
var TaskStatuses = []string{TaskPending, TaskInProgress, TaskBlocked, TaskDone, TaskCancelled}

// Task limits.
const (
	MaxTasks     = 50
	MaxTaskTitle = 200 // runes
	MaxTaskNote  = 500 // runes
)

// Task is one item of a session's plan. ID is [A-Za-z0-9._-]{1,64} (assigned
// t1, t2... when tasks_set gives none); Title and Note are agent-written,
// sanitized text. UpdatedAt is set by the daemon (ignored on input).
type Task struct {
	ID        string    `json:"id,omitempty"`
	Title     string    `json:"title"`
	Status    string    `json:"status,omitempty"`
	Note      string    `json:"note,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// TasksSetArgs are the args of op tasks_set: the caller's whole list, in
// order. A task without status is pending.
type TasksSetArgs struct {
	Tasks []Task `json:"tasks"`
}

// TaskUpdateArgs are the args of op task_update: nil fields are unchanged
// (an empty Note clears it).
type TaskUpdateArgs struct {
	ID     string  `json:"id"`
	Status *string `json:"status,omitempty"`
	Title  *string `json:"title,omitempty"`
	Note   *string `json:"note,omitempty"`
}

// TasksResult answers tasks_set and task_update (Name empty) and tasks.
type TasksResult struct {
	Name     string `json:"name,omitempty"`
	Tasks    []Task `json:"tasks"`
	Revision uint64 `json:"revision"`
}

// TaskCounts counts a list's tasks per status.
type TaskCounts struct {
	Pending    int `json:"pending"`
	InProgress int `json:"in_progress"`
	Blocked    int `json:"blocked"`
	Done       int `json:"done"`
	Cancelled  int `json:"cancelled"`
}

// TaskSummary is SessionInfo.tasks: the list's revision, counts and the
// title of the first in_progress task.
type TaskSummary struct {
	Revision uint64     `json:"revision"`
	Counts   TaskCounts `json:"counts"`
	Current  string     `json:"current,omitempty"`
}

// TaskChange is one task a revision added or changed (From = the previous
// status; absent for a new task or when the status did not change).
type TaskChange struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	From   string `json:"from,omitempty"`
}

// TaskChangedData is the data of task.changed.
type TaskChangedData struct {
	Revision uint64       `json:"revision"`
	Counts   TaskCounts   `json:"counts"`
	Current  string       `json:"current,omitempty"`
	Changed  []TaskChange `json:"changed"`
}
