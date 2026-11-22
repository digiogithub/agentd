# AGED-US-0039: agent task lists (ops tasks_set / task_update / tasks)

Date: 2026-11-22. Plan: [[plans/agentd/dashboard_plan]] ("Contract > Tasks"), epic AGED-EP-0007 (work dashboard).

## What changed

Agents report their plan (task list) to the daemon. The daemon keeps one list per session in memory, serves it,
summarizes it in `ls` / the subscribe snapshot and publishes every change on the event bus. This is the data source
for the future dashboard in agentd-desktop and agentd-app ([[AGED-US-0041]] board, [[AGED-US-0045]] desktop Panel).

- Wire (`internal/proto/task.go`, `internal/proto/proto.go`): `Task {id, title, status, note?, updated_at}`, status
  constants and `TaskStatuses`, limits `MaxTasks` (50), `MaxTaskTitle` (200 runes), `MaxTaskNote` (500 runes);
  `TasksSetArgs`, `TaskUpdateArgs` (pointer fields: nil = unchanged), `TasksResult {name?, tasks, revision}`,
  `TaskCounts`, `TaskSummary`, `TaskChange`, `TaskChangedData`; ops `OpTasksSet`, `OpTaskUpdate`, `OpTasks`; event
  `EvTaskChanged` (`task.changed`); `SessionInfo.Tasks *TaskSummary` (omitempty).
- Daemon (`internal/daemon/tasks.go`): `taskList` on `entry` (guarded by `entry.mu`), `normalizeTasks` (validation,
  `screen.Sanitize`, auto ids `t1`, `t2`... first free among the list's ids), `tasksSet`, `taskUpdate`, `tasksOp`,
  `taskSummary`, `commitTasksLocked` (bumps the revision and publishes `task.changed` under `entry.mu`, so concurrent
  writes publish in apply order, like labels). Wired in `dispatch` (`handlers.go`), `info` (`SessionInfo.tasks`),
  `knownKinds` (`events.go`, so `kinds: ["task"]` is a valid filter) and `relayGate` (`tasks` in the any-scope read
  list with the session filter; `tasks_set` / `task_update` stay denied by default).
- Permissions: writes need a running session caller (human or relay device: `forbidden`; exited: `exited`). Read:
  humans any session; a session itself unless `[security] agent_view`. `task.changed` carries session = owner, so the
  bus's existing session-event visibility applies.
- Lifetime: the list lives on the registry entry, so it is readable after exit and gone with `rm`.
- Client (`internal/client/client.go`): `TasksSet`, `TaskUpdate`, `Tasks`.
- CLI (`cmd/agentd/task.go`): `agentd task set` (stdin: JSON array of tasks, or one title per line), `task add TITLE`
  (reads the list then `tasks_set` with the new task appended), `task start|done ID`, `task block ID [--note N]`,
  `task ls [NAME] [--json]` (NAME defaults to the calling session via `whoami`).
- MCP (`internal/mcpserver/mcpserver.go`): `agentd_tasks_set {tasks[]}`, `agentd_task_update {id, status?, note?}`;
  both call `whoami` first (`asSession`) and fail with `ErrNoIdentity` for humans.
- AGENTS.md: new "Task lists" key behaviour, Ops list, MCP tools, Event bus kinds + `task` prefix, relay gate list,
  `internal/daemon` contract row.

## Design choices beyond the contract

- Titles/notes longer than the limit are truncated by `screen.Sanitize` (not rejected), so a hook adapter mirroring a
  harness todo tool never loses the whole list over one long item; a title that is blank after sanitizing is
  `invalid`. Leading/trailing spaces are trimmed.
- Duplicate ids in `tasks_set` are `invalid`.
- `updated_at` is kept for tasks that did not change (same id, title, status, note); new or changed tasks get now.
- `changed[]` lists new and modified tasks (`from` only when the status changed); removed tasks are not listed (counts
  reflect them). A reorder-only `tasks_set` bumps the revision with an empty `changed`.
- The first `tasks_set`, even with an empty list, creates revision 1 (the agent reported an empty plan), so
  `SessionInfo.tasks` appears; before that it is nil.
- `agentd_task_update` requires `status` and/or `note` (title edits only through the op / `tasks_set`).

## Verification

- `gofmt -l internal cmd` clean, `go vet ./...` clean.
- `go test -race -count=1 ./internal/daemon/... ./internal/proto/... ./internal/client/... ./internal/mcpserver/...
  ./cmd/agentd/...`: all ok.
- New tests: `internal/daemon/tasks_test.go` (`TestTasksSetUpdateAndEvents`: auto ids, sanitizing, idempotency,
  events, ls summary, error codes, human forbidden, cross-session read refused, peer subscription isolation, readable
  after exit, dropped by rm; `TestTasksVisibility`: agent_view, empty list; `TestTasksOverRelay`: device read with
  globs, writes forbidden), `internal/mcpserver/tasks_test.go` (`TestTaskTools`) plus the no-identity and tool-list
  tests, `cmd/agentd/task_test.go` (stdin parsing, output, argument validation). Sessions run `cat`; no real agent CLI.
- The KB tools (`kb_add_document`, `kb_search_documents`) were not available in this environment, so this summary
  is stored in the repository instead of the knowledge base.
