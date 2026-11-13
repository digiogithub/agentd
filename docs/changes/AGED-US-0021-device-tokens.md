# AGED-US-0021: device tokens and scopes

Plan: [[plans/api-gateway-plan]] (ADR-A09, A15). Design: [[design/api/security-remote]] 1-2.
Builds on [[changes/AGED-US-0016-serve-skeleton]].

## What changed
- New package `internal/apitoken`: token store, file format, `httpapi.Authenticator`, revoke guard.
- `agentd serve` now authenticates every route above `ScopeNone` with device tokens on every listener
  (loopback included). The deny-all stub is gone; with no devices everything but `/v1/health` is 401.
- New CLI `agentd serve devices [list | revoke <id|name> | add --name N [--scope read|write|admin] [--ttl 30d]]`.
  `add` prints the token once on stdout (message on stderr); pairing is AGED-US-0022.

## File format (`<state>/api/devices.json`, dir 0700, file 0600, lock `api/devices.lock`)
```json
{"version": 1, "devices": [{"id": "7K2M9QXD4T", "name": "ci", "platform": "", "scope": "read",
  "hash": "<hex sha256(secret)>", "created_at": "...", "expires_at": "...", "last_seen_at": "...",
  "last_seen_addr": "127.0.0.1", "paired_via": "cli"}]}
```
Token = `agd_<10 Crockford base32 id>_<43 base64url chars of 32 random bytes>`. Only the hash is stored.

## Behaviour
- Verify: parse, lookup by id, SHA-256 of the presented secret, `subtle.ConstantTimeCompare` with the stored hash
  (also run against a dummy for unknown ids), then expiry (`expires_at`, 90 days without use; `last_seen`).
- `last_seen_at` is persisted at most once per minute per device (only that field is rewritten, on top of
  the current file, so a concurrent revoke is never undone). Tokens are never logged; logs carry the id only.
- Mutations (CLI and gateway) take an exclusive `flock` and write temp file + rename.
- Reload: the gateway re-stats the file per request (at most once a second) and a poller (`Store.Run`, 500 ms)
  fires revocation hooks. `Auth.Guard` (outermost handler, via `httpapi.Server.WrapHandler`) gives each request
  a context cancelled when its device is revoked/expired, so SSE streams and long requests end within ~1 s.
- Malformed file, unknown version, bad ids/hashes/scopes, group/world-accessible file or dir, wrong owner,
  symlink: the store denies every token (error logged once). CLI mutations refuse to overwrite it.
  `serve` refuses to start on a non-loopback bind in that state; on loopback it runs deny-all.
- Header only: `Authorization: Bearer agd_...` (exactly one header). Any query string containing `agd_` or a
  parameter named token/access_token/api_key/... answers 400 (also on `/v1/health`). Cookies are ignored.

## Files and symbols
- `internal/apitoken/token.go` (`ParseToken`, `newToken`, scopes), `store.go` (`Store`, `Open`, `Create`, `List`,
  `Revoke`, `Verify`, `Run`, `Subscribe`, `IsActive`, `State`, `Options`, `Device`), `auth.go` (`Auth`, `NewAuth`,
  `Authenticate`, `Guard`), tests `apitoken_test.go`.
- `internal/httpapi/revoke.go`: `Server.WrapHandler` (additive; wraps the final handler).
- `cmd/agentd/devices.go` (`devicesCmd`), `serve.go` (store wiring, `serveOptions.DevicesDir`), tests `devices_test.go`.
- `AGENTS.md`: contract row and paths/gateway bullets.

## Verification
`gofmt -l`, `go vet ./...` clean for the touched packages; `go test -race -count=1 ./internal/apitoken ./cmd/...`
green: create/format, perms, malformed/unsafe file, negative verify, constant-time path (counting hook), expiry,
last-seen throttling, revoke by another store (CLI) + hooks, concurrent flock access, scope matrix, query-token
400, revoke closing an open stream within 1.5 s (other device's stream untouched), serve end-to-end with a real
daemon (200 with token, 401 after CLI revoke), broken store refused on a TLS non-loopback bind.
