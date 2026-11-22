# AGED-US-0045: Panel area in agentd-desktop

Part of [[plans/agentd/dashboard_plan]] (decision D1, wave 3). Builds on [[changes/AGED-US-0041-board]] (op `board`),
[[changes/AGED-US-0039-task-list]] (task lists) and [[changes/AGED-US-0040-questions]] (questions as mailbox messages).
Date: 2026-11-22.

## What

- New desktop area **Panel** (`/panel`, first sidebar entry, badge = open questions + `waiting` agentd sessions + blocked
  tasks of every host). One screen with what each agent of every host is doing, what it has pending and what needs the
  human, answered from there without opening the session.
  - **Por agente** (default): tiles (esperan tu respuesta, tareas en curso, pendientes, hechas hoy), "Necesita tu atención"
    (open questions with option buttons and a free-text field with a Buzón/Prompt toggle; terminal menus of `waiting`
    sessions with their options; blocked tasks with Responder and Abrir agente) and one card per agent (current task,
    progress n/total, next pending, footer; agents that report no tasks fall back to status + preview, "Sin tareas
    informadas"). A click on a card opens the session.
  - **Tablero**: columns Pendiente / En curso / Bloqueada / Hecha hoy from every task list ("Hecha hoy" = done with
    `updated_at` on today's local date), 8 cards per column then "Ver N más".
  - Toggle saved in `settings.json` (`panel_view`), also `/panel?view=tablero|agente`; host filter "Todos los hosts".
  - Relay devices follow `Host.Allows`: no mailbox = no reply/options/Responder; no operate = no Prompt/menu choices.
  - The human never adds or edits tasks.
- Backend contract: `backend.HostBoard`, `Backend.WatchBoard(ctx) <-chan []HostBoard`, `Backend.Reply(ctx, hostID, id, body)`.
  - `core`: per host monitor goroutine `runBoard`; the existing subscribe stream kicks it on `task.*`, `mail.*`,
    `status.*`, `attention.*`, `session.*` and `resync` events and on (re)connect; debounced 300 ms
    (`Options.BoardDebounce`), `board {since_digest}` (not_modified keeps the last board), only while `WatchBoard` has a
    consumer. A daemon without op `board` (invalid/forbidden) is marked unsupported until the stream reconnects
    (`HostBoard.Err`, the UI falls back to the session list). `Reply` = op `reply`, then a board refresh.
  - `fake`: task lists and one open question matching the mockups (`board.go`), `WatchBoard`, `Reply` (closes the
    question), test helpers `Replies`, `SetTasks`, `Tasks`, `Ask`.
- `app`: `Live.Boards` / `BoardsLoaded` / `OnBoards` (fed by `WatchBoard`), `Area.Badge` (sidebar count), settings
  `NotifyQuestion`, `NotifyBlocked` (default on) and `PanelView`; Notifier notifies a new open question ("<session>
  pregunta", body = question) and a task newly blocked ("<session> · tarea bloqueada", body = title · note), with mutes,
  the focused-window rule and the settings; a host's first board is only the baseline; a click opens the Panel.
- Ajustes: toggles "Preguntas" and "Tareas bloqueadas".

## Files and symbols

- `internal/desktopui/backend/backend.go`: `HostBoard`, `Backend.Reply`, `Backend.WatchBoard`.
- `internal/desktopui/backend/fake/board.go` (new): `seedBoard`, `boardSnapshot`, `WatchBoard`, `Reply`, `ReplyCall`,
  `QuestionID`, `Replies`, `SetTasks`, `Tasks`, `Ask`; `fake.go`: `sessRec.tasks/questions`, `boardSubs`, `publishLocked`.
- `internal/desktopui/core/board.go` (new): `boardState`, `boardEvent`, `WatchBoard`, `publishBoard`, `hostMon.runBoard`,
  `refreshBoard`, `Reply`; `core.go`: `Options.BoardDebounce`, `boardB`, `boardWatchers`, `RemoveHost`; `monitor.go`:
  `hostMon.boardK/board`, kicks in `streamOnce`, reset in `setDown`.
- `internal/desktopui/app/live.go` (`Boards`, `OnBoards`, `Board`), `registry.go` (`Area.Badge`), `layout.go` (badge),
  `prefs.go` (`NotifyQuestion`, `NotifyBlocked`, `PanelView`, `PanelByAgent`, `PanelBoard`), `notify.go` (`onBoards`,
  `sendPanel`, `PanelPath`, `BoardSessionKey`).
- `internal/desktopui/app/panel/` (new): `model.go` (`Agent`, `Item`, `Model`, `Agents`, `Collect`, `AttentionCount`,
  `CountTasks`, `SameDay`, `Progress`, `Board`, `Column`, `Card`), `panel.go` (`View`, area registration).
- `internal/desktopui/app/settings/settings.go` (toggles), `internal/desktopui/kit/icons.go` (`IconPanel` =
  lucide `list-checks`, already embedded), `internal/desktopui/desktopui.go` (blank import).
- Tests: `core/board_test.go` (+ `fakedaemon_test.go` serves `board`/`reply`), `backend/fake/board_test.go`,
  `app/panel/panel_test.go`, `app/app_test.go` (`TestNotifierBoards`), `app/settings/settings_test.go`.
- Docs: `AGENTS.md` (contract rows backend, fake, core, app, app/<area>; Desktop client > Panel),
  `internal/desktopui/README.md`.

## Why

The dashboard epic (AGED-EP-0007) needs the desktop screen where the human sees every agent's plan and answers what
blocks them (D1: both views behind a persisted toggle; D2: questions are mailbox messages answered with `reply`; D4: only
agents write tasks).

## Choices beyond the brief

- Agent cards are agentd sessions only (zellij/tmux have no task protocol); exited sessions without tasks or questions
  are hidden.
- Option buttons of a question always answer through the mailbox (`reply` with the label, which closes the question);
  the Buzón/Prompt toggle applies to the free text. A Prompt answer is prefixed "Respuesta a tu pregunta #<id>
  (<question>): " so the agent can tie it to its question; the mailbox question stays open (only a reply closes it).
- "Esperan tu respuesta" (tile) and the badge are the same number: questions + waiting sessions + blocked tasks.
- Blocked-task Responder sends `subject: "Tarea «<title>»"` (no `task` field: that field is for a session's own questions).
- `/panel?view=tablero|agente` sets the view (deep link; also used for the capture dev aid).

## Verification

- `gofmt -l internal cmd` (empty), `go vet ./...` (clean).
- `go test -race ./internal/desktopui/... ./cmd/agentd-desktop/...`: all ok.
- `go list -deps ./cmd/agentd | grep -c mygo` = 0.
- Screenshots of the fake Panel (both views) captured under Xvfb with `AGENTD_DESKTOP_FAKE=1` and
  `AGENTD_DESKTOP_CAPTURE_PATHS=/panel,/panel?view=tablero`.
- KB tools (`kb_add_document`) were not available in this session: this file is the change record.
