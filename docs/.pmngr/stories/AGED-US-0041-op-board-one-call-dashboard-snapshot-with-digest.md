---
id: AGED-US-0041
type: story
title: "Op board: one-call dashboard snapshot with digest"
status: in_progress
priority: high
parent: AGED-EP-0007
labels: [dashboard, daemon]
created: 2026-11-22T08:23:30Z
updated: 2026-11-22T08:42:51Z
started: 2026-11-22T08:42:51Z
---

## Goal
Clients paint the Panel with one call per host.
Plan and contract: [[plans/agentd/dashboard_plan]] (normative). Epic: AGED-EP-0007.

## Acceptance
- board {since_digest?} -> sessions[{name, status, attention, turn, preview, tasks[], questions[]}], unread_human, digest, not_modified.
- Visibility per the plan (humans; relay devices with globs, questions only for owner devices).
- CLI agentd board [--json].
- Tests incl. digest not_modified; AGENTS.md updated.
