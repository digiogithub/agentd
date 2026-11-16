# Plan: projects, terminals, terminate, folder browser (desktop + mobile)

Date: 2026-11-16. Asked by José. Applies to agentd-desktop (`internal/desktopui`) and the Flutter app
(`/www/MCP/agentd-app`), same behaviour and Spanish copy in both. Client-only: no daemon change.
Builds on [[desktop_plan]].

## 1. Agents grouped by project

- Agents view has a segmented toggle `Proyecto | Estado` (default `Proyecto`, persisted in the local
  settings: desktop `settings.json`, mobile `AppSettings`). `Estado` = the current grouping.
- Group key = host + project directory. agentd: `SessionInfo.cwd`; zellij/tmux/shell: the cwd/project the
  adapter knows, else `Project`. Sessions with none go to `Sin proyecto` (per host).
- Group header: label (alias whose dir equals the path, else the path with `~` for the home, e.g.
  `~/src/api`), host name when there is more than one host, session count, attention badge, and a `+`
  button that opens New session with host + project prefilled.
- Inside a group: needs-answer first, then working, then the rest by last update. Groups ordered by
  attention, then most recent activity.

## 2. New session: agent or terminal only

- First choice `Abrir`: `Agente` | `Solo terminal`. `Solo terminal` hides the agent list.
- Where: `agentd` | `zellij` | `tmux` | `SSH` (plain login shell), allowed for both kinds.
- Terminal only = the user's login shell started in the project directory:
  - agentd: `run {target: <dir>, harness: "shell", cmd: ["sh","-c","exec \"${SHELL:-/bin/sh}\" -l"]}`
    (name `shell@<label>`; profile `default` if `shell` is not a profile).
  - zellij / tmux: new session/window in the dir with no command (the mux's default shell).
  - SSH: login shell, `cd` to the dir first.
- Project field: alias chips + free text + `Explorar…` (folder browser below). Value = alias or
  absolute path.

## 3. Folder browser (remote, also local host)

- Backend op `ListDir(host, path)` = one POSIX sh script over the host shell (exec channel; locally the
  local Shell). `path` empty = `$HOME`. Output NUL-separated: absolute path (`cd -- "$p" && pwd`), home,
  then one record per directory entry `name\0flag` with flag `1` when `[ -r ] && [ -x ]` (enterable),
  `0` otherwise. Only directories (symlinks to directories included). Hidden ones only when asked.
  Sorted case-insensitively, capped at 2000 entries (+ truncated flag). `cd` failure = typed error
  (`Sin permiso` / `No existe`). Path passed single-quoted (existing quote helpers), never interpolated.
- UI: breadcrumb of the absolute path (every ancestor up to `/` clickable, so the user can leave the
  home), `Inicio (~)`, `Subir`, `Mostrar ocultas`, list of folders (non-enterable ones greyed with a
  lock, not clickable), `Usar esta carpeta`. Desktop: dialog; mobile: full screen route.

## 4. Terminate session (force close + delete)

- Session view header action `Terminar` (danger) and, in the agents list, a row action (desktop: hover
  button / context; mobile: long-press menu). Confirm dialog: "Forzar cierre y borrar <name>?" with
  the consequence (the process is killed, the session and its screen are removed).
- agentd: running/starting -> `kill {signal: "KILL"}`, wait until it is exited (poll `ls`, up to 5 s),
  then `rm`; already exited -> `rm`. zellij: `kill-session` then `delete-session --force`. tmux:
  `kill-session`. SSH shell: close the channel. Then back to the agents list.
- Desktop contract: new `Backend.Terminate(ctx, s)` (fake + core), AGENTS.md updated. Mobile:
  `SessionsRepository.terminate(s)` (real + fake).

## Phases

- [x] P1 desktop (all four) - gofmt, go vet, `go test -race ./internal/desktopui/...`, captures.
- [x] P1 mobile (all four) - `flutter analyze`, `flutter test`.
- [x] P2 review, commit, push (agentd v0.5.2, agentd-app v1.0.5).
