# Roadmap — agentd

Status legend: `[ ]` todo · `[~]` in progress · `[x]` done · `[-]` dropped.

## Phase 0 — Bootstrap
- [x] Git repo, `go.mod` with pinned dependencies, `README.md`, `AGENTS.md`, `ROADMAP.md`

## Phase 1 — Foundations (parallel)
- [x] `internal/config`: XDG paths, env overrides, `config.toml`, harness profiles (claude, codex, pando, default), ACL
- [x] Project aliases: `aliases.toml` + `AGENTD_ALIASES` env, `ResolveTarget`
- [x] `internal/mailbox`: SQLite schema + migrations, send/inbox/get/mark/reply, tests (ACL enforced by the daemon)
- [x] `internal/session`: PTY spawn, `vt.SafeEmulator` feed, fan-out subscribers, input writer,
      resize, idle + human-activity timestamps, screen/scrollback snapshot, ring log, exit tracking, tests
- [x] Spike notes: does `x/vt` render Claude/Codex/Pando TUIs correctly? (manual, documented in `docs/spike-vt.md`)

## Phase 2 — Daemon, protocol, CLI, attach
- [x] `internal/proto`: JSON ops + binary attach frames
- [x] `internal/daemon`: socket server (0600 + SO_PEERCRED), registry, op handlers, graceful shutdown, `daemon.log`
- [x] `internal/client` + CLI autostart of the daemon (setsid, wait for socket)
- [x] `internal/attach`: raw mode, SIGWINCH, `Ctrl-]` `d` detach, full redraw on attach
- [x] `cmd/agentd`: `daemon`, `run`, `ls`, `attach`, `dump`, `type`, `kill`, `rm`, `send`, `inbox`, `read`, `reply`, `peers`
- [x] `internal/delivery`: idle detection per profile, bracketed-paste injection, retries, state updates

## Phase 3 — MCP and integration
- [x] `internal/mcpserver`: stdio MCP with `agentd_peers/send/inbox/read/reply`
- [x] `agentd mcp install --print` snippets for Claude Code / Codex / Pando configs
- [x] End-to-end tests with a fake TUI agent (`internal/testutil/fakeagent`): run → send → inject → reply
- [x] systemd user unit (`contrib/agentd.service`)

## Phase 4 — Hardening and replacing launcher scripts
- [ ] Independent code review, race tests, fuzz the frame decoder
- [~] Manual validation with real Claude Code, Codex and Pando; tune default `prompt_regex`
      (startup captures in `docs/ghost-text.md`: claude/codex `prompt_regex` + `prompt_marker` set;
      Pando opened its first-run settings screen, still to capture; post-turn suggestions and busy
      screens still to validate)
- [x] Ghost text (suggested prompt / placeholder): profile rules (`ghost_faint`, `ghost_colors`,
      `ghost_right_of_cursor`, `prompt_marker`), hidden from `dump` (`--ghost` = raw, final note line),
      `input_state` in `ls --json`, ghost-filtered `prompt_regex`, paste verified before submit
- [x] Mailbox purge + retention: `[mailbox] retention` (default 5d) / `purge_interval` (1h),
      retention loop in the daemon, op/CLI `purge` (human vs session permissions, `--all` guard,
      `--dry-run`, `--vacuum`), schema v2 `reply_to ON DELETE SET NULL`
- [ ] `agentd open <alias|path> --harness claude|codex|pando` one-shot launcher (Remote Control
      flags, config-dir/account selection, idempotent start, pre-flight warnings for trust dialogs)
- [~] Migration doc from zellij/tmux launcher scripts (`docs/migration-from-zellij-tmux.md`; update
      when `open` lands)

## Phase 5 — Public release
- [x] Module `github.com/digiogithub/agentd`, MIT license, version from the tag via ldflags
- [x] Tag-driven release workflow: Linux amd64/arm64 tar.gz + `.deb` (systemd user unit), macOS
      universal binary signed + notarized (`.zip` and stapled `.pkg`), `SHA256SUMS`; CI workflow
      (gofmt, vet, `go test -race`, macOS build). See `docs/release.md`
- [ ] Windows support: ConPTY instead of Unix PTYs, peer identity without `SO_PEERCRED`/`/proc`
      (named pipes + `GetNamedPipeClientProcessId`, process tree via Toolhelp), socket ACLs; then
      enable the gated `windows` job in `.github/workflows/release.yml` (Authenticode via Azure
      Artifact Signing)

