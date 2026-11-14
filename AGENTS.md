# AGENTS.md — agentd

Instructions for any coding agent (Claude, Codex, Pando…) working in this repository.

## What this is

`agentd` is a lightweight session manager for CLI coding agents, written in Go. It launches agents
(Claude Code, Codex, Pando, opencode, gemini…) in persistent PTYs, detaches/attaches to them
(zellij/tmux-style), and lets them **exchange messages through a robust mailbox**, via CLI and MCP.
It is harness-agnostic: it never uses agent-specific APIs, it only types into the agent's terminal
like a human would.

See `README.md` (usage) and `ROADMAP.md` (phases and status).

## MANDATORY operating rules for AI agents

These rules are **MANDATORY**. They are not suggestions or best-effort guidance: an AI agent working
in this repository **MUST** follow every rule below on every task, even a small or one-line change.
When a rule conflicts with a habit or a shortcut, the rule wins. The project rules in the next
section (English, `gofmt`/`go vet`, no real agent CLIs in tests, package contracts, no tiling UI,
conventional commits) stay binding as well.

The rules assume the agent has the tool suite this project is worked with: a knowledge base (KB),
a code-indexing/remembrance subsystem, web search, the Context7 library-docs tools, and a headless
browser. Use the tool names listed below. If a tool is unavailable in the current environment, fall
back to the closest equivalent and say so explicitly.

### 1. MANDATORY: gather context BEFORE starting any task

Before writing code, designing, fixing, or planning anything that builds on prior work, you **MUST**
recover the relevant context first. Never start from a blank slate when prior knowledge exists.
In order:

1. **Knowledge base first.** Search the KB with `kb_search_documents` (or
   `hybrid_search_remembrances` when you also want indexed sessions and code in the same query).
   This returns both the stored documentation and everything saved via the memory subsystem
   (`remember`) in one semantic + full-text query, so it is the primary entry point for prior
   decisions, plans, and past fixes. Search **without** a `tags` filter for broad context recovery;
   add `tags` only when you deliberately want one document type. This repo's design notes and plans
   live in the KB (for example `api-gateway-plan.md` and `design/api/*`); read them before changing
   a contract they describe.
2. **Follow the graph, do not only search.** KB documents link each other with `[[wiki links]]`.
   `kb_search_documents` reports how connected each hit is and lists the neighbours of the best one,
   and `kb_get_document` returns the document's outgoing links and its backlinks. Hop through those
   with `kb_related_documents` instead of firing another search: a document's own links are what its
   author considered relevant. Calling `kb_related_documents` with no `file_path` lists concepts the
   KB refers to but never documented.
3. **Then the code index.** Use the code-remembrance tools to understand structure and prior
   decisions in the codebase before editing: `code_get_symbols_overview` (file/package shape),
   `code_find_symbol` (locate a function or type), `code_hybrid_search` (semantic search across the
   codebase and related indexed projects), and `code_search_pattern` (literal or regex matches).
   Prefer these over blind file reads when locating code. If the project is not indexed, say so and
   fall back to reading the files.
4. **Recall only when you know the key.** Use `recall` only when you already know roughly which
   short fact or key you are after. Do not use it as a substitute for the KB search above.

If, after searching, no relevant context exists, state that briefly and proceed.

### 2. MANDATORY: external research when the answer is not in the repo

When the task needs knowledge that is not in the repository or the KB, you **MUST** research it
instead of guessing. Pinned dependencies are listed under Toolchain; do not invent their APIs.

- **Library / framework / API usage** → use the Context7 tools: first `c7_resolve_library_id` to
  resolve the library name to its ID, then `c7_get_library_docs` to pull current, version-accurate
  documentation and usage patterns. Prefer Context7 over memory for any third-party API surface
  (`creack/pty`, `charmbracelet/x/vt`, `modernc.org/sqlite`, `modelcontextprotocol/go-sdk`, cobra,
  BurntSushi/toml, and the Go standard library included).
- **General or unknown facts, current events, error messages, release notes** → use web search
  (`google_search`, `brave_search`, or `exa_search`) and fetch the most relevant sources (`fetch`)
  to read them. Cross-check more than one source for anything load-bearing.
- **Frontend / web UI work** (rendering, layout, DOM, console errors, visual verification) → use
  the browser tools: `browser_navigate`, `browser_get_content`, `browser_evaluate`, `browser_click`,
  `browser_fill`, `browser_screenshot`, `browser_console_logs`, and `browser_network`. Verify UI
  behaviour by actually driving the page, not by assuming. There is no pane/split UI in agentd
  itself (see Rules); this applies to the reference web UI and any other web surface you are asked
  to build or check.

### 3. MANDATORY: plan before non-trivial work

For anything larger than a trivial change you **MUST** produce a written plan before implementing:

- Break the work into **phases**, each independently testable (`go test` of the packages touched,
  with `-race` for session/daemon/delivery).
- Save the plan to the KB with `kb_add_document` under a clear `file_path` (e.g.
  `agentd/plans/<short-slug>_plan.md`) so it survives the session and can be recovered later with
  `kb_search_documents`.
- If you are unsure whether a plan already exists, search for it first and confirm with the user
  before diverging from it. The API-gateway plan is `api-gateway-plan.md` (links
  `[[plans/api-gateway-plan]]`); do not start a second one.
- Keep the plan updated as phases complete.

### 4. MANDATORY: implement in small, verified increments

- Write code in small, testable increments. After each increment, **run the tests / build**
  (`gofmt`, `go vet`, `go test ./...`; `-race` for session/daemon/delivery) to confirm the change
  works before moving on.
- Match the surrounding code: naming, style, comment density, and idioms. Follow the Rules section
  and the package contracts below. If a contract must change, update this file in the same change.
- Add or update tests for new behaviour. Place them next to the package they cover. Never run real
  agent CLIs (claude, codex, pando…) in automated tests; use `bash`, `sh`, `cat`, or the fake TUI
  in `internal/testutil/fakeagent`.
- Never report something as done unless you verified it (tests passed, build succeeded, or the
  behaviour was observed). If a step was skipped or a test failed, say so plainly with the evidence.

### 5. MANDATORY: document every change in the knowledge base

Every time you modify, implement, fix, or refactor anything, you **MUST** record a summary with
`kb_add_document` once the change is done. This keeps a living documentation of the project and is
**not optional** — it applies even to small or one-line changes.

The summary **MUST** capture at least:

- **What changed** — a concise description of the behaviour or code change.
- **Files & symbols touched** — the concrete paths and functions/types.
- **Why** — the motivation or the bug being fixed.
- **How it was verified** — tests run, build status, manual checks.

Store it under a clear `file_path`: `agentd/changes/<slug>.md`, or the matching
`agentd/fixes/<slug>.md` / `agentd/features/<slug>.md`. If a related document already exists,
**update it** instead of creating a duplicate.

**Link what the document builds on.** Write `[[concept]]` (or `[[concept|label]]`) in the body to
point at the plan, feature, or fix the change continues — a full path (`[[agentd/plans/foo.md]]`)
or a bare name (`[[foo]]`) both work. The links are indexed as a navigable graph, which is what
keeps the knowledge base connected instead of a pile of loose files. Linking a document that does
not exist yet is **correct and useful**: it records a concept worth documenting later, and
`kb_related_documents` lists those pending concepts.

> Plan-mode note: while a harness "plan mode" is active you may only edit the plan file, so defer
> the `kb_add_document` write until the plan is approved and writes are allowed again — but do not
> skip it.

