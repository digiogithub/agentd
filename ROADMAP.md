# Roadmap — agentd

Status legend: `[ ]` todo · `[~]` in progress · `[x]` done.

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

## Later
- [ ] Session persistence across daemon restarts (re-exec with fd passing, or at least log replay)
- [ ] Broadcast / group addresses (`@all`, `@project:*`)
- [ ] Remote peers over SSH (no TCP listener by default)
- [ ] Optional per-agent tokens to prevent sender spoofing between local processes
- [ ] Read-only attach, session recording (asciicast)
- [ ] Codex app-server / Claude Remote Control adapters as optional delivery backends
