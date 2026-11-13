---
id: AGED-US-0012
type: story
title: Op view and agentd view CLI
status: done
priority: critical
parent: AGED-EP-0002
milestone: AGED-M-0001
labels: [daemon, cli, plan:api-s09]
estimate: 5
created: 2026-11-13T13:52:50Z
updated: 2026-11-13T15:18:39Z
started: 2026-11-13T14:23:23Z
closed: 2026-11-13T15:18:39Z
---

## Description
Op `view {name, since_revision?, lines_limit?, scrollback?}` returning the View (revision, epoch, first_line, status, attention, lines, input, options, title); per-session cache recomputed on output, revision bumps only on fingerprint change, watcher timer for working→idle. CLI `agentd view [--json]`.

## Acceptance Criteria
- `since_revision` equal to current returns not-modified quickly.
- Race test with concurrent output and view calls.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/daemon-ops]] §2
- ADR-A16
