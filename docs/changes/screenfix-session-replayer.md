# screenfix uses session.Replayer instead of copies of session helpers

Builds on [[plans/api-gateway-plan]] and [[AGED-US-0006-fixtures-fakeagent]].

## What changed
- New exported `session.Replayer` (`NewReplayer(cols, rows)`, `Feed([]byte)`, `Snapshot(screen.SnapOpts) *screen.Snapshot`,
  `Close()`): a standalone x/vt emulator fed raw PTY bytes through the same `stringFilter`, the same emulator callbacks
  and the same cell conversion as a live session. Deterministic: no clock is read (Taken, LastBell, LastNotify.At zero),
  State = running.
- `Session.Snapshot` now shares `captureScreen` / `rawScreen.convert` (snapshot.go) with the replayer; OSC 9/777
  registration is shared via `registerNotify`. No behaviour change for live sessions.
- `internal/testutil/screenfix` `Replayer` is a thin wrapper over `session.Replayer` (public API unchanged);
  `cells.go` and `filter.go` (diverged copies of `copyRow`, `attrBits`, `colorString`, `stringFilter`) are deleted.
- Close still closes the reply pipe directly (vt.Emulator.Close races with the drain goroutine under -race).
- Replay now matches live sessions on: Width:0 continuation cells, Title/Cwd sanitizing, OSC 7 decoding, OSC 9 sub-command
  filtering (numeric sub-commands, previously only `4;`).

## Files and symbols
- `internal/session/replayer.go`, `replayer_test.go` (new); `internal/session/snapshot.go` (captureScreen, rawScreen, registerNotify)
- `internal/testutil/screenfix/screenfix.go`; removed `cells.go`, `filter.go`; `AGENTS.md` session row

## Why
The screenfix copies had diverged from `internal/session` after the Width:0 / lock-scope / sanitize changes.

## Verification
`gofmt -l .`, `go vet`, `go test -race -count=1 ./internal/session ./internal/testutil/... ./internal/screen` pass; no golden changed.
