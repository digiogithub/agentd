---
id: AGED-US-0030
type: story
title: "Session view: text, prompt box, options, quick keys"
status: done
priority: critical
parent: AGED-EP-0005
milestone: AGED-M-0003
labels: [app, plan:f4]
estimate: 8
created: 2026-11-13T13:55:10Z
updated: 2026-11-13T17:27:51Z
started: 2026-11-13T16:56:16Z
closed: 2026-11-13T17:27:51Z
---

## Description
Session screen from `view/stream`: reflowed lines (monospace, selectable), prompt composer (act prompt), option buttons (act choose with revision/label), quick keys (Esc, interrupt, arrows, Enter), 409 conflict refresh, `busy` (human draft) messaging.

## Acceptance Criteria
- Never sends a resize; renders at any width.
- Widget tests with recorded view fixtures.

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/frontend-app]]
- [[design/api/http-api]] §3-§4
