---
id: AGED-US-0044
type: story
title: Notification sinks in the daemon (internal/notify, [notify])
status: in_progress
priority: high
parent: AGED-EP-0007
labels: [dashboard, notify]
created: 2026-11-22T08:23:34Z
updated: 2026-11-22T08:24:06Z
started: 2026-11-22T08:24:06Z
---

## Goal
Push something to the phone today without a backend of ours: ntfy (and webhook) from the daemon (D3 step 1).
Plan and contract: [[plans/agentd/dashboard_plan]] (normative). Epic: AGED-EP-0007.

## Acceptance
- internal/notify extracted from internal/httpapi/notify.go; gateway behaviour unchanged (its tests pass).
- Daemon [notify] config, event-bus tap, reasons question, waiting, blocked, turn_done, exited; debounce per session+reason.
- Secrets rules as [api.notify] (*_env, file mode check); never log URLs, secrets or bodies; preview only if include_preview.
- Tests with httptest sinks; AGENTS.md updated.
