# Design: HTTP API gateway (`agentd serve`)

Status: **draft** (2026-11-13). Plan: [[plans/api-gateway-plan]] (stories API-S13…S17, ADR-A01…A11
are binding). Siblings: [[design/api/semantic-model]] (the `View` type), [[design/api/daemon-ops]]
(ops `view`, `act`, `subscribe`, error codes), [[design/api/security-remote]] (tokens, pairing, TLS,
CORS, rate limits, audit). Contract: `docs/api/openapi.yaml` (OpenAPI 3.1, draft).

This page defines the HTTP surface only. It never redefines the internals of `View`, `act` or the
daemon event stream: the gateway is a thin translator, like `internal/mcpserver`.

## 1. The `agentd serve` process

A separate, long-running process (ADR-A01). The daemon keeps no TCP listener; `serve` talks to it
over the unix socket and is therefore resolved as `human:<user>` (ADR-A06). It is never auto-started
by the CLI; users run it by hand or via `contrib/agentd-serve.service` (systemd `--user`).

```
agentd serve [--listen ADDR] [--ui | --no-ui] [--no-autostart] [--log-format text|json]
agentd serve pair      # security-remote §3
agentd serve devices   # security-remote §2 (list / revoke)
```

Flags override `config.toml` `[api]`:

```toml
[api]
listen = "127.0.0.1:7477"       # loopback by default (ADR-A08); non-loopback requires TLS + tokens
tls = "off"                      # off | files | self-signed | tailscale — semantics: security-remote §4
tls_cert = ""                    # with tls = "files"
tls_key = ""
ui = false                       # serve the embedded reference web UI at / (API-S23)
cors_origins = []                # exact origins allowed for browser clients; empty = same-origin only
autostart_daemon = true          # DialAuto the daemon at start and when it disappears
max_body_kb = 256                # request body cap (413 above it)
sse_heartbeat = "15s"            # comment line on idle SSE streams
event_buffer = 2048              # events kept for Last-Event-ID replay (§4)
idempotency_ttl = "10m"          # Idempotency-Key retention (§2.5)
run_harnesses = ["claude", "codex", "pando"]  # harness values POST /v1/sessions accepts with `write`
run_env = "daemon"               # env base for API-started sessions: "daemon" | "gateway" (§3.3)
```

`[api]` parsing lives in `internal/config` (new `APIConfig`); `cmd/agentd` maps it to
`httpapi.Options`, so `internal/httpapi` itself still depends only on `client` + `proto`.

**Startup.** Load config → validate (`listen` not loopback and `tls = "off"` ⇒ refuse to start with a
clear error; same if no device token store is configured, see security-remote §2) → connect to the
daemon (`client.DialAuto` if `autostart_daemon`, else `client.Dial`) and `ping` → start the event hub
(one daemon `subscribe`, §4) → bind → log the URL. **Shutdown** (SIGINT/SIGTERM): stop accepting,
send `event: bye` on every SSE stream, `http.Server.Shutdown` with a 5 s deadline, close daemon
connections. Stopping `serve` never touches the daemon or its sessions (plan §2.2).

**Daemon restarts.** The hub notices the subscription closing, reconnects with exponential backoff
(50 ms → 5 s; autostart if enabled) and, once back, emits `resync` to every SSE client (§4.3). While
the daemon is unreachable, requests fail fast with `503 daemon_unavailable` + `Retry-After: 1`.

**Daemon connections.** `client.Client` serializes calls on one connection, and `subscribe` holds a
connection for its whole life. Decision:

- *Requests*: a small pool of idle connections (`max_idle = 4`), borrowed per request, returned
  after a successful call, discarded on any I/O error. Dialling a unix socket is cheap, so the pool
  is an optimisation, not a correctness need; a cold pool simply dials (as `mcpserver` does on every
  call). Every call runs under the request context deadline (default 10 s; `act` 15 s, since paste
  verification + submit delay can take ~1.2 s and the daemon may wait for idleness).
- *Streams*: exactly **one** daemon `subscribe` connection for the whole gateway (all sessions),
  fanned out in-process to every SSE client. View streams do not open per-client daemon streams:
  they react to `view.changed` events and fetch `view` through the pool, de-duplicated per session
  and revision (singleflight). Daemon load is O(sessions), not O(clients). `MaxConns` (1024) on the
  daemon is never approached.

