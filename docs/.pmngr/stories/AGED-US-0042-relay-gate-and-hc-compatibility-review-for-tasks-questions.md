---
id: AGED-US-0042
type: story
title: Relay gate and hc compatibility review for tasks, questions and board
status: in_progress
priority: medium
parent: AGED-EP-0007
labels: [dashboard, relay]
created: 2026-11-22T08:23:31Z
updated: 2026-11-22T08:42:52Z
started: 2026-11-22T08:42:52Z
---

## Goal
New ops pass the relay deny-by-default gate correctly; hc translator unaffected.
Plan and contract: [[plans/agentd/dashboard_plan]] (normative). Epic: AGED-EP-0007.

## Acceptance
- relayGate: tasks and board for any scope (globs), tasks_set/task_update never, send with kind question never from a device.
- hc: no new methods; check that labels/events translation ignores task.* events.
- Tests in relaygate.
