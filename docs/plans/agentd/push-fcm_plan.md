# Push notifications through Firebase Cloud Messaging (plan)

Started 2026-11-25. Continues [[dashboard_plan]] (daemon notifications `[notify]`, questions) and
[[backlog-notes_plan]] (relay devices, revoke). Mobile side lives in `agentd-app` and is done by that project's agent
against this contract.

## Goal

The phone gets a system notification when an agent needs the human to decide something, even when the agentd app
is closed or not connected; a tap opens the app on that host and question. Only blocking items:

- `question`: an `agentd ask` question addressed to a human (`mail.queued`, kind `question`, `to` = `human:*`).
- `permission`: an agent shows a permission/choice dialog: `attention.changed` set with reason `waiting` while the
  session's view has options (a menu) at that moment.

## Decisions (user, 2026-11-25)

1. Every daemon sends directly to FCM HTTP v1 with a Google service account key stored on that host (0600). No
   relay involvement; works for hosts reached over SSH or relay.
2. Triggers: `question` + `permission` by default.
3. No agent-written text in the payload unless `include_preview = true` (the payload goes through Google).
4. The app's Firebase config (`google-services.json`) stays out of git (`agentd-app` is public, MIT); CI gets it from
   a GitHub secret. Consequence: push works with the user's own Firebase project; another agentd user builds the
   app with their own project and gives their daemons their own key.

## Contract (normative)

### Config `[push]` (`internal/config/push.go`)

```toml
[push]
enabled = false
credentials_file = "~/.config/agentd/fcm-service-account.json"  # service account JSON
on = ["question", "permission"]   # also accepted: waiting, blocked, exited, turn_done
include_preview = false
debounce = "30s"                  # per session + reason
host_label = ""                   # default: hostname
```

- `credentials_file`: `~` expanded, absolute after expansion. The daemon refuses to use it (push stays off, cause
  logged, never the content) unless it is a regular file owned by the user with no group/other bits. It must be a
  service account JSON (`type = service_account`, `project_id`, `client_email`, `private_key` PEM PKCS#8,
  `token_uri`); `project_id` is the FCM project.
- Unknown `on` reasons are a config error.

### Package `internal/push` (stdlib only)

- `Credentials` (parsed service account), `TokenSource`: OAuth 2.0 JWT bearer grant (RS256 JWT, `iss` =
  client_email, `scope` = `https://www.googleapis.com/auth/firebase.messaging`, `aud` = token_uri, `exp` = now + 1 h)
  posted to `token_uri`; access token cached until 60 s before expiry; one refresh on a 401 from FCM.
- `Client.Send(ctx, Message) error` → `POST https://fcm.googleapis.com/v1/projects/<project_id>/messages:send`.
  Errors classified: `ErrUnregistered` (404 `UNREGISTERED`, 400 `INVALID_ARGUMENT` about the token) = drop the
  token; retryable (429, 5xx, network) = retry with backoff (3 tries); anything else = log and give up.
  No redirects, 10 s timeout, response bodies read capped and never logged.
- `Store`: `$STATE/push/tokens.json` (dir 0700, file 0600, atomic rename): `[{id, token, platform, name, host_ref,
  device?, created_at, last_ok_at?, last_error?}]`; `id` = `p` + first 12 hex of SHA-256(token); at most 32 tokens
  (oldest evicted); registering the same token again updates it (no duplicate).
- Never log tokens, access tokens, the private key or payload text. Logs name the token `id` only.

### Message

```json
{"message": {
  "token": "<token>",
  "notification": {"title": "<session> te pregunta" | "<session> pide permiso", "body": "<host_label>[ · <text>]"},
  "data": {"v": "1", "host": "<host_label>", "host_ref": "<host_ref given at register>", "session": "<session>",
           "reason": "question|permission|...", "message_id": "<id, question only>", "link": "agentd://open?h=...&s=..."},
  "android": {"priority": "HIGH", "collapse_key": "<session>",
              "notification": {"channel_id": "agentd_attention", "tag": "<host_label>/<session>/<reason>"}}
}}
```

`<text>` (question text or the dialog question, sanitized, <= 160 runes) only with `include_preview`. Other reasons
use titles "<session> espera tu respuesta", "<session> tiene una tarea bloqueada", "<session> terminó".

### Ops (wire `internal/proto/push.go`)

- `push_register {token, platform, name, host_ref?}` → `{id}`. Callers: humans on the socket (local, SSH) and owner
  relay devices of any scope (not guests). A relay registration records `device`; revoking that device removes
  its tokens. Token 1..4096 printable ASCII; platform `android|ios|macos|other`; name <= 64 runes sanitized;
  host_ref <= 128 printable ASCII. A session is `forbidden`.
- `push_unregister {token? | id?}` → `{removed}`. Humans any; a relay device only its own tokens.
- `push_ls` → `{tokens[{id, platform, name, host_ref, device?, created_at, last_ok_at?, last_error?}], enabled,
  project_id?, reason?}` (never the token). Humans only (relay devices: own entries).
- `push_test {id?}` → `{sent, errors[]}`: a test notification to one or every token. Humans only.
- `relayGate`: `push_register`, `push_unregister`, `push_ls` for owner devices (any scope); `push_test` never.

### Daemon tap (`internal/daemon/push.go`)

Same shape as `[notify]`: an in-process bus subscriber started in `Serve` when `[push] enabled` and the credentials
load; stopped first in `Shutdown`. Kinds: `mail.queued`, `attention.changed` (+ `task.changed`, `session.exited`
for the optional reasons). `permission` = attention set, reason `waiting`, and the session's current view has at
least one option; otherwise a `waiting` attention maps to `waiting` (only sent if listed in `on`). Every token gets
the message; `ErrUnregistered` removes the token. Reuses `internal/notify` for queue/debounce/retry through a new
`Sender` sink hook, or its own worker with the same rules (implementer's choice; same guarantees).

### CLI

`agentd push ls [--json]`, `agentd push test [ID]`, `agentd push rm ID`. (`push_register` is for apps; a hidden
`agentd push register --token T --platform P --name N` exists for tests and scripts.)

## Phases

| # | Phase | Test |
|---|---|---|
| P1 | proto + config `[push]` | `go test ./internal/config ./internal/proto` |
| P2 | `internal/push`: credentials, JWT/OAuth token source, FCM client, token store (httptest fakes) | `go test -race ./internal/push` |
| P3 | daemon ops, relay gate, tap (question + permission), revoke hook | `go test -race ./internal/daemon` |
| P4 | CLI, AGENTS.md, README, change note | `go test ./cmd/agentd` |
| P5 | mobile (agentd-app agent): firebase_messaging, channel, register/unregister per host, tap routing | flutter tests there |
| P6 | end to end with the user's phone and key | manual |

## User steps (not automatable here)

1. Firebase console > Project settings > Service accounts > Generate new private key; save it on every host as
   `~/.config/agentd/fcm-service-account.json`, `chmod 600`. Never commit it.
2. `[push] enabled = true` in `~/.config/agentd/config.toml`; restart the daemon (kills running sessions).

## Status

- 2026-11-25: plan written; P1-P4 started.
- 2026-11-25: P1-P4 done (proto, config, `internal/push`, daemon ops + tap + relay gate + revoke hook, CLI, docs); tests pass. P5 (app) and P6 (end to end) pending. Change note: [[changes/push-fcm]].
