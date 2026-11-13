---
id: AGED-US-0009
type: story
title: Extract safe-input path into internal/inject
status: done
priority: critical
parent: AGED-EP-0002
milestone: AGED-M-0001
labels: [daemon, delivery, plan:api-s08]
estimate: 5
created: 2026-11-13T13:52:46Z
updated: 2026-11-13T14:15:45Z
started: 2026-11-13T13:57:06Z
closed: 2026-11-13T14:15:45Z
---

## Description
New package `internal/inject` with `Input(ctx, target, profile, Request{Text, Needle, Submit, Guard})` reproducing today's delivery injection exactly (draft guard, PasteSafe, bracketed paste, paste verification, submit delay, human re-checks) plus a per-session input lock; delivery uses it.

## Acceptance Criteria
- All existing delivery and e2e tests pass unchanged.
- Contract table in `AGENTS.md` updated (`delivery` depends on `inject`).
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/daemon-ops]] §4
- ADR-A13
