# Work dashboard plan (AGED-EP-0007)

Status: approved 2026-11-22. Design canvas: https://claude.ai/artifact/187awNfuofy4KQZ2zYBLSG

One screen in agentd-desktop and agentd-app that shows, across hosts, what each agent is doing, what it
has pending and what needs the human (open questions, terminal menus, blocked tasks). The human answers
from that screen, to the agent's mailbox or to its prompt, without opening the session. Only agents write
tasks; the human reads status and gives feedback.

## Decisions

- D1 Desktop Panel has both views, "Por agente" (default) and "Tablero", behind a toggle (persisted).
- D2 Questions are mailbox messages (no second store). The answer is an ordinary `reply`.
- D3 Notifications: ntfy/webhook sinks in the daemon first; native push (FCM, iOS via APNs) later through
  agentd-relay acting as push gateway. The Firebase project is created by the owner later.
- D4 Humans never add or edit tasks. `tasks_set` / `task_update` are session-only ops.
- D5 Tasks live in daemon memory, per session, like turn records: sessions do not survive a daemon
  restart, so neither does their plan. Removed with the session (`rm`).

## Contract (normative for every story)

### Tasks

`proto.Task {id, title, status, note?, updated_at}`

- `id`: `[A-Za-z0-9._-]{1,64}`; when `tasks_set` gives none, the daemon assigns `t1`, `t2`... (first free).
- `status`: `pending | in_progress | blocked | done | cancelled`.
- `title` 1..200 runes, `note` 0..500 runes, both through `screen.Sanitize` (agent-written, untrusted).
- At most 50 tasks per session. Order = the agent's order.
- Op `tasks_set {tasks[]}` replaces the caller's list (idempotent; the shape matches harness todo tools).
  Op `task_update {id, status?, title?, note?}` changes one task (unknown id = `not_found`).
  Both: caller must be a running session (humans and relay devices `forbidden`). Answer `{tasks, revision}`.
- Op `tasks {name}` reads a list: humans any session; a session itself unless `[security] agent_view`.
  Relay devices: any scope, session globs applied. Answer `{name, tasks, revision}`.
- `revision`: per-session counter, bumps only when the list actually changes.
- Event `task.changed` (session = owner) data `{revision, counts{pending,in_progress,blocked,done,cancelled},
  current?, changed[{id, title, status, from?}]}`; `current` = title of the first `in_progress` task.
  `kinds` filter prefix `task`. Same visibility as other session events.
- `SessionInfo.tasks` = `{revision, counts, current?}` summary (omitempty), so `ls` and the subscribe snapshot
  already carry it.

### Questions

- Mailbox schema v4: columns `kind TEXT NOT NULL DEFAULT ''`, `options TEXT` (JSON array), `task TEXT`.
  `Message` gains `kind` (`""` | `"question"`), `options[]` (<= 8, each 1..80 runes, sanitized), `task` (a task id
  of the sender, not checked against the list). Ids and the AUTOINCREMENT counter kept (same rebuild rules as v2/v3).
- `send {to, subject, body, kind?, options?, task?}`. `kind: "question"` requires a session sender and a
  `human:<name>` recipient (`invalid` otherwise).
- An open question = `kind = "question"` and state not `replied`. Replying closes it (existing `reply`, body = the
  chosen option label or free text; the reply notice is injected into the agent as today).
- Event `mail.queued` data gains `kind` (omitempty). `session` keeps its meaning (the recipient when it is a
  session, so none for a question to a human); clients use `from`. Humans see it; the sending session sees it as
  mail it sent.
- CLI `agentd ask "<text>" [--option A]... [--task ID] [--to human:NAME]` (default recipient `human:<daemon user>`).
  MCP `agentd_ask {question, options?, task?}`.

### Board

- Op `board {since_digest?}` → `{sessions[{name, status, attention, turn, preview, tasks[], questions[]}], unread_human,
  digest, not_modified?}`; `questions` = open questions sent by that session (id, body, options, task, created_at);
  `unread_human` = unread mail to the caller (humans). Digest like `mailbox.Digest` over session revisions, task
  revisions and open question ids. Humans only on the socket; relay devices any scope with session globs, questions only
  for owner devices.
- CLI `agentd board [--json]`.

### Notifications

- New package `internal/notify`: sinks (ntfy, webhook), bounded queue, debounce, retries, payload; extracted from
  `internal/httpapi/notify.go`, which becomes a thin adapter (gateway behaviour unchanged).
- Daemon config `[notify]` (same keys as `[api.notify]`, plus reasons): taps the event bus in-process.
  Reasons: `question` (mail.queued with kind question to a human), `waiting`, `blocked` (task.changed with a task
  newly `blocked`), `turn_done`, `exited`. Default `on = ["question", "waiting", "blocked", "exited"]`. Off by default.

### Push (later stories)

- agentd-relay `[push]` with FCM credentials; daemon asks over the host link; payload encrypted for the device's relay
  key, or content-free (`private`). Device token registration through the relay tunnel.

## Phases

| Wave | Stories | Agent |
|---|---|---|
| 1 | AGED-US-0039 tasks, AGED-US-0040 questions, AGED-US-0044 notify sinks | parallel worktrees |
| 2 | AGED-US-0041 board, AGED-US-0042 relay gate + hc review, AGED-US-0043 hook adapter | after wave 1 merges |
| 3 | AGED-US-0045 desktop Panel; AGED-US-0046 mobile Panel (agentd-app agent) | after wave 2 |
| 4 | AGED-US-0047 relay push gateway, AGED-US-0048 mobile FCM client | needs the Firebase project |

Every phase: `gofmt`, `go vet`, `go test -race` of the touched packages; AGENTS.md contract updated in the same change.