### 6. MANDATORY: choosing the right memory tool

- **`remember` / `recall`** — ONLY for short, durable facts identified by a known key (e.g.
  `project.test_command`, `user.preferred_lang`). Call `remember` to upsert; call `recall` when you
  know roughly what key you want. Never use `remember` for long or structured content.
- **`kb_add_document` / `kb_search_documents`** — for any extensive or ordered information: plans,
  analyses, design notes, multi-step decisions, references. This is also what the mandatory
  pre-task search queries.

### 7. MANDATORY: general conduct

- Use **English** for code, comments, commit messages, identifiers, and documentation (same rule as
  the project Rules section).
- Parallelize independent work when possible, but never at the cost of losing context. When
  delegating to sub-agents, give them clear, self-contained instructions and the context they need
  (this file, the package contract they touch, and the relevant KB plan). Do not have them run real
  agent CLIs.
- For actions that are hard to reverse or outward-facing (force-push, release tags, publishing,
  deleting state outside a temp dir), confirm first unless durably authorized. Do not commit unless
  asked.

## Toolchain

- Go **1.27.2** (see `go.mod`). If your Go toolchain is not on `PATH` in non-login shells, add it,
  e.g. `export PATH=$HOME/.go/bin:$HOME/go/bin:$PATH`.
- Linux first (amd64/arm64). macOS is supported; Windows is out of scope for now (see `ROADMAP.md`).
- Module: `github.com/digiogithub/agentd`. Single binary: `cmd/agentd`.
- Version: `internal/version.Version` (`0.0.0-dev`), set from the tag at release time with
  `-ldflags "-X github.com/digiogithub/agentd/internal/version.Version=<x.y.z>"`. Release pipeline:
  `docs/release.md`.
- Pinned deps (already in `go.mod`, use these, do not swap libraries without updating this file):
  - PTY: `github.com/creack/pty`
  - Headless VT emulator: `github.com/charmbracelet/x/vt` (use `SafeEmulator`)
  - Raw terminal on the client: `golang.org/x/term`
  - SQLite (pure Go, no cgo): `modernc.org/sqlite` (driver name `sqlite`)
  - MCP: `github.com/modelcontextprotocol/go-sdk` (official SDK, stdio transport)
  - CLI: `github.com/spf13/cobra`
  - Config: `github.com/BurntSushi/toml`
  - Terminal QR for `agentd serve pair`: `github.com/skip2/go-qrcode` (pure Go, MIT, no transitive deps; used only in `cmd/agentd/pair.go` to render the pairing URL with half-block characters; a hand-written QR encoder would be riskier than this small, stable library)
  - OpenAPI contract tests (test-only, never linked into the binary): `github.com/pb33f/libopenapi` + `github.com/pb33f/libopenapi-validator` (OpenAPI 3.1 parser and request/response/schema validator; used only by `internal/httpapi/contract_*_test.go` and `openapi_lint_test.go` to fail CI when handlers drift from `docs/api/openapi.yaml`; chosen over kin-openapi because the spec is 3.1 (`const`, `examples`, `if/then`, `type` arrays) which kin-openapi does not fully support; no Node/Java tooling needed)

## Rules

- Docs, code comments, commit messages and identifiers in **English**.
- `gofmt` + `go vet` clean. Tests with `go test ./...` (use `-race` for session/daemon/delivery).
- Never run real agent CLIs (claude, codex, pando…) in automated tests: they consume paid usage.
  Use `bash`, `sh`, `cat`, or the fake TUI in `internal/testutil/fakeagent` for tests.
- Keep packages small and decoupled along the contracts below. If a contract must change,
  update this file in the same commit.
- Do not add a pane/split UI. One attached session per client terminal; tiling is out of scope.
- Commits: conventional commits (`feat:`, `fix:`, `docs:`, `test:`, `refactor:`), one logical change each.
  Releases are cut by pushing a `v*` tag (`docs/release.md`).

## Architecture

```
agentd (daemon; auto-started by the CLI; or systemd --user)
 │  unix socket $XDG_RUNTIME_DIR/agentd/agentd.sock (dir 0700, sock 0600, SO_PEERCRED uid check)
 ├─ registry of sessions (name → *session.Session)
 │    session: creack/pty child ──out──► vt.SafeEmulator (screen)  ─► attached clients (fan-out)
 │                               │                                 └► ring buffer + log file
 │                               └─in◄── attached client keystrokes | delivery injector
 ├─ mailbox (SQLite, $XDG_STATE_HOME/agentd/mailbox.db, WAL)
 ├─ delivery loop: queued msg + idle target ⇒ inject notice (bracketed paste + submit key)
 └─ API over the socket (internal/proto), used by the CLI and by `agentd mcp`
```

### Package contracts

| Package | Responsibility | Depends on |
|---|---|---|
| `internal/version` | Build version (`Version`, set by ldflags at release) | — |
| `internal/termcolor` | Colour specs (`"8"`, `"244"`, `"#808080"`) for ghost-text rules | stdlib |
| `internal/config` | Paths (XDG), `config.toml` loading, harness profiles | toml, termcolor |
| `internal/screen` | `Snapshot` type: immutable copy of cells (index == column; wide-char continuation cells kept as `Width:0`)/cursor/scrollback tail/modes/activity/notification counters for interpretation; title/cwd/notify text sanitized (no C0/C1 controls, capped); `Reflow` (logical lines), `Split` (transcript/input/footer/status/dialog regions) and the shared ghost-cell classification (`GhostColumns`, `GhostRules`, `Rules`) | stdlib, termcolor |
| `internal/session` | One PTY child + VT emulator + fan-out + input + idle/human-activity tracking + ghost-text filter + atomic `Snapshot()`, `OutputGen()`/`Changed()`, `NewReplayer(cols, rows)` (standalone emulator fed raw PTY bytes, same filter and cell conversion as live sessions, for fixtures), BEL/OSC 7/9/777 recording (OSC 9 bodies starting with a numeric sub-command, e.g. `9;4`, `9;9;cwd`, `9;12`, are not notifications; `Changed()` never fires after exit, select on `Done()`), frame-aligned PTY chunking for DEC 2026 synchronized updates (`syncgate.go`) | pty, vt, termcolor, screen |
| `internal/mailbox` | Message store: send, inbox, get, state transitions, replies, purge/vacuum | sqlite |
| `internal/inject` | Safe input path: guard, bracketed paste, paste verification, submit delay, per-session input lock | session, config |
| `internal/delivery` | Decide when a session is idle and inject queued messages (via `inject`) | session, mailbox, config, inject |
| `internal/proto` | Wire protocol: JSON request/response + binary framed attach stream | stdlib |
| `internal/daemon` | Socket server, registry, op handlers, lifecycle, autostart helpers; per-session view state + watcher (`view.go`, `view_rules.go`: profile → `screen.Rules`) behind ops `view`/`view_ack` and the `ls` summary; event bus + op `subscribe` (`events.go`) | all above |
| `internal/client` | Go client for the socket API (used by CLI and MCP); `Subscribe` returns a `Subscription` (event channel + `Close`) | proto |
| `internal/attach` | Client-side terminal: raw mode, SIGWINCH, detach key, redraw | client, x/term |
| `internal/mcpserver` | MCP stdio server exposing mailbox + peers tools | client (+ proto types), go-sdk |
| `internal/apitoken` | Device-token store for the gateway: `agd_<id>_<secret>` tokens, `api/devices.json` (0600, flock, SHA-256 hashes), verify/expiry/last-seen/revoke, `httpapi.Authenticator` + revoke guard; pairing: `api/pairing.json` (0600, flock, salted code hashes) + the unauthenticated `POST /v1/pair` route (`PairRoute`) | stdlib, httpapi (types only; httpapi never imports it) |
| `internal/tlsutil` | Gateway TLS: persisted self-signed cert, files mode with reload, SPKI pin, pin-verifying client callback | stdlib only |
| `internal/httpapi` | HTTP/JSON + SSE gateway (`agentd serve`): routing table, middleware (request id, recover, body cap, auth hook, JSON error envelope), daemon connection pool. Config is passed in by `cmd/agentd` | client, proto (stdlib otherwise; never daemon/session/config) |
| `internal/hcompat` | herdr-core (`hc`) client protocol, generation 1, translated onto daemon ops (Herdr Desktop / Whip compatibility, proof of concept): JSON-RPC 2.0 lines then 9-byte LE frames, per-stream credit, first paint, events, in-memory labels/keep/turn/docs | client, proto (stdlib otherwise; never daemon/session/config) |
| `cmd/agentd` | Cobra CLI wiring; invoked as `herdr-core`/`hc` (argv[0]) it is the hc CLI subset (`hc.go`) | client, attach, daemon, mcpserver, httpapi, hcompat, config, tlsutil |

