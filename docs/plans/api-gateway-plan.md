# Plan: agentd API gateway and native frontends

Status: **planning** (2026-11-13). Owner: project maintainer. Progress is tracked in the checkboxes of
§5; update them as stories land.

Related KB pages: [[design/api/semantic-model]] · [[design/api/daemon-ops]] ·
[[design/api/http-api]] · [[design/api/security-remote]] · [[design/api/frontend-app]] ·
[[ROADMAP]] · [[ghost-text]] · [[spike-vt]].

## 1. Goal

Turn agentd into a small server that exposes every agent session through an API that is
**independent of the PTY**: clients get *text, state and options*, never a screen of a given size.
On top of it, a desktop and mobile app (in the spirit of zeron.sh) lists every agent on every
machine and lets the user read what they are doing, answer their questions, pick options and send
prompts.

Non-goals for v1: an own relay service, a structured chat transcript per harness, push
notifications through APNs/FCM, Windows hosts.

## 2. Principles

1. **Harness-agnostic.** agentd still never calls agent-specific APIs. Everything is derived from the
   terminal (screen, scrollback, cursor, attributes) plus per-profile rules in `config.toml`.
   A harness with no rules still works, with less semantic detail.
2. **The session lives on the daemon host.** `run` over the API starts the agent where agentd runs.
   The app is only a front end; closing it changes nothing.
3. **API clients never touch the PTY geometry.** PTY size is owned by the daemon (profile default)
   and by local terminal attach clients only. API output is reflowed text, so a phone never shrinks
   a session.
4. **Interpret once, in the daemon.** Screen interpretation lives in the daemon (new package), exposed
   as socket ops. The CLI, MCP server and HTTP gateway all reuse it; the gateway stays thin.
5. **Same safety rules as delivery.** Every write coming from the API goes through the same checks
   as mailbox injection (pending human draft, paste verification, bracketed paste sanitising).

## 3. Decision log (binding; add rows, never edit silently)

