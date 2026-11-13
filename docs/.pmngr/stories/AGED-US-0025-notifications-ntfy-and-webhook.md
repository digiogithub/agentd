---
id: AGED-US-0025
type: story
title: "Notifications: ntfy and webhook"
status: done
priority: medium
parent: AGED-EP-0004
milestone: AGED-M-0002
labels: [gateway, plan:api-s22]
estimate: 3
created: 2026-11-13T13:55:04Z
updated: 2026-11-13T16:18:38Z
started: 2026-11-13T16:07:12Z
closed: 2026-11-13T16:18:38Z
---

## Description
Gateway sends ntfy/webhook notifications on `waiting` and on `idle` after work, no screen content by default, `agentd://open?host&session` deep link, per-session debounce.

## Acceptance Criteria
- Test with a local HTTP sink.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/security-remote]] §6
