# AGED-US-0004: status classifier and attention flag

Builds on [[plans/api-gateway-plan]] (API-S04) and [[design/api/semantic-model]] §5.

## What changed
- `screen.Classify` computes `exited > starting > waiting > working > idle` from a Snapshot, its
  Regions, detected options and a `StatusInput` (profile durations, regexes, prompt match).
- `screen.Attention` + pure `screen.Next` implement the daemon-owned attention flag (turn done,
  waiting, bell/notify) with clear rules. Nothing is wired into the daemon yet.
- New profile key `waiting_regex` (no built-in value; must not match the empty string).

## Files and symbols
- `internal/screen/status.go`: `Status`, `StatusInput`, `StatusResult`, `Classify`, `Attention`,
  `Next`, `Attention.Ack`, constants (`MinBoot`, `WaitingQuiet`, `EchoWindow`, `Reason*`).
- `internal/screen/status_test.go`: table tests (fixtures `fake-dialog`, `fake-menu`, `fake-yesno`,
  `fake-box-ghost`, `claude-post-turn-*`) and attention transition tables.
- `internal/config/status.go`: `StatusConfig`, `StatusProfile`, `Profile.Status.Waiting`;
  small hooks in `internal/config/config.go` (embed, default, validate, resolve, merge); test in
  `regions_test.go` (`TestWaitingRegex`).
- `internal/delivery` untouched: `Idle` / `InputState` behave as before.

## Rules
- exited: `State == exited`. starting: registry starting, no output yet, or younger than
  max(idle_ms, 3 s).
- waiting: options (optionally filtered by `OptionMinConfidence`, transcript-only lists ignored), a
  dialog owning the bottom of the screen, or `waiting_regex` on the screen; never while status rows
  or `busy_regex` on a footer row say busy; options/dialog need quiet >= min(idle_ms, 500 ms),
  `waiting_regex` needs idle_ms.
- working: output within idle_ms, or status/footer busy rows. Output within 300 ms after a human
  keystroke is echo and ignored; keystrokes only set `HumanActive`.
- idle: otherwise; prompt_regex set but not matching gives reason `no_prompt`, confidence 0.4.
- Attention: see `Next` doc. working->idle = turn_done; any ->waiting = waiting; counter increase
  of BEL/OSC 9/777 while idle/waiting = bell/notify, ignored while working/starting; entering
  working or a human keystroke after the flag clears it; explicit `Ack`.

## Verification
`gofmt -l .` clean, `go vet ./...` clean,
`go test -race -count=1 ./internal/screen ./internal/config ./internal/delivery` green.