**Logging.** Foreground: stderr. Under systemd: stderr → journal. Optionally
`$XDG_STATE_HOME/agentd/serve.log` with `--log-file`. One access line per request: time, device id
(never the token), method, route **pattern** (`/v1/sessions/{name}/act`), session name, status,
duration, bytes. Bodies are never logged (prompts may contain secrets). `act`, `run`, `kill`, `rm`
additionally go to the audit log defined in security-remote §5.

## 2. Conventions

### 2.1 Base, media types, auth

- Base path `/v1`. Breaking changes ⇒ `/v2`; additive fields never break (clients ignore unknown
  fields; servers tolerate none: unknown request fields ⇒ `400 invalid_argument`, mirroring
  `proto.DecodeArgs`).
- Requests and responses `application/json; charset=utf-8`; SSE `text/event-stream`. Times RFC 3339
  UTC. Session names are path segments; clients must percent-encode them (`@` and `.` are legal).
- Auth: `Authorization: Bearer <device-token>` on every route except `GET /v1/health`. There is no cookie
  fallback: browser clients read SSE with `fetch` streaming so they can send the header
  (security-remote §2, frontend-app §4.3). Query string tokens are **not** accepted (they leak into
  logs and history). Token format, hashing,
  pairing and TLS: security-remote. Scopes: `read` ⊂ `write` ⊂ `admin` (a `write` token can read). Each
  endpoint lists its minimum scope in §3 and in the OpenAPI `security` requirement.
- On loopback with `tls = "off"` tokens are still required by default; security-remote decides
  whether a "trust loopback" switch exists (§8).

### 2.2 Error envelope and status mapping

Every non-2xx response:

```json
{"error": {"code": "stale_revision", "message": "option 2 changed since revision 41", "retryable": false,
           "details": {"revision": 43}}}
```

`code` is a stable snake_case identifier; `message` is for humans and may change. The daemon's
structured codes (daemon-ops) pass through unchanged; the gateway only chooses the HTTP status:

| HTTP | `code` (daemon or gateway) |
|---|---|
| 400 | `invalid_argument`, `unknown_profile`, `unknown_alias`, `not_a_directory` |
| 401 | `unauthenticated` (missing / unknown / revoked token) |
| 403 | `forbidden` (scope too small, ACL refusal, identity rule) |
| 404 | `not_found` (session, message, route) |
| 409 | `already_exists`, `still_running`, `exited`, `stale_revision`, `option_mismatch`, `human_draft` |
| 409 + `Retry-After: 1` | `starting` (session reserved but not running yet; `retryable: true`) |
| 412 | `precondition_failed` (`If-Match` revision mismatch) |
| 413 | `payload_too_large` |
| 422 | `idempotency_mismatch` (same key, different body) |
| 429 | `rate_limited` (security-remote §5) |
| 503 + `Retry-After` | `daemon_unavailable`, `paste_unverified` (paste never appeared; nothing was submitted; `retryable: true`), `shutting_down` |
| 504 | `timeout` |
| 500 | `internal`; daemon errors **without** a code ⇒ `daemon_error` with the daemon's message |

Until daemon-ops structured codes land, the gateway maps the few legacy messages it must
distinguish (`no such session` ⇒ 404, `already exists` ⇒ 409, `is starting` ⇒ 409) in one
function with a test, and everything else to `500 daemon_error`.

### 2.3 Pagination

Only the mailbox is unbounded. `GET /v1/mailbox` takes `limit` (default 50, max 200) and `before`
(a message id; ids are monotonic and never reused, AGENTS.md "Mailbox states"); the response has
`next_before` (absent on the last page). v1 pages in the gateway over the daemon's `inbox` result;
a daemon-side cursor can come later without changing the HTTP shape. Session lists are small (tens)
and are not paginated. View lines are bounded with `tail` (§3.5).

### 2.4 Revisions and ETags

`View.revision` (semantic-model) increases on every visible change of a session. Because revisions
restart with the daemon, the gateway prefixes them with the **daemon epoch** (`ping` result: start
time + pid; daemon-ops is asked to add an explicit `epoch`, §8): `ETag: "e<epoch>-r<revision>"`.