### Key behaviours (normative)

- **Paths**: socket `$XDG_RUNTIME_DIR/agentd/agentd.sock` (fallback `/tmp/agentd-$UID/`), state
  `$XDG_STATE_HOME/agentd` (fallback `~/.local/state/agentd`): `mailbox.db`, `docs.json` (document store, 0600), `hc/core-id`, `api/devices.json` (device tokens, dir 0700, file 0600, `api/devices.lock`), `api/tls/{cert.pem,key.pem}` (self-signed gateway cert), `logs/<session>.log`,
  `daemon.log`. Config `~/.config/agentd/config.toml`. `AGENTD_SOCKET` env overrides the socket path
  (tests must use a temp socket + temp state dir via `AGENTD_STATE_DIR`).
- **Daemon lock**: the daemon holds an exclusive `flock` on `<socketdir>/agentd.lock` for its whole
  lifetime. Only the lock holder may remove a stale socket file and bind; a second daemon fails with
  `ErrAlreadyRunning`. The socket is created then immediately chmod'ed 0600 (no umask games: the 0700
  dir already protects it). On shutdown the socket is unlinked only if it is still the inode it bound
  (`os.SameFile`). The daemon `chdir`s to `/` at start (relative `AGENTD_*` path overrides are made
  absolute first). Client autostart tolerates concurrent starters (losers exit, everyone waits for
  ping) and starts the daemon without session-scoped `AGENTD_*` vars (path overrides are kept) and
  without `CLAUDECODE*` / `CLAUDE_CODE_ENTRYPOINT`.
- **Accept loop**: accept errors never stop the daemon; transient ones (EMFILE, ENFILE,
  ECONNABORTED…) are retried with exponential backoff (5 ms → 1 s). Connections beyond `MaxConns`
  (default 1024) are closed on accept.
