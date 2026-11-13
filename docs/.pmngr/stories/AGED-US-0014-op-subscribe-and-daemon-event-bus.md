---
id: AGED-US-0014
type: story
title: Op subscribe and daemon event bus
status: done
priority: critical
parent: AGED-EP-0002
milestone: AGED-M-0001
labels: [daemon, proto, plan:api-s11]
estimate: 8
created: 2026-11-13T13:52:52Z
updated: 2026-11-13T15:18:41Z
started: 2026-11-13T14:31:31Z
closed: 2026-11-13T15:18:41Z
---

## Description
Long-lived `subscribe {sessions?, kinds?, since_seq?}` streaming newline JSON events (session.created/started/exited/removed, status.changed, view.changed, input.changed, mail.queued/delivered, resync, heartbeat); bus in the daemon, bounded queue per client (256) with `resync` on overflow, 1024-event replay buffer, `epoch`.

## Acceptance Criteria
- No new import cycles (session exposes `Changed()`/`OutputGen()`, delivery a callback).
- Slow subscriber never blocks the daemon (test).
- Client helper in `internal/client`.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/daemon-ops]] §6
