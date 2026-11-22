# Broadcast prompt from the Agentes list (AGED-US-0050)

Builds on [[plans/agentd/desktop_plan]] and [[plans/agentd/workspace-ux_plan]]. Date: 2026-11-22.
Client only (`internal/desktopui/app/agents`): no daemon op, no wire change, no backend contract change.

## Behaviour

- The list header gets a "Seleccionar varios" button. In selection mode every row is a checkbox row
  (a click toggles it instead of opening the session); approvals and Terminar are hidden.
- Selectable = agentd session, not `exited`/`starting`, on a host whose device has `CapOperate`.
  zellij/tmux/shell rows and read-only hosts are shown disabled.
- Quick picks under the chips: `Todos`, one per host when the visible selectable sessions span more
  than one host, `Ninguno`. They act on what the filter chips and the search show.
- A composer under the list: text + `Enviar a N`. Enter sends, like the chat.
- Send = one act `prompt` per selected session, in parallel, each on its own connection
  (`Backend.Act`), `force` while the session is `working` (the chat's rule), no `revision` (the list
  holds no view for most sessions; the daemon's own guards still refuse a menu, a draft or a human
  typing).
- Result: sessions that took the prompt are unselected; a success toast counts them. Refusals stay
  selected and one error toast names them with the first cause. When all succeed the draft is cleared
  and selection mode ends.

## Phases

1. Selection state, rows, quick picks, composer and send (`broadcast.go`, hooks in `agents.go`).
2. Headless tests on the fake backend (`broadcast_test.go`): select + send, per-host pick, disabled
   rows, partial failure.
3. Docs: AGENTS.md "Desktop client", change note.

Out of scope: the Android app (agentd-app repository), a daemon-side broadcast op.
