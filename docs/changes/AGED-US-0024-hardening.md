# AGED-US-0024: Hardening (Host/Origin, rate limits, body caps, audit log)

Plan: [[plans/api-gateway-plan]]. Design: [[design/api/security-remote]] 1 and 5. Fixes done together:
[[gateway-review-fixes]].

## What changed

- Host allow-list (421 before auth) and Origin policy (403; `null` never), `X-Content-Type-Options`, `Referrer-Policy`,
  `Cache-Control: no-store`, `Content-Type: application/json` required for POST bodies (415).
- Rate limits: per-device token bucket (20/s burst 40), `act` 5/s, failed auth 10/min per remote address; SSE caps 8 per
  device / 64 total; 32 concurrent daemon calls; body cap (existing `max_body_kb`, 413); `http.Server` timeouts
  (header 10 s, idle 120 s, 16 KiB headers). Config keys under `[api]`: `rate_limit`, `rate_burst`, `act_rate_limit`,
  `max_streams_per_device`, `max_streams`, `audit_max_mb`, `pairing`, `autostart_daemon`.
- Audit log `<state>/api/audit.log` (JSONL, 0600, dir 0700, `audit_max_mb` default 10, one rotation to `.1`, safe with
  several writers): run, act (action, option id/label, key names, text length), kill, rm, send/reply (ids, body length),
  view_ack, device_pair (gateway), device_add / device_revoke (CLI, device `cli`). No prompt text, subjects, bodies or tokens.

## Files and symbols

`internal/httpapi/guard.go` (host set, `secure`, `limiter`, `acquireStream`), `audit.go` (`AuditEvent`, `Auditor`,
`AuditFile`, `auditOp`), `routes.go` (`wrapRoute`), `httpapi.go` (`Config`, `httpServer`), `internal/config/api.go`,
`internal/apitoken/pairhttp.go` (`PairHandlerOptions.Audit`), `cmd/agentd/serve.go`, `cmd/agentd/devices.go`
(`auditCLI`), `docs/api/openapi.yaml`, `docs/design/api/{http-api,security-remote}.md`, `AGENTS.md`.

## Why

A gateway reachable from a phone or a LAN needs DNS-rebinding/CSRF defences, abuse limits and a record of every
side-effecting action that does not itself leak the prompts.

## Verification

Tests: `internal/httpapi/hardening_test.go`, `audit_test.go` (audit of all write routes without secrets, JSONL, 0600,
rotation, second writer), `internal/apitoken/pairing_test.go` (`TestPairingIsAudited`), `cmd/agentd/hardening_test.go`
(CLI + gateway audit lines, Host 421 through `runServe`). `go vet ./...` and
`go test -race -count=1 ./internal/httpapi ./internal/apitoken ./internal/tlsutil ./internal/config ./cmd/...` pass.
Not done: auth-failure and pairing-failure audit lines (they stay in the server log), `[api.limits]` table and
`audit_bodies` from the original design, `run` rate limit.