## Phase 6 — API gateway and native frontends
Plan: `docs/plans/api-gateway-plan.md` (decisions ADR-A01…A11, stories API-S01…S27); design pages in
`docs/design/api/`.
- [x] Semantic view in the daemon (`internal/screen`): reflowed text, status, input line, options;
      ops `view`, `act`, `subscribe`; fixed PTY size (API never resizes). In active use by the hc layer
      and the native clients (Phase 8).
- [x] `agentd serve` HTTP gateway: REST + SSE, OpenAPI contract, loopback default (frozen)
- [x] Device tokens, QR pairing, TLS / Tailscale, audit log, webhook notifications (frozen)
- [-] Flutter app in `app/`: removed 2026-11-14 in favour of Herdr client compatibility (Phase 7).
      The `agentd serve` gateway is frozen (kept, no new features) for possible future use.
Backlog: gintrack project `AGED` (`docs/.pmngr`).

## Phase 7 — Herdr client compatibility (herdr-core protocol)
Analysis: `docs/architecture/herdr-compatibility-analysis.md`. Plan: `docs/plans/agentd/herdr-compat_plan.md`.
- [x] PoC: `internal/hcompat` + `herdr-core`/`hc` shim (`connect`), hello, events snapshot, attach, docs
- [x] Phase 1: daemon labels, document store, turn counter; real event translation, credit flow
- [x] Phase 2: interactive streaming (automatic input, focus, history, observe, passive/resumed, repaint)
- [x] Phase 3: send/keys/interrupt/wait, OSC 7501 program status, turn records, `[herdr]` config + audit
- [x] Phase 4: opt-in hc socket listener in the daemon (`[herdr] listen`) for Whip and relays, `server start/status/stop`
- [x] Verified with Herdr Desktop 0.1.15 and the official Herdr mobile app (Android); `install-shim --local-bin`
- [ ] Field test with Whip and the 0cv relay
- [x] Mailbox work queues (`queue:<name>`, op `claim`) and conditional inbox (`since_digest`), from the herdrup analysis

## Phase 8 — Native clients
Both clients reach hosts over SSH only (agentd through its unix socket via `direct-streamlocal`, human
identity) and show agentd, zellij, tmux and plain-shell sessions in one inbox. The gateway (Phase 6) is
not used.
- Desktop: `agentd-desktop` (MyGo v0.7.1, `cmd/agentd-desktop` + `internal/desktopui`), plan
  `docs/plans/agentd/desktop_plan.md`
  - [x] P0: Go 1.27.3, MyGo + x/crypto pinned, `client.NewConn`, `backend` contract
  - [x] P1: `remote`, `core`, `mux`, `provision`, UI (`kit`, `shell`, `app`, 8 areas), `backend/fake`,
        libghostty-vt embedded per platform
  - [ ] P2: real backend wiring in `cmd/agentd-desktop`, docs and build plumbing (AGENTS.md, README,
        `Makefile` desktop targets), release artifacts (Linux amd64/arm64, macOS universal signed)
  - [ ] P3: field test against the local daemon and one SSH host
- Mobile: `agentd-app` (Flutter, Android/iOS; separate repository)
  - [x] P0-P2: SSH transport, TOFU host keys, device key, agentd client, mux, provisioning, UI, wired on
        real repositories (`lib/src/mux` and `lib/src/provision` are the source of the desktop ports)
  - [ ] P3: field test against a real host (agentd + zellij), Android and iOS builds

## Phase 9 — Relay transport (reach hosts without SSH)
Design `docs/design/relay-transport.md`, plan `docs/plans/agentd/relay_plan.md`.
- [x] `internal/relaywire` (keys, invite link, Noise IK/IKpsk2, stream mux, host link) + Dart interop vectors
- [x] `cmd/agentd-relay` + `internal/relayserver` (join tokens, route auth, per-tunnel host data connection)
- [x] Daemon: `[relay]`, host key, devices, invites with approval, op `relay`, `agentd relay` CLI, device op gate
      (view/operate/admin + session filter), audit log
- [x] agentd-desktop: relay hosts (pair from an invite link, scope-aware UI)
- [x] agentd-app: Dart Noise + mux + WebSocket transport, relay hosts (vectors match; live run vs Go relay + daemon)
- [ ] Field test: relay on a VPS, host behind NAT, desktop + phone

## Later
- [ ] Session persistence across daemon restarts (re-exec with fd passing, or at least log replay)
- [ ] Broadcast / group addresses (`@all`, `@project:*`)
- [ ] Remote peers over SSH (no TCP listener by default)
- [ ] Optional per-agent tokens to prevent sender spoofing between local processes
- [ ] Read-only attach, session recording (asciicast)
- [ ] Codex app-server / Claude Remote Control adapters as optional delivery backends
