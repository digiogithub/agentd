# Design: gateway security and remote access

Status: **draft** (2026-11-13). Stories: API-S18..S22 of [[plans/api-gateway-plan]] (epic API-E3).
Binding ADRs: A01 (separate `agentd serve` process), A06 (gateway is `human:<user>` to the daemon),
A08 (loopback default, TLS + tokens off-loopback, Tailscale for remote, no relay), A09 (pairing,
hashed tokens). Siblings: [[design/api/http-api]] (routes, scopes per route, SSE),
[[design/api/daemon-ops]] (`view`/`act`/`subscribe`), [[design/api/frontend-app]] (clients).

Everything here lives in `internal/httpapi` (plus a small `internal/httpapi/devices` store). The
daemon gains no network code and no notion of devices: it keeps resolving the gateway as
`human:<user>` over the unix socket (AGENTS.md, "Identity").

## 1. Threat model

### Assets

- **A device token.** The gateway is a `human:<user>` client of the daemon, and `act` / `run` let a
  caller type into agents that themselves run shell commands. A `write` token is therefore
  **equivalent to an interactive shell as the user** on the daemon host (it can also start agents
  in any directory); an `admin` token runs arbitrary argv/env directly. A `read` token leaks everything the
  agents print (source code, secrets that end up on screen, mailbox content).
- **Screen and mailbox content** in transit and at rest (audit log, notifications).
- **The pairing secret** during its short life.
- **The TLS private key** (self-signed mode): whoever holds it can impersonate the gateway to a
  pinned client and harvest its token.

### Actors and what stops them

| Actor | Capability | Main controls |
|---|---|---|
| LAN / Wi-Fi attacker | Sniffs and injects on the network the gateway listens on | Loopback default; non-loopback needs TLS (refused at startup otherwise); token required; pairing code is one-shot and short-lived; cert pinning in the QR |
| Stolen or lost phone | Has the app with a stored token | Token in OS keystore behind device unlock; per-device token, `agentd serve devices revoke`; optional expiry; read-only devices via `read` scope; audit log shows what it did |
| Malicious web page in the user's browser | Sends requests to `http://127.0.0.1:<port>` or uses DNS rebinding to make `evil.example` resolve to 127.0.0.1 | Token in `Authorization` header (never cookies, so no ambient credential); strict `Host` allow-list (defeats rebinding); `Origin` allow-list; no permissive CORS; preflight-forcing JSON content type |
| Prompt-injected agent on the same host | Runs `curl http://127.0.0.1:<port>/v1/...` from inside its session | **Token required on every listener, loopback included** (§2.5); the token is not in any session's environment or in files the gateway creates readable by default tooling; audit log |
| Other local users (same machine, other uid) | Connect to any TCP port on 127.0.0.1 | Token required; token store and TLS key are 0600 in a 0700 dir; the daemon socket keeps its uid check |
| Hostile code running as the same uid | Reads files, ptraces processes | Out of scope, exactly as in AGENTS.md "Identity" limits. It can read the browser's local storage or the Secret Service; agentd does not defend against it |

### Identity laundering (the key local risk)

On the unix socket the daemon recognises an agent by process ancestry and limits it (own mailbox,
`type` only into itself, no `as` peeks, restricted `purge`). The gateway connects as
`human:<user>`, so **any request the gateway forwards runs with human privileges**. An
unauthenticated gateway on 127.0.0.1 would let any agent escape the identity model with one
`curl`. This is why §2.5 requires a token even on loopback.

## 2. Device tokens

### 2.1 Format

```
agd_<id>_<secret>
```

- `id`: 10 characters Crockford base32 (50 bits, random), public, used for lookup, listing and
  audit. Never secret.
- `secret`: 32 random bytes from `crypto/rand`, base64url without padding (43 chars).
- The `agd_` prefix makes tokens greppable by secret scanners (GitHub push protection, gitleaks).

