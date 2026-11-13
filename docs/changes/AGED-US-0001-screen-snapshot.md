# AGED-US-0001: Atomic screen snapshot for interpretation

Builds on [[plans/api-gateway-plan]] and [[design/api/semantic-model]] §2.

## What changed
- New package `internal/screen` (stdlib only): `Snapshot`, `Row`, `Cell`, `Attr`, `Pos`, `Notify`, `SnapOpts`.
- `session.Session.Snapshot(screen.SnapOpts)` copies screen cells, cursor, alt-screen flag, scrollback tail, title,
  cwd, DECCKM / bracketed-paste modes, activity timestamps, state/exit code and notification counters under
  `outMu.RLock`, so all belong to one emulator state.
- Output generation counter `OutputGen()` (atomic, +1 per PTY chunk after the emulator consumed it) and
  `Changed()` (broadcast channel closed and replaced after every chunk and on exit).
- BEL (`vt.Callbacks.Bell`), OSC 7 (`Callbacks.WorkingDirectory`), OSC 9 and OSC 777;notify
  (`Emulator.RegisterOscHandler`) are recorded with timestamps; OSC 9;4 (ConEmu progress) is ignored.
  `Session.Notifications()` returns the counters.

## Files and symbols
- `internal/screen/screen.go`, `screen_test.go`
- `internal/session/snapshot.go` (Snapshot, OutputGen, Changed, Notifications, copyRow, osc handlers),
  `snapshot_test.go`; `session.go` (new fields, callbacks, `handleOutput` bumps gen + signals, `wait` signals on exit)
- `AGENTS.md` package-contract table (screen row, session deps)

## Why
Interpretation layers (reflow, regions, status, options) need one consistent instant, a cheap "changed?" check
and notification signals (bell/OSC 9/777) for attention reasons.

## Design notes / deviations
- `SnapOpts.ScrollbackTail`: 0 = none, negative = `screen.DefaultScrollbackTail` (500); Go zero value must be safe.
- Added `LastBell` to Snapshot; `State` is only running/exited from the session (daemon owns "starting").
- Wide-char continuation cells are dropped; unwritten cells become `Text ""`, width 1; RapidBlink folds into Blink.
- Cwd: OSC 7 (file:// URL decoded) else `/proc/<pid>/cwd` while running else `Spec.Cwd`.

## Verification
`gofmt -l`, `go vet ./internal/screen ./internal/session`, `go test -race ./internal/screen ./internal/session` green
(includes concurrent-output race test, notifications, scrollback, Changed/OutputGen tests; shell/cat only).
Note: pre-existing `TestWriteTimesOutWhenChildStopsReading` is timing-flaky under `-count>1` (unrelated).
