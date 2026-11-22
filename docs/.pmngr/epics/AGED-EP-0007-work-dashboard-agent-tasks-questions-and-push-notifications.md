---
id: AGED-EP-0007
type: epic
title: "Work dashboard: agent tasks, questions and push notifications"
status: in_progress
priority: high
labels: [dashboard]
created: 2026-11-22T08:14:29Z
updated: 2026-11-22T08:24:03Z
started: 2026-11-22T08:24:03Z
---

## Description
One screen in agentd-desktop and agentd-app that shows, across every host, what each agent is working on,
what it has pending, and what needs the human: open questions, terminal menus, blocked tasks. The human
answers from that screen (to the agent's mailbox or straight to its prompt) without opening the session.
Agents report their tasks and questions through new CLI commands and MCP tools. Anything that needs the
human raises a notification: local on the desktop, push on the phone.

Design proposal (to validate before the stories are written): https://claude.ai/artifact/187awNfuofy4KQZ2zYBLSG

## Model (proposed)
- **Tasks**: per-session plan kept by the daemon. `{id, title, status: pending|in_progress|blocked|done|cancelled, note, updated_at}`,
  at most 50 per session, titles <= 200 chars, sanitized like view text (agent-written, untrusted). Stored in SQLite
  next to the mailbox; kept until the session is removed.
  - Op `tasks_set {tasks[]}` (replace the whole list, idempotent, same shape as the harness todo tools) and
    `task_update {id, status?, note?}`. A session writes only its own list; humans may read any.
  - CLI `agentd task set|add|start|block|done|ls`. MCP `agentd_tasks_set`, `agentd_task_update`.
- **Questions**: a mailbox message to `human:<user>` with `kind: "question"`, optional `options[]` and an optional
  `task` it blocks (mailbox schema v4). The answer is an ordinary `reply`, so the delivery loop types it into the
  agent when it is idle. No second store, no new state machine.
  - CLI `agentd ask "<text>" [--option A --option B] [--task ID]`. MCP `agentd_ask`.
- **Board**: op `board {since_digest?}` → per session `{status, attention, turn, tasks[], counts, open questions,
  preview}` + digest, so a client paints the screen with one call per host. Events `task.changed {session, counts}`
  and the existing `mail.queued` (now with `kind`).
- **What "needs you" means**: open questions, sessions in `waiting` (menus and free-text prompts already detected
  by the semantic view), blocked tasks, unread mail to the human.
- **Answering from the board**: question → `reply` (mailbox) or `act prompt` (prompt, `force` while working);
  terminal menu → `act choose` with `revision` + `epoch` (unchanged rules).
- **Relay gate**: `board` and task reads for any scope; `reply`/`act` as today (`operate`, owners only for mail).
- **Agents that do not report**: the board falls back to status + preview line. A Claude Code hook adapter
  (`agentd task sync`, fed by the todo tool hook) fills the list with no change to the agent's prompt.

## Notifications
- Desktop: local notifications from the live `subscribe` stream (exists); add questions and blocked tasks.
  No push service: native desktop apps cannot receive FCM.
- Mobile, step 1: notify sinks in the daemon (ntfy + webhook, ported from the frozen gateway's `[api.notify]`).
  Works with the ntfy app, no backend of ours.
- Mobile, step 2: native push. FCM always needs a server holding the Firebase credential, so a phone-to-phone or
  daemon-only setup is not possible without shipping that secret. Proposal: `agentd-relay` gains a push endpoint
  (`[push] fcm_credentials`); the daemon asks it over the host link to wake a paired device; the payload is opaque
  to the relay and to Google (encrypted for the device key, or content-free in private mode). iOS goes through FCM → APNs.

## Decisions (2026-11-22)
- Desktop Panel: both views behind a toggle.
- Questions are mailbox messages; the answer is a reply.
- Notifications: daemon ntfy/webhook sinks first, relay as FCM push gateway later (Firebase project pending, owner).
- Humans never add tasks: only agents write the board; the human gives feedback and reads status.
- Tasks are kept in daemon memory per session (sessions do not survive a restart either).

Plan and contract: [[plans/agentd/dashboard_plan]]. Stories AGED-US-0039..0048.
