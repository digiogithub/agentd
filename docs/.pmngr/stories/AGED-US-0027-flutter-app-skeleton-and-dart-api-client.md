---
id: AGED-US-0027
type: story
title: Flutter app skeleton and Dart API client
status: done
priority: critical
parent: AGED-EP-0005
milestone: AGED-M-0003
labels: [app, plan:f1]
estimate: 5
created: 2026-11-13T13:55:07Z
updated: 2026-11-13T17:27:48Z
started: 2026-11-13T16:25:59Z
closed: 2026-11-13T17:27:48Z
---

## Description
Create `app/` (package `agentd_app`) targeting Linux, macOS, Windows and Android; Dart API client for `/v1` (models from `openapi.yaml`), SSE client with Last-Event-ID, fake-gateway tests; `flutter analyze` + `flutter test` in CI.

## Acceptance Criteria
- `flutter analyze` clean, `flutter test` green.
- Client tested against a stub server; no real agents.

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/frontend-app]]
- ADR-A18
