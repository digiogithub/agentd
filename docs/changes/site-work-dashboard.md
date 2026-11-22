# Marketing site: work dashboard section

Part of [[plans/agentd/dashboard_plan]]. Builds on [[changes/AGED-US-0039-task-list]],
[[changes/AGED-US-0040-questions]], [[changes/AGED-US-0041-board]], [[changes/AGED-US-0043-task-sync]],
[[changes/AGED-US-0044-notify-sinks]] and [[changes/AGED-US-0045-desktop-panel]]. Date: 2026-11-22.
(KB unavailable: recorded here.)

## What

- New `#dashboard` section ("Work dashboard" / "Panel de trabajo") in `pages/index.html` and `pages/es/index.html`,
  between Control and Apps, with a nav link: three cards (task lists, questions, notifications), an
  `agentd ask` / `agentd board` snippet and five screenshots (desktop Panel by agent and board; Android Panel tab,
  answer sheet, agent tasks).
- Apps section: a Panel bullet for the desktop and Android apps. MCP section: ten tools (adds `agentd_ask`,
  `agentd_tasks_set`, `agentd_task_update`).
- Native push is not claimed: phone alerts are described as ntfy / signed webhook from the daemon.
- First raster images of the site: `pages/assets/shots/*.webp`, framed as taped photographs (`.shots`, `.shot` in
  `pages/assets/site.css`). `pages/README.md` lists them.

## Screenshots

Renders of the approved design artboards (Main, DesktopTablero, MovilPanel, MovilRespuesta, MovilAgente; MovilAviso
is left out because it shows push actions that are not shipped). The artboard files were rendered standalone
(their `sc-for` / `sc-if` / `{{expr}}` templates expanded with each file's own `renderVals()` data) in headless
Chrome at 2x, then saved as webp quality 84: desktop 1600x1012 (72-96 KB), phone 780x1688 (58-65 KB). The UI in
them is Spanish; both languages use the same files.

## Why

agentd v0.8.0 and agentd-app 1.2.0 shipped the work dashboard; the site did not mention it.

## Verified

Served with `python3 -m http.server -d pages` and driven in headless Chrome over CDP at 1366, 800 and 390 px, both
languages: no console messages, no failed requests, no horizontal overflow, the five images load, every `.reveal`
gets `in`. Section screenshots checked by eye at desktop and phone widths.
