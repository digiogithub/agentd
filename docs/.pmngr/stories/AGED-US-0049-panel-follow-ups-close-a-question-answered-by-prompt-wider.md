---
id: AGED-US-0049
type: story
title: "Panel follow-ups: close a question answered by prompt, wider answer field"
status: done
priority: medium
parent: AGED-EP-0007
labels: [dashboard, desktop]
created: 2026-11-22T09:12:52Z
updated: 2026-11-22T09:25:14Z
started: 2026-11-22T09:12:52Z
closed: 2026-11-22T09:25:14Z
---

## Goal
Two gaps found integrating AGED-US-0045.

1. A question answered through the agent's prompt stays open (only reply closes it). Add `reply {id, body, notify?: false}`: with notify false the reply is stored already delivered (the delivery loop never injects it) and the question closes. The desktop Panel, after a successful Prompt answer, calls it so the answer is recorded and the question disappears.
2. The free-text answer field in the attention cards is too narrow (placeholder truncated next to the Buzón/Prompt toggle): put the toggle on its own row or let the field take the remaining width.

Plan: [[plans/agentd/dashboard_plan]].