- `GET …/view` honours `If-None-Match` ⇒ `304`. `?since=<revision>` is the JSON-friendly form (§3.5).
- `GET /v1/sessions` returns a weak ETag over the list (hash of names + states + revisions).
- `POST …/act` takes the revision in the body (`revision`), as daemon-ops requires; `If-Match` is
  accepted as an alternative and wins if both are set.

### 2.5 Idempotency

`POST /v1/sessions`, `POST …/act`, `POST /v1/mailbox/messages` and `POST …/reply` accept
`Idempotency-Key` (1–128 chars of `[A-Za-z0-9._:-]`). The gateway keeps `(device, key) → (body hash,
status, response)` in memory for `idempotency_ttl` (LRU, 4096 entries): a replay returns the stored
response with `Idempotent-Replayed: true`; same key with another body ⇒ `422 idempotency_mismatch`; a
request still in flight with that key ⇒ `409 in_progress` (`retryable`). Mobile clients should always
send one on writes: a retried `act prompt` after a dropped response must not type the prompt twice.
The store is lost on gateway restart (acceptable: `act` also carries `revision`, so a stale replay
after a restart is caught by `stale_revision` for `choose`; `prompt` is the residual risk, §8).

## 3. REST endpoints

| Method | Path | Scope | Daemon op(s) |
|---|---|---|---|
| GET | `/v1/health` | none | `ping` |
| GET | `/v1/info` | read | `ping`, `whoami`, `info`* |
| GET | `/v1/sessions` | read | `ls` (+ summary fields*) |
| POST | `/v1/sessions` | write (admin for `cmd`/`env`) | `run` |
| GET | `/v1/sessions/{name}` | read | `ls` |
| DELETE | `/v1/sessions/{name}` | write | `rm` (`?force=true`: `kill` then `rm`) |
| POST | `/v1/sessions/{name}/kill` | write | `kill` |
| GET | `/v1/sessions/{name}/view` | read | `view` |
| POST | `/v1/sessions/{name}/act` | write | `act` |
| GET | `/v1/sessions/{name}/view/stream` | read | `subscribe` (shared) + `view` |
| GET | `/v1/sessions/{name}/terminal` | read | `dump` |
| GET | `/v1/events` | read | `subscribe` (shared) |
| GET | `/v1/mailbox` | read | `inbox` |
| GET | `/v1/mailbox/messages/{id}` | read | `read` (peek, no state change) |
| POST | `/v1/mailbox/messages/{id}/read` | write | `read` (marks read) |
| POST | `/v1/mailbox/messages` | write | `send` |
| POST | `/v1/mailbox/messages/{id}/reply` | write | `reply` |

\* needs a daemon-ops addition (§8). There is deliberately no `type` endpoint: raw input goes through
`act {action: keys}` and its safety checks. No purge, no `shutdown` in v1.

### 3.1 `GET /v1/info`

What a client needs to render a host and launch agents on it: the aliases and harnesses live on the
daemon host, not on the phone.

```json
{"host": "workstation", "gateway": {"version": "0.4.0"},
 "daemon": {"version": "0.4.0", "pid": 4121, "epoch": "1763030000-4121"},
 "identity": "human:jose", "scopes": ["read", "write"],
 "harnesses": ["claude", "codex", "pando"],
 "profiles": [{"name": "claude", "builtin": true}, {"name": "codex", "builtin": true},
              {"name": "default", "builtin": true}, {"name": "pando", "builtin": true}],
 "aliases": [{"name": "webapp", "dir": "/home/jose/src/webapp", "source": "file"}],
 "limits": {"max_body_kb": 256, "prompt_max_chars": 16000}}
```

