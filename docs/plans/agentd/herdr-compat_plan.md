# Plan: Herdr client compatibility (herdr-core protocol, generation 1)

Analysis: `docs/architecture/herdr-compatibility-analysis.md`. Links: [[herdr-compatibility-analysis]],
[[plans/api-gateway-plan]] (gateway frozen, not extended).

Decisions (2026-11-14, user): remove the Flutter app (`app/`); freeze `agentd serve` (kept, no new
features); start with the proof of concept below.

## PoC (done 2026-11-14, Desktop GUI check pending: [[herdr-compat-poc]])

Scope: Herdr Desktop, pointed at agentd's shim (settings `core` path), connects, lists agentd sessions
and attaches to one.

- `internal/hcompat` (depends on `client`, `proto`, stdlib):
  - `wire.go`: 9-byte LE frames, kinds 0..6, 16 MiB cap.
  - `server.go`: plain JSON-RPC lines until `hello {framed:true}`, then frames; channel-0 calls
    concurrent; per-channel credit (256 KiB initial window).
  - methods: `hello`, `server.status`, `events.subscribe` (ls snapshot + daemon subscribe translated:
    created/finished/removed/status/turn), `terminal.list/get/attach/kill/read/run/label/keep`,
    `doc.get/doc.set` (in-memory per connection), `machine.list` (empty), `templates.list` (empty).
  - address = agentd session name (stable, no `/`); labels: `herdr.space` = project label, `herdr.tab`
    = session name, overlay per connection.
  - status mapping: working→working, waiting→blocked, idle→idle (done after a turn), exited→null.
- `cmd/agentd`: argv[0] `herdr-core`/`hc` dispatch and `agentd hc ...`: `--version` (`hc 0.1.21`),
  `connect [--session S]`, `machine ls --json`, `install-shim [--dir]` (symlinks, never overwrites).
- Tests: protocol test drives `hcompat.Serve` over pipes like Desktop (hello line, frames, subscribe
  snapshot, attach shows a `sh`/fakeagent session's output, input round trip, CLOSE on exit).

## Phase 1 (done 2026-11-14: [[herdr-compat-phase1]])

- Daemon: op `label` + `run {labels}` + `SessionInfo.labels` + event `session.labels`; ops `doc_get`/`doc_set`
  (versioned, `docs.json`, event `doc.changed`, humans only for writes); `turn` in view state, `ls` and
  `status.changed`.
- hcompat: stateless (labels/keep/docs/turn from the daemon), events from `session.labels` / `doc.changed`,
  attach drained always + bounded backlog + repaint, results validated against the released OpenRPC schema.
- Decision: hc addresses stay agentd session names (stable across `hc connect` processes and readable) instead
  of `tN`; names are reused only after `rm`, when hc clients already dropped the terminal.

## Phase 2 (done 2026-11-14: [[herdr-compat-phase2]])

Daemon attach frames `A` (automatic input) and `F` (focus, DEC 1004 reports), attach options `observe`,
`passive`, `history`; hcompat maps AUTOMATIC_INPUT (advertised), FOCUS, observe, resumed, history; repaints
reattach passive. FACTS ignored; `resize_notifications` not offered (decided: observers use size events).

## Phase 3..4
See analysis §10. Next: Phase 3 (send/keys/interrupt/wait, OSC 7501 parsing, turn records with outcome/origin and
"only my turns" notifications from [[herdrup-analysis]], `[herdr]` config + audit), then Phase 4 (official Herdr
mobile app capture, Whip, hc socket listener).
