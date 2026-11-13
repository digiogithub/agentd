# AGED-US-0038: load-robust timing tests (snapshot, redraw history)

Builds on [[AGED-US-0037-flaky-ls-preview-test]] and [[AGED-US-0036-history-sampling]].

## TestSnapshotConsistentUnderOutput (internal/session): test assumption
Root cause: the child script spawned ~9 command substitutions (forks) per generation
x 400 generations, and `waitFor` had a fixed 5 s budget; under CPU load the forks alone
exceeded it ("timeout waiting for output"). Not a snapshot bug: the torn-snapshot check
never fired. (The old script also dropped every `\n` through `$(...)`, so all rows landed
on one screen row; the new one really draws 8 rows.)
Fix (`internal/session/snapshot_test.go`): one `printf` per generation with the format
repeated per argument (2 forks instead of 9; the test now takes ~0.2 s) and `waitFor` scales
its timeout with `testutil.Scale`. The assertions (monotonic OutputSeq, <= 2 generations per
snapshot) are unchanged.

## TestViewRedrawCleanHistory (internal/daemon)
### sync2026: real bug, fixed in code
Under load the history lost lines (gap markers, "37 content lines, want 40"). Debug traces
showed `observeLocked` skipped almost every refresh because `snap.SyncOpen` was true:
when the PTY reader lags (load, race detector) `ptmx.Read` returns chunks cut at arbitrary
points, nearly always inside a synchronized-update frame, so every Snapshot saw an open frame
and no complete frame was sampled for 0.7 s, longer than the viewport takes to scroll away.
Fixes:
- `internal/session/syncgate.go` (+ `readLoop`/`feed`/`flushHeld` in `session.go`): frame-aligned
  chunking. The trailing open frame (BSU without ESU) is held back and processed with the
  rest of the frame, so chunks end on frame boundaries; a frame that never completes is
  released after 100 ms (or 256 KiB), so `SyncOpen`/`SyncTimeout` semantics are unchanged.
- `internal/screen/history.go` `SyncSettleMax` (50 ms) + `ShouldObserveSync`, and
  `internal/daemon/view.go` `viewSyncDebounce` (40 ms): children that use 2026 deliver
  complete frames, so the history is sampled more often (was: every ~250-350 ms, only a few
  turns of margin even on an idle machine).
### nosync: test assumption (documented lossy path)
Without 2026 a forced sample can land on a half drawn frame (stored as junk, the real frame
then becomes a gap) or miss the last frames; that is the design (see AGED-US-0036). The case
now asserts what must always hold (no transient rows, script lines in script order, at
least half of the script captured) and keeps the exact transcript check for sync2026 only.
Common: the child redraw delay and the wait timeout are scaled by `testutil.Scale`.

## testutil.Slow / testutil.Scale (internal/testutil/testutil.go)
`Slow()` = max(env `AGENTD_TEST_SLOW`, 1-minute load average per CPU from /proc/loadavg), clamped
to [1, 8], computed once. `Scale(d)` multiplies a duration. Only timeouts and child pacing
are scaled, never assertions. (A scheduler-latency probe was tried first and read 1.0 under
32 busy loops, so it was dropped.)

## Evidence
- Reproduction: 8-16 busy loops, `GOMAXPROCS=2 go test -race -count=10 -run TestViewRedrawCleanHistory ./internal/daemon`
  failed 10/10 (sync2026 gaps) before; sync2026 failed ~10-20% even after only the sampling
  changes; with frame-aligned chunking 0 failures in 50+ sync2026 runs.
- After (32 busy loops on 24 CPUs): `TestViewRedrawCleanHistory` -count=10 x3 all pass;
  `TestSnapshotConsistentUnderOutput` -count=10 passes.
- `go test -race -count=1 ./internal/session ./internal/screen ./internal/daemon`, `gofmt -l`,
  `go vet` clean.
