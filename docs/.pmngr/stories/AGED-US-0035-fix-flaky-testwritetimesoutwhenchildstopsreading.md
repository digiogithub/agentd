---
id: AGED-US-0035
type: story
title: Fix flaky TestWriteTimesOutWhenChildStopsReading
status: done
priority: medium
parent: AGED-EP-0002
milestone: AGED-M-0001
labels: [test, bug]
estimate: 1
created: 2026-11-13T14:02:03Z
updated: 2026-11-13T14:15:50Z
started: 2026-11-13T14:02:03Z
closed: 2026-11-13T14:15:50Z
---

## Description
The second write in `internal/session/lifecycle_test.go` was 1 byte; after the first write timed out the tty sometimes had room for it, so it succeeded (`second write err = <nil>`). Reproduced on a clean HEAD with `-count=8` (pre-existing, not caused by AGED-US-0001).

## Fix
Second write is 1 MiB and timed like the first one.

## Verification
`go test -race -count=15 -run TestWriteTimesOutWhenChildStopsReading ./internal/session` green.