`harnesses` = `[api] run_harnesses`; `profiles` and `aliases` must come **from the daemon** (new op
`info`, §8): the gateway must not read `aliases.toml` itself (ADR-A01, and the daemon's
`AGENTD_ALIASES` may differ from the gateway's).

### 3.2 Sessions list and detail

`GET /v1/sessions` → `{"sessions": [SessionSummary…]}` sorted by name. `SessionSummary` is
`proto.SessionInfo` (name, state, pid, cwd, cmd, profile, exit_code, started_at, last_output,
idle_ms, unread, attached, cols, rows, title, input_state) plus semantic fields from the daemon:
`status`, `attention`, `revision` and `preview` (last non-empty transcript logical line, ≤ 200 chars):

```json
{"sessions": [{"name": "claude@webapp", "state": "running", "status": "waiting", "attention": true,
  "revision": 57, "preview": "Do you want to proceed? 1. Yes  2. No", "profile": "claude",
  "cwd": "/home/jose/src/webapp", "started_at": "2026-11-13T09:12:03Z", "idle_ms": 4200,
  "unread": 0, "attached": 0, "cols": 160, "rows": 50, "input_state": "empty"}]}
```

`GET /v1/sessions/{name}` returns one `SessionSummary` (404 if unknown).

### 3.3 `POST /v1/sessions` — run on the daemon host

Mirrors `agentd run <alias|path> [--harness H] [--name N] [--profile P] [-- cmd…]`:

```json
{"target": "webapp", "harness": "codex", "name": "codex@webapp", "args": ["--model", "o4"]}
```

- `target` (required): alias name or **absolute** path on the daemon host (relative paths are
  meaningless over the network ⇒ 400). Resolution is the CLI's `ResolveTarget` (alias wins; label =
  alias, matching alias, or basename rule), executed in the daemon (§8).
- `harness` (default `claude`): must be in `run_harnesses` for `write`; anything else needs `admin`.
  Default argv = `[harness, args…]`; default name `<harness>@<label>`; default profile = harness.
- `name`, `profile`: optional. `args` (appended to the harness), `cmd` (full argv, replaces
  harness+args) and `env` (map): **admin only** — `args` change what is executed (`--dangerously-…`
  style flags, `-c` for a shell), so they are arbitrary code execution just like `cmd`.
- No `cols`/`rows`: the session gets the profile default size (ADR-A02, daemon-ops §5).
- Environment: the CLI sends its own `os.Environ()` as `env_base`; the gateway has no meaningful
  "caller environment". `run_env = "daemon"` (default) omits `env_base` so the daemon uses its own
  environment; `"gateway"` sends the gateway's. Either way the daemon strips `AGENTD_*`,
  `CLAUDECODE*` and `[run] strip_env`.

`201 Created`, `Location: /v1/sessions/codex%40webapp`, body = `SessionSummary` (state `starting` or
`running`). `409 already_exists` if the name is taken (with `Idempotency-Key`, a replay returns the
original 201 instead).

### 3.4 Kill and delete

`POST …/kill` `{"signal": "TERM"}` (default TERM; names or numbers, as `kill`) → `202` with
`SessionSummary`; the exit arrives as `session.exited` on `/v1/events`. `DELETE /v1/sessions/{name}`
→ `204`; `409 still_running` unless `?force=true` (= `kill --rm`: TERM, wait up to 3 s, KILL, rm).

### 3.5 `GET /v1/sessions/{name}/view`

Query: `since` (revision the client has), `tail` (max logical lines, default 500, max 5000),
`scrollback` (bool, default true: include scrollback lines before the screen).

- No `since`: `200` with the full `View` (semantic-model; fields `revision`, `status`, `attention`,
  `title`, `lines[] {text, role}`, `input {text, ghost, draft}`, `options[] {id, label, selected,
  confidence}`). `ETag` set.
- `since` = current revision: `200 {"unchanged": true, "revision": 57}` (or `304` with
  `If-None-Match`).
- `since` older and still in the gateway's per-session view cache: `200` with a `ViewPatch` (§4.2)
  and `"patch": true`; otherwise the full view. Clients must handle both.

### 3.6 `POST /v1/sessions/{name}/act`

Body = daemon-ops `act` args, passed through after validation; the gateway adds nothing but
idempotency and scope checks:

```json
{"action": "choose", "revision": 57, "option_id": "2", "option_label": "No"}
{"action": "prompt", "revision": 57, "text": "Run the tests again", "submit": true}
{"action": "keys", "keys": ["Escape"]}
{"action": "interrupt"}
```

`revision` (+ `epoch`) is required for `choose` and optional for `prompt`, `keys`, `interrupt`, `cancel`:
when present the daemon refuses with `conflict` (409, fresh view in `details`) if the interaction state
(status, menu instance/options, dialog, draft; not transcript text) changed since that revision. Clients
that show a view to a person (the app) always send them.

