---
id: AGED-US-0040
type: story
title: "Questions as mailbox messages: kind, options, task (schema v4) and agentd ask"
status: done
priority: high
parent: AGED-EP-0007
labels: [dashboard, mailbox]
created: 2026-11-22T08:23:29Z
updated: 2026-11-22T08:32:52Z
started: 2026-11-22T08:24:05Z
closed: 2026-11-22T08:32:52Z
---

## Goal
An agent asks the human a question with optional answer options; the reply goes back through the mailbox (D2).
Plan and contract: [[plans/agentd/dashboard_plan]] (normative). Epic: AGED-EP-0007.

## Acceptance
- Mailbox schema v4 (kind, options JSON, task), ids and AUTOINCREMENT kept, foreign_key_check.
- send accepts kind/options/task; kind question requires a session sender and a human recipient.
- Open question = kind question and not replied; reply closes it.
- mail.queued carries kind.
- CLI agentd ask TEXT [--option A]... [--task ID] [--to human:NAME]; inbox shows questions and their options.
- MCP agentd_ask {question, options?, task?}.
- Tests: mailbox migration from v3 fixtures, daemon, CLI, MCP; AGENTS.md updated.
