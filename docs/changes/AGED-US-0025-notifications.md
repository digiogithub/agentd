# AGED-US-0025 Notifications: ntfy and webhook

Builds on [[plans/api-gateway-plan]], [[design/api/security-remote]] (section 6) and the gateway event hub
([[AGED-US-0018-sse-streams]]).

## What changed
The gateway can push notifications to an ntfy topic and/or a signed webhook when a session needs a human
(`waiting`), finished its turn (`turn_done`) or exited (`exited`). Off by default (`[api.notify] enabled`).

## Files and symbols
- `internal/httpapi/notify.go` (new): `NotifyConfig`, `notifier` (`offer`, `run`, `deliver`, `send`), `mapEvent`,
  `Sign`, `SignatureHeader`, `Server.startNotifier` / `waitNotifier`.
- `internal/httpapi/events.go`: `hub.tap` hook called at the end of `hub.handle` (non-blocking).
- `internal/httpapi/httpapi.go`: `Config.Notify`, `Server.notify`, `Serve` starts the notifier, `Close`/`Serve` wait for it.
- `internal/config/api.go`: `APIConfig.Notify` (`NotifyConfig`), defaults, `ValidateNotifyURL` (https, or http on loopback).
- `cmd/agentd/serve.go`: maps config onto `httpapi.NotifyConfig`.
- Tests: `internal/httpapi/notify_test.go`, `internal/config/notify_test.go`. AGENTS.md gateway bullet.

## Behaviour
Mapping: `attention.changed` with `attention=true` -> `waiting` (reasons waiting/bell/notify) or `turn_done`;
`session.exited` -> `exited`. `on` filters; `debounce` (default 30s) per session and reason. Payloads carry no
screen text; `include_preview` adds only the daemon's sanitized preview line (<=160 chars, fetched by the worker,
never on the hub path). Deep link `agentd://open?h=<host>&s=<session>`.
Delivery: one worker, queue of 256 (drops oldest, logs), 10 s timeout, 3 retries (1s, 2s, 4s) on network
error/429/5xx, no redirects; logs never contain URLs or secrets.

Webhook body: `{"host":"box","session":"claude@webapp","reason":"waiting","status":"waiting","at":"2026-11-13T16:00:00Z","link":"agentd://open?h=box&s=claude%40webapp"}`.
ntfy: POST body `claude@webapp is waiting for input`, headers `Title: agentd: claude@webapp`, `Tags`, `Priority`,
`Click: agentd://open?h=box&s=claude%40webapp`, optional `Authorization: Bearer <token>`.

## Why
Remote users (Flutter app, see design/api/frontend-app section 6) need a wake-up when an agent blocks, without APNs/FCM in v1.

## Verification
`gofmt -l .` clean, `go vet ./...`, `go test -race -count=1 ./internal/httpapi ./internal/config ./cmd/...` pass.
Tests use httptest sinks only: trigger mapping, on filter, debounce, HMAC, ntfy headers, retry/backoff and log
redaction, no retry on 4xx, queue overflow, disabled = no goroutines/tap, https validation, and an end-to-end run
against a real daemon with `include_preview` off (no screen text) and on.