Clients send `Authorization: Bearer agd_…`. Tokens are **never** accepted in query strings or
cookies (logs, history, `Referer`; cookies are ambient credentials). SSE clients (the Flutter app's
hand-written Dart SSE client, [[design/api/frontend-app]] §4.3) send the header instead of using `EventSource`, so the cookie
fallback mentioned in [[design/api/http-api]] §2.1 is **not** adopted.

### 2.2 Storage

- File: `$XDG_STATE_HOME/agentd/api/devices.json` (respects `AGENTD_STATE_DIR` via
  `config.StateDir()`), dir `api/` 0700, file 0600, written atomically (temp file + `rename`) under
  an exclusive `flock` on `api/devices.lock`. Both the gateway and the CLI subcommands
  (`pair`, `devices`) take the lock, so they can run concurrently; the gateway re-reads the file
  when its mtime changes (checked at most once per second), so a revoke is effective within ~1 s
  without talking to the gateway.
- Stored per device: `id`, `name`, `platform`, `scopes`, `hash`, `created_at`, `expires_at?`,
  `last_seen_at`, `last_seen_addr`, `paired_via` (`qr` | `code` | `cli`).
- `hash = SHA-256(secret)`, hex. A slow KDF is unnecessary: the secret has 256 bits of entropy, so
  offline guessing is hopeless, and a fast hash keeps per-request verification cheap. Comparison
  with `subtle.ConstantTimeCompare` after lookup by `id`.
- The clear token is shown exactly once (pairing response or `token create` output).
- `last_seen_at` is kept in memory and flushed at most once per minute.

### 2.3 Scopes

| Scope | Allows (details per route in [[design/api/http-api]]) |
|---|---|
| `read` | info, list sessions, `view`, view/event streams, L0 snapshot, mailbox list / peek |
| `write` | `read` + `act` (prompt, choose, keys, interrupt, cancel), mailbox send / reply / mark read, `run` of an allowed harness (`[api] run_harnesses`) on an alias or path, `kill`, `rm` |
| `admin` | `write` + `run` with `cmd` / `env` / other harnesses, device management over HTTP |

Scopes are cumulative (`read` ⊂ `write` ⊂ `admin`, as in [[design/api/http-api]] §3); a device
holds one level. The pairing default is `write`; `admin` must be asked for explicitly
(`agentd serve pair --scope admin`): arbitrary argv/env over the network is rare and high-impact.

### 2.4 Expiry, last-seen, revoke

- `expires_at`: optional absolute expiry. Default **none** for paired devices, plus an **inactivity
  expiry** of 90 days (`[api] device_idle_expiry = "90d"`, `"0"` disables): a device not seen for
  that long is rejected and shown as `expired` in the list. CLI-created tokens (`token create`) get
  `--ttl` (default 30 days) because they tend to be pasted into scripts.
- `agentd serve devices` lists `id, name, platform, scopes, created, last seen (addr), expires, state`.
- `agentd serve devices revoke <id|name>` deletes the record; `rename`, `set-scope` (downgrade only
  without re-pairing; an upgrade requires re-pairing so it needs physical presence at the host).
- Revocation also closes that device's open SSE streams (the gateway tracks streams per device id).

### 2.5 Loopback without token? Decision: no

Every TCP listener requires a valid token, **including 127.0.0.1**. Reasons:

1. Agents run on the same host (§1 identity laundering). Loopback proves only "some process of some
   user on this machine", which is precisely the population we must not trust.
2. With `tailscale serve` (§4.2) all remote traffic reaches the gateway *from 127.0.0.1*, so
   "loopback ⇒ local human" is false in the recommended remote setup.
3. Browsers can reach loopback (rebinding, `fetch` from any page).

Local humans already have a token-free path: the CLI and `agentd attach` over the unix socket,
where the daemon's identity resolution applies. The Flutter desktop app on the same host is paired once like
any device (§3); there is no embedded web UI (ADR-A18).

