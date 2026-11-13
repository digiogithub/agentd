---
id: AGED-US-0024
type: story
title: "Hardening: Host/Origin, rate limits, body caps, audit log"
status: done
priority: high
parent: AGED-EP-0004
milestone: AGED-M-0002
labels: [security, gateway, plan:api-s21]
estimate: 5
created: 2026-11-13T13:55:03Z
updated: 2026-11-13T16:06:39Z
started: 2026-11-13T15:42:20Z
closed: 2026-11-13T16:06:39Z
---

## Description
421 on unknown Host (DNS rebinding), Origin allow-list, per-token rate limits, body caps, JSONL audit log of run/act/kill/rm/send (no prompt text by default, rotation), `run_env = daemon`.

## Acceptance Criteria
- Tests for rebinding, CORS preflight, rate limiting, audit entries.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/security-remote]] §5
- ADR-A17
