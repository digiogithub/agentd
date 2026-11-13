---
id: AGED-US-0017
type: story
title: REST endpoints v1
status: done
priority: critical
parent: AGED-EP-0003
milestone: AGED-M-0002
labels: [gateway, plan:api-s14]
estimate: 8
created: 2026-11-13T13:52:56Z
updated: 2026-11-13T16:06:28Z
started: 2026-11-13T15:34:27Z
closed: 2026-11-13T16:06:28Z
---

## Description
Implement `/v1/info`, sessions list/get/run/kill/delete, view, act, mailbox (inbox, send, reply, read) with the error envelope, ETag `e<epoch>-r<revision>`, Idempotency-Key on run/act/send/reply, scopes per endpoint (enforced once API-S18 lands; stub now).

## Acceptance Criteria
- Every route in `openapi.yaml` implemented and tested.
- Daemon codes map to HTTP status per http-api §2.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/http-api]] §2-§3
