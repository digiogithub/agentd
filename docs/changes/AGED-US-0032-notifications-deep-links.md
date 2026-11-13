# AGED-US-0032 Notifications and deep links (Flutter app)

Builds on [[design/api/frontend-app]] section 6, [[design/api/security-remote]] section 6,
[[AGED-US-0025-notifications]] (gateway ntfy/webhook) and [[app-review-fixes]] (deep-link dedupe, host by id).

## What changed
- In-app attention notifications (desktop; Android while running): when `agentsProvider` shows attention
  rising (waiting / turn_done / bell) or a session exit, a local notification `<session> on <host>` with a
  short reason. No screen content unless the "Include preview" setting (default off) is on.
- Rules (`NotificationPolicy`): global mute, per-host mute, no notification for the session open and visible
  (app foreground), 30 s debounce per session + reason, the first loaded snapshot of a host is a baseline
  (no storm at start or reconnect).
- Tap (or launch notification, or tray entry) emits `agentd://open?h=<host id>&s=<session>` through
  `notificationTapsProvider` into `DeepLinkListener`: same routing, dedupe and ambiguity handling as deep links.
- Desktop tray (`tray_manager`): generated icon with a red count badge, tooltip, menu `Open agentd`, attention
  sessions (click opens), `Mute/Unmute notifications`, `Quit`. Closing the window hides it
  (`window_manager`, setting "Keep running in the tray", default on) when a tray is supported; otherwise quits.
- Settings screen `/settings` (overflow menu in the agents app bar): mute, include preview, per-host switches,
  tray switch (desktop), Android permission tile with rationale dialog (`POST_NOTIFICATIONS`, requested only
  after the user taps Allow), disabled "Keep connected" foreground service ("coming later"), ntfy help: per host
  the `[api.notify]` snippet (`ntfy_url`, `on`, `ntfy_token_env`) with copy, and "Open ntfy app"
  (`ntfy://subscribe` via url_launcher; snackbar when not installed). No server test endpoint exists.
- `AgentEntry.attentionReason`: the agents runner now remembers the `reason` of attention events.

## Files and symbols
- `app/lib/state/notifications_model.dart` (pure): `NotifySettings`, `AttentionTracker`, `NotificationPolicy`,
  `AttentionEvent`, `NotificationRequest`, `TrayModel`, `sessionKeyOfLocation`, `notificationPayload`.
- `app/lib/state/notifications.dart`: providers, `NotificationController`, `NotifySettingsStore`.
- `app/lib/platform/notifications_platform.dart`: `LocalNotifier`, `TrayService`, `WindowService`, `LinkOpener`
  and plugin implementations (no-ops under `flutter test`).
- `app/lib/ui/notifications_host.dart`, `app/lib/ui/settings/settings_screen.dart`, `ui/app_shell.dart`
  (route, host widget, tap subscription), `ui/agents/agents_screen.dart` (overflow menu).
- `app/lib/state/agents.dart`, `agents_model.dart`: attention reason. `app/lib/state/README.md`.
- `app/android/app/src/main/AndroidManifest.xml`: `POST_NOTIFICATIONS`.
- Deps: `flutter_local_notifications ^23.0.2`, `tray_manager ^0.9.0`, `window_manager ^0.7.0`, `url_launcher ^6.3.3`.

## Behaviour notes / limits
- Settings are stored in `notifications.json` (app support dir), not shared_preferences (no new dependency).
- Android background alerts rely on ntfy; the foreground service is deferred (setting shown disabled).
- Linux tray needs a StatusNotifierItem host (GNOME: AppIndicator extension). Without tray support the window
  just closes (quit). tray_manager/window_manager legacy (0.5-compatible) API is used behind interfaces.
- Windows toasts use a fixed AUMID/GUID; unpackaged apps may need an MSIX/shortcut for activation (see design 6.1).
- Not verified on real desktops/devices here (no GUI session); plugin calls are best effort and logged.

## Verification
`dart format --output=none --set-exit-if-changed .`, `flutter analyze`, `flutter test`, `flutter build linux
--debug`, `flutter build apk --debug` (results in the task report). New tests: `test/state/notifications_model_test.dart`
(tracker, debounce, mute, visible suppression, no preview by default, settings json, tray model),
`test/state/notifications_controller_test.dart` (end to end with fake feeds and plugins, persistence, tray
actions, hide on close, launch payload), `test/ui/settings_screen_test.dart` (screen, rationale flow, ntfy
snippet, tap routing).
