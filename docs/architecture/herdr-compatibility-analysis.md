# Herdr compatibility analysis

Status: research (Phase 0). Date: 2026-11-14. No code changed yet.

Goal: let unmodified Herdr clients (Herdr Desktop, and mobile clients of the Herdr ecosystem) drive the
terminals and agents that agentd runs, by making agentd speak the **herdr-core (`hc`) protocol**, without
running herdr-core and without a fork. agentd keeps its own mailbox / MCP / identity model on top.

Every claim below comes from the source of the cloned repositories (commits listed in §11). Paths are
relative to each repository root.

## 0. Executive summary

1. **The target protocol is small and well specified.** herdr-core publishes its whole API as OpenRPC
   (`schema/hc.openrpc.json`, 0.1.21, protocol *generation 1*) plus `schema/README.md` for the parts
   OpenRPC cannot describe: JSON-RPC 2.0 per line ("plain mode"), switched by `hello {framed:true}` to a
   9-byte frame format with per-channel credit flow control. Attach streams are raw VT bytes after a
   first paint. 31 methods, 12 event types. This is very close to what agentd already has
   (`internal/proto` frames, `subscribe`, `SubscribeWithRedraw`).
2. **No SSH server is needed.** No client opens an SSH session to a custom server:
   - Herdr Desktop never runs ssh. It spawns a **local** `herdr-core connect` (or `hc connect`) child and
     speaks the protocol over its stdin/stdout (`herdr-app/src/hc.rs:325-348`). Remote machines are reached
     *by the core* (`machine.*`, `on` params), which itself runs
     `ssh <target> sh -c '... exec herdr-core connect'` (`herdr-core/crates/herdr-core/src/machines.rs:242-262`).
   - Whip (the only phone client that talks to the host directly) uses the system sshd: a
     `direct-streamlocal` channel to the hc unix socket, or as fallback an exec of `hc connect --session S`.
   So the compatibility surface is **"a binary named `herdr-core`/`hc` whose `connect` subcommand speaks
   hc over stdio"**, plus optionally a unix socket at the hc path. System sshd keeps doing login, keys and
   encryption, exactly as herdr-core does (`docs/security.md` "Remote trust"). This replaces §5.3 of the
   initial spec (an agentd SSH server): it would add attack surface and no client would use it.
