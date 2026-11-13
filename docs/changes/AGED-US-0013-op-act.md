# AGED-US-0013: op `act` and `agentd act`

Plan: [[plans/api-gateway-plan]]. Design: [[design/api/daemon-ops]] §3, [[design/api/semantic-model]] §6.
Builds on [[AGED-US-0012-op-view]] (view state, `chooseKeys`) and [[AGED-US-0009-internal-inject]].

## What changed

- New op `act {name, action, text?, submit?, option_id?, label?, revision?, keys?[], force?}` ->
  `{revision, status, result, submitted?, warning?}`, actions `prompt`, `choose`, `keys`, `interrupt`, `cancel`.
- `prompt` through `inject.Input` (paste verified); `choose` re-detects and executes `screen.PlanChooseKeys`
  step by step with verification; `keys` maps abstract names to bytes (never raw bytes); interrupt/cancel use new
  profile keys `interrupt_keys` / `cancel_keys`.
- Permissions as `type`; audit line `act` in `daemon.log` (never the prompt text).
- CLI `agentd act NAME prompt|choose|keys|interrupt|cancel` (`--force`, `--no-submit`, `--label`, `--revision`, `--json`).

## Decisions

- `force` skips status (and recent-keystroke) checks only. A pending human draft is always `busy` for prompt,
  keys and choose; interrupt/cancel refuse it unless `force` (they are the "stop" actions, `esc` keeps a draft,
  ctrl-c may not).
- Stale `revision` in choose: option ids already hash question + kind + labels (= the menu fingerprint), so a found
  id means the same menu. To also catch "the menu went away and an identical-looking one came back" the view keeps
  a menu generation per revision (`menuTrack`); a stale revision with another generation, or unknown, is `conflict`.
  Limit: two identical dialogs with the same question seen by a client with a *current* revision cannot be told apart.
- Human marking: draft-neutral keys (escape sequences, Enter, Ctrl-C) are written as human input; printable keys
  are not (draft). The act's own marks are remembered (`actMarks`) so the next act is not `busy` because of them.
  prompt is not human input.
- Defaults: `interrupt_keys` = `["ctrl-c"]` (default, pando), `["esc"]` (claude, codex; TO CONFIRM in F8);
  `cancel_keys` = `["esc"]`.
- Home/End also use SS3 in application cursor mode (xterm behaviour); the key table is exactly the story's list.
- Errors: unknown action/key, bad sizes -> `invalid`; wrong status -> `not_ready`; draft/keystroke/lock -> `busy`;
  option gone/label/revision/step mismatch -> `conflict` (data: `{revision, view}`); screen did not react ->
  `timeout`; paste not visible -> `verify_failed`; exited -> `exited`; permissions -> `forbidden`.

## Files and symbols

- `internal/daemon/act.go`: `Server.actOp`, `actPrompt`, `actKeys`, `actChoose`, `awaitChange`, `satisfied`,
  `menuTrack` (hooked into `viewState.refreshLocked`, one field + one call in `view.go`), `humanRecent`, `actMarks`.
- `internal/daemon/keys.go`: key table, `resolveKeys`, `KeyNames`.
- `internal/daemon/handlers.go`: dispatch case `OpAct`.
- `internal/proto/act.go`: `OpAct`, `ActArgs`, `ActResult`, `ActConflict`, action constants.
- `internal/config/actkeys.go` (+ `config.go` wiring): `ActConfig`, `ActProfile`, `Profile.Act`.
- `internal/client/client.go`: `Client.Act`. `cmd/agentd/act.go`: `agentd act`.
- Tests: `internal/daemon/act_test.go`, `act_internal_test.go`, `internal/config/actkeys_test.go`, `cmd/agentd/main_test.go`.
- `AGENTS.md`: ops list, `act` permission, "Act" section.

## Verification

`gofmt -l .` and `go vet ./...` clean; `go test -race -count=1 ./internal/daemon ./internal/proto ./internal/client
./internal/config ./cmd/...` green. Daemon tests use bash and the fake agent (`-menu`, `-numbered`, `-digitmove`,
`-dialog`, `-appcursor`) only: prompt lands/submits, busy on draft, not_ready while working, choose by pointer / digit /
digit-then-Enter / dialog / SS3, conflicts (gone id, label, fresh view in data), two consecutive dialogs with the same
labels and another question (the second is never answered), keys table, interrupt/cancel, permissions, serialization
with the inject lock and concurrent prompts.
