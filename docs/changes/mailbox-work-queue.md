# Shared mailbox work queues and conditional inbox fetch

Builds on [[herdrup-analysis]] §3 items 4 (Gram grab queue) and 7 (conditional inbox fetch); plan
[[agentd/plans/mailbox-queue_plan.md]].

## What changed

- **Work queues**: `send {to: "queue:<name>"}` (name `[a-zA-Z0-9._-]{1,64}`) stores an `unclaimed` message
  (`recipient = "queue:<name>"`, `queue = <name>`). New op `claim {queue, id?, inject?}` (sessions only) assigns the
  oldest (or the given) unclaimed item to the caller in one `BEGIN IMMEDIATE` transaction; first claim wins
  (`not_found` for an empty queue or a foreign id, `conflict` for an item already claimed). The item becomes a normal
  message of the claimer (`recipient` = claimer, `claimed_at` set, `queue` kept), so read, reply (to the poster) and
  purge work unchanged. The claim answer is the fetch (`delivered`); `inject: true` leaves it `queued` so the delivery
  loop types its notice in. The delivery loop only reads `queued`, so unclaimed items are never injected.
- **ACL**: posting = `[acl] "queue:<name>"` (same lookup as any recipient, `"*"` fallback, default allow). Claiming =
  new table `[claim] "<name>" = ["session-glob", ...]` (`"*"` fallback; no entry = any session; empty list = nobody).
  Humans never claim (`forbidden`).
- **Listing**: new op `queue {name?, bodies?, since_digest?}`: items of a queue (bodies only with `bodies: true`) plus
  `digest`, or every non-empty queue with its count. Humans see every queue; a session only those it may claim from.
- **Events**: queue post = `mail.queued` with no `session`, `to: "queue:<name>"`, `queue`; it is also shown to the
  session subscribers allowed to claim from that queue. Claim = `mail.claimed {id, from, to, queue, by}` (`session` =
  claimer), then `mail.delivered` unless `inject`.
- **Conditional inbox**: `inbox` answers carry `digest` (`d` + 16 hex of SHA-256 over `(id, state)` of the listing);
  `since_digest` equal to the current one returns `{messages: [], digest, not_modified: true}` without reading
  bodies or changing states. Same for `queue {name, since_digest}`. All new fields are `omitempty`.
- **Schema v3**: table rebuild like v2 (state CHECK gains `unclaimed`, new columns `queue`, `claimed_at`), ids and
  the AUTOINCREMENT counter kept, followed by `PRAGMA foreign_key_check`. Retention purges unclaimed items by age;
  `purge --recipient queue:<name>` / `--state unclaimed` work for humans.
- **CLI**: `agentd send queue:NAME ...` (prints "posted … (unclaimed)"), `agentd claim QUEUE [ID] [--inject] [--json]`,
  `agentd queue [NAME] [--bodies] [--since-digest D] [--json]`, `agentd inbox --since-digest D` (with `--json` it
  prints the whole answer, digest included).
- **MCP**: `agentd_claim {queue, id?}`, `agentd_queue {name?}` (never bodies), `agentd_inbox.since_digest` →
  `digest`, `not_modified`; messages carry `queue`.

## Files and symbols

- `internal/mailbox/mailbox.go`: migration v3, `StateUnclaimed`, `ErrClaimed`, `Message.Queue/ClaimedAt`,
  `selectCols`/`scanMessage`, `Send` (queue addresses), `insert`, `State.Valid`.
- `internal/mailbox/queue.go` (new): `QueuePrefix`, `ValidQueueName`, `QueueAddr`, `QueueOf`, `ClaimOptions`,
  `Store.Claim`, `QueueItems`, `Queues`, `QueueCount`, `Digest`, `InboxDigest`, `QueueDigest`.
- `internal/config/config.go`: `Config.Claim` (`[claim]`), glob validation, `Config.ClaimAllowed`.
- `internal/proto/proto.go`: `OpClaim`, `OpQueue`, `ClaimArgs`, `QueueArgs`, `QueueCount`, `QueueResult`,
  `InboxArgs.SinceDigest`, `InboxResult.Digest/NotModified`, `Message.Queue/ClaimedAt`, `EvMailClaimed`,
  `MailEventData.Queue/By`.
- `internal/daemon/queueops.go` (new): `claim`, `queueList`; `mailboxops.go` (`send` queue posts, `inbox` digest);
  `events.go` (`busEvent.queue`, `eventFilter.canClaim`, `bus.publishQueue`, `publishQueueMail`, known kind);
  `handlers.go` (dispatch, `toWire`); `retention.go` (state list in the error).
- `internal/client/client.go`: `InboxWith`, `Claim`, `Queue`.
- `cmd/agentd/queue.go` (new): `claimCmd`/`newClaimCmd`, `queueCmd`/`newQueueCmd`; `main.go` (root, send, inbox).
- `internal/mcpserver/mcpserver.go`: `agentd_claim`, `agentd_queue`, inbox digest.
- Tests: `internal/mailbox/queue_test.go`, `internal/config/config_test.go` (`TestClaimAllowed`),
  `internal/daemon/queue_test.go`, `cmd/agentd/queue_test.go`, `internal/mcpserver/queue_test.go`,
  `internal/mcpserver/mcpserver_test.go` (tool list).
- Docs: `AGENTS.md` (package table, Mailbox states, Work queues, Conditional fetch, ACL, Recipients, Ops, MCP, Event
  bus, Identity), `README.md`.

## Why

agentd orchestrates several agents; a coordinator (human or agent) needs to post tasks that whichever agent is
free picks up, without choosing a recipient up front. Pollers (MCP tools, scripts) also re-downloaded the whole
inbox on every call; a digest makes an unchanged poll cheap.

## Design notes

- No new delivery path: a claimed item is just a message of the claimer, so injection, read, reply, purge and
  events reuse the existing code.
- Default claim = fetched (`delivered`): the caller already holds the body, typing the notice into its terminal
  would duplicate the task. `inject` exists for claims made by scripts on behalf of the session.
- No wake-up of idle sessions on a post (agents pull); session subscribers allowed to claim see `mail.queued`.
- Not done: no queue attachments, no unclaim/release op, no per-queue retention.

## How it was verified

- `gofmt -l .` empty, `go vet ./...` clean.
- `go test -race ./internal/mailbox ./internal/daemon ./internal/delivery` and `go test ./...` pass (v2 → v3
  migration on a v2 file with data and a burnt id, 12 concurrent claimers on 5 items and on one item, purge of
  queue items, digest stability; daemon ACL/identity/listing/events/double claim/delivery after claim with the
  fake agent; CLI argument handling; MCP tools end to end against an in-process daemon).
- The KB / code-index / Context7 tools named in AGENTS.md were not available in this environment: the plan and
  this record are kept in the repository instead.
