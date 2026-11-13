---
id: AGED-EP-0003
type: epic
title: HTTP gateway (agentd serve)
status: done
priority: high
milestone: AGED-M-0002
labels: [api]
created: 2026-11-13T13:52:33Z
updated: 2026-11-13T16:26:09Z
closed: 2026-11-13T16:26:09Z
---

## Description
Separate process `agentd serve` (`internal/httpapi`, depends only on client + proto): REST /v1, SSE streams, OpenAPI contract, L0 terminal snapshot. Design: [[design/api/http-api]], contract `docs/api/openapi.yaml`.

## Notes
- Plan: [[plans/api-gateway-plan]]
