# AGED-US-0018: SSE streams (`/v1/events`, `/v1/sessions/{name}/view/stream`)

Plan: [[plans/api-gateway-plan]]. Design: [[design/api/http-api]] section 4. Builds on [[AGED-US-0014-op-subscribe]].

## What changed

- Gateway event hub: ONE daemon `subscribe` (all kinds) per gateway, started lazily on the first stream,
  reconnecting with backoff (250 ms to 5 s) using `since_seq` + `epoch`. Events fan out to HTTP subscribers
  through a bounded per-client queue (256); overflow replaces the queue by one `resync` (reason `overflow`)
  and the client continues. A ring of `event_buffer` events (default 1024) is keyed by `<epoch>:<seq>`.
  Daemon `resync` / epoch change clears the ring and sends `resync` to all clients.
- `GET /v1/events` (read): query `session`/`kind` (aliases `sessions`, `kinds`, `types`; kinds exact, prefix or
  `mail.*`). Frames `id`/`event`/`data`; `retry: 3000`; first `hello` `{epoch, seq, last_id, api_version}`;
  `Last-Event-ID` (or `?last_event_id=`) replays from the ring or sends `resync` (`since_seq_too_old`,
  `epoch_mismatch`); `: ping` every `sse_heartbeat` (15 s default); `bye` `{reason: shutdown}` on drain.
- `GET /v1/sessions/{name}/view/stream` (read, `tail` 1..5000): first a full `view`, then `view.patch`
  `{from_revision, revision, epoch, first_line, splice:{at, delete, insert[]}, set:{changed fields}}` computed
  from consecutive views; fetch on `view.changed`/`attention.changed` with singleflight per session+tail,
  coalesced to at most one view event per 100 ms. `status.changed`/`input.changed` are forwarded;
  `session.exited` (after a final patch) and `session.removed` end with `bye`. A patch not smaller than the full
  view is sent as `view`. Event id `e<epoch>-r<revision>`; `Last-Event-ID` with a revision still cached yields a
  patch from it. After `resync` the next view event is full.
- Headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `X-Accel-Buffering: no`; flush per
  frame; per-write deadline; handlers stop on client disconnect and on `Server.Draining`.
- New `Server.Close()` (drain, stop hub, close pool) for callers using `Handler()` directly; `Serve` also waits
  for the hub.
- OpenAPI: `/v1/events` and view stream descriptions, `session`/`kind` params, new schema `ViewStreamPatch`.

## Files and symbols

- `internal/httpapi/sse.go`: `startSSE`, `sseWriter`, `controlPayload`, defaults.
- `internal/httpapi/events.go`: `hub`, `subscriber`, `eventFilter`, `hub.attach/detach/session/handle`,
  `Server.handleEvents`, `parseEventID`.
- `internal/httpapi/viewstream.go`: `flightGroup`, `viewCache`, `hub.fetchView`, `diffViews`, `applyViewPatch`
  (reference client), `Server.handleViewStream`, `viewStream`.
- `internal/httpapi/httpapi.go` (`hub`, `sseQueue` fields, `Close`, `Serve`), `routes.go` (two routes, features
  `events`, `view_stream`), `docs/api/openapi.yaml`.
- Tests: `internal/httpapi/sse_test.go`.

## Why

Mobile/web clients need push for the session list, notifications and live views without polling or one daemon
connection per browser tab.

## Verification

`gofmt -l .`, `go vet ./...`, `go test -race -count=1 ./internal/httpapi` (repeated -count=3 on the SSE tests).
Tests (sh and cat sessions, temp daemon, test Authenticator): hello + live events and headers, filters,
Last-Event-ID resume (no gaps, no duplicates), resync on garbage/foreign/future/out-of-ring ids, slow subscriber
overflow does not block others, 12 clients => exactly one daemon subscribe, heartbeat with 40 ms interval,
`bye` on `Close`, goroutine count stable over 30 connect/disconnect cycles, view stream patches applied equal a
fresh `view` after every burst and view events are at least ~100 ms apart, `diffViews`/`applyViewPatch` random
property test, exit/removal end with `bye`, singleflight collapses concurrent fetches.
