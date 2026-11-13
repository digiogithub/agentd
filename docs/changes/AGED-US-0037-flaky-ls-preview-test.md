# AGED-US-0037: flaky TestReviewLsStripsPreviewForSessions

Builds on [[ops-review-fixes]] and [[AGED-US-0036-history-sampling]].

## Root cause
A real race, not a test timing assumption. `session.handleOutput` stamped `lastOutput`
BEFORE writing the chunk to the emulator (log write and `outMu` wait in between). The
daemon view watcher (`viewState.refreshLocked` -> `observeLocked`) could run in that
window: it saw the new `LastOutput` but the old (empty) screen, observed an empty
frame and recorded `hist.LastObserved() = now` (> `LastOutput`). When the emulator
write finished, `ShouldObserve` required `LastOutput.After(lastObserved)`, which was
false, so the real frame was never added to the transcript history and `ls` kept an
empty `Preview` until further output. `sh -c "echo x; sleep 60"` produces no more
output, so the test's 20 s wait expired. CPU load widens the window.

## Fix
`internal/session/session.go` `handleOutput`: stamp `lastOutput` a second time right
after `outputGen.Add(1)` (screen now shows the chunk), so the timestamp is always newer
than any observation made against the pre-write screen. The first stamp stays (idle
detection sees output in progress). The test already waited on a condition, no change.

## Verification
- Not reproducible before the fix with GOMAXPROCS=2 -count=30 even with 30 busy loops
  (narrow window), so verified by reasoning from the code path.
- After: `GOMAXPROCS=2 go test -race -count=20 -run TestReviewLsStripsPreviewForSessions ./internal/daemon` ok;
  `go test -race -count=1 ./internal/daemon` ok (also while session/screen tests ran
  concurrently); `gofmt -l`, `go vet` clean.
- Notes: under extreme load (30 busy loops) `TestViewRedrawCleanHistory` failed on
  history line counts, and one concurrent full run saw `TestSnapshotConsistentUnderOutput`
  time out (passes alone); both unrelated to this change.
