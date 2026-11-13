---
id: AGED-US-0006
type: story
title: Capture fixtures and fakeagent menu/dialog modes
status: done
priority: high
parent: AGED-EP-0001
milestone: AGED-M-0001
labels: [test, screen, plan:api-s06]
estimate: 5
created: 2026-11-13T13:52:42Z
updated: 2026-11-13T14:33:43Z
started: 2026-11-13T14:06:35Z
closed: 2026-11-13T14:33:43Z
---

## Description
Add fakeagent flags (`-menu`, `-numbered`, `-yesno`, `-dialog`, `-box`, `-appcursor`, `-bell`, `-osc9`, `-wrap`) and a fixture format (PTY-log replay into x/vt + golden JSON View) used by the screen tests.

## Acceptance Criteria
- Fixtures replay deterministically; golden update via a test flag.
- No real agent CLI is executed; values marked † in semantic-model are listed as to-confirm in F8.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/semantic-model]] §8