| ID | Decision | Rationale |
|---|---|---|
| ADR-A01 | Gateway is a separate process, `agentd serve`, package `internal/httpapi`, depending only on `internal/client` + `internal/proto`. The daemon keeps **no TCP listener**. | Network surface stays out of the process that owns the PTYs; same pattern as `internal/mcpserver`. |
| ADR-A02 | API never resizes a PTY. Sessions get a fixed default size from the profile (`cols`/`rows`, default 160x50) unless a local terminal is attached. API responses carry **logical lines** (soft-wrapped rows joined), not grid rows. | User requirement: the phone is a front end and must not shrink the session. |
| ADR-A03 | Three API layers: **L1 semantic view** (status, reflowed text, input line, detected options) = primary; **L0 raw terminal** (ANSI snapshot + byte stream, read-only by default) = fallback/debug; **L2 structured transcript** (per-harness adapters) = later, not a priority. | Text + options covers every harness; L0 covers what L1 misreads; L2 is optional polish. |
| ADR-A04 | Semantic parsing = generic heuristics + per-profile rules (regex, markers, colours). Rules are data in the profile, never harness-specific Go code paths. | Keeps "any harness" true; new harnesses are a config change. |
| ADR-A05 | New daemon package `internal/screen` (pure functions over a screen snapshot) and new ops `view`, `act`, `subscribe`. | Interpretation once, reused by CLI, MCP and gateway; testable against recorded captures. |
| ADR-A06 | The gateway talks to the daemon over the unix socket, so the daemon resolves it as `human:<user>`. API devices authenticate to the gateway, not to the daemon. | Identity model unchanged; a remote user is a human. |
| ADR-A07 | Transport: HTTP/JSON for requests, **SSE** for events and text streams. WebSocket only for the optional L0 raw terminal stream, added later with an `AGENTS.md` dependency update. | SSE + POST needs only the stdlib and works through proxies and mobile networks. |
| ADR-A08 | Gateway binds `127.0.0.1` by default. Any non-loopback bind requires TLS and device tokens. Remote access = Tailscale (or any VPN/SSH tunnel). No own relay in v1. | User decision; avoids running infrastructure. |
| ADR-A09 | Device pairing: `agentd serve pair` shows a QR/one-time code; the device gets a long random token stored hashed; tokens are listed and revoked with `agentd serve devices`. | A device token is equivalent to a shell as the user, so it must be revocable and never stored in clear. |
| ADR-A10 | Front end: one TypeScript web UI, embedded in the gateway as a reference client and wrapped with **Tauri 2** for desktop and mobile, in a separate repository (`agentd-app`). The API contract is an OpenAPI file in this repo. | One UI for all platforms; the contract keeps both repos honest. |
| ADR-A11 | Structured chat transcript (L2) is deferred until L1 has been validated with real Claude Code / Codex / Pando sessions. | User decision: not a priority. |
| ADR-A12 | Reference web UI: Preact + Vite + TS in `web/`; built assets committed under `internal/httpapi/ui/dist` and embedded with `go:embed`; CI checks they are current. Published as `@agentd/web-ui` for `agentd-app`. | `go install` keeps working without Node; one UI for gateway and apps. Needs an `AGENTS.md` toolchain update. |
| ADR-A13 | New package `internal/inject` owns the safe-input path (draft guard, bracketed paste, paste verification, submit) used by both `internal/delivery` and op `act`, with a per-session input lock. | Same safety for mailbox injection and API prompts; no interleaved pastes. [[design/api/daemon-ops]] §4 |
| ADR-A14 | New daemon op `info` (host, version, epoch, profiles, aliases, harnesses) and `run` accepts `target` + `harness`, resolved **in the daemon** (same `ResolveTarget` rules as the CLI). The gateway never reads aliases itself. | Keeps `internal/httpapi` dependent only on `client` + `proto`; one resolution path. |
| ADR-A15 | Device tokens are required on **every** gateway listener, loopback included; only the `Authorization` header is accepted (no cookies, no query string). Default port `7477`. | Agents on the same host could `curl` localhost; `tailscale serve` makes remote traffic look local. [[design/api/security-remote]] §2 |
| ADR-A16 | `View` carries `first_line` (absolute index of `lines[0]`) and the daemon `epoch`; ETags are `"e<epoch>-r<revision>"`. `/v1/info` exposes `api_version` and `features[]` for client capability negotiation. | Patch streams with absolute indices; clients detect daemon restarts; apps degrade gracefully against older gateways. |
| ADR-A18 | **Supersedes ADR-A10 and ADR-A12.** Client app in **Flutter** (desktop Linux/macOS/Windows + Android; iOS later), living in this repository under `app/` (Dart package `agentd_app`). No embedded web UI in the gateway for v1; real-harness validation uses the Flutter desktop build. The API contract stays `docs/api/openapi.yaml`; the Dart client is generated from it or hand-written against it with contract tests. | User decision (2026-11-13). One codebase for desktop and Android; monorepo keeps API and client changes in one review. |
| ADR-A17 | API-started sessions use the daemon's environment (`run_env = "daemon"`), not the gateway caller's; `cmd`/`env` overrides on `run` require the `admin` scope; harnesses allowed for `write` are listed in `[api] run_harnesses`. | A remote device must not inject arbitrary environment or commands with a normal token. |

Pending cross-page questions (decide during implementation, then add ADR rows): ACL for sessions
calling `view`/`subscribe` outside MCP; `act prompt` while `working` with `force`; restore PTY size
on last detach (Claude scrollback duplication?); interrupt key per harness (Esc vs Ctrl-C); whether
the gateway should refuse loopback callers whose pid is inside a session; attention acknowledgement
per device or per session; upstream wrap flag for `x/vt` vs heuristic reflow only.

## 4. Architecture

```
 phone / desktop app (Tauri)       browser (reference UI)
            │  HTTPS + SSE (Tailscale)     │
            ▼                              ▼
     agentd serve  (internal/httpapi)  ── device tokens, TLS, pairing, rate limits
            │  unix socket, identity = human:<user>
            ▼
     agentd daemon
       ├─ sessions (PTY + vt emulator, fixed size)  ◄── local `agentd attach` (may resize)
       ├─ internal/screen: snapshot → View{status, lines, input, options}
       ├─ ops: view / act / subscribe (+ existing ls/run/kill/rm/dump/type/mailbox)
       └─ delivery loop (unchanged, shares the safe-input path with `act`)
```

## 5. Phases, epics and stories

IDs are permanent (`API-Ex` epic, `API-Sxx` story). Each story is one or a few conventional commits,
with tests, `gofmt`/`go vet` clean, `go test -race` for daemon/session/delivery.

### Phase 1 — Semantic model in the daemon (no network) · epic API-E1

