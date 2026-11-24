# Backlog notes, Panel agent detail, relay device revoke (plan)

Started 2026-11-24. Continues [[dashboard_plan]] (task lists, board, Panel) and [[relay_plan]] (devices).
Mobile side lives in the `agentd-app` repository and is done by that project's agent against this contract.

## Goals

1. **Panel agent detail (desktop)**: a click on an agent card in the Panel opens a detail view of that agent's
   work (tasks in progress, blocked, pending, done, cancelled; open questions), as the mobile app does, instead of
   jumping to the chat. The detail has an "Abrir agente" button for the chat.
2. **Backlog notes**: the human writes down notes / future tasks on a host. They are persisted by the daemon in
   SQLite and can later be assigned to a running session, where they become a pending or in-progress task.
   Desktop and mobile.
3. **Revoke device access** from the settings of the desktop and mobile apps.
4. **Relay invite diagnosis**: an invite could be created while the host link to the relay was down (join token
   refused), so the pasted link never connected. Make that visible.
5. **QR scan in the mobile app** (agentd-app only).

## Contract (normative)

Wire types: `internal/proto/note.go`, `internal/proto/task.go` (`Task.Origin`).

### Store

New package `internal/backlog` (depends on sqlite and proto): file `$STATE/backlog.db` (WAL,
0600), table `notes(id INTEGER PRIMARY KEY AUTOINCREMENT, title, body, state CHECK IN (open, assigned, done),
assigned_to, task_id, created_at, updated_at, assigned_at, done_at)`. Ids are never reused. At most
`proto.MaxNotes` (1000) notes (`note_add` beyond it is `invalid`).

### Ops

Callers: humans on the socket (local, SSH). A session is `forbidden` on every backlog op. Relay devices: owner
devices with the `operate` scope that are not limited to some sessions (`relayGate`); `note_assign` to a session
the device cannot see is `not_found`.

- `notes {states?[], since_digest?}` → `{notes[], digest, not_modified?}`. Order: open (newest first), assigned
  (most recently assigned first), done (most recently done first). `digest` = `d` + 16 hex of SHA-256 over
  `(id, state, updated_at)`.
- `note_add {title, body?}` → `Note` (state `open`). Title 1..200 runes, body 0..4000 runes, both through
  `screen.Sanitize` (the body keeps `\n`); a blank title is `invalid`.
- `note_update {id, title?, body?, state?}` → `Note`. `state` may only be `open` (takes an assigned or done note
  back; the session's task is not touched). Unknown id `not_found`.
- `note_rm {id}` → the removed `Note`. The session's task, if any, stays.
- `note_assign {id, session, status?, notify?}` → `{note, task, message_id?, warning?}`:
  - `session` must be a running session (`not_found` / `exited` / `starting`); `status` is `pending` (default) or
    `in_progress` (`invalid` otherwise); the note must be `open` or `assigned` (re-assigning moves it; the task in
    the previous session is left alone).
  - A task `{id: "n<note id>", title: note title, note: first 500 runes of the body, status, origin: "backlog"}`
    is appended to the session's task list (replaced in place if that id exists); a full list (50) is `invalid`.
    `task.changed` is published as for any task write.
  - The note becomes `assigned` (`assigned_to`, `task_id`, `assigned_at`).
  - Unless `notify` is false, a mailbox message from the caller to the session is stored `queued` (subject
    `Backlog task n<id>: <title>`, body = title + body + one line telling the agent the task is already in its
    task list as `n<id>` and to keep that id when it rewrites the list). The delivery loop injects it like any
    message. A mailbox failure does not undo the assignment: `warning` says so.
- **Backlog tasks survive `tasks_set`**: a task with `origin == "backlog"` and status `pending`, `in_progress` or
  `blocked` that the new list leaves out is kept (appended after the agent's tasks, counting towards the limit of
  50). A task in the new list whose id matches keeps `origin`. The agent drops one by setting it `done` or
  `cancelled` (then a later `tasks_set` without it removes it). `Origin` on input is ignored.
- **Completion**: when a task with origin `backlog` becomes `done`, the note whose `task_id` and `assigned_to`
  match becomes `done` (`done_at`). `rm` of the session does not change the note.
- Event `note.changed {id, state?, removed?}`: no `session`, so only humans (and owner, non-limited relay devices)
  see it. Kind prefix `note`.

### CLI

`agentd note add TITLE [--body B | --body -]`, `note ls [--all] [--json]` (default: open + assigned),
`note edit ID [--title T] [--body B] [--reopen]`, `note rm ID`,
`note assign ID SESSION [--start] [--no-notify]`. No MCP tools (agents do not manage the backlog).

### Relay devices (op `relay`)

Unchanged for humans on the socket. New, for relay devices (`relayGate` + `relayOp`):

- `relay {action: "devices"}`: owner device with `admin`, not session-limited → every device (no invites).
  Any other device → only its own entry.
- `relay {action: "revoke", device}`: any device may revoke **itself** (id or the empty string); an owner `admin`
  device that is not session-limited may revoke any device. Anything else `forbidden`.
- Every other action stays `forbidden` for devices.

### Relay link visibility

- The daemon logs once, at error level, when `[relay] join_token_env` names a variable that is not set in its
  environment and no plain `join_token` exists (typical under systemd `--user`).
- `agentd relay invite` asks `relay status` first; when the state is not `online` it prints a warning on stderr
  with the state and the last error ("devices cannot connect until the host is linked") and still creates the
  invite.

## Desktop

- `backend.Backend`: `NoteAdd`, `NoteUpdate`, `NoteRemove`, `NoteAssign`, `RelayDevices`, `RevokeRelayDevice`;
  `HostBoard.Notes` / `NotesErr`; capabilities `CapBacklog`, `CapDevices`.
- `core`: `notes` fetched with the board (and on `note.*` events); a daemon without the op = `NotesErr`.
- Panel: route `/panel/agent/<host>/<session>` (detail), section "Backlog" (list, add, edit, delete, assign to a
  session of the same host as pending or in progress).
- Ajustes: section "Dispositivos" per agentd host: list (name, platform, scope, last seen, online), "Revocar"
  behind a confirm dialog.

## Phases

| # | Phase | Test |
|---|---|---|
| P1 | proto + backend contract, stubs | `go build ./...` |
| P2 | daemon: `internal/backlog`, ops, tasks_set rule, events, relay gate, CLI, client | `go test -race ./internal/backlog ./internal/daemon ./cmd/agentd ./internal/client` |
| P3 | relay: devices/revoke for devices, invite warning, join token log | `go test -race ./internal/daemon ./cmd/agentd` |
| P4 | desktop: core + fake notes, Panel detail + backlog | `go test ./internal/desktopui/...` |
| P5 | desktop: Ajustes devices | `go test ./internal/desktopui/...` |
| P6 | mobile (agentd-app agent): panel backlog, QR scan, revoke, invite errors | flutter tests in that repo |
| P7 | docs: AGENTS.md, README, change notes | — |

## Status

- 2026-11-24: P1 to P5 and P7 done (see `changes/backlog-notes-panel-detail-revoke.md`). `internal/backlog` also
  imports `internal/proto` (returns `proto.Note`). P6 (mobile) delegated to the agentd-app agent, pending.
