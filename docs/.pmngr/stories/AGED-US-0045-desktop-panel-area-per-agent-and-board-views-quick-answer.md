---
id: AGED-US-0045
type: story
title: "Desktop: Panel area (per-agent and board views), quick answer, notifications"
status: done
priority: high
parent: AGED-EP-0007
labels: [dashboard, desktop]
created: 2026-11-22T08:23:35Z
updated: 2026-11-22T09:12:40Z
started: 2026-11-22T08:53:43Z
closed: 2026-11-22T09:12:40Z
---

## Goal
Panel area in agentd-desktop as in the design canvas.
Plan and contract: [[plans/agentd/dashboard_plan]] (normative). Epic: AGED-EP-0007.

## Acceptance
- New area panel (sidebar first, badge = items needing attention), views Por agente / Tablero with a persisted toggle (D1).
- Attention column: open questions (option buttons + free text, destination Buzón = reply, Prompt = act prompt with force while working), terminal menus (act choose with revision + epoch), blocked tasks.
- Agent cards: current task, progress, next pending, fallback to preview when no tasks.
- Notifier: questions and newly blocked tasks.
- Backend contract + fake backend + core over board/tasks/subscribe; headless tests.
