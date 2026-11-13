---
id: AGED-US-0038
type: story
title: Load-robust timing tests (snapshot, redraw history)
status: done
priority: low
parent: AGED-EP-0001
milestone: AGED-M-0002
labels: [test]
estimate: 2
created: 2026-11-13T15:45:17Z
updated: 2026-11-13T16:25:18Z
started: 2026-11-13T15:45:44Z
closed: 2026-11-13T16:25:18Z
---

## Description
Under heavy CPU load (30 busy loops, concurrent package tests) `TestSnapshotConsistentUnderOutput` (session) timed out once and `TestViewRedrawCleanHistory` (daemon, sync2026/nosync) failed on line counts/gaps. Pass in normal runs. Make waits condition-based and scale timing with load, or document minimum resources for CI.

## Acceptance Criteria
- Both tests pass `-count=10` with `GOMAXPROCS=2` while another package's race tests run concurrently.

## Notes
- [[AGED-US-0037-flaky-ls-preview-test]], [[AGED-US-0036-history-sampling]]