`200 {"action": "choose", "revision": 58, "status": "working"}` (result shape: daemon-ops §3).
Refusals map per §2.2: `stale_revision` / `option_mismatch` / `human_draft` ⇒ 409, `paste_unverified`
⇒ 503 retryable. An API prompt is a **human** prompt (the gateway is `human:<user>`): it is typed as
is, without the mailbox "untrusted peer message" notice. To send an agent a message it should treat
as peer input, use the mailbox.

### 3.7 Mailbox (as `human:<user>`)

- `GET /v1/mailbox?all=false&mailbox=&limit=&before=` → `{"messages": [Message…], "next_before": 12}`.
  Own mailbox: unread only unless `all`; listing marks fetched messages `delivered` (daemon
  semantics). `mailbox=<session|human:x>` peeks at another mailbox (`inbox {as}`), no state change;
  it needs the **admin scope** (403 otherwise), as does `GET /v1/mailbox/messages/{id}?mailbox=`.
- `GET /v1/mailbox/messages/{id}` → `Message`, **no state change** (GET must be safe; needs `read
  {as: <self>}` to be a peek, §8). `POST …/{id}/read` → marks read, returns `Message`.
- `POST /v1/mailbox/messages` `{"to": "codex@webapp", "subject": "", "body": "…", "reply_to": 12}` →
  `201 Message`. Sender is always the gateway's identity; there is no `from`.
- `POST /v1/mailbox/messages/{id}/reply` `{"body": "…"}` → `201 Message` (`warning` passed through).

## 4. Server-Sent Events

### 4.1 `GET /v1/events`

All hosts-level activity, for the session list and notifications. Query `sessions=a,b` filters,
`types=status.changed,mail.*` filters by name (glob on the dot segment). Frame format:

```
id: 1763030000-4121.18342
event: status.changed
data: {"session":"claude@webapp","ts":"2026-11-13T09:20:11Z","data":{"from":"working","to":"waiting","attention":true,"revision":57}}
```

Event names are the daemon's (daemon-ops §6): `session.*` (created/started/exited/removed),
`status.changed`, `view.changed` (revision only, no lines), `input.changed`, `mail.*`. Unknown daemon
event names are forwarded unchanged. Gateway-generated events: `hello` (first event: `{epoch,
last_id}`), `resync` (client must refetch `GET /v1/sessions` and any open view), `bye` (shutdown).

### 4.2 `GET /v1/sessions/{name}/view/stream`

One session's view as it changes. First event `view` (full `View`), then `view.patch` per revision,
`status.changed` / `input.changed` for that session, `session.exited`, and `resync`/`bye`. `id` =
`e<epoch>-r<revision>`. Patches are computed by the gateway against the last view it sent to that
stream (it keeps one view per stream), as one splice on `lines` plus replaced scalar fields:

```json
{"base": 56, "revision": 57, "status": "waiting", "attention": true,
 "lines": {"start": 211, "delete": 2, "insert": [{"text": "Do you want to proceed?", "role": "question"}]},
 "input": {"text": "", "ghost": "", "draft": false},
 "options": [{"id": "1", "label": "Yes", "selected": true, "confidence": 0.92}]}
```

Fields absent from a patch are unchanged; `options` and `input` are replaced whole when present.
`lines.start` is an absolute logical-line index; since views are tail-truncated, the gateway adds
`first_line` (absolute index of `lines[0]`) to every `View` it serves (proposed to semantic-model).
Bursts are coalesced: at most one event per 100 ms per stream (latest revision wins), so a busy
agent costs a phone a few events per second, not one per PTY write. If a patch would be larger than
the full view, the full `view` event is sent instead.

### 4.3 Resume, heartbeats, mobile guidance

- `Last-Event-ID` (header, as `EventSource` sends it): on `/v1/events` the gateway replays from its
  ring (`event_buffer`); if the id is older than the ring or from another epoch it sends `resync`
  first. On a view stream, a known `r<revision>` yields a patch from it, otherwise a full `view`.
- Heartbeat: a `: ping` comment every `sse_heartbeat` (15 s) of silence; `retry: 3000` on connect.
- Headers: `Cache-Control: no-store`, `X-Accel-Buffering: no`; flush after every event.
- Mobile: keep one `/v1/events` stream while foregrounded and at most the visible session's view
  stream; on background close them (OS will anyway), on foreground reconnect with `Last-Event-ID`
  and handle `resync`. Reconnect backoff 1 s → 30 s with jitter; a `401` ends retries (token
  revoked). Notifications while the app is closed are security-remote §6 (webhook/ntfy), not SSE.

