# AGED-US-0014: op `subscribe` and the daemon event bus

Plan: [[plans/api-gateway-plan]]. Design: [[design/api/daemon-ops]] section 6. Builds on [[AGED-US-0012-op-view]].

## What changed

- New op `subscribe {sessions?, kinds?, since_seq?, epoch?, heartbeat_ms?}`: after an OK line
  (`{epoch, seq, sessions[]}`) the connection becomes a newline-JSON event stream
  `{seq, epoch, time, kind, session?, data}`.
- Daemon event bus: monotonic `seq` per epoch, 1024-event replay ring, per-subscriber queue of 256.
  `publish` never blocks; on overflow the queue is dropped and one `resync` is queued, then live.
  `since_seq` inside the ring replays then goes live; outside it, or other epoch, `resync` first.
- Kinds: hello, session.created/started/exited/removed, status.changed, view.changed, input.changed,
  attention.changed, mail.queued/delivered/read (no bodies), resync, heartbeat (15 s), bye (shutdown).
- Identity: humans see everything; sessions only their own events and own mail
  (`[security] agent_view` lifts the session-event limit, never the mail one).
- View events are published from `viewState.refreshLocked` / `refreshAfterAck` via
  `publishChangesLocked` only when status, attention, input state or revision changed.
- `delivery.Loop.OnDelivered` callback (behaviour unchanged) feeds `mail.delivered`.
- Client: `client.Subscribe(args, buffer)` -> `*Subscription{Info, Events, Err(), Close()}`.
- CLI: `agentd events [--session X] [--kind K] [--since-seq N] [--json]`.
- Shutdown: `bus.close()` before connections are closed; subscribers write `bye` and end.

## Files and symbols

- `internal/daemon/events.go` (new): `bus`, `subscriber`, `eventFilter`, `handleSubscribe`, `Server.publish`,
  `publishMail`, `Server.HeartbeatInterval`.
- `internal/daemon/{server,handlers,mailboxops,delivery,view}.go`: hooks (`New`, `Shutdown`, `handleConn`,
  `run`, `rm`, `send`, `reply`, `inbox`, `read`, `NewDeliveryLoop`, `publishChangesLocked`).
- `internal/delivery/delivery.go`: `Loop.OnDelivered`.
- `internal/proto/proto.go`: `OpSubscribe`, `Ev*`, `Resync*`, `SubscribeArgs/Result`, `Event`, data types.
- `internal/client/subscribe.go`, `cmd/agentd/main.go` (`eventsCmd`), `AGENTS.md` ("Event bus").

## Why

The API gateway needs one push stream (lifecycle, status, view revisions, mail) instead of polling `ls`/`view`.

## Verification

`gofmt -l .`, `go vet ./...`, `go test -race -count=1 ./internal/daemon ./internal/proto ./internal/client
./internal/delivery ./cmd/...`. Tests: `internal/daemon/events_test.go` (ordering, filters, replay, resync on
ring miss/epoch/future seq, slow reader does not block publishers, heartbeat, bye, unregister, identity
filtering without bodies, lifecycle + status sequence with `sh`, mail queued/delivered with fakeagent),
`events_internal_test.go` (exactly one resync on overflow, ring wrap), `proto_test.go` (event JSON shape).
