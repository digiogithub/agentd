# AGED-US-0012: op `view` and `agentd view`

Plan: [[plans/api-gateway-plan]] (ADR-A02, A05, A16). Design: [[design/api/daemon-ops]] §2,
[[design/api/semantic-model]] §5 and appendix.

## What changed

- New daemon op `view {name, since_revision?, epoch?, lines_limit?, from_line?, scrollback?}` returning the
  semantic View (status, attention, reflowed transcript lines with `first_line`, input, options, hints,
  dialog question, title, cwd, idle_ms, exit_code) or `{not_modified:true, epoch, revision}`.
- New op `view_ack {name}` clearing the attention flag (humans only).
- `ls` fills `status`, `attention`, `revision`, `preview` (last non-blank transcript line, sanitized, <=160).
- Per-session view state with a watcher goroutine: History observed on settled output
  (`screen.ShouldObserve`), timers so `working -> idle` without output is noticed, attention through
  `screen.Next`, `revision` bumped only when the published fingerprint changes.
- Identity: view is read-only; humans any session, sessions only themselves unless
  `[security] agent_view = true` (new key, default false).
- CLI `agentd view [NAME] [--json] [--since N] [-n LINES] [--scrollback]`.

## Files and symbols

- `internal/daemon/view.go`: `viewState`, `refreshLocked`, `watch`, `wakeIn`, `fingerprint`,
  `Server.viewOp`, `Server.viewAck`.
- `internal/daemon/view_rules.go`: `screenRules(config.Profile) screen.Rules`, `chooseKeys` (menu_submit
  resolves to the profile submit; for the future `act`), `statusInput`.
- Hooks: `internal/daemon/server.go` (`entry.view`, shutdown), `handlers.go` (`run`, `rm`, `dispatch`, `info`).
- `internal/proto/proto.go`: `OpView`, `OpViewAck`, `ViewArgs`, `ViewNotModified`, `ViewAckResult`, `View`,
  `ViewLine`, `ViewInput`, `ViewOption`, `ViewHint`, `ViewDialog`, `ViewAttention`.
- `internal/client/client.go`: `Client.View`, `Client.ViewAck`.
- `internal/config/config.go`: `Security.AgentView`.
- `cmd/agentd/main.go`: `viewCmd`, `viewText`. `AGENTS.md`: ops, identity, "Semantic view" section.
- Tests: `internal/daemon/view_test.go`, `export_test.go` (`ViewDoneForTest`), `internal/proto/proto_test.go`,
  `cmd/agentd/main_test.go`.

## Why

The API gateway (and the CLI) need one daemon-owned interpretation of a session's screen with a cheap
change check (`since_revision`) and stable line paging (`first_line`), instead of re-parsing `dump`.

## Verification

`gofmt -l .` clean; `go vet ./...` clean; `go test -race -count=1 ./internal/daemon ./internal/proto
./internal/client ./cmd/...` green. Daemon tests (sh, fakeagent `-menu`, `-box -ghost`, `-alt -redraw`):
starting/working/idle transitions, attention `turn_done` cleared by ack, waiting + options for a menu (no key
fields on the wire), ghost/suggestion input and draft, revision stability without output, not_modified (and
epoch mismatch), `first_line` paging, ls summary, permissions, watcher lifecycle (exit, kill+rm, shutdown),
race test with concurrent output, view and ls calls.

## Known limits

- Sampling the transcript during continuous repaints (`SettleMax`) can capture a mid-repaint frame (status
  row leaking into history, gap lines); a screen-package matter, visible with the fast `-redraw` script.
- `ls` uses the cached view (at most one debounce interval stale); `view` always refreshes.
