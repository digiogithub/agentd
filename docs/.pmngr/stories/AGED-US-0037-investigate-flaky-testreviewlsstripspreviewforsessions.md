---
id: AGED-US-0037
type: story
title: Investigate flaky TestReviewLsStripsPreviewForSessions under load
status: done
priority: medium
parent: AGED-EP-0002
milestone: AGED-M-0001
labels: [test, bug]
estimate: 1
created: 2026-11-13T15:31:05Z
updated: 2026-11-13T15:45:16Z
started: 2026-11-13T15:34:45Z
closed: 2026-11-13T15:45:16Z
---

## Description
Timed out at 20 s once during a full `go test -race ./internal/daemon` run (while other packages built/tested concurrently); passed 5 isolated reruns. Find the cause (boot minimum 3 s + status waits under CPU load?) and make it deterministic.

## Acceptance Criteria
- Root cause documented; test passes `-count=20` under `-race` with `GOMAXPROCS=2`.

## Notes
- [[ops-review-fixes]], [[AGED-US-0036-history-sampling]]
