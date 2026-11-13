---
id: AGED-US-0019
type: story
title: OpenAPI contract tests
status: done
priority: high
parent: AGED-EP-0003
milestone: AGED-M-0002
labels: [gateway, test, plan:api-s16]
estimate: 3
created: 2026-11-13T13:54:57Z
updated: 2026-11-13T16:18:36Z
started: 2026-11-13T16:07:12Z
closed: 2026-11-13T16:18:36Z
---

## Description
Validate handlers against `docs/api/openapi.yaml` in tests (route table + `x-agentd-scope` + response schemas); lint the spec in CI. Add the chosen test-only library to `AGENTS.md` pinned deps.

## Acceptance Criteria
- CI fails when a route or schema drifts from the spec.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/http-api]] §6
