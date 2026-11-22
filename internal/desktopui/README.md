# agentd-desktop (MyGo)

`cmd/agentd-desktop` is the native desktop client of agentd: the agents of every host (agentd
sessions, zellij/tmux sessions, plain SSH shells; hosts reached over SSH or, paired with an invite
link, through agentd-relay), their Chat (semantic view + act) and Terminal
(attach), the Panel (what every agent is doing, its tasks and what needs your answer), hosts and
provisioning, the mailbox and work queues, settings. Plan:
`docs/plans/agentd/desktop_plan.md`; normative rules: "Desktop client" in `AGENTS.md`. Design: the
validated mockups linked from the plan (phone layout adapted to a window). Reference implementation:
Pando's `internal/nativeui` (same toolkit, same patterns).

Toolkit: [MyGo](https://github.com/egoist/mygo) **v0.7.1, pinned exactly** (pure Go via purego, GPU
rendered with OpenGL, immediate mode with keyed retained state). It needs Go >= 1.27.3:
`GOTOOLCHAIN=go1.27.3 go ...` (the `Makefile` exports it). MyGo docs:
`$(go env GOMODCACHE)/github.com/egoist/mygo@v0.7.1/docs/ui/*.md`.

The daemon and the `agentd` CLI never import anything here:
`GOTOOLCHAIN=go1.27.3 go list -deps ./cmd/agentd | grep -c mygo` must print `0`.

## Packages

| Package | Role | Imports MyGo |
|---|---|---|
| `backend/` | Contract between the UI and the code that reaches hosts (`Backend`, `Shell`, `PTY`, `Session`, `Host` with its transport: SSH or `Relay *RelayTarget`, `Host.Allows(Capability)` from the relay device scope, `PairRelay`, provisioning types, `HostBoard` + `WatchBoard` + `Reply` for the Panel). Change it only together with every implementation | **no** |
| `backend/fake/` | In-memory `Backend` with the mockup data (hosts estudio, vps-hetzner, portatil, raspberry, and nube over the relay with an `operate` device; a permission dialog, a question with options, a working session...). Act bumps revisions, the PTY echoes, provisioning streams events, `PairRelay` accepts `fake.InviteLink`, task lists and an open question for the Panel (`WatchBoard`, `Reply`, `SetTasks`, `Ask`); never runs a program | no |
| `remote/` | `backend.Shell` over SSH (`golang.org/x/crypto/ssh`) and on this computer, `DialUnix` (direct-streamlocal), app key, relay device key (`RelayKey`), TOFU host keys, `hosts.json` (SSH and relay hosts), `~/.ssh/config` import, connection pool. `remote/sshtest/`: in-process SSH server for tests | no |
| `core/` | The real `Backend`: hosts + shells, daemon socket probe, session aggregation (agentd `ls` + `subscribe`, zellij/tmux polling, plain shells), views, mailbox, act, attach, run, kill, the board per host (`board.go`: op `board {since_digest}` after the host's `task.*`/`mail.*`/`status.*`/`attention.*`/`session.*` events, debounced 300 ms). Every daemon op on a fresh `client.Client` (`client.Dial` locally, `client.NewConn` over a direct-streamlocal channel for SSH hosts, over a stream of the host's relay tunnel for relay hosts: `relay.go`, `internal/relayclient`) | no |
| `dirlist/` | Folder browser, host side: the sh script that lists the directories under a path (run through `Shell.Exec`) and the parser of its NUL-separated output; tested under a real `sh` | **no** |
| `mux/` | zellij and tmux adapters over a `Shell` (port of agentd-app `lib/src/mux`). `mux/muxtest/`: fake Shell | no |
| `provision/` | Detect script, install plan and steps (agentd `install.sh` + `SHA256SUMS`, systemd user unit, `[herdr] listen`, MCP snippets, agent CLIs); port of agentd-app `lib/src/provision` | no |
| `natives/` | libghostty-vt embedded per platform (go:embed); `Install` extracts it to `<user cache>/agentd-desktop/natives/<sha>/` and sets `$MYGO_GHOSTTY_VT` before the terminal loads | no |
| `chatfmt/` | Pure Chat text parsing, no MyGo: `Split` finds tables in an assistant block (box-drawing light/heavy/double, columns from the border junctions, wrapped cell fragments joined with a space, header = first row before a separator, markdown pipe tables with alignment; anything inconsistent stays plain text), `Linkify`/`TrimURL` (http/https, trailing punctuation trimmed unless balanced), `OpenURL` (xdg-open / open, replaceable in tests) | no |
| `kit/` | Palette of the mockups -> `ui.Theme`, embedded fonts (Inter, JetBrains Mono, Atkinson Hyperlegible Next, Literata; OFL 1.1, license texts in `kit/fonts/OFL-*.txt`, embedded too), lucide icon subset (`kit/icons`, ISC), base components | yes |
| `shell/` | `mygo.App`, main window, application menu, desktop notifications; owns the event loop (main goroutine) | yes |
| `app/` | `Context`, area registry (`Area.Badge` = sidebar count), router, layout (sidebar + content), `Live` hosts/sessions/boards, `Views` cache, notifier (sessions and boards), prefs (`settings.json`). `app/apptest/`: headless harness on the fake backend | yes |
| `app/<area>/` | `panel` (Panel: Por agente / Tablero), `agents`, `session` (Chat + Terminal), `newsession`, `hosts`, `addhost`, `provision`, `mailbox`, `settings` | yes |
| `desktopui.go` | `Run(Options)`: context, natives, root view, shell; blank-imports the areas; dev aids | yes |

