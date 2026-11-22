---
id: AGED-US-0043
type: story
title: "Claude Code hook adapter: agentd task sync"
status: backlog
priority: medium
parent: AGED-EP-0007
labels: [dashboard, cli]
created: 2026-11-22T08:23:33Z
updated: 2026-11-22T08:23:33Z
---

## Goal
Agents that already keep a todo list (Claude Code TodoWrite) fill the board without prompt changes.
Plan and contract: [[plans/agentd/dashboard_plan]] (normative). Epic: AGED-EP-0007.

## Acceptance
- agentd task sync reads a Claude Code PostToolUse hook payload (TodoWrite) on stdin and calls tasks_set; no-op outside a session.
- agentd mcp install --print documents the hook snippet and short agent instructions for agentd_tasks_set / agentd_ask.
- Tests with captured payload fixtures; never runs a real agent CLI.
