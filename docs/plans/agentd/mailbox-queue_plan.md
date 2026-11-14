# Plan: shared mailbox work queues and conditional inbox fetch

Source: [[herdrup-analysis]] §3 items 4 (Gram grab queue) and 7 (conditional inbox fetch).
Change record: [[agentd/changes/mailbox-work-queue.md]].

## Design

- **Address** `queue:<name>`, name charset `[a-zA-Z0-9._-]{1,64}` (same as `human:<name>`).
- **State** `unclaimed` (new, schema v3): a queue post is stored with `recipient = "queue:<name>"`,
  `queue = <name>`, `state = unclaimed`. The delivery loop only reads `queued`, so it never injects an unclaimed
  item. Claim moves it to `recipient = <claimer>`, `state = queued` (or `delivered`, see below),
  `claimed_at = now`; `queue` keeps the origin. From there it is a normal message (inbox, read, reply to the
  original sender).
- **Claim**: op `claim {queue, id?, inject?}`, sessions only (humans: `forbidden`; a human assigns work with a
  plain `send`). One `UPDATE ... WHERE id = (oldest unclaimed) AND state = 'unclaimed' RETURNING` inside a
  `BEGIN IMMEDIATE` transaction: first claimer wins; an empty queue or an id already claimed is `not_found`
  (id claimed by someone else: `conflict`). Default: the claim response is the fetch, the message is
  `delivered` (like `inbox` on one's own mailbox); `inject: true` leaves it `queued` so the delivery loop types
  the notice into the claimer's terminal.
- **ACL**: posting uses `[acl] "queue:<name>"` exactly like a recipient (falls back to `"*"`, default allow).
  Claiming uses a new table `[claim] "<name>" = ["session-glob", ...]` (falls back to `"*"`, default: any
  session). An empty list denies everyone.
- **Listing**: op `queue {name?, bodies?, since_digest?}`. Without name: `{queues[{name, unclaimed}]}`. With
  name: `{queue, items[], digest}` of unclaimed items, ordered by id; bodies omitted unless `bodies: true`.
  Humans may list any queue; a session only queues it may claim from.
- **Events**: `mail.queued` for a post (`session` empty, data `{id, from, to: "queue:<name>", queue}`),
  `mail.claimed {id, from, to, queue, by}` (`session` = claimer), then `mail.delivered` when claimed without
  `inject`. A session subscriber sees queue posts for queues it may claim from (no body).
- **Conditional inbox**: `inbox {..., since_digest?}` → result always carries `digest`; when it equals
  `since_digest` the result is `{messages: [], not_modified: true, digest}`. Digest = `d` + 16 hex of
  SHA-256 over `(id, state)` of the listed rows (computed in SQL without bodies). Old clients ignore the
  fields (omitempty).
- **Purge/retention**: unclaimed items are plain rows: retention purges them by age; `purge --recipient
  queue:<name>` / `--states unclaimed` work for humans. Sessions still purge only their own read/replied mail.

## Phases

1. **mailbox**: schema v3 (table rebuild like v2: ids + AUTOINCREMENT kept, `foreign_key_check`), state
   `unclaimed`, columns `queue`, `claimed_at`; `Store.Post`, `Claim`, `QueueItems`, `Queues`, `InboxDigest`,
   `Digest`. Tests: v2 file with data → v3, concurrent claimers (`-race`), purge of queue items.
   `go test -race ./internal/mailbox`.
2. **config + proto + daemon**: `[claim]` table and `Config.ClaimAllowed`; proto `OpClaim`, `OpQueue`, args
   and results, `EvMailClaimed`, `MailEventData.queue/by`, inbox digest fields; handlers, event filter for
   queue posts. Tests: ACL, identity, events, delivery after claim, double claim.
   `go test -race ./internal/daemon ./internal/delivery ./internal/config`.
3. **client + CLI + MCP**: `Client.Claim`, `Queue`, `InboxWith`; `agentd claim`, `agentd queue`,
   `agentd inbox --since-digest`; MCP `agentd_claim`, `agentd_queue`, `agentd_inbox.since_digest`. Tests in
   the existing CLI/MCP patterns. `go test ./cmd/agentd ./internal/mcpserver ./internal/client`.
4. **docs**: AGENTS.md (Mailbox states, Ops, Identity, Event bus, MCP, ACL), change record. Full
   `go test ./...`, `gofmt`, `go vet`.

## Status

- [x] Phase 1
- [x] Phase 2
- [x] Phase 3
- [x] Phase 4