3. **Correction (2026-11-14): there IS an official Herdr mobile app** (iPhone, iPad, Android:
   App Store id6828898364, Google Play `dev.herdr.app`; https://herdr.dev/mobile/). Its source is not in the
   public herdrdev repositories, so its exact calls are not verified. The page states: "Your phone talks to
   herdr-core on each machine over SSH", "no Herdr account and no server of ours in between", "Herdr TUI isn't
   supported", terminals/agents appear with herdr-core alone while "Spaces appear only when the Herdr app also
   runs on that machine" (i.e. it reads the `herdr-app` document), and status comes from OSC 7501. It most
   likely uses the same hc generation 1 path as Desktop/Whip (SSH exec `herdr-core connect` or streamlocal to
   the hc socket); confirm with a protocol capture (log the shim's stdin/stdout) before Phase 4. The older
   herdr TUI docs ("works on your phone without a mobile app ... any SSH client",
   `herdr/docs/next/website/src/content/docs/how-to-work.mdx:49`) predate it. Third-party phone clients:
   **Whip** (React Native, AGPL; speaks hc and the legacy TUI protocol over SSH), **0cv/herdr-mobile-relay**
   (host relay + PWA; can use hc), **HerdrGo** and **teasec4/herdr-mobile-app** (host bridges over the
   legacy TUI socket API and the `herdr` CLI), **herdrup** (iOS/macOS, on a fork of the herdr TUI; see
   `docs/architecture/herdrup-analysis.md`). Phase 4 targets the official app first, then Whip.
4. **Two Herdr protocols exist; only one is worth implementing.** The Herdr TUI (`herdrdev/herdr`, v0.9.5)
   is standalone and has its own socket API (non-JSON-RPC, ~90 methods, workspace/tab/pane model) and a
   bincode binary terminal protocol (`PROTOCOL_VERSION = 22`, clients gate on 17..=22). herdr-core is the
   new, documented, append-only API. Implement **hc generation 1 only**; leave the legacy TUI protocol out
   (moving target, undocumented binary format, only bridges use it).
5. **Recommended architecture:** a stateless translator package `internal/hcompat`, run per connection by
   `agentd hc connect` (and by an optional socket listener), that maps hc methods onto agentd daemon ops
   over the existing unix socket — the same pattern as `internal/httpapi`. Shared state that hc needs and
   agentd lacks (stable `tN` addresses, labels, the `herdr-app` document store, turn counter, OSC 7501-style
   status) is added to the daemon as small, generic ops. Mailbox, ACL, identity, delivery, MCP stay as
   they are.
6. **Effort:** PoC (Desktop connects and lists/attaches one terminal) ~2-3 days. Desktop parity
   (Phases 1-3) ~3-4 weeks. Whip ~1 week more. Main risks: status semantics (hc never infers status,
   agentd does), the `herdr-app` document (workspaces/tabs live there), binary-name collision with a real
   herdr-core install, and upstream protocol drift (mitigated by the published `released.openrpc.json`).

## 1. Current agentd architecture (what can be reused)

See `CLAUDE.md` for the normative contracts. Relevant pieces:

| Need (hc) | agentd today | Reuse |
|---|---|---|
| Persistent PTYs independent of clients | `internal/session` (creack/pty + `vt.SafeEmulator`, fan-out, ring buffer, logs) | as is |
| First paint on attach | `Session.SubscribeWithRedraw()` (Render + cursor) | as is; add scrollback lines (`history`) |
| Viewer input, human vs automatic | `Session.Write(p, human)`, `WriteAct`, `termreply.go` heuristics | map DATA → human, AUTOMATIC_INPUT → non-human (no heuristic needed) |
| Resize, last active viewer wins | attach `R` frames, restore-on-detach | as is |
| Event stream with seq, snapshot, resync | op `subscribe` (`events.go`: epoch, seq, ring, `resync`) | map to hc `event` / `missed` / `heartbeat` |
| Status | semantic view status `idle/working/waiting/exited` + attention (`view.go`) | map to hc `Status{state,app}` + `turn` (deviation, §7.3) |
| send / keys / interrupt | ops `act prompt/keys/interrupt`, `inject` (paste verify, draft guard) | map; hc key names need translation |
| read | op `dump` / `view` | map |
| run / kill / rm | ops `run` / `kill` / `rm` | map |
| Peer identity | `SO_PEERCRED` + ancestry + token (`identity.go`) | the translator connects to the daemon as the calling process: Desktop / sshd user = `human:<user>`, an `hc` run *inside* a session = that session (restricted) |
| Stable terminal addresses `tN` | none (names only) | **new** |
| Labels `map[string]string` | none | **new** |
| Versioned JSON documents (`doc.get/set` + `doc` event) | none | **new** |
| Turn counter | none (attention `turn_done` exists) | **new**, derived from view status |
| Foreground process `{pid,name,argv,cwd}` | cwd via OSC 7 only | **new** (Linux `/proc/<pid>/stat` tpgid); optional |
| Persistent core id `{id,name}` | daemon `epoch` (per start) | **new**, persisted in state dir |

Not reusable for this goal: `internal/httpapi` + `internal/apitoken` + `internal/tlsutil` (HTTP/SSE gateway,
device tokens, QR pairing) and `app/` (Flutter client). They solve the same problem with a bespoke
protocol nobody else speaks. See §9 for what to do with them.

## 2. herdr-core architecture

`herdrdev/herdr-core` (Rust, Apache-2.0, binary `herdr-core`, alias `hc`, version 0.1.21).

- **Core process** (`server.rs`, `api.rs`, `runtime.rs`): listens on `$XDG_RUNTIME_DIR/herdr-core/<session>.sock`
  (fallback `/run/user/<uid>/herdr-core/`, then the state dir; `HC_SOCKET` overrides; `docs/security.md`).
  No per-client auth: the socket's 0700 directory is the boundary.
- **Holders** (`holder.rs`, `holder_proto.rs`): separate processes that own the PTYs so the core can restart
  or update without killing programs. agentd keeps PTYs in the daemon (a daemon restart ends sessions);
  not needed for compatibility.
- **VT**: `crates/ghostty-vt` (Zig libghostty-vt). Parses OSC 7501 status reports.
- **Status**: OSC 7501 "Program Status Protocol" (`status.rs`): `ESC ] 7501 ; state=working:id=..:kind=..:progress=..:app=..:title=<b64>:msg=<b64> ESC \`.
  Core stores reports as is and **never infers status from the screen**; `turn` increments each time the
  root record leaves `working`.
- **Machines** (`machines.rs`, `remote.rs`, `relay.rs`): core-to-core over `ssh target sh -c '... exec herdr-core connect'`;
  remote terminals appear as `machine/tN` in the same list and event stream.
- **Documents** (`api/doc.rs`): opaque JSON blobs with optimistic versioning, `docs.json` in state.
- **`hc connect`** (`cli.rs:700-760`): a plain byte pump between stdin/stdout and the core socket
  (autostarts the core). No protocol logic in it.

## 3. The protocol (generation 1)

### 3.1 Plain mode
JSON-RPC 2.0, one message per line, both directions. Errors:
`{"code":-32000,"message":"no terminal t9","data":{"kind":"not_found","hint":"..."}}`. Stable `data.kind`
values used by clients: `not_found`, `finished`, `unavailable`, `conflict`, `invalid_params`,
`unsupported_generation`, `needs_update`, `needs_login`. `$/cancelRequest {id}` is honoured.

### 3.2 hello
Params (all optional): `generation`, `framed`, `features[]`, `core{id,name}` (another core dialing in),
`calls_you`. A generation other than 1 → error kind `unsupported_generation`.
Result (required): `generation:1, version, core{id,name}, framed, methods[], features[]`; optional
`login_session`, `keeps_running_after_logout`, `logout_linger_user` (`api.rs:178-197`).
Features advertised by 0.1.21: `framed, attach, events, status, shell, resize_notifications,
automatic_input, restart_environment` (`api.rs:17-26`).

### 3.3 Framed mode
`[channel u32 LE][kind u8][len u32 LE][payload]`, payload ≤ 16 MiB (`wire.rs`).

| kind | name | payload |
|---|---|---|
| 0 | DATA | ch 0: one JSON-RPC message; other channels: stream bytes |
| 1 | CREDIT | u32 LE bytes consumed |
| 2 | CLOSE | channel over; from core a JSON reason `{reason,exit_code,signal}`, from client empty |
| 3 | RESIZE | u16 LE cols, u16 LE rows (viewer→core; core→observer when negotiated) |
| 4 | FOCUS | 1 byte: 1 gained, 0 lost |
| 5 | FACTS | JSON `Display {fg,bg,cursor,cell:[w,h]}` |
| 6 | AUTOMATIC_INPUT | terminal-generated replies; only if `hello.features` and attach result both say `automatic_input` |

Channel 0 calls run concurrently (`server.rs:351-410`). Unknown frame kinds are ignored.

**Flow control:** each stream starts with 256 KiB credit (`WINDOW`); viewer returns CREDIT for bytes it
consumed; a viewer > 4 MiB behind (`MAX_PENDING`) gets a fresh repaint instead of the backlog; a stalled
stream never blocks channel 0. Event channels use credit too (Desktop grants 1 MiB up front).

### 3.4 Attach
`terminal.attach {terminal, cols, rows, display?, history?, observe?, resize_notifications?, resumed?, caller?}`
→ `{channel, terminal: Terminal, resize_notifications, automatic_input}`. The reply precedes stream frames.
First paint (`viewers.rs:485-507`): `CAN (0x18)` + screen paint (+ `history` scrollback lines) +
`CSI ?9001h|l` + one OSC 7501 per status record; then live raw output, minus terminal queries the core
already answered. Viewer sends keys as DATA. Last active viewer sets the size; `observe` never types or
resizes. Program end → CLOSE `{"reason":"exited","exit_code":0,"signal":null}`.

### 3.5 Events
`events.subscribe {terminal?, children_of?, labels?, docs?}` → `{seq, terminals[], channel?}`; in framed mode
events arrive as NDJSON on `channel` (lines may span frames):
`{"jsonrpc":"2.0","method":"event","params":{"seq","type","terminal","data"}}`, `heartbeat {seq}` every 15 s,
`missed {seq, terminals}` for a slow subscriber. Types: `created, lost, restarted, keep, finished, removed,
status, turn, title, labels, size, foreground, doc` (apply rules in `schema/README.md` "Events").

### 3.6 Methods
`hello, server.status, server.stop, server.restart, terminal.run, templates.list, terminal.list, terminal.get,
terminal.kill, terminal.rm, terminal.keep, terminal.label, terminal.read, terminal.attach, terminal.send,
terminal.wait, terminal.keys, terminal.interrupt, terminal.put, machine.exec, file.read, file.write, file.edit,
doc.get, doc.set, events.subscribe, machine.add, machine.list, machine.connect, machine.rename, machine.rm,
rpc.discover`. Full params/results: `schema/hc.openrpc.json`.

`Terminal` record (required): `address, labels, argv, cwd, state ("live"|"finished"), created_ms, screen
("exact"|"inexact"), cols, rows`; optional `name, pid, title, status, records, reported, status_ms, turn,
keep, parent, notify, rm, reason, exit_code, signal, finished_ms, foreground, template, identity, ...`.

## 4. What each client really uses

### 4.1 Herdr Desktop (`herdrdev/herdr-app`, Apache-2.0)
- **Discovery** (`src/core_bin.rs:77-139`): settings `core` path, else `HERDR_CORE_DIRS`, else PATH and
  usual dirs (`~/.local/bin`, `/usr/local/bin`, ...), trying `herdr-core` then `hc`. A candidate counts only if
  `<bin> --version` exits 0 and stdout starts with `"hc "`.
- **Connect**: spawns `<bin> connect`, writes plain `{"jsonrpc":"2.0","id":0,"method":"hello","params":{"framed":true}}`,
  reads one line, then frames (`hc.rs:325-362`).
- **Gate** (`hc.rs:205-264`): `generation == 1`; `version ≥ 0.1.11` where the version is parsed by splitting
  on `.` and `-` (so `0.1.21+agentd` parses as `0.1.0` and is **rejected**; use a plain `0.1.21`);
  `methods` ⊇ `terminal.run, terminal.list, terminal.kill, terminal.label, terminal.attach, terminal.keep,
  doc.get, doc.set, events.subscribe`.
- **Calls**: `events.subscribe {}` (the only source of the terminal list; `terminal.list` is never called),
  `terminal.attach {terminal,cols,rows,history:2000,display}`, `doc.get/doc.set {name:"herdr-app"}`,
  `terminal.run {argv,cwd,env{TERM,COLORTERM,TERM_PROGRAM:herdr},labels{herdr.space,herdr.tab,herdr.seen},keep:true,display,size?}`,
  `terminal.get`, `terminal.label` (merge, empty value deletes), `terminal.keep`, `terminal.kill`,
  `terminal.read {lines:5}`, `machine.list` (polled ~2.5 s, failure tolerated), `server.status`
  (`version`, `installed_by`: non-null disables the app's core self-update — `update.rs:237-242`).
- **CLI calls on the same binary**: `update --json` (skipped when `installed_by` is set), `put` and `exec`
  (remote addresses only), bundled mods run `hc machine ls --json`.
- **Attach handling** (`terminal.rs:340-560`): resets its VT, expects a full repaint + history; credits each
  DATA frame after ingesting; sends no initial credit (server window applies); RESIZE on fit; FOCUS;
  FACTS on change; AUTOMATIC_INPUT only if negotiated; CLOSE with reason `exited|killed|finished` ends the
  pane. No `resumed`; on child death it reconnects every 1 s and reattaches.
- **Workspaces/tabs live in doc `herdr-app`** (`src/mirror.rs`, `src/layout.rs`):
  `{"spaces":[{"id","name","folder","tabs":[{"id","name","root":Node}]}]}`, `Node = {id, type: block|row|column|stack,
  children[], active?, size?, ...}`, terminal leaf `{"type":"block","content":{"terminal":"t1"}}`. Unknown
  fields are preserved. Write conflict → re-read.
- **Adoption** (`window.rs:2236-2262`): a live terminal with no pane but a non-empty label `herdr.space`
  (optional `herdr.tab`) is adopted into that space/tab (unknown ids are created unnamed; folder from
  `foreground.cwd`/`cwd`), and `terminal.keep {keep:true}` is called. Terminals without `herdr.space` are
  not shown in screens.
- **Status**: reads `status.state` (`working|blocked|error|idle|done`) and `status.app`; "done/unseen" =
  `turn > labels["herdr.seen"]`.

### 4.2 Whip (`kosumic/whip`, app AGPL-3.0+, `crates/herdr-protocol` MIT)
- Own Rust SSH client on the phone (keys/password, known_hosts, jump hosts). No host software required.
- Discovery script over SSH exec prints `whip-hc-endpoint-v1\0` + session, socket, installed flag, command
  (`crates/herdr-protocol/src/hc/endpoint.rs:37-79`): socket from `$HC_SOCKET`, `herdr-core/<s>/core.lock`,
  `$XDG_RUNTIME_DIR/herdr-core/<s>.sock`, `/run/user/$UID/...`; `hc` installed ⇒ Core backend.
- Transport: SSH `direct-streamlocal` to that socket; if refused, `hc server start --json` and rediscover;
  else exec `'<hc>' connect --session '<s>'` (`endpoint.rs:147-170`).
- Hello `{"generation":1,"framed":true}`; requires `generation==1`, `framed`, `core`, `features`, `version`,
  and `methods` ⊇ `terminal.list, terminal.attach, terminal.run, terminal.send, terminal.read, terminal.kill,
  events.subscribe, doc.get, doc.set` (`hc/mod.rs:23-33`). Schema pinned to 0.1.16.
- Calls also `terminal.get/label/keys`, `server.restart`; attach with `resumed`.

### 4.3 Others
- **0cv/herdr-mobile-relay** (AGPL-3.0+): host relay; `HERDR_RELAY_BACKEND=hc` dials the hc unix socket
  directly (not stdio), per-call `hello {generation:1}`, framed for attach. Works if agentd also listens
  on the hc socket path.
- **HerdrGo** (no license file) and **teasec4/herdr-mobile-app** (MIT): legacy TUI socket API at
  `~/.config/herdr/herdr.sock` + `herdr` CLI subcommands (`herdr api snapshot`, `herdr agent start`,
  `herdr terminal session control`...). Out of scope (§0.4).

## 5. Entity mapping

| hc | agentd | Notes |
|---|---|---|
| core `{id,name}` | daemon | `id` = new persisted random id (`state/hc/core-id`); `name` = hostname (`info.host`) |
| terminal `address` `tN` | session | new monotonic `N` assigned at `run`, never reused (persisted counter, like mailbox ids); bare, no `/` |
| terminal `name` | session name (`claude@webapp`) | hc names are `[a-z0-9-_.]{1,32}`; clients only display it. Resolve both `tN` and agentd name on input |
| `argv`, `cwd`, `created_ms`, `cols`, `rows`, `pid`, `title` | `SessionInfo` | direct |
| `state live/finished`, `reason`, `exit_code`, `signal`, `finished_ms` | lifecycle running/exited | `starting` → live without `pid`; `kill` → `reason:"killed"` |
| `labels` | **new** session labels | defaults: `herdr.space=<project label>`, `herdr.tab=<session name>`, `agentd.harness=<harness>` |
| `status{state,app}` + `turn` | view status + attention | §7.3 |
| `keep` | — | stored flag only; agentd has no shell-replacement recovery (documented deviation) |
| `parent`, `notify`, children | — (agentd has mailbox, not parenthood) | null; later: `run` from a session could set parent |
| workspace / tab | doc `herdr-app` spaces/tabs | agentd project (alias label) ⇒ space; stored as an opaque versioned doc |
| attach channel | op `attach` stream | per hc channel one daemon attach connection |
| event `seq` | bus `seq` per epoch | restart ⇒ client takes a fresh snapshot (hc semantics identical) |
| machine | — | `machine.list` → `{machines:[]}` until Phase 5 |
| mailbox, rooms, ACL, MCP | — | no hc equivalent; stays agentd-only (§8) |

## 6. Proposed design

```
Herdr Desktop ─spawn─► herdr-core connect ─┐   (agentd binary via argv[0] / `agentd hc connect`)
Whip ─ssh exec─► hc connect --session s ───┤
Whip / 0cv relay ─streamlocal─► hc socket ─┘   (optional: `agentd hc serve` listener, Phase 4)
                                           │  hc gen 1 (plain → framed)
                                   internal/hcompat   (per connection, stateless)
                                     wire.go   frames, credit windows, 4 MiB repaint rule
                                     rpc.go    JSON-RPC 2.0, concurrent calls, $/cancelRequest
                                     schema.go hc types (generated/checked against released.openrpc.json)
                                     methods.go hc method → daemon op mapping
                                     attach.go one daemon attach per channel, first paint, CLOSE reasons
                                     events.go daemon subscribe → hc event/missed/heartbeat
                                     keys.go   hc key names → agentd key names
                                           │  internal/client over $XDG_RUNTIME_DIR/agentd/agentd.sock
                                   agentd daemon (registry, sessions, view, bus, mailbox, docs, labels)
```

Rules:
- `internal/hcompat` depends on `client` + `proto` only (same rule as `httpapi`); no business logic: every
  decision (permissions, draft guard, paste verification, ACL) stays in the daemon.
- The binary dispatches on `argv[0]`: invoked as `herdr-core` or `hc`, it accepts `--version` (prints
  `hc 0.1.21`), `connect [--session S]`, `machine ls --json` (`{"machines":[]}`), `server status --json`, and
  refuses `update`/`server restart` with a clear error. Installation creates a symlink in a dedicated dir
  (`~/.local/share/agentd/hc/herdr-core`), never overwriting a real herdr-core (§10 R3).
- hc `hello.version` = the herdr-core release whose released schema we conform to (`0.1.21`); agentd's own
  identity goes in `server.status.installed_by = "agentd"` and `version_detail`. Config `[herdr] enabled`
  (default false) gates the whole surface.

### 6.1 Daemon changes (generic, not hc-specific)
1. `SessionInfo.seq` (stable `tN`), persisted counter.
2. Labels: `run {labels}`, new op `label {name, labels}` (merge; empty deletes), event `session.labels`.
3. Documents: ops `doc_get {name}` / `doc_set {name, value, version}` (`conflict` on stale), file
   `state/docs.json` (atomic rename, 1 MiB cap, `.bad` on corruption), bus event `doc.changed {name, version}`.
   Humans only by default (sessions: read-only unless configured).
4. View: `turn` counter (bumped when status leaves `working`), exposed in `ls`/`view`/`status.changed`.
5. Optional: foreground process (`/proc/<pid>/stat` field 8 tpgid) sampled ~2/s, event `session.foreground`.
6. Attach: an automatic-input frame type (`A`) so hc kind 6 bypasses `termreply` heuristics; a focus frame
   (`F`) forwarded to the child when it enabled mode 1004.
7. Attach `history N`: emit N scrollback lines before the redraw.

### 6.2 Method mapping

| hc method | agentd op | Notes |
|---|---|---|
| hello | ping + info | gate generation; build methods list = implemented set |
| server.status | ping/info | `installed_by:"agentd"` |
| events.subscribe | subscribe (+ ls snapshot) | translate kinds; filters `terminal`, `labels`, `docs`; credit on channel |
| terminal.list / get | ls | `all` → include exited |
| terminal.run | run | `argv`/`line` (`sh -c`), `cwd`, `env`, `labels`, `size`; `name` sanitized; harness profile from `argv[0]` basename |
| terminal.attach | attach | channel per attach; `observe` = no input/resize |
| terminal.kill / rm | kill / rm | |
| terminal.label / keep | label / (flag) | |
| terminal.read | dump / view | `lines` |
| terminal.send | act prompt | waits for idle/draft like hc (`timeout`); `force` → act force; `if` checked against status |
| terminal.keys | act keys | name translation (`ctrl+c`→`ctrl-c`, add `alt+x`, `f1..f12`, letters) |
| terminal.interrupt | act interrupt | |
| doc.get / doc.set | doc_get / doc_set | |
| machine.list | — | `{machines:[]}` |
| templates.list | — | `{templates:[]}` (later: harness profiles as templates) |
| terminal.wait, put, file.*, machine.exec/add/... | — | Phase 3+/5 or `not_supported`; not advertised until implemented, except where a client gate requires the name (then a clear error) |

### 6.3 Security
- Same trust model as herdr-core and agentd: OS account + 0700 socket dir; remote = system sshd
  (keys, `authorized_keys` restrictions such as `command=` / `restrict` are the per-key authorization tool).
- The translator inherits agentd identity: callers inside a session are that session (no `view`/`type`/`act`
  on others unless `[security] agent_view/agent_type`), so an agent cannot use `hc` to escape agentd's ACLs.
- `[herdr] read_only = true` (observe-only attach, no run/send/keys/kill/doc_set); `[herdr] allow_run`
  (default: built-in harnesses only, like `[api] run_harnesses`).
- Audit: reuse the daemon `act` audit log lines with `via: "hc"`.
- Resource limits: frame ≤ 16 MiB (reject larger), per-connection stream cap, credit windows bound memory
  (4 MiB per stream then repaint), concurrent call cap.

## 7. Risks and deviations

- **R1 First paint fidelity.** hc paints with libghostty; agentd with `x/vt` `Render()`. Clients reset their
  VT and accept any correct VT stream, so this is fine, but scrollback (`history:2000`) needs new code and
  fixtures (`session.NewReplayer`).
- **R2 Credit flow.** Daemon attach has no credit; the translator buffers per channel and on > 4 MiB backlog
  reattaches to get a fresh redraw (the hc rule). Needs `-race` tests with a slow reader.
- **R3 Status semantics (deviation).** hc only reports OSC 7501 from the program. Claude/Codex do not emit
  7501, so herdr-core shows *no* status for them; agentd's classifier is heuristic. Mapping:
  `working→working`, `waiting→blocked` (`kind:"question"`/`"permission"` from the dialog), `idle` after a turn
  → `done` until `herdr.seen`/attention ack, else `idle`, `exited→` cleared; `app` = harness name. Programs that
  *do* emit OSC 7501 should win over the heuristic (parse 7501 in `session`). Document as deviation.
- **R4 Binary name collision.** Desktop and Whip find the first `herdr-core`/`hc`. If a real herdr-core is
  installed both cores run side by side and the client picks one. Mitigation: never install over an
  existing binary; for Desktop use the `core` setting pointing at agentd's shim dir; for Whip put the shim
  first in the remote PATH or let it use the hc socket path (agentd listens only if no herdr-core socket
  exists).
- **R5 `herdr-app` doc ownership.** The app writes layout as it likes; agentd must store it opaquely and
  never rewrite it except optional seeding (one space per project on first use) with versioned writes.
- **R6 Upstream drift.** The API is append-only within generation 1 and `released.openrpc.json` is the
  contract. Add a test that validates our results against a vendored copy of the schema (as the
  OpenAPI contract tests do) and a conformance suite run against a real herdr-core in CI (§11).
- **R7 Session-name constraints.** hc names are lowercase `[a-z0-9-_.]`; agentd names allow `@` and
  uppercase. Addresses are `tN`, names are display only; `terminal.run {name}` accepts hc names (subset).
- **R8 keep / parents / machines** are not modelled by agentd. Advertise only what works; keep is a stored
  flag (agentd sessions do not outlive the daemon anyway).

## 8. agentd's own capabilities stay independent

Mailbox, delivery, ACL, identity, MCP tools, purge, aliases, profiles are untouched. hc clients see
terminals; agents keep using `agentd send/reply` and `agentd_*` MCP tools. Later options that need no client
change: expose an agent's inbox count in a label (`agentd.inbox=3`) or as an OSC 7501 child record
(`id=mail`, `msg=...`), which Desktop renders as status records; a `herdr-app` mod (Desktop supports JS mods
in `defaults/mods`) could show mailbox UI — that would be agentd-owned code, not a client modification.

## 9. What to do with the current gateway and Flutter app

The HTTP/SSE gateway (`internal/httpapi`, `apitoken`, `tlsutil`, `agentd serve`, ~6.6k Go lines) and the
Flutter app (`app/`, ~10.5k Dart lines) duplicate what hc gives with existing clients. Proposal:
1. Remove `app/`, `.github/workflows/app*.yml`, `packaging/app/`, app docs, in one `refactor:` commit.
2. Keep `agentd serve` frozen (notifications via ntfy/webhook are still useful and hc has no push
   notifications) until hc parity is reached; then decide: keep only `[api.notify]` as a daemon-side
   feature, or drop the gateway.
3. Update `CLAUDE.md` contracts in the same commits.

## 10. Phased plan

| Phase | Content | Exit test | Estimate |
|---|---|---|---|
| 0 | This analysis | — | done |
| PoC | `internal/hcompat` wire + rpc + hello; argv[0] shim (`--version`, `connect`); `events.subscribe` snapshot from `ls` (synthetic `tN` = list index, labels `herdr.space`); `terminal.get/list`; `terminal.attach` over daemon attach (no credit limits, no history); `doc.get/set` in-memory; `machine.list` empty; `server.status` | Go test drives the shim over stdio like Desktop (`hello` line, frames, subscribe, attach shows fakeagent output); manual: Desktop with `core` setting lists and opens one agentd session | 2-3 days |
| 1 | Daemon: stable `seq`, labels, docs store + `doc.changed`, `turn`; hcompat: real events translation, credit flow, CLOSE reasons, error kinds, schema test vs `released.openrpc.json` | conformance tests (plain + framed) | 1 week |
| 2 | Interactive: input DATA/AUTOMATIC_INPUT, RESIZE, FOCUS, FACTS, `history`, 4 MiB repaint, multi-terminal, reconnect, observe | `-race` slow-reader and multi-viewer tests; Desktop manual checklist | 1 week |
| 3 | run/send/keys/interrupt/read/kill/rm/label/keep; status mapping + OSC 7501 parsing; doc seeding per project; foreground; `[herdr]` config + audit | Desktop: create, drive and close an agent in a workspace | 1-1.5 weeks |
| 4 | Whip: `connect --session`, hc socket listener (`agentd hc serve` / daemon-side), discovery paths, `resumed`; then 0cv relay hc backend | Whip on Android over SSH | 1 week |
| 5 (optional) | `machine.*` relay (agentd↔agentd or agentd↔herdr-core), `terminal.wait`, templates from profiles | — | open |

Conformance environment (§11 of the spec): a CI job installs a pinned herdr-core release (Apache-2.0,
static Linux build) and runs the same scripted protocol session (hello, subscribe, run `sh`, attach, input,
resize, CLOSE, reconnect) against both servers, comparing result *shapes* (schema-validated) and event
sequences, never screen pixels. GUI checks stay manual.

## 11. Licences and sources

| Repo | Commit | Licence | Use |
|---|---|---|---|
| herdrdev/herdr-core | c84d6e7 | Apache-2.0 | protocol reference; vendoring `released.openrpc.json` for tests is allowed with NOTICE |
| herdrdev/herdr-app | b67715e | Apache-2.0 | client behaviour reference |
| herdrdev/herdr | ee5bfb5 | Apache-2.0 | legacy TUI protocol (not targeted) |
| kosumic/whip | 41205ad | AGPL-3.0+ (protocol crate MIT) | read only; no code copied |
| 0cv/herdr-mobile-relay | d17e7d7 | AGPL-3.0+ | read only |
| teasec4/herdr-mobile-app | 0273404 | MIT | read only |
| herdr-go/herdr-go | 336b77a | none (all rights reserved) | read only |

Implementing a protocol from a public specification is independent of these licences; agentd (MIT) copies
no code. Rust is not required: everything is Go stdlib + existing deps.

Tooling note: the knowledge-base, code-index and Context7 tools required by `CLAUDE.md` were not available
in the session that wrote this; the repositories were cloned and read directly instead.