- **Herdr compatibility** (proof of concept; analysis `docs/architecture/herdr-compatibility-analysis.md`, plan
  `docs/plans/agentd/herdr-compat_plan.md`): `agentd hc install-shim [--dir D]` makes `herdr-core` and `hc` symlinks
  to agentd (default `~/.local/share/agentd/hc/`; never replaces an existing file or foreign symlink). Invoked under
  those names: `--version` prints `hc 0.1.21` (`hcompat.CompatVersion`, plain x.y.z: Herdr Desktop parses it by
  `.`/`-` and needs >= 0.1.11), `connect [--session S]` speaks hc over stdio (session ignored), `machine ls --json`
  prints `{"machines":[]}`, `update` and anything else exit 2 with `{"error":{message,hint}}` on stderr. Addresses
  are agentd session names; default labels `herdr.space` (project: part after `@`, else cwd base), `herdr.tab`
  (session name), `agentd.harness`. Status: working->working, waiting->blocked (kind question), idle->idle, others
  null (heuristic, a deviation from hc's OSC 7501). `hello` advertises only implemented methods; `server.status`
  `installed_by:"agentd"`. Core id persisted in `$STATE/hc/core-id`. The translator keeps no state: labels go to op `label`
  (hc `keep` = reserved label `agentd.hc.keep`; the first client label change materializes the defaults and sets
  `agentd.hc.labels` so deleting a default sticks; reserved labels are hidden and refused), documents to
  `doc_get`/`doc_set` (`herdr-app` holds Desktop's spaces/tabs/layout), `turn` comes from the daemon. Attach: the
  daemon stream is always drained; output waits for credit (256 KiB window); a viewer more than
  `Server.MaxPending` (4 MiB) behind gets a repaint (daemon stream reopened, its redraw = CAN + screen) instead of
  the backlog. Results are tested against the vendored `internal/hcompat/testdata/herdr-core-released.openrpc.json`
  (Apache-2.0, `testdata/NOTICE`). The translator connects to the daemon as the calling process, so identity rules
  apply unchanged (inside a session, `doc_set` is refused).
- **HTTP gateway** (frozen 2026-11-14: kept for possible future use, no new features; the Flutter app was removed) (`agentd serve [--listen addr] [--config path] [--no-autostart] [--log-level l]`, design:
  `docs/design/api/http-api.md`): a separate foreground process (logs to stderr, exit non-zero on config
  error) that talks to the daemon over the unix socket and stops on SIGINT/SIGTERM without touching the
  daemon (SSE streams get `bye`, 5 s drain). Config table `[api]` (parsed in `internal/config/api.go`,
  defaults applied on load): `listen` (`127.0.0.1:7477`), `tls` (`off`|`self-signed`|`files`; `tls_cert`,
  `tls_key`; `tls_hosts` = extra SANs of the self-signed cert), `cors_origins`, `allowed_hosts`, `max_body_kb` (256), `sse_heartbeat` (`15s`),
  `event_buffer` (1024), `idempotency_ttl` (`10m`), `run_harnesses` (default: built-in profiles except
  `default`), `run_env` (`daemon` only), `pairing` (default true: mounts `POST /v1/pair`), `autostart_daemon`
  (default true; request-time only: the SSE hub never starts a daemon), `rate_limit`/`rate_burst`/`act_rate_limit`
  (20/40/5 per device), `max_streams_per_device`/`max_streams` (8/64), `audit_max_mb` (10). A non-loopback `listen` with `tls = "off"` is a startup error with
  no override (also for `--listen`). Every route except `GET /v1/health` requires an `Authorization: Bearer`
  token (`agd_<id>_<secret>`, header only; a token in the query string is a 400) verified by
  `internal/apitoken` against `api/devices.json` (scopes read<write<admin, 90-day inactivity expiry,
  `last_seen` persisted at most once a minute, revoke = next request + open streams cancelled within ~1 s,
  file change noticed within ~1 s; malformed/group-readable file = deny all, fatal on non-loopback binds).
  `agentd serve devices [list|revoke <id|name>|add --name N [--scope S] [--ttl 30d]]` manages them.
  `agentd serve pair [--scope write|read|admin] [--name HINT] [--ttl 5m] [--url URL] [--no-approve] [--no-pin]`
  issues a one-time 60-bit code (`XXXX-XXXX-XXXX`, TTL 5 min, max 30; at most 3 live codes), prints a terminal QR
  of `agentd://pair?v=1&u=<url>&c=<code>&fp=<spki sha256, tls=files only>&h=<host>` plus URL/code, and waits;
  the device `POST /v1/pair {code,name,platform}` (unauthenticated, mounted via `httpapi.Server.AddRoute`) is
  held open until the host answers `Approve? [y/N]` (default no; `--no-approve` for scripts) and then gets
  `201 {token, device_id, scope}` (default scope `write`, `paired_via: qr`). Single use; 5 failed attempts burn
  the live codes; 10 requests/min per remote address; 20 failures/10 min lock pairing for 10 min (429);
  denied 403 `pairing_denied`, wrong/used/expired code 403 `pairing_invalid`, expiry while waiting 410.
  The CLI and the gateway share `api/pairing.json` under `api/pairing.lock`; codes and tokens are never logged. Errors are `{"error":{"code","message","retryable","details?"}}`; daemon codes map to HTTP
  statuses in `httpapi.FromDaemonError` (internal/unknown daemon errors become a generic `500 internal`, detail only
  in the log). **Hardening (AGED-US-0024)**: unknown `Host` => 421 before auth (allow-list = loopback names, bind
  address, hostname, `tls_hosts`, `allowed_hosts`); `Origin` must be in `cors_origins` (default none, `null`
  never) else 403; POST bodies must be `application/json` (415); `nosniff`/`no-referrer`/`no-store` headers; per-device
  token buckets, failed-auth limit per address (10/min), SSE caps, 32 concurrent daemon calls (503 `busy`), `/v1/health`
  cached 1 s; `http.Server` header timeout 10 s / idle 120 s / 16 KiB headers. Side-effecting calls
  (`run/act/kill/rm/send/reply/view_ack`) use a fresh daemon connection, are never retried, and a lost answer is
  `502/504 outcome_unknown` (not retryable; act deadline 40 s); idempotency keys keep such outcomes as unknown (409 on
  retry). `args`/`cmd`/`env` on `POST /v1/sessions` and `?mailbox=` need the admin scope; act text <= 16000 chars.
  ETags and view event ids are `e<epoch>-i<instance>-r<rev>` (instance = session start time). Audit trail
  `api/audit.log` (JSONL 0600, size-capped, one rotation `.1`; run, act, kill, rm, send, reply, view_ack, device
  pair/add/revoke; never prompt text or bodies). `httpapi.Server.Guard` (device-token guard) runs inside the
  request-id/access-log chain; `last_seen` writes are asynchronous and coalesced. 
  **Notifications** (`internal/httpapi/notify.go`, `[api.notify]`, off by default): the gateway taps its event hub
  (`hub.tap`, never HTTP) and, for `attention.changed` (set; reason `waiting`/`bell`/`notify` map to `waiting`,
  `turn_done` stays) and `session.exited`, queues a POST per sink: ntfy (`ntfy_url`, optional Bearer `ntfy_token` or `ntfy_token_env`;
  headers Title/Tags/Priority/Click, short text message) and webhook (`webhook_url`, JSON `{host, session, reason,
  status, exit_code?, at, link, preview?}`, optional `X-Agentd-Signature: sha256=<hex HMAC-SHA256 of the body>` from
  `webhook_secret` or `webhook_secret_env`). `on` filters reasons (`waiting`, `turn_done`, `exited`), `debounce` (30s) is per session+reason.
  Secrets: `*_env` names an environment variable read at `serve` start (wins over the plain value; named but unset = error
  unless a plain value exists); a plain secret in `config.toml` makes `serve` refuse to start unless the file is owned by the user
  with no group/other bits (`config.NotifyConfig.ResolveSecrets`).
  No screen text unless `include_preview` (then only the sanitized preview line, <=160 chars). `link` =
  `deep_link_base` (`agentd://open`) `?h=<host_label>&s=<session>`. One worker, bounded queue (256, drops oldest),
  10 s timeout, 3 retries with backoff (429/5xx/network only), no redirects; URLs and secrets are never logged.
  Config validation: https required unless the host is loopback. Disabled = no goroutine, no hub tap.
  **TLS** (`internal/tlsutil`): `self-signed` generates and persists an ECDSA P-256 cert (5-year validity,
  SANs = hostname, `localhost`, loopback, every interface IP, `tls_hosts`; no external commands) under
  `$XDG_STATE_HOME/agentd/api/tls/{cert.pem,key.pem}` (dir 0700, key 0600), reused across restarts (corrupt
  state is an error, never a silent new pin; expired = regenerated). `files` serves `tls_cert`/`tls_key`
  (`tailscale cert`), reloaded on SIGHUP or file change (mtime, checked at most 1/s per handshake; a broken
  replacement keeps the old cert). TLS 1.2 minimum with ECDHE+AEAD suites (1.3 when offered). Pin =
  `sha256:<hex>` of the leaf SPKI (`tlsutil.Pin`; `CurrentPin(mode, cfg)` is what `agentd serve pair` puts in
  the QR `fp`). `agentd serve tls [show|rotate]` prints pin/SANs/expiry or regenerates the self-signed pair
  (restart serve; devices re-pair). Tailscale identity headers are never trusted.
- **Project aliases**: short name → working directory, kept apart from `config.toml`.
  - File `~/.config/agentd/aliases.toml` (override path with `AGENTD_ALIASES_FILE`; respects
    `$XDG_CONFIG_HOME`), flat table: `webapp = "~/src/webapp"`. `~` is expanded;
    paths must be absolute after expansion.
  - Env `AGENTD_ALIASES="webapp=~/src/webapp,api=/srv/projects/api"`
    (comma-separated `name=path`); entries override the file. Missing file = no aliases.
  - Alias names follow the session-name charset without `@`. Resolution (`ResolveTarget(arg)`):
    alias match → its dir, label = alias; otherwise the arg is a path (relative to cwd, must exist and
    be a directory), label = alias whose dir equals it, else basename (generic basenames `devops`,
    `app`, `src`, `repo` become `<parent>-<base>`).
  - CLI: `agentd run <alias|path> [--harness codex] [--name …] [--size COLSxROWS] [-- cmd…]` (the CLI sends
    `target`, `harness` and its cwd; the daemon resolves them): cwd = resolved dir, default
    command = harness binary, default name `<harness>@<label>`, default profile = harness.
    `agentd aliases` lists them with their source (file/env).
- **Session names**: `[a-zA-Z0-9._@-]+`, unique; convention `<harness>@<project>` (e.g. `codex@webapp`).
- **Child env**: the daemon sets `AGENTD_ID=<session name>`, `AGENTD_SOCKET=<path>` and `TERM=xterm-256color`
  in each child, plus `AGENTD_TOKEN` (random per-session secret, see "Identity"). Who is speaking is
  decided by the daemon, not by these variables (`AGENTD_ID` is only a hint).
  - **Env base**: `run` carries the CLI's environment (`RunArgs.env_base` = `os.Environ()` of the
    caller); the daemon uses it as the child's base (falling back to its own environment when absent),
    always stripping `AGENTD_*`, `CLAUDECODE`, `CLAUDE_CODE_ENTRYPOINT` and any name/glob in config
    `[run] strip_env = [...]`. Precedence: base < `--env` < daemon-set `AGENTD_*` < `TERM`.
- **Session lifecycle**: (starting →) running → exited (exit code kept). `run` reserves the name and
  starts the process without holding the registry lock; while starting, `ls` shows the session with
  state `starting` and other ops answer "is starting". Exited sessions stay listed until
  `agentd rm` (or `kill --rm`); their final screen remains dumpable.
- **PTY size** (fixed, ADR-A02): a session started without an explicit size gets its profile's
  `cols`/`rows` (`[profiles.X]`, built-in default 160x50, 1..1000), never the caller's terminal size: the
  CLI `run` no longer sends its terminal size (`--size COLSxROWS` sets one explicitly; `RunArgs.cols/rows`
  are honoured per dimension when non-zero). Local `attach` keeps resizing (the most recently active
  client's size wins); when the **last** attached client detaches the daemon resizes the session back to
  the size it started with (the profile size unless `run` gave one) if `[run] restore_size_on_detach`
  (default true). API clients never resize. `session.DefaultCols/Rows` (120x40) are only the library fallback.
- **Write timeouts**: PTY input (human keystrokes, `type`, injections, terminal replies) is written in
  4 KiB chunks with a per-chunk deadline (default 5 s, `Server.WriteTimeout` / `session.Spec`): a child
  that stops reading stdin makes the write fail instead of holding the input lock forever, and closing
  a session aborts pending writes at once. Shutdown stops delivery and closes sessions in parallel.
  Attach: client input goes through a bounded per-attach queue (excess dropped), so resize/detach are
  always processed; every frame to an attached client has a write deadline (default 10 s) and a client
  that misses it is dropped.
- **Session logs**: `logs/<session>.log` is capped at config `[run] log_max_mb` (default 50, 0 = no cap)
  with a single rotation to `<session>.log.1`.
- **Idle detection** (per harness profile, configurable): idle ⇔ no PTY output for `idle_ms`
  (default 1500) AND (no `prompt_regex` configured OR it matches the current screen text) AND no
  human keystroke from any attached client (or key written by an `act`: `Session.LastInput`) for
  `human_grace_ms` (default 5000) AND the session has
  produced output at least once AND it started at least max(`idle_ms`, 3 s) ago AND no pending human
  draft (human typed bytes that were not followed by `\r`, `\n` or Ctrl-C, or text an act pasted without
  submitting; cursor/focus/mouse sequences ignored) younger than 10 min. Terminal replies from attached clients (CPR, DA, DECRPM,
  OSC colour answers, DCS) are not human input and are forwarded only from the most recently active
  client (`internal/daemon/termreply.go`). The `prompt_regex` check uses the ghost-filtered screen.
- **Ghost text** (placeholder / suggested next prompt drawn by the harness in its input line, e.g.
  `❯ Try "fix lint errors"`; captures in `docs/ghost-text.md`): it is output, never user input.
  `session.ScreenFiltered(ScreenOpts{HideGhost, Ghost: GhostRules})` blanks it and returns it. Scope is
  only the input line = the cursor's row from the cursor column rightwards (Codex draws other faint
  text elsewhere). Profile rules: `ghost_faint` (default true: faint SGR 2 cells), `ghost_colors`
  (fg colours, `"N"` palette index or `"#RRGGBB"`, default none), `ghost_right_of_cursor` (only when
  the text left of the cursor ends with `prompt_marker`, ignoring spaces/NBSP; with no style rule set,
  every non-blank cell in scope is ghost, otherwise cells must also match a style rule) and
  `prompt_marker`. Built-ins: claude `❯` + right-of-cursor + `prompt_regex '(?m)^❯([ \x{a0}]|$)'`
  (Claude Code puts U+00A0 after `❯`); codex `›` + right-of-cursor + `'(?m)^›( |$)'`; pando and
  default: faint only, no marker. Drafts are keystroke-based, so ghost text never counts as one.
  `dump` hides it by default (`ghost:true` = raw; ANSI dumps are raw) and reports it in
  `DumpResult.ghost` / `ghost_hidden`; the CLI then prints a final line
  `[agentd: suggested prompt hidden: "<text>"]`. `SessionInfo.input_state` (running sessions):
  `draft` (pending human draft < 10 min) > `suggestion` (ghost visible) > `empty`.
- **Frame-aligned PTY chunking** (`internal/session/syncgate.go`, `feed`/`flushHeld`): a PTY read that ends inside a
  DEC 2026 synchronized-update frame (last `ESC[?2026h` with no later `ESC[?2026l`) is split in `feed`: the part before the
  BSU goes through `handleOutput` at once, the open tail is held and prepended to the next chunk, so chunks end on frame
  boundaries when the child delivers complete frames. The held tail is released as is after 100 ms (`syncHold`, timer) or
  when it would reach 256 KiB (`syncHoldMax`, not held at all), and at PTY EOF; `SyncOpen`/`SyncTimeout` semantics are
  unchanged. Everything in `handleOutput` is delayed for the held bytes, not only the emulator: the log file, the ring
  buffer, attached clients (fan-out), `lastOutput` (idle detection), `OutputGen` and `Changed()` see them only when the frame
  closes or is released (at most 100 ms; no effect on children that do not use 2026). History sampling is sync-aware:
  `screen.SyncSettleMax` (50 ms, `ShouldObserveSync`) and `viewSyncDebounce` (40 ms, `internal/daemon/view.go`) replace the
  longer settle/debounce when the child uses synchronized updates, so complete frames are sampled often enough
  (AGED-US-0038).
- **Emulator input filter**: PTY output goes to the VT emulator through `session.stringFilter`
  (`oscfix.go`): inside ESC-introduced OSC/DCS/SOS/PM/APC strings, a UTF-8 rune containing byte
  `0x9C` becomes U+FFFD (x/ansi otherwise takes that byte for 8-bit ST, ends the string and prints
  the rest, e.g. Claude Code's `✳ <topic>` titles leaked into its input line). The log, ring buffer
  and attached clients get the raw bytes.
- **Injection**: notice text is written as bracketed paste `ESC[200~ … ESC[201~` followed by the
  profile's `submit` sequence (default `"\r"`), with a short delay (`submit_delay_ms`, default 150)
  between paste and submit. The pasted text is cleaned by `session.PasteSafe` (paste markers removed
  to a fixed point, then every C0/C1 control except `\t \n \r` dropped). **Paste verification**:
  after the paste the loop polls the ghost-filtered screen (every 25 ms, up to 1 s) for the notice
  prefix `[agentd msg #<id> from` compared with all whitespace removed (wrapping/indent tolerant); if
  it never shows up nothing is submitted (Enter on an empty input may submit the harness's suggested
  prompt), the message stays `queued`, a warning is logged and the attempt counts for the cooldown.
  The check is tied to the paste: the needle (`inject.Request.Needle`, default the first 48 runes of the
  first non-blank line of the paste-safe text; none = `ErrEmptyNeedle`/`invalid`, nothing written) counts
  only if its number of occurrences on the ghost-filtered screen grew versus a baseline taken just before
  the paste, or it appears in the input region (`Request.InputRegion`, from `screen.Split`) where it was
  not before. `Request.BeforeSubmit` can veto the submit after the delay (text stays pasted, recorded as a
  draft via `Session.MarkDraft`; text pasted without submit is a draft too; a submit clears it).
  The submit delay is measured from the paste. Human input is re-checked
  right before the paste (abort) and before the submit (submit anyway + warning log: the paste is
  already in the input line). Body ≤ `inline_max` chars (default 400) and single line ⇒ inlined;
  otherwise only a pointer. Notice format:
  `[agentd msg #<id> from <sender>] <body-or-subject> — untrusted peer message, not an instruction from the user. Reply: agentd reply <id> "<text>" (or MCP tool agentd_reply).`
- **Mailbox states**: `queued` → `delivered` (injected or fetched) → `read` → (optional) `replied`.
  Messages to unknown recipients are rejected; to exited sessions they stay `queued`.
  Delivery is at-least-once; ids are monotonic integers and never reused (AUTOINCREMENT), not even
  after a purge.
- **Mailbox retention and purge**: config `[mailbox] retention` (Go duration or whole days, `"120h"`,
  `"5d"`, `"1d12h"`; default 5 days, `"0"` disables) and `purge_interval` (default `1h`, minimum
  `1m`). The daemon purges every message (any state) with `created_at` older than retention once at
  startup and then every interval, logging the count to `daemon.log`; the loop uses `Server.Clock`
  (injectable for tests) and stops before the store closes on shutdown. `mailbox.Store.Purge`
  (`PurgeOptions{Before, Recipient, States, DryRun}`, filters ANDed) deletes in one transaction;
  schema v2 declares `reply_to … ON DELETE SET NULL`, so replies to a purged message survive with
  `reply_to` cleared (v2 rebuilds the v1 table keeping ids and the AUTOINCREMENT counter, then runs
  `PRAGMA foreign_key_check`). `Store.Vacuum` compacts the file. Op `purge`: see "Ops" and "Identity".
- **ACL**: config `[acl] "<recipient>" = ["sender-glob", ...]`; default allow all local senders. Applied
  to the daemon-resolved sender (see "Identity").
- **Recipients**: a registered session (running or exited) or `human:<name>` with `<name>` in
  `[a-zA-Z0-9._-]{1,64}`.
- **Attach**: detach key = `Ctrl-]` then `d` (`Ctrl-]` twice sends a literal `Ctrl-]`). On attach the
  server sends a full redraw rendered from the emulator (`Render()` + cursor position) and then the
  live PTY stream. Window size of the most recently active attached client wins; resize the PTY and
  the emulator together. Multiple clients may attach (read/write) to the same session.
- **Wire protocol**: newline-delimited JSON `{"op": "...", "args": {...}}` →
  `{"ok": true, "data": ...}` / `{"ok": false, "error": "...", "code": "..."}`. `code` is an optional
  stable error class (`proto.Code*`: `invalid`, `not_found`, `starting`, `exited`, `exists`,
  `forbidden`, `conflict`, `busy`, `not_ready`, `verify_failed`, `timeout`, `internal`); `error` wording is
  unchanged, old clients ignore `code`, and `""` means unclassified. Server side: `proto.CodedError` /
  `proto.Errorf(code, …)` (supports `%w`; `CodedError.Err`/`Unwrap` keep the cause for `errors.Is/As`); the
  daemon maps `session.ErrWriteTimeout` → `timeout`, `inject.ErrPasteNotVisible` → `verify_failed`,
  `inject.ErrDraft` → `busy`, purge argument errors → `invalid` (session permission refusals → `forbidden`); client side: `*proto.RemoteError{Msg, Code, Data}`, `client.ErrorCode(err)`.
  Op `attach` upgrades the connection to
  binary frames `[1 byte type][4 bytes big-endian length][payload]`, types: `D` data, `R` resize
  (`uint16 cols, uint16 rows`), `X` detach/close, `E` exit (payload = exit code as text).
  Op `subscribe` upgrades the connection to a server→client stream of newline-delimited JSON events
  after its OK response line (`{epoch, seq, sessions[]}`; anything the client writes is discarded, the
  client closes to unsubscribe); see "Event bus".

### Ops (daemon API, mirrored by CLI and partially by MCP)

`ping` (→ `{version, pid, epoch}`; `epoch` is unique per daemon start),
`info {env_base?[]}` (→ `{host, version, pid, epoch, harnesses[], profiles[{name, builtin}], aliases[{name, dir, source}]}`: the
daemon's view of config and aliases; with `env_base` the aliases are those a `run` carrying the same `env_base` resolves),
`run {name, target?, harness?, cwd, cmd[], env_base[], env{}, profile, cols, rows}` (with `target` + `harness`
the daemon resolves like the CLI via `config.Aliases.ResolveRun`: `target` = alias or directory, a relative path
is resolved against the caller's absolute `cwd`; `harness` defaults to `cmd[0]` or `claude`; empty `cmd` =
`[harness]`, empty `name` = `<harness>@<label>`, empty `profile` = harness basename; the daemon re-reads
`aliases.toml`/`AGENTD_ALIASES` on every `run`/`info`, reading `AGENTD_ALIASES`, `AGENTD_ALIASES_FILE`,
`XDG_CONFIG_HOME` and `HOME` (for `~`) from `env_base` when present, else from its own environment;
`agentd aliases` reads the CLI's own environment, so it agrees with `run`), `ls` (a session caller without `[security] agent_view` gets other sessions' `preview` blanked; status and
attention stay), `kill {name, signal}`, `rm {name}`,
`dump {name, scrollback:bool, ansi:bool, ghost:bool, cells:bool, row?:int}` (→ `{text, ghost, ghost_hidden, cells?}`; `cells` = `{cursor_x, cursor_y, row, cells:[{x, content, rune, fg, bg, attrs[], width}]}` for `row` or the cursor's row, diagnostics), `type {name, text, submit:bool}` (raw injection, for humans/scripts),
`send {to, subject, body, reply_to}`, `inbox {as?, all:bool}`, `read {as?, id}`, `reply {id, body}`,
`purge {before?: RFC3339 | older_than?: duration, recipient?, states?[], all:bool, dry_run:bool, vacuum:bool}`
(→ `{count, dry_run}`; at least one filter or `all` is required),
`view {name, since_revision?, epoch?, lines_limit?, from_line?, scrollback?}` (→ the semantic View
`{name, epoch, revision, status, status_reason, attention{set,reason,since}, title, cwd, first_line,
lines[{text, role}], input{text, ghost, state}, options[{id, label, index, kind, selected, checked?,
confidence}], hints[{key, action}], dialog?{question}, idle_ms, exit_code?}`, or
`{not_modified:true, epoch, revision}` when `since_revision` is still current AND `epoch` equals the daemon's
(a missing or foreign `epoch` never gets `not_modified`);
options never carry key sequences), `view_ack {name}` (→ `{revision}`; clears attention, humans only),
`subscribe {sessions?[], kinds?[], since_seq?, epoch?, heartbeat_ms?}` (`since_seq` requires `epoch`, else `invalid`) (→ `{epoch, seq, sessions[]}` then an event
stream, see "Event bus"; CLI `agentd events [--session X] [--kind K] [--json]`),
`act {name, action, text?, submit?, option_id?, label?, revision?, epoch?, keys?[], force?}` (→ `{revision, status, result,
submitted?, warning?}`; actions `prompt|choose|keys|interrupt|cancel`, see "Act"; CLI `agentd act NAME ACTION ...`),
`label {name, labels{}}` (→ `{labels}`: merge, an empty value removes the key; keys without spaces/`=`/controls
<=128 bytes, values <=1024, at most 64; humans any session, a session only itself unless `[security] agent_type`;
publishes `session.labels {labels}`; `run {labels}` sets the initial ones; `ls`/`SessionInfo.labels`),
`doc_get {name}` (→ `{value, version}`; never written = `null`, 0) / `doc_set {name, value, version}` (→ `{version}`;
optimistic: a `version` other than the current one is `conflict`; `null` deletes the value and keeps the version;
names `[A-Za-z0-9._-]{1,64}`, value <=1 MiB of JSON (`daemon.MaxDocBytes`); humans only, sessions may read; stored
compacted in `docs.json`, atomic rename; a broken file is renamed `.bad` and the store starts empty; publishes
`doc.changed {name, version}` with no session, so session subscribers never see it),
`peers`, `whoami`, `attach {name, cols, rows}`, `shutdown`. Every request may carry
`"auth": {"id", "token"}` (filled by `internal/client` from `AGENTD_ID` / `AGENTD_TOKEN`).

MCP tools (`agentd mcp`, identity resolved by the daemon, see "Identity"): `agentd_peers`,
`agentd_send`, `agentd_inbox`, `agentd_read`, `agentd_reply` (no purge tool). The mailbox tools call `whoami` first
and fail with a clear error when the daemon sees the caller as a human (MCP server not started from
inside a session). No run/kill/type over MCP in v0.1 (an agent must not spawn or drive other agents'
terminals directly). `agentd_peers` also returns each peer's `status` and `attention` (no screen content).
`agentd_view {name, lines?}` (read-only; status, input state, option labels, last N transcript lines, default 40,
max 200, framed as untrusted peer screen content) is registered only when the daemon reports
`info.agent_view` (`[security] agent_view = true`, default off); the daemon enforces both that switch and `[acl]`
(a session may view a peer only if it may message it).

### Act (op `act`)

Types into agent terminals: when in doubt it refuses with a coded error instead of sending keys
(`internal/daemon/act.go`, `keys.go`; wire types `internal/proto/act.go`; design `docs/design/api/daemon-ops.md` §3).

- **Serialization**: every action holds the per-session `inject.Lock` (shared with the delivery loop and
  other acts); a lock wait over 10 s is `busy`. The request context is cancelled when the client
  disconnects (a Peek watcher on the connection), so an abandoned act never types later.
- **prompt** `{text, submit?}`: `inject.Input` (paste verified, submit by default, `verify_failed` when
  not visible). Needs status `idle`, or `waiting` with no options (free text), else `not_ready`; `force`
  overrides the status. A pending draft (human-typed, or pasted by a prompt and not submitted; < 10 min) is
  always `busy` (`force` never overrides it); a non-empty input line (`view.input.text`) is `busy` too; a
  real human keystroke within `human_grace_ms` is `busy` unless `force`. Text 1..64 KiB with at least one
  non-blank line after `PasteSafe` (else `invalid`). The paste verification is tied to the paste (see
  "Injection"); right before the Enter a `BeforeSubmit` re-check runs (draft, human keystroke, a menu that
  appeared since the guard): on failure nothing is submitted, the text stays pasted, is recorded as a draft
  (`Session.MarkDraft`) and the answer is `conflict`. `submit:false` also leaves a draft until a submit,
  Ctrl-C (forced interrupt) or the 10-minute timeout; a draft is cleared by a submit (`ClearDraft`).
- **choose** `{option_id, label?, revision, epoch}`: `revision` and `epoch` (both from the view) are
  REQUIRED (missing = `invalid`; an `epoch` that is not the daemon's = `conflict`; this holds for any act
  that carries a `revision`). The daemon forces a view refresh and re-detects on a fresh snapshot. The option
  id must exist (ids hash question + kind + labels), `label` must match, and the unit of identity is the menu
  INSTANCE (`viewState.instanceFor`): fingerprint + `screen.MenuContext` (hash of up to 8 non-blank rows above
  the dialog question) + transcript length and tail. The client's `revision` is accepted if it is the
  current one, or if the instance recorded for it (`menuTrack`, per revision) equals the current instance;
  otherwise `conflict` with `data = {revision, view}` (the view only for callers allowed to view the
  session). Needs status `waiting`/`idle` (`force` overrides), no pending draft, and no real human keystroke
  within `human_grace_ms` unless `force` (`busy`). Then runs `screen.PlanChooseKeys` step by step: write,
  wait for `OutputGen` to advance and the screen to change (`timeout` otherwise) and go quiet, re-detect,
  check every `Expect` only against the menu with the plan's fingerprint AND instance. **Every step after
  the first requires the same instance on screen before its key is written** (yes/no word + Enter
  included); if the menu is gone or replaced the plan stops with `conflict` (an `IfNotGone` step instead
  ends the plan as done: the previous key already chose). Digits in numbered menus follow the profile key
  `menu_digit_action`: `select` = the digit only, never an Enter afterwards (verified by the instance
  disappearing or changing); `move` = the digit, then Enter only if the same instance is up with the pointer
  on the target. Arrow navigation + Enter is used for options without a digit (or `menu_select = "arrows"`).
  SS3 arrows in application cursor mode.
- **Revision on the other actions**: `prompt`, `keys`, `interrupt` and `cancel` take an OPTIONAL `revision`
  (+ required `epoch`; the CLI omits it, API clients should send it). When set, the daemon compares the
  interaction fingerprint of the current view (`interactionFP`: status, menu instance + options with
  pointer/checks, dialog question, pending-draft state; NOT transcript lines, title, cwd, idle time or a
  ghost suggestion) with the one recorded for that revision (`menuTrack.noteInter`). A change, an unknown or
  future revision is `conflict` with `data = {revision, view}` and nothing is sent (prompt: checked in the
  inject guard, before the paste). `force` does not override it.
- **keys** `{keys[]}`: abstract names only (`enter esc tab shift-tab up down left right home end pgup
  pgdown backspace delete ctrl-c ctrl-d ctrl-l space y n 0-9`), 1..32 keys, 30 ms apart; an unknown
  name (raw bytes included) is `invalid` before anything is written. `enter` = profile `submit`;
  cursor keys use SS3 when the session is in DECCKM mode. Refused with `busy` on a pending draft.
  `enter` with no menu on screen, an empty input line and a ghost suggestion visible, reached before any
  key that types text (`y n space 0-9`), is `not_ready` (the harness would submit the suggestion; `force`
  does not override). Keys containing `ctrl-c` / `ctrl-d` get the interrupt status guard (working or
  waiting, else `not_ready`; `force` overrides).
- **interrupt** / **cancel**: profile keys `interrupt_keys` (default `["ctrl-c"]`; claude and codex
  `["esc"]`, to confirm against the real TUIs, plan F8) and `cancel_keys` (default `["esc"]`). interrupt
  needs `working` or `waiting` (`not_ready` at idle: ctrl-c may quit the harness; `force` overrides).
  Both refuse a pending draft with `busy` unless `force`.
- **Input marking**: draft-neutral keys of an act (escape sequences, Enter, Ctrl-C) are written with
  `Session.WriteAct`: they clear the attention flag, hold delivery for `human_grace_ms` and track drafts
  like human input, but record `LastActInput`, never `LastHumanInput` (real attach-client keystrokes only;
  `LastInput` = the later of both feeds delivery idle detection and the status classifier). Printable
  keys are plain writes (a lone `y` would count as a ten-minute draft). So an act never makes the next act
  `busy`, while a real keystroke does. prompt is not human input (completed by its submit).
- **Profile key** `menu_digit_action = "select" | "move"` (config default `move`; built-in `claude` and
  `codex` = `select`, both to confirm against the real TUIs, plan F8): see choose.
- **Audit**: every act logs `act` at info to `daemon.log` (session, action, caller, via, force, option
  id/label, key names, outcome code); prompt text is never logged (only its length).

### Semantic view (op `view`)

Each registered session has a daemon-side view state (`internal/daemon/view.go`): the transcript
`screen.History`, the `screen.Attention` tracker, the interpretation of the last snapshot and the
cached View. A per-session watcher goroutine (started by `run`, ended by session exit, `rm` or daemon
shutdown) refreshes it on output (100 ms debounce, `screen.ShouldObserve` settle policy) and on timers
so `working → idle` without output is noticed; **History sampling** (`viewState.observeLocked`): never while the child has an open synchronized update
(DEC 2026 BSU without ESU; `Snapshot.SyncOpen`, `Session.InSyncUpdate`, honoured at most
`session.SyncTimeout` = 1 s; `Session.SyncFrames` / `Snapshot.SyncFrames` count completed updates, tracked
in order by `trackMode` so a set+reset inside one PTY chunk counts). A child that uses 2026 is thus observed
on complete frames only. Without 2026, a sample taken before the output went quiet (`SettleMax`) uses
`History.ObserveForced`: rows matching `spinner_chars`/`busy_regex` (`HistoryOpts.Transient`, all observations)
never enter the history, and a frame that does not align with the stored tail is deferred to the next settle
(up to `screen.MaxDeferrals` = 3 in a row) instead of inserting a gap; a quiet-settled frame is always accepted. `view` also refreshes on every call. `revision` starts
at 1 and bumps only when the published fingerprint changes (status, reason, attention, input,
options, hints, dialog, title, cwd, exit code, transcript tail); `idle_ms` and `attention.since` are
not part of it, and the title is fingerprinted without a leading spinner/braille glyph (an animated
`✳`/`⠋` title does not bump the revision; the view still returns it raw). `epoch` is the daemon start epoch (revisions restart with it). `first_line` is the
absolute index of `lines[0]` (page with `from_line`; default = newest `lines_limit`, 500, max 10000).
`scrollback:true` returns scrollback + screen as one unpaged transcript (`first_line` 0, main screen
only). `ls` carries `status`, `attention`, `revision` and `preview` (last non-blank transcript line,
sanitized, ≤160 chars) from the cached view (blanked for other sessions when the caller is a session without
`agent_view`; `dump` is a pre-existing feature that still returns any session's screen to any caller, a known
gap tracked apart). CLI: `agentd view [NAME] [--json] [--since N] [-n LINES]
[--scrollback]` (NAME defaults to `$AGENTD_ID`, or the only session).

### Event bus (op `subscribe`)

`internal/daemon/events.go`. Event line: `{"seq", "epoch", "time", "kind", "session"?, "data"?}`; `seq` is
monotonic per `epoch` (random per daemon start; seqs and view revisions reset with it) for bus events and
`0` for control events. Kinds: `hello` (first line; data `{epoch, seq}`), `session.created` /
`session.started` (data: session info; both published only after a successful start, so a failed `run`
emits nothing), `session.exited` `{exit_code}`, `session.removed` (always after `session.exited`: `rm` waits
for the exit publisher),
`status.changed` `{from, status, reason, attention, revision, turn?}` (`turn` = times the published status left
`working`, also `SessionInfo.turn`), `view.changed` `{revision}` (no content:
call `view`), `input.changed` `{state}`, `attention.changed` `{attention, reason}`, `mail.queued` /
`mail.delivered` / `mail.read` `{id, from, to}` (never the body; `session` = the recipient when it is a
session), `session.labels` `{labels}`, `doc.changed` `{name, version}` (no `session`), `resync` `{reason: overflow|since_seq_too_old|epoch_mismatch}`, `heartbeat` (every 15 s,
`Server.HeartbeatInterval`; `heartbeat_ms` min 1000) and `bye` (daemon shutdown, sent before connections
are closed). `kinds` filters by exact kind or prefix (`session`, `status`, `view`, `input`, `attention`,
`mail`, `doc`); control kinds always pass.
- The bus keeps a 1024-event replay ring. `since_seq` requires `epoch` (`invalid` otherwise); inside the
  ring (same `epoch`) it replays the missed events, then goes live; outside it, or with another `epoch`,
  the stream starts with `resync`.
- Snapshot ordering: the subscriber is registered first, `seq` is read, then the `sessions[]` snapshot is
  built; queued live events with `seq` <= the reported `seq` are skipped (already reflected), so no event
  predates the snapshot. A `since_seq` replay is not filtered.
- Every subscriber has a bounded queue (256). `publish` never blocks: on overflow the queue is dropped,
  one `resync` is queued and the stream continues live. A write exceeding the attach write deadline
  drops the client. Publishers: handlers (`run`, exit watcher, `rm`, `send`, `reply`, `inbox`, `read`),
  the per-session view watcher (only when the published fingerprint/revision, status, attention or input
  state changes, never per output byte) and `delivery.Loop.OnDelivered` (set by `NewDeliveryLoop`;
  `delivery` does not import `daemon`).
- Identity: humans see every event; a session sees only events about itself and mail it sent or
  received (`[security] agent_view = true` also lifts the session-event restriction, never the mail one).
  A session-identity subscription ends with `bye` when its session is removed (whatever its `kinds`
  filter); `client.Subscription` closes its connection on `bye`.

### Identity

The daemon decides who is calling; clients cannot choose a sender (`internal/daemon/identity.go`).
For every identity-dependent op (`send`, `inbox`, `read`, `reply`, `type`, `purge`, `whoami`):

1. **Process ancestry** (kernel-verified): the peer pid from `SO_PEERCRED` (Linux) /
   `LOCAL_PEERPID` (macOS, best effort) and its ancestors (`/proc/<pid>/stat` ppid on Linux,
   `sysctl kern.proc.pid` on macOS; at most 64 levels, stop at pid 1). If one is the child pid of a
   *running* session, the caller is that session. Works for harnesses that strip env vars from MCP
   servers (Codex).
2. **Token**: otherwise, a request `auth.token` equal to a running session's `AGENTD_TOKEN`
   (32 random bytes, hex; generated at `run`, kept only in daemon memory and the child env)
   identifies that session. A valid token for a session other than the one found by ancestry, an
   unknown token, or the token of an exited session is an error.
3. Otherwise the caller is `human:<user>` (user name of the peer uid, mapped to the human charset).

`auth.id` (`AGENTD_ID`) is a hint only: if set and different from the resolved identity the request
is rejected ("identity mismatch"). If the hint names a session that is still `starting`, resolution
waits up to 2 s for it to register.

Permissions on the resolved identity:
- `send`: sender = caller; ACL applies to it. There is no `from` argument.
- `inbox` / `read`: the caller's own mailbox. Humans may pass `as` (`--as`) to look at any mailbox;
  that is a peek: no state change (not delivered, not read). Sessions cannot use `as`.
- `reply`: the caller must be the original recipient; the reply goes to the original sender. If that
  sender is not a known mailbox, the reply is stored and the response carries `warning`.
- `purge`: humans may purge any mailbox and state, and `vacuum`. A session may purge only its own
  mailbox (`recipient` forced to itself; another one is an error) and only `read`/`replied`
  messages (the default states for it; others are an error); no `vacuum`. Either way at least one
  filter or `all: true` is required, so an empty request never wipes the mailbox.
- `view`: read-only; humans may view any session, a session only itself unless config
  `[security] agent_view = true` (default false). `view_ack`: humans only.
- `type`: humans may type into any session; a session only into itself unless config
  `[security] agent_type = true`.
- `act`: same as `type` (humans any session; a session only itself unless `[security] agent_type = true`).

Limits: this protects against confused or prompt-injected agents claiming another identity, not
against hostile code running as the same uid (which can read `/proc/<pid>/environ` or ptrace). Any
process of the user outside sessions is `human:<user>`.
