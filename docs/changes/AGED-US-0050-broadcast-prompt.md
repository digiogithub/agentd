# AGED-US-0050: select several agents and send one prompt to all (desktop)

Implements [[plans/agentd/broadcast-prompt_plan]]. Builds on [[plans/agentd/desktop_plan]] and
[[plans/agentd/workspace-ux_plan]]. Date: 2026-11-22. (KB unavailable: recorded here.)

## What

Selection mode in the Agentes list of agentd-desktop: `Seleccionar varios` in the header turns the rows into
checkboxes; quick picks select all the shown sessions, all of one host, or none; a composer under the list sends one
act `prompt` to every selected session at once. Client only: no daemon, wire or backend contract change.

- Selectable: agentd sessions that are not `exited`/`starting`, on a host whose device has `CapOperate`. zellij/tmux/
  shell rows and read-only hosts are shown disabled with the reason.
- Send: `Backend.Act` per session in parallel, `force` only while that session is `working`, no `revision`.
- Result: delivered sessions are unselected and counted in a success toast; refused ones stay selected and are named
  in one error toast (cause of the first refusal). All delivered = draft cleared, mode off.

## Files and symbols

- `internal/desktopui/app/agents/broadcast.go` (new): `broadcast`, `View.Selecting`, `selectable`, `toggleSelecting`,
  `targets`, `pickBar`, `pickRow`, `broadcastBar`, `sendAll`.
- `internal/desktopui/app/agents/agents.go`: `View.sel`; `Build` adds `broadcastBar`; `inbox` computes `shown` before
  the header, adds the header button and `pickBar`; `card` renders `pickRow` in selection mode.
- `internal/desktopui/app/agents/broadcast_test.go` (new).
- `AGENTS.md` "Desktop client" > Broadcast prompt.

## Why

Telling several agents the same thing (commit, report status, stop) meant opening each session in turn.

## Verified

`gofmt`, `go vet`, `go test ./internal/desktopui/...`: host pick + send (four acts, forced only for the working
session, mode ends), row toggles without opening a session, disabled rows, filter-aware `Todos`, read-only relay
host, partial failure keeps the refused session selected. The mode was rendered headless (`ui.Tester.Image`) and
checked by eye. Not exercised against a real daemon.
