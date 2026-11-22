---
id: AGED-US-0050
type: story
title: "Desktop: select several agents and send one prompt to all"
status: done
priority: medium
labels: [desktop]
created: 2026-11-22T20:18:39Z
updated: 2026-11-22T20:21:07Z
started: 2026-11-22T20:18:39Z
closed: 2026-11-22T20:21:07Z
---

## Goal
In the Agentes list of agentd-desktop, a selection mode: pick several sessions, or all of a host (or all visible), and send one prompt to every selected agent at once (act `prompt` per session, forced while working, as the chat does).

Client only: no daemon or wire change. Only agentd sessions that are running on a host whose device may operate can be selected.

## Acceptance
- "Seleccionar" in the list header toggles selection mode; rows become checkboxes.
- "Todos", one button per host, and "Ninguno" in selection mode; they follow the current filter and search.
- A composer under the list sends the text to every selected session; the result says how many got it and names the ones that refused, which stay selected.
- Headless tests on the fake backend.

Plan: [[plans/agentd/broadcast-prompt_plan]].
