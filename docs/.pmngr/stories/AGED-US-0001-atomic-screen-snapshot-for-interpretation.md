---
id: AGED-US-0001
type: story
title: Atomic screen snapshot for interpretation
status: done
priority: critical
parent: AGED-EP-0001
milestone: AGED-M-0001
labels: [daemon, screen, plan:api-s01]
estimate: 3
created: 2026-11-13T13:52:36Z
updated: 2026-11-13T14:15:43Z
started: 2026-11-13T13:57:05Z
closed: 2026-11-13T14:15:43Z
---

## Description
Add `internal/screen` with the `Snapshot` type (cells with content/fg/bg/attrs/width, cursor, size, alt-screen flag, title, scrollback rows) and `session.Snapshot()` taken under the output lock, plus an output generation counter and BEL / OSC 7 / OSC 9 / OSC 777 hooks.

## Acceptance Criteria
- `internal/screen` depends only on stdlib + `termcolor`; `session` imports it (contract row added to `AGENTS.md`).
- `Snapshot()` is consistent under concurrent output (race test).
- Output counter increments per processed chunk; BEL and OSC 9/777 are recorded with timestamps.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/semantic-model]] §2
- x/vt v0.1.0 exposes no soft-wrap flag (§3).
