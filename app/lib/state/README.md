# state/ providers

Riverpod layer between `ui/` and `repositories/`. Later stories build on these.

| Provider | Type | Purpose |
|---|---|---|
| `hostsProvider` | `AsyncNotifierProvider<HostsNotifier, List<Host>>` | Paired hosts (metadata from `hosts.json`). `add(host, token)`, `rename`, `remove`, `markOk`. |
| `hostClientProvider(hostId)` | `FutureProvider.family<AgentdClient, String>` | Client with base URL, SPKI pin and token (read from the secret store, never held in `Host`). Closed on dispose. Throws `ApiException` `no_token` (401) when the token is gone: show "re-pair". |
| `hostStatusProvider(hostId)` | `StreamProvider.autoDispose.family<HostStatus, String>` | Periodic `health` + `info` probe: `checking`, `online`, `offline`, `authError`, `pinMismatch`. 15 s while online, backoff up to 8x while failing. |
| `pairingControllerProvider` | `NotifierProvider.autoDispose<PairingController, PairingState>` | Pairing state machine: `idle`, `waiting`, `success`, `failed`, `cancelled`; `start(link, replaceHostId:)`, `cancel()`, `reset()`. |
| `hostStoreProvider`, `secretStoreProvider`, `appSupportDirProvider` | | Persistence seams. |
| `gatewayFactoryProvider`, `hostProbeProvider`, `healthIntervalProvider`, `deepLinkSourceProvider` | | Test seams (override with fakes). |

Rules: the token never leaves `SecretStore` except inside `hostClientProvider`; `Host` and
`hosts.json` hold no secret. Deep links resolve through `routeForDeepLink` (`deep_link_router.dart`).

Agent list (`agents.dart`, `agents_model.dart`, AGED-US-0029):

| Provider | Purpose |
|---|---|
| `agentsProvider` | `NotifierProvider<AgentsNotifier, AgentsState>`: sorted `entries` (`AgentEntry`: host, `SessionSummary`, `hostOnline`, local `attention`) and per-host `HostFeedInfo` (online, loaded, error, authError, lastSeen). `refresh()`, `reconnectAll()`, `markSeen(hostId, name)`. Per host: stream first, then `GET /v1/sessions`, events buffered during the fetch; `resync` or a `hello` with a new epoch refetch (replace, no merge); `view.changed` triggers a throttled refetch to keep previews fresh. |
| `hostFeedFactoryProvider` | Seam: `HostFeed` (list + events + states) per host. Default builds its own `AgentdClient` (not `hostClientProvider`, whose client is closed on every hosts-list change). |
| `reconnectTriggerProvider` | Seam: app resume (`AppLifecycleListener`) + `connectivity_plus` (debounced 500 ms). |
| `agentsRetryProvider`, `agentsPreviewRefreshProvider` | Timing seams. |

Routes (`ui/app_shell.dart`): `/` agents list (`ui/agents/agents_screen.dart`), `/hosts`, `/pair?link=&rehost=`,
`/hosts/:id/sessions/:name` placeholder (AGED-US-0030).

## Session view (AGED-US-0030, `session_view.dart`)

| Provider | Purpose |
|---|---|
| `sessionViewProvider(sessionKey(hostId, name))` | `NotifierProvider.autoDispose.family`: live `SessionViewState` (view, loading, error, reconnecting, acting, feedback, canRetry, reviewAgain) from `view/stream` (full + patches; refetch on `needsRefetch`/`resync`/`bye`). Methods: `prompt`, `choose`, `keys`, `interrupt`, `cancelMenu`, `retry`, `dismissFeedback`, `reconnect()` (resume), `setVisible()` (debounced `view/ack`). |
| `sessionGatewayProvider(key)` | Test seam (`SessionGateway`): stream, fetch, act, ack. |

## Launch, session actions, mailbox (AGED-US-0031)

| Provider | Purpose |
|---|---|
| `hostInfoProvider(hostId)` | `GET /v1/info` of a host (aliases, harnesses, scopes). |
| `launchControllerProvider(hostId)` | Launch state machine: `idle`, `launching`, `success`, `failed`, `outcomeUnknown`; `launch(RunRequest, expectedName:)`, `checkOutcome()`. One `Idempotency-Key` per payload, reused on retry. |
| `sessionActionsProvider` | `SessionActions.kill(hostId, name, signal:)` / `.remove(hostId, name, force:)` returning `ActionOk` / `ActionFailed`. UI: `ui/launch/session_actions_menu.dart` (`SessionActionsMenu`, `showKillDialog`, `showRemoveDialog`). |
| `mailboxFeedProvider` | Unified human inbox of all hosts, newest first, per-host `next_before` paging: `refresh()`, `loadMore()`. `mailboxUnreadProvider` = unread badge. |
| `mailboxActionsProvider` | `markRead`, `reply`, `send` with stable idempotency keys; auto-disposed (compose and message screens watch it), `send(fresh: true)` = "Send again (new message)". |
| `messageProvider`, `recipientsProvider`, `peekProvider` | Detail, recipient picker, admin-only peek. |

Routes: `/hosts/:id/launch`, `/mailbox`, `/mailbox/compose?host=&to=`, `/hosts/:id/mailbox/:msgId`.

## Notifications, tray, settings (AGED-US-0032, `notifications*.dart`)

| Provider | Purpose |
|---|---|
| `notificationControllerProvider` | Started by `ui/notifications_host.dart`. Feeds each `agentsProvider` snapshot to `AttentionTracker` (attention rising or exit; the first loaded snapshot of a host is a baseline), then `NotificationPolicy.decide` (global/per-host mute, open+visible session, 30 s debounce per session+reason) and shows a local notification. Also drives the desktop tray and hide-on-close. |
| `notificationTapsProvider` | `Stream<Uri>` of `agentd://open?h=<host id>&s=<session>` from notification taps, the launch notification and tray entries; consumed by `DeepLinkListener` (same routing and dedupe as deep links). |
| `notifySettingsProvider` | `NotifySettings` (`muted`, `mutedHosts`, `includePreview` off, `keepInTray` on, `foregroundService` off/deferred), persisted in `notifications.json` (`notifySettingsStoreProvider`). |
| `openSessionProvider`, `appVisibleProvider` | Open session key and foreground flag (set by `NotificationsHost`). |
| `trayModelProvider` | `TrayModel` (attention count, menu items). |
| `notifyPermissionProvider` | Android 13+ permission state; `request()` after the rationale dialog. |
| `localNotifierProvider`, `trayServiceProvider`, `windowServiceProvider`, `linkOpenerProvider`, `isDesktopProvider`, `isAndroidProvider`, `notifyDebounceProvider` | Plugin seams (`platform/notifications_platform.dart`); real impls are no-ops under `flutter test`. |

Route: `/settings` (overflow menu of the agents screen).
