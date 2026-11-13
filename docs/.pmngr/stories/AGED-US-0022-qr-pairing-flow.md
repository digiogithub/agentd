---
id: AGED-US-0022
type: story
title: QR pairing flow
status: done
priority: high
parent: AGED-EP-0004
milestone: AGED-M-0002
labels: [security, cli, plan:api-s19]
estimate: 5
created: 2026-11-13T13:55:00Z
updated: 2026-11-13T16:06:35Z
started: 2026-11-13T15:39:12Z
closed: 2026-11-13T16:06:35Z
---

## Description
`agentd serve pair`: QR `agentd://pair?v&u&c&fp&h` + printable code, 60-bit one-time code, 5-min TTL, 5 attempts, host approval prompt (y/N) by default; issues a `write` token.

## Acceptance Criteria
- Brute-force and replay tests.
- QR rendered in terminal (no new dep unless added to AGENTS.md).
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/security-remote]] §3
