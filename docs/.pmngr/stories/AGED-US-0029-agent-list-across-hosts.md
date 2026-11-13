---
id: AGED-US-0029
type: story
title: Agent list across hosts
status: done
priority: critical
parent: AGED-EP-0005
milestone: AGED-M-0003
labels: [app, plan:f3]
estimate: 5
created: 2026-11-13T13:55:09Z
updated: 2026-11-13T17:27:50Z
started: 2026-11-13T16:56:13Z
closed: 2026-11-13T17:27:50Z
---

## Description
Unified list of sessions from every paired host via `/v1/events`, status badges (working/idle/waiting/exited), attention highlight, preview line, filters by host/project.

## Acceptance Criteria
- Reconnect on network change / app resume without duplicates.

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/frontend-app]]
