---
id: AGED-US-0010
type: story
title: Structured error codes in proto.Response
status: done
priority: high
parent: AGED-EP-0002
milestone: AGED-M-0001
labels: [proto, plan:api-s29]
estimate: 2
created: 2026-11-13T13:52:47Z
updated: 2026-11-13T14:15:47Z
started: 2026-11-13T13:57:12Z
closed: 2026-11-13T14:15:47Z
---

## Description
Optional `code` field in `proto.Response` (`busy`, `not_ready`, `conflict`, `not_found`, `forbidden`, `invalid`, `starting`…) via a `proto.CodedError`; old clients unaffected; `internal/client` exposes the code.

## Acceptance Criteria
- Round-trip tests; existing error strings unchanged.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/daemon-ops]] §3.5