- [ ] API-S01 `internal/screen`: snapshot type (cells, wrap flags, cursor, attrs) taken atomically from a session. [[design/api/semantic-model]] §2
- [ ] API-S02 Logical-line reflow: join soft-wrapped rows, trim, keep hard breaks; scrollback + screen as one text. §3
- [ ] API-S03 Region split: transcript / input line / footer, using `prompt_marker`, rules and box-drawing heuristics. §4
- [ ] API-S04 Status classifier: `starting|working|idle|waiting|exited` (+ `attention` flag) from idle timers, `prompt_regex`, option detection, BEL/OSC 9. §5
- [ ] API-S05 Option detection: numbered menus, pointer menus (`❯`, `>`, `›`), yes/no prompts; profile rules `menu_*`. §6
- [ ] API-S06 Recorded capture fixtures (claude, codex, pando, fakeagent) + golden tests for API-S02..S05. §8
- [ ] API-S07 Fixed PTY size policy: profile `cols`/`rows`; API never resizes; attach keeps current behaviour. [[design/api/daemon-ops]] §5
- [ ] API-S08 Extract the safe-input path from `internal/delivery` (draft check, paste, verify, submit) into a reusable function. daemon-ops §4
- [ ] API-S09 Op `view` (+ `agentd view` CLI, `--json`). daemon-ops §2
- [ ] API-S10 Op `act`: `prompt`, `choose`, `keys`, `interrupt`, `cancel`; option ids validated against the latest view revision. daemon-ops §3
- [ ] API-S11 Op `subscribe`: long-lived event stream (session lifecycle, status changes, view revisions, mail). daemon-ops §6
- [ ] API-S12 MCP: status in `agentd_peers`; `agentd_view` only behind `[security] agent_view` (default off). daemon-ops §7
- [ ] API-S28 Op `info` + `run {target, harness}` resolved in the daemon; `ls` summary fields (status, attention, revision, preview). ADR-A14
- [ ] API-S29 Structured error `code` in `proto.Response` (`busy`, `not_ready`, `conflict`, `not_found`…), backward compatible. daemon-ops §3.5
- [ ] API-S30 Transcript `History` for alternate-screen TUIs (settled-frame merge, gap markers). semantic-model §3.5

### Phase 2 — HTTP gateway · epic API-E2

- [ ] API-S13 `agentd serve` skeleton: config `[api]`, loopback bind, graceful shutdown, daemon autostart. [[design/api/http-api]] §1
- [ ] API-S14 REST: info, sessions CRUD (run on daemon host via alias/path + harness), view, act, mailbox. http-api §3
- [ ] API-S15 SSE: `/v1/events` and `/v1/sessions/{name}/view/stream` (diffs by revision, resumable with `Last-Event-ID`). http-api §4
- [ ] API-S16 OpenAPI 3.1 contract `docs/api/openapi.yaml` + contract tests in CI. http-api §6
- [ ] API-S17 L0 raw terminal: `GET …/terminal` ANSI snapshot; read-only stream later (WebSocket, ADR-A07). http-api §5

### Phase 3 — Security and remote access · epic API-E3

- [ ] API-S18 Device tokens: store (hashed), scopes (`read`, `write`, `admin`), revoke, last-seen. [[design/api/security-remote]] §2
- [ ] API-S19 Pairing flow: `agentd serve pair` (QR + one-time code, short TTL). security-remote §3
- [ ] API-S20 TLS: self-signed with pinned fingerprint in the QR, or Tailscale certs (`tailscale cert`). security-remote §4
- [ ] API-S21 Hardening: Origin/CORS policy, rate limits, request size caps, audit log of `act`/`run`/`kill`. security-remote §5
- [ ] API-S22 Notifications v1: webhook / ntfy on `waiting` and `idle` after work. security-remote §6

### Phase 4 — Flutter app (desktop + Android) · epic API-E4/E5 (`app/`)

Superseded stories: the embedded web UI (old API-S23) and the Tauri apps (old API-S25..S27) are
replaced by the Flutter stories below (ADR-A18). Canonical ids live in gintrack (§8).

- [ ] F1 Flutter skeleton in `app/`, Dart API client against `openapi.yaml`, CI build/test.
- [ ] F2 Hosts and pairing: QR scan (Android), code entry (desktop), secure token storage, TLS pinning.
- [ ] F3 Agent list across hosts: `/v1/events` SSE, status badges, attention.
- [ ] F4 Session view: reflowed lines, prompt box, option buttons, quick keys, 409 handling.
- [ ] F5 Launch agent (alias + harness), kill/rm, mailbox.
- [ ] F6 Notifications and deep links (desktop local notifications, Android).
- [ ] F7 Release builds (Linux/macOS/Windows desktop, Android APK) per `digio-release-signing`.
- [ ] F8 Real-harness validation with the maintainer (Claude Code, Codex, Pando); tune profile rules.

