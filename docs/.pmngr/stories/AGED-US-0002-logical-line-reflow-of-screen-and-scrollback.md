---
id: AGED-US-0002
type: story
title: Logical-line reflow of screen and scrollback
status: done
priority: high
parent: AGED-EP-0001
milestone: AGED-M-0001
labels: [screen, plan:api-s02]
estimate: 5
created: 2026-11-13T13:52:38Z
updated: 2026-11-13T14:33:38Z
started: 2026-11-13T14:06:36Z
closed: 2026-11-13T14:33:38Z
---

## Description
Join soft-wrapped rows into logical lines with the heuristic of semantic-model §3.3 (edge-filled rows, next-word-fit rule, indent and list markers), trim trailing blanks, handle wide characters, merge scrollback + screen into one text.

## Acceptance Criteria
- Pure functions over `Snapshot`, golden tests on synthetic grids at 80/120/160 cols give identical logical lines.
- Wide chars and NBSP handled; hard breaks preserved.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/semantic-model]] §3
