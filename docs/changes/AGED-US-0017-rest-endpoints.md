# AGED-US-0017: REST endpoints v1

Plan: [[plans/api-gateway-plan]]. Design: [[design/api/http-api]] 2, 3, 5. Contract: `docs/api/openapi.yaml`.
Builds on [[changes/AGED-US-0016-serve-skeleton]] and [[changes/ops-review-fixes]] (choose needs revision + epoch).

## What changed
Routes added to the gateway (scope in brackets; all registered from `Server.baseRoutes` via `restRoutes`):
- `GET /v1/sessions` [read] (weak list ETag, 304), `POST /v1/sessions` [write; admin for cmd/env/other harnesses],
  `GET|DELETE /v1/sessions/{name}` [read|write] (`?force=true` = TERM, wait 3 s, KILL, rm; running without force = 409
  `still_running`), `POST /v1/sessions/{name}/kill` [write] (202 + summary).
- `GET /v1/sessions/{name}/view` [read]: `since`+`epoch`, `lines` (alias `tail`), `from`, `scrollback`, ETag
  `"e<epoch>-r<revision>"`, `If-None-Match` => 304. `POST .../view/ack` [write].
- `POST /v1/sessions/{name}/act` [write]: proxy to daemon `act` (action, text, submit, option_id, label, revision,
  epoch, keys, force). `If-Match` supplies epoch+revision. A daemon `conflict` is 409 with `details {revision, view}`
  and the fresh view's ETag; with `If-Match` and a moved view it is 412 `precondition_failed`.
- Mailbox as the gateway's human identity: `GET /v1/mailbox` (newest first, `limit`, `before`, `next_before`,
  `mailbox=` peeks another box), `POST /v1/mailbox/messages`, `GET /v1/mailbox/messages/{id}` (never marks read),
  `POST .../{id}/read`, `POST .../{id}/reply`.
- `Idempotency-Key` on POST sessions/act/messages/reply (see below). `GET /v1/info` features now list
  sessions, view, act, terminal, mailbox.
- AGED-US-0020 (`GET .../terminal`) is documented in [[changes/AGED-US-0020-terminal-snapshot]].

## Files and symbols
- `internal/httpapi/sessions.go`: `restRoutes`, session/view/act handlers, `actError`, `parseETag`, `findSession`.
- `internal/httpapi/mailbox.go`: mailbox handlers. `internal/httpapi/idempotency.go`: `idemStore`, `idempotent`.
- `internal/httpapi/routes.go`: `baseRoutes` appends `restRoutes()`. `internal/httpapi/httpapi_test.go`: two assertions
  loosened (route count, features list) because the table grew.
- `docs/api/openapi.yaml` (see below). Tests: `internal/httpapi/rest_test.go`.

## Behaviour notes
- Run: `target` is an alias or an absolute path (anything else with a `/` is 400); the daemon resolves it
  (`RunArgs.Target/Harness`, relative alias anchored with `cwd "/"`). No `env_base` is sent, so the daemon uses its own
  environment (`run_env = "daemon"`; the `"gateway"` mode is not wired: `httpapi.Config` has no such field yet).
  `harness` not in `run_harnesses`, or `cmd`/`env`, needs admin (403 `forbidden`).
- Idempotency: store keyed by (device, key), TTL `idempotency_ttl` (default 10 m), 4096 entries; only 2xx responses are
  stored and replayed (`Idempotent-Replayed: true`, status, body, Content-Type, Location, ETag); same key with another
  method/path/body => 422 `idempotency_mismatch`; in flight => 409 `in_progress`; failures can be retried.
- `GET` message of the own mailbox goes through `inbox {all}` because the daemon treats `as == self` as a real read; it
  may move `queued` to `delivered` (same as listing) but never to `read`.
- `since` is honoured only with `epoch` (the daemon never answers `not_modified` without it); view patches are not
  implemented (older `since` returns the full view).
- Act call uses the 10 s `WithDaemon` cap (design says 15 s for act; pool.go is not part of this story).

## openapi.yaml changes (to match the daemon)
New path `/v1/sessions/{name}/view/ack`; view params `epoch`, `lines`, `from`, `scrollback` (default false) replace
`tail`; `View` rewritten as `proto.View` (name, epoch, attention object, input.state, options kind/index, hints, dialog);
`ActRequest` gains `epoch`, `force`, `option_label` became `label`, `revision` no longer schema-required; `ActResult` =
`{action, result, submitted, revision, status, warning}`; act 409 documents `details {revision, view}`; `ViewUnchanged`
gets `epoch`; getMessage description.

## Verification
`gofmt -l .` clean, `go vet ./...` clean, `go test -race -count=1 ./internal/httpapi ./cmd/...` for the REST tests:
auth scope denial for every write route, session lifecycle (percent-encoded `@`/`.` names, list ETag 304, kill, force
delete), run policy (allow-list, admin override, validation), view ETag/304/since/lines, act prompt/keys/choose against
fakeagent `-menu` with revision+epoch and via `If-Match`, conflict 409 shape and 412, idempotent replay/mismatch/failed
retry, mailbox (send/list/pagination/get/read/reply/peek), route table vs openapi.yaml (stdlib scan of paths + scopes).
No real agent CLI is run (sh and fakeagent only). The SSE tests of the parallel story (`sse_test.go`) were failing
during the run and are not part of this change.
