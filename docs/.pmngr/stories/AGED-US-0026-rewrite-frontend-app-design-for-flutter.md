---
id: AGED-US-0026
type: story
title: Rewrite frontend-app design for Flutter
status: done
priority: high
parent: AGED-EP-0005
milestone: AGED-M-0003
labels: [docs, app, plan:doc]
estimate: 2
created: 2026-11-13T13:55:05Z
updated: 2026-11-13T14:15:49Z
started: 2026-11-13T13:57:06Z
closed: 2026-11-13T14:15:49Z
---

## Description
Rewrite `docs/design/api/frontend-app.md` for ADR-A18: Flutter (desktop + Android) in `app/`, package choices (http/SSE, secure storage, QR scanning, notifications), state management, API client strategy, CI.

## Acceptance Criteria
- No Tauri/Preact leftovers; sections referenced by F1-F8 exist.

## Notes
- Plan: [[plans/api-gateway-plan]]
- ADR-A18
