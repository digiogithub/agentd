---
id: AGED-US-0039
type: story
title: "Per-session task list: ops tasks_set, task_update, tasks and event task.changed"
status: done
priority: high
parent: AGED-EP-0007
labels: [dashboard, daemon]
created: 2026-11-22T08:23:28Z
updated: 2026-11-22T08:36:29Z
started: 2026-11-22T08:24:04Z
closed: 2026-11-22T08:36:29Z
---

## Goal
Agents report their plan; the daemon keeps it per session (memory only, D5) and publishes changes.
Plan and contract: [[plans/agentd/dashboard_plan]] (normative). Epic: AGED-EP-0007.

## Acceptance
- proto.Task, ops tasks_set / task_update / tasks with the identity rules of the plan (session-only writes, humans forbidden).
- Limits (50 tasks, title 200, note 500, id charset), screen.Sanitize, auto ids t1.., revision bumps only on change.
- Event task.changed {revision, counts, current, changed[]}, kinds prefix task; SessionInfo.tasks summary in ls and the subscribe snapshot.
- List dropped on rm; kept (readable) after exit.
- CLI agentd task set (JSON or lines from stdin) | add | start | block | done | ls [NAME] [--json].
- MCP agentd_tasks_set {tasks[]}, agentd_task_update {id, status?, note?}.
- Tests: daemon (-race), CLI, MCP; AGENTS.md contract updated.
