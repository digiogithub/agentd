# AGENTS.md — agentd

Instructions for any coding agent (Claude, Codex, Pando…) working in this repository.

## What this is

`agentd` is a lightweight session manager for CLI coding agents, written in Go. It launches agents
(Claude Code, Codex, Pando, opencode, gemini…) in persistent PTYs, detaches/attaches to them
(zellij/tmux-style), and lets them **exchange messages through a robust mailbox**, via CLI and MCP.
It is harness-agnostic: it never uses agent-specific APIs, it only types into the agent's terminal
like a human would.

See `README.md` (usage) and `ROADMAP.md` (phases and status).

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
| `internal/session` | One PTY child + VT emulator + fan-out + input + idle/human-activity tracking + ghost-text filter | pty, vt, termcolor |
| `internal/mailbox` | Message store: send, inbox, get, state transitions, replies, purge/vacuum | sqlite |
| `internal/delivery` | Decide when a session is idle and inject queued messages | session, mailbox, config |
| `internal/proto` | Wire protocol: JSON request/response + binary framed attach stream | stdlib |
| `internal/daemon` | Socket server, registry, op handlers, lifecycle, autostart helpers | all above |
| `internal/client` | Go client for the socket API (used by CLI and MCP) | proto |
| `internal/attach` | Client-side terminal: raw mode, SIGWINCH, detach key, redraw | client, x/term |
| `internal/mcpserver` | MCP stdio server exposing mailbox + peers tools | client (+ proto types), go-sdk |
| `cmd/agentd` | Cobra CLI wiring | client, attach, daemon, mcpserver |

### Key behaviours (normative)

- **Paths**: socket `$XDG_RUNTIME_DIR/agentd/agentd.sock` (fallback `/tmp/agentd-$UID/`), state
  `$XDG_STATE_HOME/agentd` (fallback `~/.local/state/agentd`): `mailbox.db`, `logs/<session>.log`,
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
  - CLI: `agentd run <alias|path> [--harness codex] [--name …] [-- cmd…]`: cwd = resolved dir, default
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
  human keystroke from any attached client for `human_grace_ms` (default 5000) AND the session has
  produced output at least once AND it started at least max(`idle_ms`, 3 s) ago AND no pending human
  draft (human typed bytes that were not followed by `\r`, `\n` or Ctrl-C; cursor/focus/mouse
  sequences ignored) younger than 10 min. Terminal replies from attached clients (CPR, DA, DECRPM,
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
  `{"ok": true, "data": ...}` / `{"ok": false, "error": "..."}`. Op `attach` upgrades the connection to
  binary frames `[1 byte type][4 bytes big-endian length][payload]`, types: `D` data, `R` resize
  (`uint16 cols, uint16 rows`), `X` detach/close, `E` exit (payload = exit code as text).

### Ops (daemon API, mirrored by CLI and partially by MCP)

`ping`, `run {name, cwd, cmd[], env_base[], env{}, profile, cols, rows}`, `ls`, `kill {name, signal}`, `rm {name}`,
`dump {name, scrollback:bool, ansi:bool, ghost:bool, cells:bool, row?:int}` (→ `{text, ghost, ghost_hidden, cells?}`; `cells` = `{cursor_x, cursor_y, row, cells:[{x, content, rune, fg, bg, attrs[], width}]}` for `row` or the cursor's row, diagnostics), `type {name, text, submit:bool}` (raw injection, for humans/scripts),
`send {to, subject, body, reply_to}`, `inbox {as?, all:bool}`, `read {as?, id}`, `reply {id, body}`,
`purge {before?: RFC3339 | older_than?: duration, recipient?, states?[], all:bool, dry_run:bool, vacuum:bool}`
(→ `{count, dry_run}`; at least one filter or `all` is required),
`peers`, `whoami`, `attach {name, cols, rows}`, `shutdown`. Every request may carry
`"auth": {"id", "token"}` (filled by `internal/client` from `AGENTD_ID` / `AGENTD_TOKEN`).

MCP tools (`agentd mcp`, identity resolved by the daemon, see "Identity"): `agentd_peers`,
`agentd_send`, `agentd_inbox`, `agentd_read`, `agentd_reply` (no purge tool). The mailbox tools call `whoami` first
and fail with a clear error when the daemon sees the caller as a human (MCP server not started from
inside a session). No run/kill/type over MCP in v0.1 (an agent must not spawn or drive other agents'
terminals directly).

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
- `type`: humans may type into any session; a session only into itself unless config
  `[security] agent_type = true`.

Limits: this protects against confused or prompt-injected agents claiming another identity, not
against hostile code running as the same uid (which can read `/proc/<pid>/environ` or ptrace). Any
process of the user outside sessions is `human:<user>`.
