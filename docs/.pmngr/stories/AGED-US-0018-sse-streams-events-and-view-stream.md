---
id: AGED-US-0018
type: story
title: "SSE streams: events and view stream"
status: done
priority: critical
parent: AGED-EP-0003
milestone: AGED-M-0002
labels: [gateway, plan:api-s15]
estimate: 8
created: 2026-11-13T13:54:56Z
updated: 2026-11-13T16:06:30Z
started: 2026-11-13T15:34:53Z
closed: 2026-11-13T16:06:30Z
---

## Description
`/v1/events` (hello, daemon events, resync, bye, heartbeats, Last-Event-ID resume from ring buffer) and `/v1/sessions/{name}/view/stream` (full view then `view.patch` splices, ≤1 event/100 ms); one daemon subscribe fanned out; singleflight view fetches.

## Acceptance Criteria
- Reconnect with Last-Event-ID resumes without gaps or sends resync.
- Load test: N clients cause one daemon subscription.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/http-api]] §4
