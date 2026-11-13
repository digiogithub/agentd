---
id: AGED-US-0036
type: story
title: "History sampling: observe only on complete frames"
status: done
priority: high
parent: AGED-EP-0001
milestone: AGED-M-0001
labels: [screen, daemon]
estimate: 3
created: 2026-11-13T14:31:07Z
updated: 2026-11-13T15:31:04Z
started: 2026-11-13T15:19:23Z
closed: 2026-11-13T15:31:04Z
---

## Description
With fast full-screen redraws (fakeagent -alt -redraw, ~0.7 s per repaint) the daemon watcher observes mid-repaint frames when SettleMax (250 ms) forces observation: History gets gap lines, duplicates and status/footer rows ("✻ Thinking…", "? for shortcuts"). Found while implementing AGED-US-0012.

## Acceptance Criteria
- Observe on synchronized-update boundaries when the child uses DEC mode 2026 (BSU/ESU): never sample inside an open synchronized update.
- Without 2026, SettleMax sampling skips frames whose status/footer rows are classified as transient (spinner_chars, busy_regex) from the transcript.
- Daemon test with fakeagent -alt -redraw asserts a duplicate-free, gap-free transcript.
- `go test -race` green.

## Notes
- Plan: [[plans/api-gateway-plan]]; [[design/api/semantic-model]] §3.5; [[AGED-US-0012-op-view]].
- Claude built-in spinner_chars/busy_regex are still provisional (F8).
