---
id: AGED-US-0028
type: story
title: Hosts and pairing in the app
status: done
priority: critical
parent: AGED-EP-0005
milestone: AGED-M-0003
labels: [app, security, plan:f2]
estimate: 8
created: 2026-11-13T13:55:08Z
updated: 2026-11-13T17:27:49Z
started: 2026-11-13T16:45:48Z
closed: 2026-11-13T17:27:49Z
---

## Description
Host list, pairing by QR scan (Android camera) or code entry (desktop), token in secure storage (keystore/keychain/libsecret), TLS SPKI pinning, host status (online/offline).

## Acceptance Criteria
- Token never in plain prefs or logs.
- Pin mismatch blocks the connection with a clear error.

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/frontend-app]]
- [[design/api/security-remote]] §3-§4
