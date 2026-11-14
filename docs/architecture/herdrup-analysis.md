# herdrup analysis: what it adds on top of herdr, and what agentd could take

Status: research. Date: 2026-11-14. Related: [[herdr-compatibility-analysis]], [[agentd/plans/herdr-compat_plan.md]].

Sources (read only, nothing built or run): https://herdrup.app/, `github.com/jerryfane/herdr` (commit 32bf0dc,
Apache-2.0; "F" below) and `github.com/jerryfane/herdrup` (commit 5990f82, Apache-2.0; "A" below).

## 1. What herdrup is

An iOS/iPadOS/macOS app (plus a Linux/Windows/macOS desktop preview and a web guest) that runs on a **fork of the
Herdr TUI** (based on ~0.8.2), **not on herdr-core**. The fork adds about 56 socket methods to upstream's 102
(F `src/api/schema.rs:64-220`), the `pane.turn_completed` event, and the CLI commands `pair`, `gram`, `guest`,
`accounts` and a hidden `api-bridge`.

Consequence for agentd: herdrup clients speak the legacy TUI JSON socket API over `ssh host herdr api-bridge
[--multi]` (F `src/remote/api_bridge.rs:1-12`), not hc. Our hc layer does not serve them, and emulating the
legacy API (plus the fork's extensions and bespoke `pane.stream` NDJSON) is a second moving target. **Serving
herdrup clients is out of scope.** The question is which of its *features* are worth having in agentd.

## 2. What the fork adds (summary)

| Area | Fork | Notes |
|---|---|---|
| Transport | SSH exec `herdr api-bridge <b64>` per request, or `--multi` (NDJSON on stdin, 16 in flight, 1 MiB lines); streams on their own channels | F `src/remote.rs:26-43` |
| Pairing | `herdr pair`: one-time 32-byte token, raw TCP listener on the Tailscale IP (or `--lan`), QR `{v,host,port,user,token,fp}`; the phone sends its own ed25519 public key, which is **appended to `~/.ssh/authorized_keys` without a `command=` restriction** (marker `herdr-pair`) | F `src/pairing.rs:720-830`. The phone gets a full shell |
| Live terminal | `pane.stream`: NDJSON `{frame: reset/data/resize/ping/exited, seq, epoch, data_b64, history_b64, lagged}`; `pane.history` pages; `pane.input.stream` held input channel; per-viewer width leases (`pane.set_pty_size`, narrowest wins) | F `src/api/server/pane_output_stream.rs` |
| Gram | owner↔agent persistent messages: `gram.send` (agent → owner, push), `gram.post` (owner → agent, or `to:null` into a **grab queue** claimed atomically by `gram.grab`), chunked file attachments with sha256, digest polling | F `src/api/schema/gram.rs:13-134` |
| Push | kinds NeedsInput / Finished (optionally only turns the owner started: `turn_origin mine|other`) / Died / Gram; per-device preferences and muted panes; APNs direct (JWT) or via the publisher-hosted Cloudflare Worker `push.herdrup.app` | F `src/push/mod.rs`, `src/push/relay.rs` |
| AskPush | for **omp agents only**: the question and ≤4 options read from omp's session JSONL go into the notification; buttons call `agent.answer` / `agent.answer_form` over SSH | F `src/push/ask.rs`, A `AskPush/` |
| Turns | `pane.turns` records `{turn, outcome: completed|aborted (idle ≤15 s after Esc/Ctrl-C), message, turn_origin}`; composer author `human|api_client` | F `src/api/schema/panes.rs:808-839` |
| Transcript | `agent.transcript` (normalized entries, pending ask, model, plan, subagents) from omp JSONL; `agent.answer` confirms via the session file | F `src/agent_transcript/mod.rs` |
| Guests | share one agent with an outsider through `guest.herdrup.app` (Durable Object, Noise_IK end to end, short invite links, web guest); allow-list: watch, scrollback, labelled prompts, `agent.answer`, menu keys only while a menu is up; revoke + audit | F `src/api/server/guest_gate.rs` |
| Federation | coordinator polls peers (`agent.list` every 5 s + `events.subscribe`), rewrites ids `<alias>/…`; TCP federation with capability tiers; Gram over `ssh -R` | F `src/api/federation_relay.rs` |
| Accounts | credential profiles, usage fetch, `agent.restart {account}`, Claude↔Codex transcript transfer | F `src/api/schema/accounts.rs`, `src/session_transfer.rs` |
| Updates | staged builds + live PTY handoff (handoff is upstream), minisign-verified in-app update with a guard that rolls back once; refuses under a systemd main process (killed 30 panes) | F `src/update/guard.rs`, `src/server/supervision.rs` |

## 3. Recommendation for agentd

agentd already has: semantic view (status, options, dialog), `act` (prompt/choose/keys/interrupt), event bus,
mailbox between agents and `human:<name>`, ntfy/webhook notifications (in the frozen gateway), labels/documents,
`turn`, and the hc layer for Herdr clients. Herdr clients only see what hc exposes, so a feature reaches them only
through hc surfaces they already render (status records, labels, `turn`, documents).

### Worth adding (ranked)

1. **Turn records** — `turn` exists; add per turn `outcome` (`completed` | `aborted`: back to idle within 15 s of
   an interrupt/cancel act or a human Ctrl-C/Esc), `origin` (`human` | `act` | `mail` | `peer`, from
   `LastHumanInput` / `LastActInput` / delivery) and the last transcript line; op `turns {name}` + event
   `turn.completed`. Cheap, uses what the view already knows; feeds hc `turn` and notifications.
2. **"Only my turns" notification filter** — with `origin`, notify `turn_done` only for turns a human started,
   not for agent-to-agent mail traffic. Removes most notification noise.
3. **Agent → human messages as notifications (Gram `gram.send`)** — agentd's mailbox already accepts
   `human:<name>`; add notification reason `mail` when a session sends to a human, and expose unread human mail
   to Herdr clients as an OSC 7501-style status *record* (`records.mail`, shown by Desktop) or a label.
4. **Shared work queue (Gram grab queue)** — `send {to: "queue:<name>"}` + `claim` op (atomic, first claim
   wins, state `claimed` → normal delivery to the claimer). Fits agentd's orchestration role: an owner or a
   coordinator agent posts tasks, idle agents pull them. Small mailbox schema change.
5. **Answer from the notification (AskPush)** — agentd detects menus for Claude/Codex from the screen
   (`view.options`), which the fork cannot (omp only). Put option ids/labels + `revision`/`epoch` in the
   notification payload; the answer goes through `act choose` (all its instance/revision guards apply). Needs an
   authenticated callback, i.e. the frozen gateway or a future one: design now, build when a client exists.
6. **Mailbox attachments** — chunked, sha256, size-capped, stored beside `mailbox.db`; useful for screenshots
   and logs between owner and agents. Medium cost.
7. **Conditional inbox fetch** (digest/ETag on `inbox`) — trivial, cuts polling cost for MCP and clients.

### Not worth it / out of scope

- **SSH-exec API and `pair` writing an unrestricted `authorized_keys` line** — grants the phone a full shell;
  agentd's model (system sshd + user-managed keys, or scoped tokens) is safer. Do not copy.
- **Serving herdrup clients** (legacy TUI API + fork extensions) — second protocol, not documented, moving.
- **APNs direct/relay, Live Activities** — needs an Apple developer account and a hosted relay; ntfy covers
  push until agentd ships an iOS client.
- **Guest relay** (hosted Noise_IK relay, invites, web guest) — large hosted surface. The useful core, if ever
  wanted: a local `guest` capability limited to watch + answer menus + labelled prompts on one session.
- **Federation** — clients aggregate machines themselves (Herdr Desktop/mobile already do over SSH); a
  daemon-to-daemon trust model would widen agentd's identity model.
- **Accounts, usage fetch, account swap, Claude↔Codex transcript transfer** — handle harness credentials and
  formats; agentd stays harness-agnostic.
- **Self-update with live handoff** — agentd cannot hand off PTYs; release tags + restart suffice. Lesson kept: a
  future re-exec must check systemd `KillMode`.
- **Width leases** — agentd sizes are fixed per profile (ADR-A02) and hc gives "last active viewer wins".
- **Transcript readers (JSONL)** — useful to confirm answers, but per-harness formats; revisit only for
  Claude/Codex if screen-based confirmation proves unreliable.

## 4. Proposed placement in the plan

Items 1-3 fit Phase 3 of [[agentd/plans/herdr-compat_plan.md]] (status/notifications); item 4 is a mailbox
feature independent of Herdr; items 5-7 wait for a client that uses them.