## Relay hosts

Añadir host > Relay (invitación): paste the link printed by `agentd relay invite` on the host
(`agentd://relay?v=1&…`), optionally name this device (default: the hostname), and wait while the host
owner approves it (`Backend.PairRelay` blocks; Cancelar abandons it). The host is then saved in
`hosts.json` with its relay URL, route, pinned host key (X25519), device id, scope, session globs and
kind; the device key is `relay_key` (one per install, shared by every relay host). `core` keeps one
tunnel per relay host and opens a fresh stream per daemon op; the host's welcome refreshes the stored
scope (a downgrade on the host shows at once). Errors: `ErrRelayHostOffline` (host not linked to its
relay) and `ErrDeviceRevoked` (revoked, unknown or expired device) are host errors
(`relay_host_offline`, `device_revoked`).

No shell over the relay: no zellij/tmux/plain shells, no Solo terminal, no folder browser, no Preparar
host (`ErrNeedsSSH`). Areas hide what the device scope would make the daemon refuse
(`Live.Allows(host, cap)`): `view` = read only (no composer, no answers, no Interrumpir, the terminal
only observes, no mailbox), `operate` = no Nueva sesión, Terminar or kill, `admin` = everything a relay
allows; guests have no mailbox. The link carries a one-time code: never log it (nor the key).

Only `kit`, `shell`, `app`, `app/<area>` and the root import MyGo. Areas talk to hosts only through
`backend.Backend` (never `remote`, `core` or `mux` directly); `cmd/agentd-desktop` picks the backend.

New session also takes `?project=<alias|path>` (the `+` of a project group in the agents list).

