# Plan: agentd-desktop, native desktop client on MyGo

Date: 2026-11-15. Decisions by José: binary `cmd/agentd-desktop` in this module (reuses
`internal/client` and `internal/proto`); the Flutter app `/www/MCP/agentd-app` becomes mobile only.
Design (validated mockups, phone layout to adapt to a desktop window):
https://claude.ai/artifact/T3omboYBBsufo6zJHQpZdP. Reference implementation: Pando desktop
(`/www/MCP/Pando/pando/internal/nativeui`, README there), toolkit `github.com/egoist/mygo` v0.7.1
pinned, Go 1.27.3 (`GOTOOLCHAIN=go1.27.3`). Background: `/www/MCP/herdr-movil/ideas-agentd.md`.

## Architecture

- One window: sidebar (Agentes, Hosts, Buzón, Ajustes) + content; session view with Chat and
  Terminal tabs. Same screens as the mockups.
- Hosts: `local` (this computer: daemon socket via `client.Dial`) and SSH hosts
  (`golang.org/x/crypto/ssh`; ssh-agent, the app's own ed25519 key, key files; trust on first use;
  import from `~/.ssh/config`). agentd over SSH = `direct-streamlocal` to the daemon socket wrapped
  with `client.NewConn` (the remote daemon sees the SSH user's human identity).
- zellij / tmux: exec + PTY on the host. Provisioning: one detect script, agentd `install.sh`
  checked against `SHA256SUMS`, systemd user unit, `[herdr] listen`, MCP snippets, CLIs.
  Ported from the Flutter app's `lib/src/mux` and `lib/src/provision` (same commands, same tests).
- Terminal: MyGo `plugins/terminal` with libghostty-vt as in Pando (`natives`).

## Packages (`internal/desktopui/`)

Package guide (rules, adding an area, tests, dev env vars, natives): `internal/desktopui/README.md`.
Contracts: AGENTS.md "Package contracts" and "Desktop client".

| Package | Content | MyGo | Tests (top-level, P1) |
|---|---|---|---|
| `backend` | contract (Backend, Shell, PTY, Session, Host, provisioning types) | no | - |
| `backend/fake` | fake Backend with the mockup data; act bumps revisions, PTY echo, provisioning events | no | 5 |
| `remote` | Shell over SSH and local, `DialUnix`, app key, TOFU known hosts, hosts.json, ssh config import, pool; `remote/sshtest` in-process SSH server | no | 13 |
| `core` | real Backend: hosts, socket probe, session aggregation (agentd ls + subscribe, mux polling, shells), views, mailbox, act, attach, run, kill | no | 11 |
| `mux` | zellij and tmux adapters (port of agentd-app `lib/src/mux`); `mux/muxtest` fake Shell | no | 14 |
| `provision` | detect script, plan, install steps (port of agentd-app `lib/src/provision`) | no | 12 |
| `natives` | libghostty-vt embedded per platform, extracted at start | no | 6 |
| `kit` | palette, fonts, icons, components | yes | 3 |
| `shell` | MyGo app, window, menu, notifications | yes | - |
| `app` | Context, registry, router, layout, Live, Views, notifier, prefs; `app/apptest` | yes | 7 |
| `app/<area>` | agents 7, session 6, newsession 5, hosts 4, addhost 6, provision 3, mailbox 2, settings 2 | yes | 35 |
| root (`desktopui.go`, `capture.go`) | `Run`, dev aids | yes | 2 |

Verified 2026-11-15: `GOTOOLCHAIN=go1.27.3 go test -race -count=1 ./internal/desktopui/...` passes
(17 packages with tests, 108 top-level tests, 131 including subtests; also passes with no `DISPLAY`/
`WAYLAND_DISPLAY`). `go list -deps ./cmd/agentd | grep -c mygo` = 0. `cmd/agentd-desktop` cross-compiles
with `CGO_ENABLED=0` for linux/amd64, linux/arm64, darwin/amd64 and darwin/arm64.

State files: `$XDG_CONFIG_HOME/agentd/desktop/hosts.json` (0600), `known_hosts`, key
`id_ed25519` (0600), `settings.json` (0600; `$AGENTD_DESKTOP_DIR` overrides the directory). No secrets
in logs.

## Phases

- [x] P0: go 1.27.3, mygo v0.7.1, x/crypto; `client.NewConn`; `backend` contract; this plan.
- [x] P1 (parallel): A `remote` + `core`; B `mux` + `provision`; C UI (`kit`, `shell`, `app`,
  areas, `backend/fake`), plus `natives`. Each: gofmt, go vet, go test -race of its packages.
  - `remote`: SSH with ssh-agent / app key / key file / password callback, keepalives, PTY with
    window-change and signals, `direct-streamlocal`, TOFU (`HostKeyMismatch` on change), hosts.json,
    `~/.ssh/config` import, per-host pool; local Shell for this computer.
  - `core`: socket probe (`$AGENTD_SOCKET`, `$XDG_RUNTIME_DIR`, `/run/user/<uid>`, `/tmp/agentd-<uid>`),
    fresh connection per op, `ls` + `subscribe` aggregation, view streams, mailbox/queues, act with
    revision/epoch, attach, run, kill; tested against a fake daemon socket.
  - `mux` / `provision`: same commands and parsers as the Flutter layers, tested on `mux/muxtest`.
  - UI: sidebar (Agentes, Hosts, Buzón, Ajustes; Ctrl/Cmd+digit), agents inbox grouped by
    need-answer/working/done with inline approve/deny, session page (Chat from the semantic view,
    Terminal via MyGo `plugins/terminal`), new session, hosts, add host (ssh config import, manual form,
    app key, fingerprint confirmation), provision (plan + streamed install), mailbox (messages + queues),
    settings (notifications, mutes, app key, theme); desktop notifications (waiting, turn done at most
    every 30 s per session, exited; none when focused or muted; a click opens the session).
- [x] P2: `cmd/agentd-desktop` runs the real backend by default (`core` over `remote` + `mux` + `provision`,
  `AGENTD_DESKTOP_FAKE=1` for the fake); host-key and password prompts reach the UI as typed errors +
  dialogs (`TrustHostKey`, `ConnectWithPassword` with optional key install); inline approval views
  capped at 12; `go mod tidy`; whole repo `go test -race ./...` green; E2E under xvfb against a temp
  daemon. Also done in P2: AGENTS.md (Go 1.27.3, pinned deps,
  package contracts, "Desktop client" section), README "Desktop app", `Makefile` (`desktop`,
  `desktop-natives-*`, `desktop-run-fake`, darwin `NATIVES_SIGN`), `scripts/fetch-ghostty-vt`,
  `scripts/codesign-macos`, `internal/desktopui/README.md`, ROADMAP Phase 8; pending: release matrix
  (Linux amd64/arm64 tar.gz, macOS universal signed with the signed dylib), written as a TODO in
  `docs/release.md` until the real backend is wired.
- [ ] P3: field test against local daemon and one SSH host.

## Decisions taken

- One module, one repo: `cmd/agentd-desktop` reuses `internal/client` and `internal/proto`; the Flutter
  app (`agentd-app`) is mobile only.
- SSH is the only remote transport (no use of the frozen HTTP gateway); agentd over SSH =
  `direct-streamlocal` to the daemon socket + `client.NewConn(conn, nil)`, so the remote daemon sees
  `human:<ssh user>`; every op on a fresh connection.
- UI talks to hosts only through `backend.Backend`; only `kit`, `shell`, `app/...` and the root import
  MyGo, so `remote`/`core`/`mux`/`provision` stay testable without a GPU.
- libghostty-vt embedded per platform (go:embed), pins taken from MyGo's manifest, darwin dylib signed
  before `go build`; same scheme as Pando.
- Mutes and notification choices are local (`settings.json`), not stored on the host.
- UI copy in Spanish, as in the mockups; code, comments and docs in English.
- Pure Go build (`CGO_ENABLED=0`, purego), so release cross-compilation needs no C toolchain.

## Open gaps

- Host-side mutes: mutes live on this computer only; other clients (phone) do not see them. Needs a
  daemon label or doc convention.
- Devices and tokens (Ajustes > Seguridad): placeholder toast; the gateway's device tokens are frozen and
  the SSH model has no devices yet (what to list: authorized keys added by the apps?).
- Pairing link (Añadir host > "Pegar enlace"): placeholder toast; decide whether an `agentd://` link
  carries SSH coordinates + host-key fingerprint instead of gateway tokens.
- Image attach in the chat composer: not implemented (needs a way to put files on the host and reference
  them in a prompt).
- Dictation (mic button of the mockups): not implemented on desktop.
- Notification actions (approve/deny from the OS notification): MyGo notifications have no action
  buttons, so a click only opens the session on its question.
- Chat parsing heuristics: the Chat tab relies on the daemon's semantic view (roles, options, dialog);
  harness-specific transcript cleanup is not tuned against real Claude/Codex output yet.
- Field test (P3): only a temp local daemon under xvfb so far; no real SSH host or real harness yet.
- Release workflow (see `docs/release.md`, "Desktop client"): not added to `release.yml` yet.
