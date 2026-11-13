# AGED-US-0036: history sampling observes only complete frames

Plan: [[plans/api-gateway-plan]]. Builds on [[AGED-US-0007-transcript-history]] and
[[AGED-US-0012-op-view]]; design [[design/api/semantic-model]] section 3.5.

## What changed
With fast full-screen redraws the view watcher observed mid-repaint frames when `SettleMax` forced a
sample, leaking gap markers, duplicates and status/footer rows into `screen.History`.
- The session tracks DEC private mode 2026 (synchronized output): `InSyncUpdate()` (open BSU younger than
  `SyncTimeout` = 1 s, so a stuck BSU cannot freeze history) and `SyncFrames()` (completed BSU..ESU pairs).
  Tracking happens in `trackMode` (emulator callbacks, in order, under `outMu`), so a mode set and reset
  inside one PTY chunk, or a sequence split across chunks, is handled. `Snapshot` carries `SyncOpen`,
  `SyncSince`, `SyncFrames`.
- Watcher (`viewState.observeLocked`): never observes while `SyncOpen`; a child that uses 2026 is sampled on
  complete frames with the usual settle debounce. Without 2026, a sample before the output went quiet is
  forced (`History.ObserveForced`).
- `History`: `HistoryOpts.Transient` drops status rows (spinner_chars / busy_regex, built by `transientFn`)
  from every frame; a forced observation whose frame does not align with the stored tail is deferred (nothing
  stored, retried at the next settle) up to `MaxDeferrals` (3) consecutive times, then the gap is accepted.
  A quiet-settled `Observe` always records (a real `/clear` still yields a gap).

## Files and symbols
- `internal/screen/history.go`: `ObserveForced`, `Deferred`, `MaxDeferrals`, `HistoryOpts.Transient`.
- `internal/screen/screen.go`: `Snapshot.SyncOpen/SyncSince/SyncFrames`.
- `internal/session/session.go`, `snapshot.go`: `syncSince`, `syncFrames`, `trackMode` (2026), `InSyncUpdate`,
  `SyncFrames`, `SyncTimeout`.
- `internal/daemon/view.go`: `observeLocked`, `transientFn`, `wakeIn` (sync timeout deadline).
- `internal/testutil/fakeagent/redraw.go`: `-redrawnosync` (no BSU/ESU), `-redrawdelay D` (pause per repaint;
  also holds 500 ms at the end so the last frame settles).
- Tests: `internal/screen/history_test.go` (transient rows, forced deferral, bound, retry),
  `internal/session/sync_test.go` (chunk-split BSU, set+reset in one chunk, timeout),
  `internal/daemon/view_test.go` `TestViewRedrawCleanHistory` (sync and nosync; duplicate-free, gap-free,
  ordered, no transient rows).

## Why
Acceptance criteria of AGED-US-0036: never sample inside an open synchronized update, skip transient rows
in the fallback, and a clean transcript for `-alt -redraw`.

## Verification
`gofmt -l .`, `go vet ./...`, `go test -race -count=1` of `./internal/screen ./internal/session ./internal/daemon`
(see the story comment). The daemon test fails (gap, status rows) when the 2026 check, forced path and
transient filter are removed at 2 ms repaints.

## Known limits
Without 2026 and with repaints so fast that more rows scroll out of the viewport between two samples than it
holds, lines are physically lost (gap marker). `? for shortcuts` style footers need `footer_regex` /
`busy_regex` in the profile to be filtered when Split misclassifies a half-drawn frame.
