---
id: AGED-US-0020
type: story
title: L0 raw terminal snapshot endpoint
status: done
priority: medium
parent: AGED-EP-0003
milestone: AGED-M-0002
labels: [gateway, plan:api-s17]
estimate: 2
created: 2026-11-13T13:54:58Z
updated: 2026-11-13T16:06:31Z
started: 2026-11-13T15:34:30Z
closed: 2026-11-13T16:06:31Z
---

## Description
`GET /v1/sessions/{name}/terminal?format=ansi|text` backed by `dump`; never resizes; WebSocket live stream explicitly deferred.

## Acceptance Criteria
- Snapshot matches `agentd dump` output.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/http-api]] §5
