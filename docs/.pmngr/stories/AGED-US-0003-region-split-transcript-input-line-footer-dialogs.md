---
id: AGED-US-0003
type: story
title: "Region split: transcript, input line, footer, dialogs"
status: done
priority: high
parent: AGED-EP-0001
milestone: AGED-M-0001
labels: [screen, plan:api-s03]
estimate: 5
created: 2026-11-13T13:52:39Z
updated: 2026-11-13T14:33:40Z
started: 2026-11-13T14:16:19Z
closed: 2026-11-13T14:33:40Z
---

## Description
Classify rows into transcript / input / footer / status line / dialog using `prompt_marker`, box-drawing rules, `input_bg_colors` and frames; reuse the existing ghost-text logic (shared, not duplicated).

## Acceptance Criteria
- Input line text excludes ghost text and reports it separately.
- Works with the claude/codex fixtures from `internal/testutil` and fakeagent screens.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/semantic-model]] §4
- [[ghost-text]]
