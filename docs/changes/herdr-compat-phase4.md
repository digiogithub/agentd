# Herdr compatibility, Phase 4: opt-in hc socket listener, `hc server` shim

Continues [[herdr-compat-phase3]] under the plan [[agentd/plans/herdr-compat_plan.md]] (analysis:
`docs/architecture/herdr-compatibility-analysis.md` §4.2, §4.3, §6, §7). The KB, code-index and Context7 tools
named in AGENTS.md were not available in this session, so this record is a repository file.

## Findings: what the socket clients need

Sources read (shallow clones, read only, nothing built or run): Whip `672872e567`, herdr-mobile-relay
`29f1a09232`, herdr-core `c84d6e7615`.

### Whip (kosumic/whip)

- Discovery script, `crates/herdr-protocol/src/hc/endpoint.rs:52-74`: prints the marker `whip-hc-endpoint-v1\0`,
  then `session\0socket\0installed\0command\0`. The socket is, in order: the explicit socket of the host profile,
  `$HC_SOCKET`, **the content of `${XDG_STATE_HOME:-$HOME/.local/state}/herdr-core/<session>/core.lock`**,
  `$XDG_RUNTIME_DIR/herdr-core/<session>.sock`, `/run/user/$(id -u)/herdr-core/<session>.sock` when that dir
  exists, else `<state>/<session>.sock`. `installed` = `command -v hc` (or `~/.local/bin/hc`). Parsing
  (`endpoint.rs:81-128`) needs exactly those 4 fields and an absolute socket path. No other file is read
  (no `core.json`).
- Default command `hc` (`packages/react-native-whip-ssh/rust/src/herdr_connection.rs:171-178`).
- Transport, `packages/react-native-whip-ssh/rust/src/hc_transport.rs:139-222`: SSH `direct-streamlocal` to the
  discovered socket (`:168`). If that fails and `installed` (`:172`), it runs
  `HC_SESSION=<s> [HC_SOCKET=<p>] hc server start --json` (`endpoint.rs:132-145`) and checks **only the exit
  status** (`hc_transport.rs:177`; stdout is not parsed), rediscovers and retries the socket (`:183-192`); if the
  socket still fails it falls back to exec `hc connect --session <s>` (`:197-222`, `endpoint.rs:147-170`). If
  `installed` is 0 and the socket fails, it is an error (`:195`). Whip's own test of the refused-streamlocal
  fallback: `hc_transport/tests.rs:340-345`; its real-core test harness runs `server start --json` / `server stop
  --json` (`hc_transport/tests.rs:78-115`).
- **Did Whip work before this change?** No. With the shim on `PATH` as `hc`, the socket open failed (no socket),
  Whip ran `hc server start --json`, the shim exited 2 ("unsupported herdr-core command") and Whip stopped with
  `core_start_failed` before reaching the exec fallback. Making `server start` exit 0 alone makes Whip work through
  `hc connect --session S` (the minimal fix); the listener lets it use streamlocal directly.

### herdr-mobile-relay (0cv), hc backend

- Socket only, no exec fallback, no discovery files: `internal/config/config.go:120-121` and `:175-190`
  (`coreSocketPath`): `$HC_SOCKET_PATH`, `$HC_SOCKET`, else `$XDG_RUNTIME_DIR/herdr-core/<HC_SESSION|default>.sock`,
  else `/run/user/<uid>/herdr-core/...`, else `<state>/herdr-core/<session>/<session>.sock`.
- One connection per call, plain mode: `internal/hc/client.go:25-58` dials, sends `hello {generation:1}` and
  checks the generation (`:63-80`), then one request. It never runs `hc server ...` (only its real-core test helper
  does, `internal/hc/hctest/core.go:66`). Methods used: `doc.get`, `doc.set`, `terminal.get`, `terminal.label`,
  `terminal.list`, `terminal.run`, plus attach and events (`internal/hc/*.go`). Without a socket listener it cannot
  use agentd at all.

### herdr-core itself (layout the listener copies)

- Paths, `crates/herdr-core/src/paths.rs:17-49` and `platform/unix.rs:117-143`: state
  `${XDG_STATE_HOME:-~/.local/state}/herdr-core/<session>`; socket `HC_SOCKET`, else config `socket`, else
  `$XDG_RUNTIME_DIR/herdr-core/<session>.sock`, else `/run/user/<uid>/herdr-core/...` if it is a dir, else the state
  dir; dirs created 0700.
- Ownership, `server.rs:43-65`: `serve()` takes a non-blocking flock on `<state>/core.lock`; if it is held it exits
  without serving; it writes the socket path into the lock (no newline) and holds it for its lifetime.
  `client.rs:131-141` reads `core.lock` to tell "the core listens elsewhere".
- CLI, `cli.rs:542-566`: `server start|status --json` print the `server.status` result (`api/types.rs:144-158`:
  `core, version, pid, uptime_ms, holder, holders, live, finished, installed_by`); `server stop --json` prints
  `{"stopped": bool}` (`cli/server.rs:53-61`, `api/types.rs:205-207`); no core = `{"stopped":false}`.

## What changed

