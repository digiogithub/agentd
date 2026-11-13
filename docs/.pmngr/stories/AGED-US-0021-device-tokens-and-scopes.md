---
id: AGED-US-0021
type: story
title: Device tokens and scopes
status: done
priority: critical
parent: AGED-EP-0004
milestone: AGED-M-0002
labels: [security, gateway, plan:api-s18]
estimate: 5
created: 2026-11-13T13:54:59Z
updated: 2026-11-13T16:06:33Z
started: 2026-11-13T15:31:47Z
closed: 2026-11-13T16:06:33Z
---

## Description
Tokens `agd_<id>_<secret>` (256-bit), stored as SHA-256 in `$XDG_STATE_HOME/agentd/api/devices.json` (0700/0600, flock), scopes read/write/admin, 90-day inactivity expiry, last-seen, revoke closes streams within ~1 s; required on every listener incl. loopback; header only.

## Acceptance Criteria
- Constant-time comparison; tokens never logged.
- `agentd serve devices` list/revoke.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/security-remote]] §2
- ADR-A15
