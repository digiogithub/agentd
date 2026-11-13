---
id: AGED-US-0016
type: story
title: agentd serve skeleton and [api] config
status: done
priority: critical
parent: AGED-EP-0003
milestone: AGED-M-0002
labels: [gateway, config, plan:api-s13]
estimate: 3
created: 2026-11-13T13:52:55Z
updated: 2026-11-13T16:06:26Z
started: 2026-11-13T15:24:06Z
closed: 2026-11-13T16:06:26Z
---

## Description
Command `agentd serve`, package `internal/httpapi` (depends only on client + proto), `[api]` config parsed in `internal/config` (listen default 127.0.0.1:7477, tls, cors_origins, max_body_kb, sse_heartbeat, run_harnesses, run_env…), daemon autostart, small daemon connection pool, graceful shutdown, `/v1/health`.

## Acceptance Criteria
- Non-loopback bind without TLS refuses to start.
- httptest + temp daemon socket tests.
- `AGENTS.md` contract row for `internal/httpapi`.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/http-api]] §1, §7
