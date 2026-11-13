# AGED-US-0028: Hosts and pairing in the app

Plan: [[plans/api-gateway-plan]]. Design: [[design/api/frontend-app]] sections 1.2, 3, 5, 6.3. Builds on
[[AGED-US-0027-flutter-skeleton]] and the server side [[AGED-US-0022-qr-pairing]].

## What changed

- Host model and `HostStore`: metadata in `hosts.json` (app support dir, atomic temp + rename, serialized writes),
  token only in a `SecretStore` under `token/<host_id>`. The token is written first, so a refusing store leaves no
  record. `FlutterSecretStore` (flutter_secure_storage, macOS `usesDataProtectionKeychain: false`) maps platform
  errors to `SecretStoreUnavailable` with an explanatory message (Linux: start gnome-keyring/KWallet/KeePassXC);
  there is no plaintext fallback. `MemorySecretStore` for tests.
- Pairing: `PairLink.parse` / `PairLink.fromFields` (v=1 only, https required except loopback/10.0.2.2, no
  credentials/path/query in `u`, 12-symbol code normalized with Crockford look-alikes, `fp` optional and validated;
  absent `fp` means system trust). `PairingController` state machine (`idle`, `waiting`, `success`, `failed`,
  `cancelled`) over a `PairingGateway` seam; success saves host + token, runs `health` + `info` (failure is only a
  warning), uses the `info.host` as hostname/label. Errors map to friendly messages (denied, expired, invalid,
  rate limited with Retry-After, pin mismatch, network, secret store unavailable). Cancel aborts the request.
  Re-pair keeps the host id/label/addedAt and replaces device + token.
- UI: pairing screen (paste link or URL + code + optional fingerprint, confirm dialog showing host, URL and
  fingerprint before any network call, "Waiting for approval on <h>..." with Cancel), QR scan screen
  (`mobile_scanner`, QR only, shown only when `Platform.isAndroid`), hosts screen (status per host from periodic
  `health` + `info` with backoff, rename, remove with the `agentd serve devices revoke <device id>` command and a
  copy button, re-pair), placeholders for the agent list and session route, light/dark themes, semantics labels.
- Routing with `go_router`: `/`, `/hosts`, `/pair?link=&rehost=`, `/hosts/:id/sessions/:name`. Deep links via
  `app_links` (`DeepLinkSource`): `agentd://pair?...` opens the prefilled pairing form (never auto-submits),
  `agentd://open?h=&s=` resolves the host by id, label or hostname (case-insensitive) and opens the session route;
  unknown host or bad link shows a snackbar. Cold-start and running-app links both handled.
- Android manifest: `CAMERA` (camera feature not required), `INTERNET` (was only in debug/profile), VIEW intent
  filter for scheme `agentd`.

## Files and symbols

- `app/lib/repositories/{host,host_store,pair_link,pairing_gateway}.dart`
- `app/lib/platform/{secret_store,device_info,deep_links}.dart`
- `app/lib/state/{providers,pairing_controller,deep_link_router}.dart`, `app/lib/state/README.md` (provider docs)
- `app/lib/ui/{app_shell,hosts_screen,pairing_screen,pairing_scan_screen}.dart`, `app/lib/main.dart`
- `app/android/app/src/main/AndroidManifest.xml`, `app/pubspec.yaml` (+ `go_router`, `flutter_secure_storage`,
  `path_provider`, `mobile_scanner`, `app_links`), `app/pubspec.lock`.
- Providers: `hostsProvider`, `hostClientProvider(hostId)`, `hostStatusProvider(hostId)`,
  `pairingControllerProvider`, plus seams `hostStoreProvider`, `secretStoreProvider`, `appSupportDirProvider`,
  `gatewayFactoryProvider`, `hostProbeProvider`, `healthIntervalProvider`, `deepLinkSourceProvider`.
- Tests: `app/test/repositories/{pair_link,host_store}_test.dart`, `app/test/state/{pairing_controller,
  deep_link_router}_test.dart`, `app/test/ui/{hosts_screen,pairing_screen,deep_link_widget}_test.dart`,
  `app/test/support/fakes.dart`.

## Why

F2 of the Flutter stories: the user can pair the app with a gateway and keep several hosts, with the token in OS
secure storage and the pairing payload treated as untrusted input.

## Verification

In `app/`: `dart format --output=none --set-exit-if-changed .`, `flutter analyze` (no issues), `flutter test`
(127 tests including the 68 of the API layer), `flutter build linux --debug`, `flutter build apk --debug` all pass.
Tests cover URL parsing (valid, 18 invalid/edge cases, round trip), HostStore with the fake secret store (token
never in `hosts.json` or any file in the dir; refusing store stores nothing; corrupt file), the pairing state
machine (success, health failure warning, denied, expired, invalid, rate limited, network, pin mismatch, cancel
with late result, secret store refusal, re-pair), hosts list (online/offline, rename, remove with device id), pairing
form (paste, prefill, validation, confirm, waiting, cancel, success navigation) and deep link routing.

## Deviations and notes

- The local Linux build needed `libsecret-1-dev`, not installed here (no sudo): verified by extracting the .deb to a
  scratch dir and pointing `PKG_CONFIG_PATH`/`CPATH` at it. CI already installs `libsecret-1-dev`.
- The gateway has no pending-token 403 phase (pairing is one long poll), so the app waits inside `pair()`.
- Not done here: the real Linux/Windows scheme registration and single-instance forwarding (installer story F7);
  QR scanning and camera permission were only built, not run on a device; the real Secret Service was not exercised
  (tests use the in-memory store).
- `SessionPlaceholder` / `AgentsPlaceholder` are replaced by AGED-US-0029/0030.
