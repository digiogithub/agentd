---
id: AGED-US-0004
type: story
title: Status classifier and attention flag
status: done
priority: high
parent: AGED-EP-0001
milestone: AGED-M-0001
labels: [screen, daemon, plan:api-s04]
estimate: 5
created: 2026-11-13T13:52:40Z
updated: 2026-11-13T14:33:41Z
started: 2026-11-13T14:18:43Z
closed: 2026-11-13T14:33:41Z
---

## Description
Compute `exited > starting > waiting > working > idle` from the existing idle machinery, `prompt_regex`, `busy_regex`, `waiting_regex`, detected options and BEL/OSC 9 signals; daemon-owned `attention` flag (turn done, waiting, bell) with clear rules.

## Acceptance Criteria
- `delivery.Idle` behaviour unchanged (its tests pass untouched).
- Table tests for each transition; human typing never counts as working.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/semantic-model]] §5
