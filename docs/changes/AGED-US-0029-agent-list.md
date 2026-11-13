# AGED-US-0029: Agent list across hosts

Plan: [[plans/api-gateway-plan]]. Design: [[design/api/frontend-app]] sections 1.2, 2.1, 4.3, 4.4. Builds on
[[AGED-US-0027-flutter-skeleton]] and [[AGED-US-0028-hosts-pairing]].

## What changed

- `agentsProvider` merges every paired host into one sorted list. Per host: `/v1/events` stream opened first, then
  `GET /v1/sessions` (events arriving during the fetch are buffered and applied after it, so nothing is lost);
  applied events: `session.created/started/exited/removed`, `status.changed`, `attention.changed`, `input.changed`,
  `view.changed` (revision + throttled list refetch for the preview line); `resync` and a `hello` with a new epoch
  refetch and replace. State is keyed by session name, so reconnects never duplicate rows.
- Offline host: stream `reconnecting` or a failed list marks it offline; its sessions stay, greyed, "host offline"
  with a banner (last seen, error; auth errors point at Hosts). Failed first list is retried; auth errors are not.
- Reconnect on app resume and connectivity change (`reconnectTriggerProvider`, `connectivity_plus`, 500 ms
  debounce), plus pull to refresh: every host is refetched and its stream restarted.
- Attention is cleared client-locally when the row is opened (`markSeen`) and raised again by the daemon.
- UI (`app/lib/ui/agents/`): grouping-free list with host and project filter chips (project = part after `@`, else
  cwd basename), rows with attention dot, name, `host · project · relative time`, one-line preview, status badge
  (colour + icon + text, semantics label per row), sort attention > waiting > working > idle > starting > exited,
  then recent; empty states (no hosts: Pair a host; no sessions: Launch an agent -> `/hosts/:id/launch`, with a host
  picker when several); tap -> `/hosts/:id/sessions/:name`. `/` in `app_shell.dart` now builds `AgentsScreen`.

## Files

`app/lib/state/{agents,agents_model}.dart`, `app/lib/state/README.md`, `app/lib/ui/agents/{agents_screen,agent_row,status_badge}.dart`,
`app/lib/ui/app_shell.dart` (route body, placeholder removed), `app/pubspec.yaml`/`pubspec.lock` (+ `connectivity_plus`).
Tests: `app/test/state/agents_test.dart`, `app/test/ui/agents_screen_test.dart`, `app/test/support/agents_fakes.dart`.

## Verification

In `app/`: `dart format --output=none --set-exit-if-changed .`, `flutter analyze`, `flutter test`,
`flutter build linux --debug`, `flutter build apk --debug` (see the story comment for the results).

## Deviations and notes

- The default feed builds its own `AgentdClient` instead of `hostClientProvider`: that provider watches the hosts
  list and closes its client on every change (rename, `lastOk` every 5 min), which would cut the stream.
- Resume/refresh restarts the stream without `Last-Event-ID` (`SseClient` keeps the id privately) and refetches the
  list instead; correctness is the same, only the replay optimisation is skipped.
- No host grouping headers: filters only. The `hostStatusProvider` probe is not used; offline comes from the stream.