A unix-socket listener for the gateway (`--listen unix:/path`) is **not** offered in v1: local
programs that want HTTP semantics can use a token; everything else should use the daemon socket.

### 2.6 Headless tokens

`agentd serve token create --name ci --scope read [--ttl 30d]` mints a token without pairing (scripts,
status bars); running the CLI on the host is the same trust bar as pairing.

## 3. Pairing

### 3.1 Flow

```
host$ agentd serve pair [--name "Pixel"] [--scope write] [--ttl 5m]
  1. generate code (one-time), store hash(code), expiry, scope, attempts=0 in devices.json (pending)
  2. print QR (terminal, half-block chars) + text: URL, code, TLS fingerprint
  3. block, polling the store, until the code is consumed or expires
device: scan QR (or type URL + code) ─► verify TLS pin ─► POST /v1/pair {code, name, platform}
gateway: check code (constant time), TTL, attempts ─► create device ─► 201 {token, device_id, scopes}
host$ "Device 'Pixel' (android, 100.101.4.7) wants write access. Approve? [y/N]"
  4. on yes: device record becomes active; the token starts working
     on no / Ctrl-C / timeout: record deleted, token never valid
```

- **Confirmation step** (default on when stdin is a terminal; `--yes` skips it for scripting):
  the token is returned to the device immediately but stays `pending` until approved; requests with
  a pending token get `403 {"error":"pairing not approved yet"}` and the app shows "approve on the
  host". This defeats a shoulder-surfed or photographed QR: the attacker would have to win the race
  *and* the user would see an unexpected device name/address.
- The code is consumed on first successful use; a second `POST /v1/pair` with it fails.
- `POST /v1/pair` is the only unauthenticated route besides `GET /v1/health` (no data).

### 3.2 QR payload

```
agentd://pair?v=1&u=https%3A%2F%2Fbox.tail1234.ts.net&c=7K2M-9QXD-4T&fp=sha256:9f3c…&h=box
```

| Field | Meaning |
|---|---|
| `v` | Payload version |
| `u` | Base URL of the gateway as the device must reach it (from `[api] public_url`, else derived from the bind address; the CLI warns when it is a loopback URL) |
| `c` | One-time code: 12 Crockford base32 chars (60 bits) shown grouped for manual entry |
| `fp` | SHA-256 of the leaf certificate's SubjectPublicKeyInfo, only in self-signed mode; omitted with publicly trusted certs (Tailscale) |
| `h` | Host label for the app's host list (editable) |

The `agentd://` scheme lets the system camera open the app through a deep link; the in-app scanner
reads the same string. The text fallback prints the three values on separate lines.

### 3.3 TTL and brute force

- Default TTL 5 minutes, max 30 minutes. At most 3 pending codes; a new `pair` beyond that evicts
  the oldest.
- Per code: 5 failed attempts burn it. Globally: 20 failed `POST /v1/pair` per 10 minutes puts
  pairing in a 10-minute lockout (logged to `api/gateway.log` and the audit log).
- With 60-bit codes and 5 attempts, online guessing is impractical; the limits mainly stop log spam.
  `[api] pairing = false` disables pairing (tokens only via `token create`).

### 3.4 Device management commands

`agentd serve devices` (table incl. pending/expired), `devices revoke|rename|set-scope` and
`agentd serve token create` as in §2.4/§2.6. The same operations exist over HTTP for `admin`
devices, except creating `admin` tokens, which is CLI-only.

## 4. TLS options

Startup rule (ADR-A08): **non-loopback bind + TLS off ⇒ `agentd serve` refuses to start**, with no
override flag. `0.0.0.0` / `::` binds also warn that every interface is exposed.

`[api] tls = "off" | "self-signed" | "files"` (default `off`, valid only with loopback binds).

### 4.1 Self-signed with pinning (`tls = "self-signed"`)

