---
type: comment
item: AGED-US-0024
author: jose-f-rives-lirola
author_name: Jose F. Rives Lirola
author_email: jose@digio.es
created: 2026-11-13T16:05:53Z
kind: comment
---

Implemented: Host allow-list 421 + Origin policy, security headers, 415, per-device/act rate limits, failed-auth limit, SSE caps, server timeouts, in-flight cap, audit log api/audit.log (JSONL 0600, rotation; run/act/kill/rm/send/reply/view_ack/device pair/add/revoke, no text). Tests in internal/httpapi (hardening_test, audit_test), apitoken, cmd/agentd; race-clean. See docs/changes/AGED-US-0024-hardening.md. Not done: auth-failure audit lines, audit_bodies, run rate limit.
