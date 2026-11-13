---
id: AGED-US-0011
type: story
title: Op info and run with target + harness resolved in daemon
status: done
priority: high
parent: AGED-EP-0002
milestone: AGED-M-0001
labels: [daemon, proto, plan:api-s28]
estimate: 3
created: 2026-11-13T13:52:49Z
updated: 2026-11-13T14:15:48Z
started: 2026-11-13T13:57:13Z
closed: 2026-11-13T14:15:48Z
---

## Description
Op `info` (host, version, epoch, profiles, aliases with source, harnesses); `run` accepts `target` + `harness` resolved by the daemon with `ResolveTarget`; `ping` returns `epoch`; `ls` gains summary fields placeholder (status, attention, revision, preview) filled once API-S09 lands.

## Acceptance Criteria
- CLI `run` keeps working; daemon-side resolution matches CLI rules (tests).
- `AGENTS.md` ops list updated.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- ADR-A14
- [[design/api/http-api]] §8
