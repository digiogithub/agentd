# AGED-US-0007: transcript history for alternate-screen TUIs

Plan: [[plans/api-gateway-plan]]. Design: [[design/api/semantic-model]] §3.5.

## What changed
`screen.History` accumulates the transcript region of each settled frame of a TUI that repaints a
viewport (alternate screen, no scrollback) into an ordered, duplicate-free, bounded list of lines
with absolute indices (`first_line`, ADR-A16). A fakeagent mode and a fixture exercise it.

## Files and symbols
- `internal/screen/history.go`: `History`, `NewHistory`, `HistoryOpts{MaxLines,MaxBytes,TailSlack,Normalize}`,
  `Observe(reg, gen, now) int`, `Lines(from, limit) ([]Line, first)`, `Len`, `First`, `Gaps`, `Epoch`,
  `LastObserved`, `Reset`, `NormalizeLine`, `ShouldObserve`, constants `DefaultHistoryMaxLines` (5000),
  `DefaultHistoryMaxBytes` (1 MiB), `DefaultTailSlack` (3), `SettleQuiet` (60 ms), `SettleMax` (250 ms).
- `internal/screen/history_test.go`: unit tests, fixture test, `BenchmarkHistoryObserve2000`.
- `internal/testutil/fakeagent/redraw.go` (+ hook in `main.go`): `-redraw -redrawturns N -redrawlines N
  -redrawwords N`; each repaint is wrapped in ESC[?2026h/l so tests can split frames.
- `internal/testutil/screenfix/record/main.go`: recipe `fake-redraw-claude`; fixture
  `internal/screen/testdata/fake-redraw-claude.{log,json,golden.json}` (60x14, 5 turns).

## Merge algorithm
Per frame: keep `RoleTranscript` lines, trim leading/trailing blanks, compute normalised keys
(`NormalizeLine`: leading spinner glyph dropped, parenthesised groups with digits become `(#)`, whitespace
collapsed). Find the largest overlap where a suffix of the stored lines equals a prefix of the frame
(blank lines are not anchors); the newest `TailSlack` stored lines may differ (streaming tail), provided at
least one non-blank line matches and mismatches do not outnumber matches. Lines in the tail region are
refreshed with the newest text, the rest of the frame is appended. A frame lying entirely inside the stored
tail (a dialog hid the lower rows) adds nothing. No alignment: append a `RoleGap` line and the frame, `Gaps++`.
An unchanged non-zero `gen` is a no-op. Trim drops the oldest lines when a cap is exceeded by 1/8 and
keeps the absolute index monotonic (`Lines` clamps `from` to `First`).

## Limits
Lines that scroll through between two observations are lost (gap marker); identical repeated lines can
misalign; collapsed/rewritten earlier output may leave both versions; in memory only.

## Verification
`gofmt -l .` clean, `go vet ./...` clean, `go test -race -count=1 ./internal/screen ./internal/testutil/...`
green. Benchmark (2000-line history, 50-line frame, 24 threads): redraw ~54 us, scroll by 1 ~49 us, gap
~67 us per Observe, ~600 allocs (mostly frame construction in the benchmark itself).
