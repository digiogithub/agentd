---
id: AGED-US-0032
type: story
title: Notifications and deep links
status: done
priority: medium
parent: AGED-EP-0005
milestone: AGED-M-0003
labels: [app, plan:f6]
estimate: 5
created: 2026-11-13T13:55:13Z
updated: 2026-11-13T17:46:56Z
started: 2026-11-13T17:28:27Z
closed: 2026-11-13T17:46:56Z
---

## Description
Desktop local notifications on waiting/turn-done while the app runs; Android: foreground stream + ntfy integration for background; `agentd://open` deep link opens the session.

## Acceptance Criteria
- No screen content in notifications by default.

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/frontend-app]]
- [[design/api/security-remote]] §6
