---
id: AGED-US-0008
type: story
title: Fixed PTY size policy (API never resizes)
status: done
priority: critical
parent: AGED-EP-0002
milestone: AGED-M-0001
labels: [daemon, config, plan:api-s07]
estimate: 3
created: 2026-11-13T13:52:45Z
updated: 2026-11-13T14:15:44Z
started: 2026-11-13T13:57:14Z
closed: 2026-11-13T14:15:44Z
---

## Description
Profile `cols`/`rows` (default 160x50) for daemon-started sessions; CLI `run` stops sending its terminal size (new `--size`); local attach keeps resizing; `restore_size_on_detach` (default true) restores the profile size when the last local client detaches.

## Acceptance Criteria
- Sessions started without a size get the profile size.
- Detach of the last client restores the size (test with fake client).
- `AGENTS.md` key behaviours updated.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/daemon-ops]] §5
- ADR-A02