- **Config** `[herdr]`: `listen` (default false), `socket` (override, absolute or `~/`), `session` (default
  `default`, herdr-core's folder-name rule). `HerdrConfig.Paths(getenv)` resolves socket + `core.lock` like
  herdr-core (no `HC_SOCKET`: a client variable). `config.HCCoreIDPath`.
- **`hcompat.Listener`** (`internal/hcompat/listen.go`, stdlib only): `Listen` takes `core.lock` (held =
  `ErrOwned`), creates missing dirs 0700 (an existing socket dir must be ours), refuses a live socket or a
  non-socket file (`ErrOwned`), replaces only a socket nobody answers on, binds, chmods 0600 and writes the socket
  path into `core.lock`; on failure it removes the lock file it made and empty dirs it made. `Serve` accepts (cap
  128), runs `Admit` per connection (refusal = one JSON-RPC `forbidden` error answering the first request, then
  close) and `Server.Serve` over the conn. `Close` closes connections, removes the socket only if it is still its
  inode, empties and removes `core.lock` while holding it (only if still its inode), removes empty created dirs.
  No `core.json` is written (a real core would adopt its identity).
- **`hcompat.Server`**: `PID`, `Started` (server.status of a daemon-hosted core), `Status()` (shared by the method
  and the shim), `LoadCoreID` (moved from `cmd/agentd`).
- **Daemon** (`internal/daemon/hclisten.go`): `startHC` in `Serve` when `[herdr] enabled && listen`; `stopHC`
  first in `Shutdown`; `admitHC`/`admitHCPeer`; op `hc_listener {action: status|start|stop}` (humans only) →
  `proto.HCListenResult`. The translator dials the daemon's own socket. `resolveOnce` never matches the daemon's own
  pid against session ancestry. `procenv_linux.go`/`procenv_other.go`: `procEnvSet`.
- **Proto/client**: `OpHCListen = "hc_listener"`, `HCListenArgs`, `HCListenResult`, `HCListen*` actions;
  `client.HCListener`.
- **Shim** (`cmd/agentd/hc.go`, `hcServer`): `server start|status|stop|restart [--json]`. `start` autostarts the
  daemon, asks `hc_listener start`, prints `server.status` + `agentd.listener`, exit 0 even when the listener stays
  off; `status` does not autostart (no daemon = exit 2 + `{"error":…}`); `stop` stops only the listener
  (`{"stopped": bool}`; no daemon = `{"stopped":false}`); `restart` = stop + start of the listener. `connect
  --session S` unchanged (accepted, ignored).
- **AGENTS.md**: contracts (daemon now uses hcompat/client; hcompat `Listener`), Herdr normative paragraph (hc
  socket listener), ops list, Identity (own pid; `hc_listener`).

## Design choice: listener inside the daemon

Chosen over a separate `agentd hc serve` process started by `hc server start`:

- **Identity** is the deciding point. hcompat acts with the privileges of whoever dials the daemon. In the daemon
  that is the daemon's own pid, which is never a session (now explicit in `resolveOnce`), and the admission check
  reuses the daemon's kernel-verified ancestry (`sessionByAncestry`) in process. A separate process would need a
  new op to ask the daemon "is pid X a session?", and if `hc server start` were run from inside a session the
  detached listener could inherit that session's ancestry or token.
- **Lifecycle**: no extra long-lived process, pid file or reaping; the socket and `core.lock` live and die with the
  daemon (`Shutdown` cleans up), and `hc server stop` is a plain op that never touches sessions.
- **Contract cost**: one edge, daemon → hcompat (→ client, proto). hcompat still never imports daemon, session or
  config; documented in AGENTS.md.

## Security handling

- Opt-in (`listen = false` default), and `[herdr] enabled = false` keeps it off.
- Never steals a core: herdr-core's own `core.lock` flock decides ownership; a live socket and non-socket files are
  never replaced; a stale socket only when nothing answers.
- Dir 0700 (created), socket 0600, existing socket dir must belong to the user.
- Peers: `SO_PEERCRED` uid must equal ours; unknown pid = refused; a running session among the ancestors =
  refused; `AGENTD_TOKEN` in `/proc/<pid>/environ` (process that left a session tree; Linux, best effort) =
  refused. Refused means not served at all, not read-only: through this socket a session would act as
  `human:<user>` (run, kill, rm, doc_set, view any session), which `[security] agent_type/agent_view` never grant.
  Sessions keep their own path (`agentd` CLI, MCP, or `hc connect`, which dials as the session itself).
- `hc_listener` is human-only. Same limits as AGENTS.md "Identity": no protection against hostile same-uid code
  that scrubs its environment and leaves the session tree.

## How it was verified

- `gofmt -l` clean, `go vet ./...` clean.
- New tests: `internal/hcompat/listen_test.go` (end to end over the socket: framed hello, `terminal.list`, attach
  + input echo, plain-mode relay-style calls; modes 0700/0600; `core.lock` content; cleanup of socket, lock and
  created dirs only; never binds over a live foreign socket or a regular file; respects a held `core.lock`;
  replaces a stale socket; refusal reply), `internal/daemon/hclisten_test.go` (daemon with `listen = true`: status,
  plain client, a plain subprocess served, a subprocess with `AGENTD_TOKEN` refused, **a program running inside a
  session refused** (the test binary re-executed as the session program), session caller of `hc_listener`
  forbidden, stop keeps daemon and sessions, start again, shutdown cleanup; off by default creates nothing; a live
  foreign socket leaves the listener off), `internal/daemon/hclisten_internal_test.go` (other uid, unknown pid,
  session grandchild, own pid), `cmd/agentd/hc_test.go` (`server start/status/stop --json` shapes, human output,
  listener off still exit 0, no daemon: status exit 2 / stop `{"stopped":false}` without autostart, `connect
  --session S`), `internal/config/herdr_test.go` (paths, validation).
- `go test -race` on internal/hcompat, internal/daemon, internal/config, internal/proto, internal/client,
  cmd/agentd, and the full `go test ./...`: all pass (see the commit report).
- Not verified against the real Whip app or the relay (no devices here); the shapes and paths come from their
  source as cited above.
