# Backlog notes, Panel agent detail, device revoke, relay invite warning (2026-11-24)

Continues [[backlog-notes_plan]], [[dashboard_plan]] and [[relay_plan]].

## What changed

- **Backlog notes**: new SQLite store `internal/backlog` (`$STATE/backlog.db`), daemon ops `notes`, `note_add`,
  `note_update`, `note_rm`, `note_assign`, event `note.changed`, CLI `agentd note ...`, client methods. An assigned note
  becomes task `n<id>` (`Task.Origin = "backlog"`) of a running session and the agent is told through the mailbox;
  `tasks_set` keeps open backlog tasks; a done task closes its note.
- **Desktop Panel**: agent detail page `/panel/agent/<host>/<session>` (card click no longer jumps to the chat) and a
  Backlog section (add, edit, delete, assign, return to backlog).
- **Desktop Ajustes > Dispositivos**: list and revoke the relay devices of each host.
- **Relay**: paired devices may call `relay devices` / `relay revoke` (self always; others only for an owner admin
  device); `agentd relay invite` warns when the host link is not online; the daemon logs a missing `join_token_env`
  variable.

## Files and symbols

- `internal/proto/note.go`, `internal/proto/task.go` (`Task.Origin`)
- `internal/backlog/backlog.go`; `internal/config/paths.go` (`BacklogPath`)
- `internal/daemon/notes.go`, `tasks.go` (`tasksSetLocked`, `taskUpdateLocked`, `keepsBacklog`, `backlogDone`),
  `events.go`, `handlers.go`, `server.go`, `main.go`, `relaygate.go`, `relay.go` (`relayDeviceOp`, `relayListDevices`,
  `relayRevoke`, `relayDeviceAdmin`)
- `internal/client/client.go` (`Notes`, `NoteAdd`, `NoteUpdate`, `NoteRm`, `NoteAssign`)
- `cmd/agentd/note.go`, `cmd/agentd/relay.go` (`runRelayInvite` warning)
- `internal/desktopui/backend/backend.go` (`CapBacklog`, `CapDevices`, `HostBoard.Notes`, new `Backend` methods),
  `backend/fake/{notes,devices}.go`, `core/{notes,devices,board}.go`, `app/panel/{detail,backlog,model,panel}.go`,
  `app/settings/{devices,settings}.go`

## Why

- The desktop Panel lacked the per-agent work detail the mobile app has, and there was no place to park future work and
  hand it to an agent later.
- Device access could only be revoked from the host's terminal.
- Diagnosis of "the invite link does not connect": the daemon ran under systemd without the variable named by
  `[relay] join_token_env`, so the relay refused the host (`join token refused`) and the route was never online; invites
  were printed anyway and nothing said the link was down.

## Verification

- `go test -race` on `internal/backlog`, `internal/daemon`, `internal/client`, `internal/proto`, `internal/config`,
  `cmd/agentd`; `go test ./internal/desktopui/...`; gofmt and go vet clean.
- Manual: isolated daemon (temp socket and state): `note add`, `note assign --start`, `task ls` shows `n1 in_progress`,
  `note ls` shows `assigned -> session`.
- Headless captures of the Panel, the agent detail and Ajustes looked at.
- Not verified: the mobile app (separate repository) and a real relay pairing on this machine (needs the join token in
  the systemd unit and a daemon restart).