## 5. L0 raw terminal

`GET /v1/sessions/{name}/terminal?format=text|ansi&scrollback=false&ghost=false` → `dump`:
`{"format": "text", "text": "…", "ghost": "Try \"fix lint\"", "ghost_hidden": true, "cols": 160,
"rows": 50}`. `Accept: text/plain` returns the bare text. `ansi` with `scrollback` ⇒ 400 (daemon
limitation). ANSI is rendered at the PTY size; clients scroll horizontally, never ask for a resize.

**Later (API-S17, needs a WebSocket dependency + AGENTS.md update, ADR-A07):**
`GET /v1/sessions/{name}/terminal/stream` upgraded to WebSocket, **read-only**. Binary messages carry
the `proto` frames unchanged (`[type][len][payload]`): first `D` = full redraw, then `D` live
output, `E` exit; plus a gateway `S` frame (`uint16 cols, rows`) when the PTY size changes. The
daemon side needs a *watch* attach (daemon-ops): it must not count as a sizing client, must not
forward input or terminal replies, and must not affect `attached` / human-activity tracking. The
gateway drops any `R`/`D` frame from the client.

"Take control" (interactive L0) is **out of v1**. If added: `admin` scope, explicit opt-in per
session, input goes through the same human-activity tracking as an attached client, and it still
**never resizes** the PTY (no `R` frames accepted; ADR-A02).

## 6. OpenAPI contract and contract tests

`docs/api/openapi.yaml` is the source of truth for the gateway and the Flutter app in `app/` (ADR-A18); the app's
hand-written Dart client is kept honest by contract tests against it (frontend-app §7). CI gates:

1. **Route coverage (stdlib only).** `httpapi` exposes its route table (`[]Route{Method, Pattern,
   Scope}`); a test parses the spec's `paths` (the YAML is kept to a simple subset, or a generated
   `openapi.json` sibling is committed and checked for freshness) and asserts both sets are equal
   and the scopes match `x-agentd-scope`.
2. **Response validation.** Handler tests (§7) wrap the client transport with a validator that
   checks every request/response against the spec. Proposed library:
   `github.com/pb33f/libopenapi-validator` (OpenAPI 3.1 / JSON Schema 2020-12). Fallback:
   `github.com/getkin/kin-openapi` (`openapi3filter`) if the spec is kept 3.0-compatible. Either is
   a **test-only** dependency but still lands in `go.mod`, so the commit that adds it must update the
   pinned-deps list in `AGENTS.md` (repo rule).
3. **Spec lint.** CI job running a linter (e.g. `redocly lint` via `npx`) on the YAML; no Go dep.
4. **Breaking-change check** (later): `oasdiff breaking` against the last release tag.

## 7. Package layout: `internal/httpapi`

Imports: stdlib, `internal/client`, `internal/proto` (ADR-A01). Auth is an interface
(`Authenticator`) implemented by the token store from security-remote and injected by `cmd/agentd`.

```
internal/httpapi/
  server.go        Server, Options{Dial, Auth, UI fs.FS, Logger, Clock, ...}, Serve/Shutdown
  routes.go        route table (method, pattern, scope, handler) on http.ServeMux (Go 1.22 patterns)
  middleware.go    request id, auth+scope, body cap, CORS (policy from security-remote), recover, access log
  errors.go        apiError, daemon code → HTTP status (§2.2), writeError
  daemonpool.go    pooled client.Client borrow/return, autostart, epoch tracking
  hub.go           single daemon subscribe → ring buffer → fan-out; reconnect + resync
  sse.go           SSE writer: id/event/data, heartbeat, flush, coalescing
  viewcache.go     per-session last views, singleflight fetch, ViewPatch diff (one splice)
  idempotency.go   (device, key) LRU store
  h_info.go h_sessions.go h_view.go h_act.go h_mailbox.go h_terminal.go h_events.go
  ui.go            optional embedded reference UI (go:embed, API-S23)
  *_test.go        unit tests; contract_test.go (§6)
```

