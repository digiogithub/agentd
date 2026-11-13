---
id: AGED-US-0023
type: story
title: "TLS modes: self-signed with pin, files, Tailscale"
status: done
priority: high
parent: AGED-EP-0004
milestone: AGED-M-0002
labels: [security, plan:api-s20]
estimate: 5
created: 2026-11-13T13:55:02Z
updated: 2026-11-13T16:06:37Z
started: 2026-11-13T15:35:48Z
closed: 2026-11-13T16:06:37Z
---

## Description
`tls = off|self-signed|files`; self-signed cert generated and persisted with SPKI pin included in the pairing QR; `files` for `tailscale cert` output; docs for `tailscale serve` in front of a loopback gateway (identity headers never trusted).

## Acceptance Criteria
- Pin verified by an integration test client.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/security-remote]] §4
