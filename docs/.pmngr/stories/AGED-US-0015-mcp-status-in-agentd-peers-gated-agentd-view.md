---
id: AGED-US-0015
type: story
title: "MCP: status in agentd_peers, gated agentd_view"
status: done
priority: low
parent: AGED-EP-0002
milestone: AGED-M-0001
labels: [mcp, plan:api-s12]
estimate: 2
created: 2026-11-13T13:52:53Z
updated: 2026-11-13T15:18:42Z
started: 2026-11-13T14:48:06Z
closed: 2026-11-13T15:18:42Z
---

## Description
Add status/attention to `agentd_peers`; optional read-only `agentd_view` only when `[security] agent_view = true` (default off), limited content, ACL applied.

## Acceptance Criteria
- Default config exposes no view tool.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/daemon-ops]] §7