### Later

- L2 transcript adapters (Claude JSONL, Codex rollout), own relay with E2E + push, Windows hosts,
  multi-user access.

## 6. Risks

- **Screen interpretation is heuristic.** Mitigation: captures + golden tests, profile rules, L0
  always available, `confidence` field on options.
- **Fixed size vs local attach.** A local attach still resizes; the view stays valid because it is
  reflowed, but option row positions change — `act choose` is resolved against the current screen at
  execution time, never against stale coordinates.
- **Choosing the wrong option.** `act choose` carries the view revision and option label; the daemon
  re-detects and refuses if the option moved or changed (409).
- **Remote write = shell access.** Tokens hashed, scoped, revocable; TLS mandatory off-loopback;
  audit log.

## 7. How subagents work this plan

Each story is self-contained: read this plan, the linked design page section, `AGENTS.md`, then
implement with tests.

Execution waves (stories inside a wave can run in parallel, each agent in its own git worktree;
a wave starts when the previous one is merged):

1. **W1 foundations:** API-S01 (snapshot), API-S08 (`internal/inject` extraction, delivery tests
   unchanged), API-S29 (error codes), API-S07 (fixed size), API-S28 (`info`, `run target`).
2. **W2 interpretation:** API-S02 + API-S03 (one agent: reflow + regions), API-S05 (options),
   API-S06 (captures + fakeagent flags; feeds the other two).
3. **W3 state and ops:** API-S04 (status), API-S30 (history), then API-S09 `view`, API-S10 `act`,
   API-S11 `subscribe`, API-S12 MCP.
4. **W4 gateway:** API-S13 → API-S14, API-S15, API-S16, API-S17 in parallel.
5. **W5 security:** API-S18 → API-S19, API-S20, API-S21, API-S22.
6. **W6 UI:** API-S23, then API-S24 (manual, with the maintainer, real harnesses).
7. **W7 apps:** API-S25..S27 in `agentd-app`.

Each wave ends with an independent review agent (diff only, no conclusions handed over) and
`go test -race ./...` green. Do not change a decision in §3 — propose a new ADR row instead. Update the
checkbox and add a short note under the story when done.

## 8. Backlog (gintrack project `AGED`, `docs/.pmngr`)

Milestones: `AGED-M-0001` v0.2 semantic view + daemon ops · `AGED-M-0002` v0.3 gateway + security ·
`AGED-M-0003` v0.4 Flutter app. Epics: `AGED-EP-0001` screen model · `AGED-EP-0002` daemon ops ·
`AGED-EP-0003` gateway · `AGED-EP-0004` security · `AGED-EP-0005` Flutter app.

| Plan id | Story | Plan id | Story | Plan id | Story |
|---|---|---|---|---|---|
| API-S01 | AGED-US-0001 | API-S11 | AGED-US-0014 | API-S21 | AGED-US-0024 |
| API-S02 | AGED-US-0002 | API-S12 | AGED-US-0015 | API-S22 | AGED-US-0025 |
| API-S03 | AGED-US-0003 | API-S13 | AGED-US-0016 | DOC (Flutter design) | AGED-US-0026 |
| API-S04 | AGED-US-0004 | API-S14 | AGED-US-0017 | F1 | AGED-US-0027 |
| API-S05 | AGED-US-0005 | API-S15 | AGED-US-0018 | F2 | AGED-US-0028 |
| API-S06 | AGED-US-0006 | API-S16 | AGED-US-0019 | F3 | AGED-US-0029 |
| API-S07 | AGED-US-0008 | API-S17 | AGED-US-0020 | F4 | AGED-US-0030 |
| API-S08 | AGED-US-0009 | API-S18 | AGED-US-0021 | F5 | AGED-US-0031 |
| API-S09 | AGED-US-0012 | API-S19 | AGED-US-0022 | F6 | AGED-US-0032 |
| API-S10 | AGED-US-0013 | API-S20 | AGED-US-0023 | F7 | AGED-US-0033 |
| API-S28 | AGED-US-0011 | API-S29 | AGED-US-0010 | F8 | AGED-US-0034 |
| API-S30 | AGED-US-0007 | | | | |

Story workflow: `backlog` → `in_progress` (agent starts) → `in_review` (tests green) → `done`
(after the wave review). Use `gintrack item move` / `gintrack item comment`.