Handler pattern: handlers are `func(r *http.Request) (status int, body any, err error)` wrapped by
one adapter that encodes JSON or the error envelope, so handlers never write headers directly
(streams excepted). Decoding uses `DisallowUnknownFields` and the `max_body_kb` cap.

Testing: `httptest.NewServer` in front of an in-process daemon started on a temp socket
(`AGENTD_SOCKET`) and temp state dir (`AGENTD_STATE_DIR`) — test files may import
`internal/daemon`; production code may not. Sessions run `sh`, `cat` or
`internal/testutil/fakeagent` (menus, prompts, ghost text) — **never real agent CLIs**. Required:
SSE resume across a daemon restart, coalescing, idempotent replay, scope refusals, `-race`. A fake
`Authenticator` grants scopes per test token.

## 8. Open questions

1. **Daemon-side resolution** (for daemon-ops): new op `info` (profiles, aliases with source,
   hostname, epoch) and `run` accepting `target` + `harness` resolved by the daemon with
   `ResolveTarget`. Alternative: gateway imports `internal/config` read-only — rejected here as it
   breaks ADR-A01 and can disagree with the daemon's `AGENTD_ALIASES`.
2. `ls` summary fields (`status`, `attention`, `revision`, `preview`) in the daemon vs one `view` per
   session in the gateway (N calls per list refresh).
3. Is `read {as: <own mailbox>}` a peek? Needed for a safe `GET /v1/mailbox/messages/{id}`.
4. Explicit daemon `epoch` in `ping` (instead of start time + pid).
5. `act prompt` duplicate after gateway restart within the idempotency window: persist keys on disk,
   or let daemon `act` take an idempotency key?
6. Trust-loopback mode without tokens (security-remote) and the default port (`7477` proposed).
7. Should `admin` be able to run arbitrary `cmd` at all over the network, or only on loopback?
8. `run_env` default: `daemon` is predictable under systemd but may lack the user's interactive
   `PATH`; `gateway` mirrors CLI behaviour when `serve` is started from a shell.
9. Mailbox purge and `type` over HTTP: deliberately absent from v1; revisit with real use.


## Hardening notes (AGED-US-0024 and review fixes)

- **ETag / event ids**: `"e<epoch>-i<instance>-r<revision>"`; `<instance>` = session start time in
  base 36, so a session removed and re-created under the same name never matches an old ETag or
  `Last-Event-ID` (full `view` instead of a patch, `412` on `If-Match`). `If-Match` follows RFC 9110
  (strong comparison, `*`, lists; weak tags fail); `If-None-Match` uses weak comparison, lists and `*`.
- **Outcome unknown**: `run`, `act`, `kill`, `rm`, `send`, `reply`, `view/ack` use a fresh daemon
  connection and are never retried by the gateway. If the request may have reached the daemon but no
  answer was seen (I/O error, timeout, client disconnect, revoke) the answer is `502`/`504`
  `outcome_unknown` (`retryable:false`; `act` adds the fresh view and `ETag`). The `act` deadline is
  40 s (the daemon's act budget is a 10 s input-lock wait plus 3 s per key step). With an
  `Idempotency-Key` the entry is kept as "outcome unknown" and a retry gets `409 outcome_unknown`;
  only errors known to have happened before dispatch (validation, auth, dial) or definite daemon
  refusals free the key. Per device at most 256 keys (LRU), 4096 in total; `If-Match` is part of the
  request hash.
- **Errors**: only known daemon codes pass their message through; internal and unknown daemon errors
  are `500 internal` / "internal error" and the detail goes to the gateway log.
- **Headers**: all responses carry `X-Content-Type-Options: nosniff` and `Referrer-Policy:
  no-referrer`, JSON responses `Cache-Control: no-store`; a POST with a body must be
  `application/json` (415). `HEAD` on the SSE and mailbox routes is `405`.
- **View streams**: `tail` is fetched in buckets 50/100/200/500/5000 and trimmed per stream; the
  last-view cache is an LRU with an 8 MiB budget, cleared on resync and on removal/exit. After a
  daemon-side `resync` an older `Last-Event-ID` gets `resync`. The event hub dials the daemon without
  autostart (`[api] autostart_daemon = false` also stops request-time autostart).
- **Pairing** is mounted only with `[api] pairing = true` (default).
