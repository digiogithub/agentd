---
id: AGED-US-0007
type: story
title: Transcript history for alternate-screen TUIs
status: done
priority: medium
parent: AGED-EP-0001
milestone: AGED-M-0001
labels: [screen, daemon, plan:api-s30]
estimate: 8
created: 2026-11-13T13:52:44Z
updated: 2026-11-13T14:33:45Z
started: 2026-11-13T14:18:46Z
closed: 2026-11-13T14:33:45Z
---

## Description
Per-session `History` that merges the transcript region on each settled frame (60 ms quiet, at least every 250 ms) by suffix/prefix alignment, keeps rows scrolled out of view and inserts gap markers when frames cannot be aligned; bounded memory.

## Acceptance Criteria
- Claude-like redraw fixture produces a growing, duplicate-free transcript.
- Memory bounded (config cap) and cost measured with many sessions (benchmark).
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/semantic-model]] §3.5