- On first start the gateway generates an ECDSA P-256 key and a certificate (SANs: hostname, the
  bind IP, `public_url` host; plus `[api] tls_hosts`) at `$XDG_STATE_HOME/agentd/api/tls/{cert.pem,key.pem}` (key 0600).
  Validity 5 years (as implemented in `internal/tlsutil`): trust comes from the pin, not from the expiry.
- The QR carries the SPKI fingerprint; the app pins it per host and refuses any other key (no
  "accept anyway" button). `agentd serve tls rotate` makes a new key; paired devices then need
  re-pairing or `agentd serve tls show` to confirm the new pin manually.
- Browsers warn on self-signed certs: this mode targets the Flutter app (pins the SPKI in Dart,
  [[design/api/frontend-app]] §4.2) on LANs without Tailscale.

### 4.2 Tailscale (recommended for remote)

Option A, **`tailscale serve` in front of a loopback gateway** (simplest):

```
agentd serve                          # 127.0.0.1:7477, tls off
tailscale serve --bg https / http://127.0.0.1:7477
# device URL: https://<machine>.<tailnet>.ts.net
```

Tailscale terminates TLS with a publicly trusted certificate for the MagicDNS name and only tailnet
members can reach it ([Tailscale Serve](https://tailscale.com/docs/features/tailscale-serve);
HTTPS certs need MagicDNS and HTTPS enabled in the admin console,
[Enabling HTTPS](https://tailscale.com/docs/how-to/set-up-https-certificates)). Consequences:

- All requests arrive from 127.0.0.1: per-IP rate limits degrade to per-device limits; the audit log
  records `X-Forwarded-For` only as an untrusted hint.
- `Host` is `<machine>.<tailnet>.ts.net`: it must be in the allow-list (`public_url`).
- Tailscale identity headers (`Tailscale-User-Login`) are **not** used for auth: any local process
  can send them to the loopback port. Tokens stay mandatory.
- Never use Funnel (public internet exposure); the docs say so explicitly.

Implemented (AGED-US-0023): pin format `sha256:<hex>` of the leaf SPKI; TLS 1.2 minimum, ECDHE+AEAD
suites; `files` reloads on SIGHUP or mtime change. See `docs/changes/AGED-US-0023-tls-modes.md`.

Option B, **`tls = "files"` with `tailscale cert`**: the gateway binds the Tailscale IP
(`100.x.y.z:7878`) and serves `tailscale cert` output; it reloads cert/key when their mtime changes
(certs are short-lived, renewal is the user's cron/systemd timer). Useful when the gateway should
not depend on the `tailscale serve` config.

Note: `*.ts.net` certificates appear in Certificate Transparency logs (names, not reachability).

### 4.3 Plain HTTP

Only on loopback binds (`127.0.0.0/8`, `::1`). SSH tunnels (`ssh -L 7878:127.0.0.1:7477 host`) fit
here: the tunnel provides encryption, the gateway still requires a token.

## 5. Hardening

### 5.1 Host and Origin (DNS rebinding, CSRF)

- **Host allow-list** (implemented, AGED-US-0024): `localhost`, `127.0.0.1`, `[::1]`, the bind IP
  (every local interface address for a wildcard bind), the machine hostname, each `[api] tls_hosts`
  entry and each `[api] allowed_hosts` entry; the port is ignored. Any other `Host` → `421
  misdirected_request`, before authentication and routing (also `/v1/health`, `/v1/pair`, 404s). This
  alone defeats DNS rebinding.
- **Origin** (implemented): a request carrying `Origin` must match an exact entry of `[api]
  cors_origins` (empty by default), otherwise `403`; `Origin: null` is always `403`. A request without
  `Origin` (Flutter `dart:io` `HttpClient`, curl, [[design/api/frontend-app]] §4.1) is accepted: the
  token is then the only credential and is never ambient.
- **CORS**: no `Access-Control-Allow-Origin: *`, no credentials mode. Preflights are answered only
  for allowed origins. State-changing routes require `Content-Type: application/json` (forces a
  preflight from browsers).
- Headers: `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`, `Referrer-Policy:
  no-referrer`; the gateway serves no UI assets (ADR-A18), so no CSP is needed.

### 5.2 Rate limits and caps

Implemented (AGED-US-0024; `[api] rate_limit`, `rate_burst`, `act_rate_limit`, `max_streams_per_device`,
`max_streams`, `audit_max_mb`): per-device token bucket 20 req/s burst 40 (`429 rate_limited`, `Retry-After`);
`act` per device 5/s (burst 10); failed authentication 10/min per remote address (then `429`, even for a
valid token, until the window slides); SSE streams 8 per device / 64 total (`429 too_many_streams`);
`prompt_max_chars` 16000 characters; `http.Server` `ReadHeaderTimeout` 10 s, `IdleTimeout` 120 s,
`MaxHeaderBytes` 16 KiB; at most 32 concurrent daemon calls (`503 busy`); `/v1/health` is answered from a
1 s cache. The table below is the original target (`run` per device and `[api.limits]` are not built).

| Limit | Default |
|---|---|
| Failed auth per source IP (or globally behind `tailscale serve`) | 10/min, then `429` + 1 min backoff |
| Requests per device | 20/s burst 60 |
| `act` per device per session | 5/s (a stuck client must not flood a PTY) |
| `run` per device | 10/min |
| Concurrent SSE streams per device / total | 8 / 64 |
| JSON body | 256 KiB; `act prompt` ≤ 16 000 chars (http-api `limits`; larger content via the mailbox) |
| Header size / read header timeout | 16 KiB / 10 s; idle timeout 120 s; SSE heartbeats every 15 s |

All configurable under `[api.limits]`. Exceeding a cap returns a JSON error, never a dropped
connection, except header abuse.

### 5.3 Audit log

- Implemented (AGED-US-0024): `internal/httpapi/audit.go`. Events: `run`, `act` (action, option id/label,
  key names, text LENGTH), `kill`, `rm`, `send`/`reply` (ids and body length), `view_ack`, `device_pair`,
  `device_add`, `device_revoke` (the last two from the CLI, device `cli`). Fields: `ts, device, remote,
  request_id, action, session, result (ok|error code), detail`. Never text, subjects, bodies or tokens.
  Pairing attempts that fail and auth failures stay in the server log, not the audit file.
- File `$XDG_STATE_HOME/agentd/api/audit.log`, 0600, JSON lines; rotated like session logs
  (`[api] audit_max_mb`, default 10, single rotation to `audit.log.1`).
- Logged: every `write`/`admin` request (`act`, `run`, `kill`, `rm`, mailbox send/reply/read),
  every pairing event (code issued, attempt failed, device approved/rejected), revokes,
  auth failures (rate-limited aggregation).
- Fields: `ts, device_id, device_name, remote_addr, route, session, action, outcome, http_status,
  bytes`. **Not** logged by default: prompt text, option labels, mail bodies (they may contain
  secrets); `[api] audit_bodies = true` adds a truncated (256 chars) copy for users who want it.

### 5.4 Sessions spawned through the API: environment

`run` over the CLI sends `env_base = os.Environ()` of the user's shell (AGENTS.md "Env base").
The gateway is usually started by systemd or autostart with a different, poorer environment. Decision
(matches `[api] run_env = "daemon"`, the default in [[design/api/http-api]] §3.3):

- The gateway **does not send `env_base`**. The daemon then uses its own environment (already
  stripped of `AGENTD_*`, `CLAUDECODE*`, `CLAUDE_CODE_ENTRYPOINT` and `[run] strip_env`). This is
  deterministic and independent of how the gateway was launched. `run_env = "gateway"` sends the
  gateway's environment instead; then every secret in the gateway's env (ntfy/webhook secrets, §6)
  reaches the agents, so the docs must say to keep those out or use `strip_env`.
- Implication 1: the agent gets the daemon's `PATH` and credentials. If the daemon was autostarted
  from a login shell, that is the user's environment at that time; under systemd it may lack
  `~/.local/bin` or API keys. `agentd doctor` (later) should report this; profiles may set `env`.
- Implication 2: with the default, secrets that exist only in the gateway's environment never
  leak into agents.
- API `env{}` and `cmd[]` are `admin` only (http-api §3.3): `env` can set `LD_PRELOAD`, `PATH`,
  `BASH_ENV`, so it is arbitrary code execution; the audit log records the variable names (not
  values). `write` devices can only start an allowed harness in an alias or absolute path.

### 5.5 Process hygiene

The gateway refuses to run as uid 0, holds `flock` on `<socketdir>/agentd-serve.lock` (one gateway
per user) and never returns file paths or stack traces to clients.

## 6. Notifications v1

No APNs/FCM in v1 (plan non-goal). The gateway (the only network-facing process, ADR-A01)
subscribes to daemon events and fans out to configured sinks:

```toml
[api.notify]
on = ["waiting", "idle_after_work", "exited_error"]
min_work_s = 30        # idle_after_work only if the session worked at least this long
debounce_s = 20        # per session, per event kind
[[api.notify.sink]]
kind = "ntfy"
url = "https://ntfy.example.net/agentd-3f9c1a7e2b"   # random topic
token_env = "AGENTD_NTFY_TOKEN"                      # read from the gateway env, never from the file
[[api.notify.sink]]
kind = "webhook"
url = "https://hooks.example.net/agentd"
secret_env = "AGENTD_WEBHOOK_SECRET"                 # HMAC-SHA256 of the body in X-Agentd-Signature
```

- **Secrets (as implemented, `[api.notify]` flat form)**: `ntfy_token_env` / `webhook_secret_env`
  name an environment variable read when `agentd serve` starts; the variable wins over the plain
  `ntfy_token` / `webhook_secret`, and a named variable that is unset is an error unless a plain value
  exists. When a plain secret is present in `config.toml`, the file must be owned by the user and have
  no group/other bits (0600), otherwise the gateway refuses to start with a clear error (the secret is
  never in the message). Env-only setups do not check the file.
- **Payload minimisation**: `{host, session, harness, event, status, ts, url}`; human text is
  "codex@webapp is waiting for you". **No screen content, option labels or prompts by default**;
  `include_excerpt = true` adds the first 120 characters of the detected question for users who
  accept that it transits the ntfy server.
- `url` is a deep link `agentd://open?h=<host>&s=<session>` (ntfy click action) so tapping opens the
  app on that session.
- ntfy topic names are effectively passwords on shared servers: generate a random topic, prefer a
  self-hosted server or access tokens. With a self-hosted server, iOS delivery goes through the
  public ntfy instance's APNs relay, which sees only a message id poll request (document this).
- Best effort: 5 s timeout, 3 retries, never blocking the event loop. No gateway running, no
  notifications (systemd user unit recommended).

## 7. Open questions

1. Map loopback connections to a peer pid (Linux `sock_diag`) and refuse requests from inside a
   session even with a valid token? Cheap defence against a leaked token; Linux-only and racy.
2. Pairing confirmation by default: acceptable friction on headless hosts reached by SSH?
3. 90-day inactivity expiry vs. an absolute expiry for every device.
4. Do `admin` devices need host-side re-confirmation for `cmd`/`env` runs? Probably not in v1.
5. Record the device id on mailbox messages sent via the API, or is the audit log enough?
6. `tls = "self-signed"` key rotation UX: push a new pin over the old channel (signed by the old
   key) instead of re-pairing?
7. Is per-device scope enough, or do users want per-session restrictions (a read-only device
   limited to one project)?
8. Is a host-side `notify-send` sink worth adding for desktop users without ntfy or the app?