Chat text (Ajustes > Vista chat and the chat header's "Texto del chat" menu, same saved settings in
`settings.json`): size (`chat_font_size`), font (`chat_font`: `sans` Inter, `serif` system serif, `mono`
JetBrains Mono, `legible` Atkinson Hyperlegible Next, `reading` Literata; the last two are embedded static
Regular + Bold TTFs) and line spacing (`chat_line_height`: `compact` | `normal` | `wide`, a 0.87 / 1 / 1.2
scale of the agent prose line height 1.5, the user messages' 1.45 and the paragraph gaps). Unknown values
fall back to `sans` / `normal`.

## Panel

`app/panel` (first sidebar entry, badge = items needing attention on every host): one screen built from
`Live.Boards` (`Backend.WatchBoard`, op `board` per host) joined with the session list. "Por agente"
(default) shows four tiles (esperan tu respuesta, tareas en curso, pendientes, hechas hoy), "Necesita tu
atención" (open questions: an option or free text answered with `Backend.Reply` (Buzón) or act `prompt`
with the view's revision + epoch, forced while working (Prompt); menus of `waiting` sessions: act
`choose` with revision + epoch, a conflict refreshes the cached view and toasts; blocked tasks: Responder
= `Backend.Send` to the agent, Abrir agente) and one card per agent (current task, progress, next
pending, footer; agents that report no tasks show status + preview, "Sin tareas informadas"). "Tablero"
shows four columns from every task list (Pendiente, En curso, Bloqueada, Hecha hoy = done with
`updated_at` on today's local date). The view is saved as `panel_view` in `settings.json`
(`/panel?view=tablero|agente` sets it too); "Todos los hosts" filters by host. Relay devices follow
`Host.Allows`: no mailbox = no Buzón/options/Responder, no operate = no Prompt/menu choices. The human
never adds or edits tasks. Notifications: a new open question ("<sesión> pregunta") and a task newly
blocked, behind Ajustes toggles "Preguntas" / "Tareas bloqueadas" (default on); a click opens the Panel.

Routes: panel `/panel`, agents `/agents` (+ `/agents/s/{host}/{source}/{name}`), hosts `/hosts`, addhost `/hosts/add`,
provision `/hosts/provision/{host}`, newsession `/new` (+ `/new/{host}`), mailbox `/mailbox`
(+ `/mailbox/{host}`), settings `/settings`. UI copy is Spanish, as in the mockups.

## Adding an area

1. Create `internal/desktopui/app/<area>/` with a type implementing `app.View`
   (`Build(c *ui.Context, r *ui.Route)`).
2. Register it from `init()`:

   ```go
   func init() {
       app.Register(app.Area{
           ID: "mailbox", Path: "/mailbox", Patterns: []string{"/mailbox", "/mailbox/{host}"},
           Title: "Buzón", Icon: kit.IconMailbox, Section: app.SectionMain, Order: 2, Shortcut: 3,
           New: func(a *app.Context) app.View { return newView(a) },
       })
   }
   ```

   `SectionHidden` + `Parent` for routable pages without a sidebar entry (new session, add host).
3. Blank-import the package in `desktopui.go`.
4. Implement `app.Closer` when the view owns goroutines or streams.
5. Add a headless test with `app/apptest` (see below).

## Rules

- **UI thread only.** View state is touched only in `Build`, or from goroutines through
  `a.Update(func(){...})` (`app.Context.Update` -> `mygo.Window.Update`).
- **Streams feed the UI in batches**: `app.NewBatch(a.Update, apply)` turns many `Push` calls into one
  update per frame.
- **Background work** goes through `a.Go(func(ctx){...})`: a panic becomes an error toast, `ctx` ends
  at shutdown. Errors to the user: `a.Toasts.Error(title, detail)` (safe from any goroutine).
- **Acts** carry the view's `revision` and `epoch` (`choose` requires them); a `conflict` shows the view it
  carries (`app.ConflictView`, `Views.Set`) instead of retrying blindly.
- **One session view per content area**: no pane/split UI (AGENTS.md Rules). On wide windows the
  agents list shows beside the open session; that is not a split of sessions.
- **Look and feel** comes from `kit` (tokens, components); add new shared components there. Icons are
  lucide SVGs in `kit/icons` with a constant in `kit/icons.go`.
- **Keys**: elements built in loops that keep state need `c.Key(id)`.
- **Secrets**: never log passwords, passphrases, private keys, tokens, prompt text or message bodies.
  Passwords are asked through callbacks and never stored.

## State files

`remote.Dir()`: `$AGENTD_DESKTOP_DIR`, else `$XDG_CONFIG_HOME/agentd/desktop`, else
`~/.config/agentd/desktop`:

| File | Content |
|---|---|
| `hosts.json` | Saved hosts (0600, atomic writes) |
| `known_hosts` | Host keys accepted on first use (`~/.ssh/known_hosts` is read too, never written) |
| `id_ed25519`, `id_ed25519.pub` | The app's own SSH key (0600), generated on first use |
| `relay_key` | The relay device key (X25519 private half, base64url, 0600), generated on the first pairing; one per install |
| `settings.json` | UI preferences: notifications, muted sessions, theme, agents grouping, Panel view (0600; `app.DefaultPrefsPath`) |

## Tests

```sh
GOTOOLCHAIN=go1.27.3 go test -race ./internal/desktopui/...
```

- Headless UI with `ui.NewTester` through `app/apptest` (`apptest.New(t, path, w, h)`, `Click`, `Fill`,
  `Has`, `Settle`, `Eventually`) on `backend/fake`; goroutine updates land through
  `app.QueueDispatcher`.
- Terminal tests call `apptest.UseGhosttyVT(t)` and skip without a fetched library
  (`make desktop-natives-host`).
- SSH against `remote/sshtest` (runs only `sh`, `cat`, `stty`...), zellij/tmux and provisioning against
  `mux/muxtest`, the daemon against a fake socket in `core`. Never a real agent CLI, never a real host.

## Development aids

| Variable | Effect |
|---|---|
| `AGENTD_DESKTOP_FAKE=1` | Run on `backend/fake` (mockup data, settings kept in memory, nothing contacted); `make desktop-run-fake` |
| `AGENTD_DESKTOP_PATH=/hosts` | Route opened at start (default `/agents`) |
| `AGENTD_DESKTOP_SIZE=1600x900` | Initial window size |
| `AGENTD_DESKTOP_CAPTURE=<dir>` | Write `<dir>/capture-<n>s.png` with MyGo's renderer at the seconds in `AGENTD_DESKTOP_CAPTURE_AT` (default `6`); works where compositor screenshots are refused |
| `AGENTD_DESKTOP_CAPTURE_PATHS=/agents,/hosts,/settings` | With `AGENTD_DESKTOP_CAPTURE`: visit each route in turn (`AGENTD_DESKTOP_CAPTURE_STEP` seconds apart, default 3) and write `capture-<route slug>.png` |
| `AGENTD_DESKTOP_DIR=<dir>` | State directory (hosts, keys, known hosts) |
| `MYGO_GHOSTTY_VT=<path>` | Use this libghostty-vt instead of the embedded one |
| `MYGO_FRAME_STATS=all` | MyGo frame timings in the log |

## Natives (libghostty-vt)

The terminal (MyGo `plugins/terminal`) loads libghostty-vt with purego. The Makefile embeds it:

```sh
make desktop-natives-host                # host platform (make desktop does this first)
make desktop-natives-linux-arm64         # one target: linux|darwin|windows - amd64|arm64
make desktop-natives                     # every target
make desktop-natives-clean               # remove fetched files (keeps .keep)
```

`scripts/fetch-ghostty-vt <goos>-<goarch> <dir>` reads URL, file name and SHA-256 from
`plugins/terminal/mygo-plugin.json` of the MyGo version in `go.mod`, verifies the download, caches it
(`$AGENTD_NATIVES_CACHE`, default `<user cache>/agentd-build/natives/<sha256>/`) and copies it into
`natives/<goos>-<goarch>/`. Fetched files are gitignored. On darwin the dylib is signed there with the
Developer ID (`scripts/codesign-macos`, hardened runtime, timestamp) before `go build`, so the
hardened-runtime binary loads it without `disable-library-validation`: `NATIVES_SIGN=auto` (sign when
`SIGNING_IDENTITY`/`MACOS_SIGN_IDENTITY` is set), `required` (release builds) or `off`. A binary built
without the library falls back to MyGo's lookup (executable dir, download into the user cache).
