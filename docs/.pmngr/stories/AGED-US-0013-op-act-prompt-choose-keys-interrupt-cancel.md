---
id: AGED-US-0013
type: story
title: "Op act: prompt, choose, keys, interrupt, cancel"
status: done
priority: critical
parent: AGED-EP-0002
milestone: AGED-M-0001
labels: [daemon, cli, plan:api-s10]
estimate: 8
created: 2026-11-13T13:52:51Z
updated: 2026-11-13T15:18:40Z
started: 2026-11-13T14:45:20Z
closed: 2026-11-13T15:18:40Z
---

## Description
Op `act` with the abstract key table, preconditions (`busy` on pending human draft or recent keystroke, `not_ready` on wrong status, `force` override), `choose` re-detection with revision/label/fingerprint check and step-by-step verification, permissions as `type`. CLI `agentd act`.

## Acceptance Criteria
- `choose` on a changed menu returns `conflict` with the fresh view.
- Prompt goes through `internal/inject` (paste verified).
- Session callers limited like `type` / `[security] agent_type`.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/daemon-ops]] §3
