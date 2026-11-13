# AGED-US-0016: `agentd serve` skeleton and `[api]` config

Plan: [[plans/api-gateway-plan]] (ADR-A01, A07, A08, A15, A16, A17). Design: [[design/api/http-api]] 1, 2, 7;
[[design/api/security-remote]] 2 (tokens, implemented later in AGED-US-0021).

## What changed
- New `[api]` config table with defaults and validation. A non-loopback `listen` with `tls = "off"` is a
  config error (no override; `--listen` is re-validated).
- New package `internal/httpapi` (depends only on `internal/client` and `internal/proto`): `Server`, routing
  table, middleware chain, JSON error envelope, daemon connection pool, graceful shutdown.
- Endpoints: `GET /v1/health` (no auth; pings the daemon) and `GET /v1/info` (daemon `info` + `whoami`,
  plus `api_version` and `features[]`, ADR-A16).
- New command `agentd serve [--listen] [--config] [--no-autostart] [--log-level]`.

## Files and symbols
- `internal/config/api.go`: `APIConfig`, `(*APIConfig).applyDefaults`, `APIConfig.Validate`, `IsLoopbackAddr`;
  hook in `internal/config/config.go` (`Config.API`, `init`). Tests: `api_test.go`.
- `internal/httpapi`: `httpapi.go` (`Server`, `Config`, `Handler`, `Routes`, `Draining`, `Serve`),
  `routes.go` (`Route`, `Handler`, `baseRoutes`, health/info handlers, 404/405 envelopes), `auth.go`
  (`Scope`, `Principal`, `Authenticator`, `AuthenticatorFunc`, `DenyAll`, `ErrUnauthenticated`,
  `ErrForbidden`), `errors.go` (`Error`, `WriteError`, `FromDaemonError`), `json.go` (`WriteJSON`,
  `DecodeJSON`), `pool.go` (`Server.WithDaemon`, pool of max 4 idle), `middleware.go`.
- `cmd/agentd/serve.go` (`serveCmd`, `runServe`, `serveTLS`), registered in `main.go`.
- `AGENTS.md`: contract row, `[api]` / `agentd serve` bullet.

## Why
First step of the HTTP gateway: a safe skeleton (loopback default, TLS required off-loopback, auth hook,
stable error envelope) that the next stories extend with sessions, view, act, events and mailbox routes.

## Behaviour notes
- Middleware order: request id, access log (route pattern, status, duration, bytes, device label; never
  tokens, query strings, bodies), recover, host check + CORS, mux; per route: body cap (413), auth, handler.
- Unauthenticated routes: only `/v1/health`. `Server.Auth == nil` = `DenyAll`.
- Daemon calls run under a 10 s cap; a failing pooled connection is retried once on a fresh dial (the CLI's
  dialer autostarts the daemon); daemon unreachable = 503 `daemon_unavailable` + `Retry-After: 1`.
- Status mapping: invalid 400, not_found 404, starting 409 (+Retry-After, retryable), exited/exists
  (`already_exists`)/conflict 409, busy and not_ready 409 retryable, forbidden 403, verify_failed 503
  `paste_unverified` (http-api wins over the 422 in daemon-ops), timeout 504, internal 500, uncoded 500
  `daemon_error` (legacy messages for 404/409 matched by text).
- Not done here: TLS `self-signed` (rejected at start as not implemented), device tokens/pairing, SSE hub,
  idempotency store, rate limits, `--ui`, `--log-format`.

## Verification
`gofmt -l .` clean, `go vet ./...` clean, `go test -race -count=1 ./internal/httpapi ./internal/config ./cmd/...`
green. Tests use a temp daemon socket and state dir (no agent CLIs): health without token, info needs auth,
error mapping table, body cap, 404/405 envelopes, access log secrecy, host/CORS, pool reuse and stale retry,
call timeout, graceful shutdown with SSE `bye`, `go list -deps` dependency check, non-loopback-without-TLS
refusal, config defaults/validation.
