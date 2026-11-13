# Design: client app (Flutter, desktop + Android)

Status: **draft** (2026-11-13), rewritten for ADR-A18. Stories: F1..F8 of [[plans/api-gateway-plan]]
(AGED-US-0027..0034). Binding ADRs: A02 (API never resizes a PTY, reflowed lines), A03 (L1 primary,
L0 fallback, L2 deferred), A08 (remote via Tailscale), A09 (pairing), A11 (no transcript), A15
(token on every listener, `Authorization` header only), A16 (`first_line`, epoch, `features[]`),
**A18 (this app is Flutter, in `app/`, supersedes A10 and A12; no web UI in the gateway)**.
Siblings: [[design/api/http-api]], [[design/api/semantic-model]], [[design/api/daemon-ops]],
[[design/api/security-remote]].

Product reference: zeron.sh, one native control app for coding agents running on your own machines:
see every agent on every machine, follow from the phone, answer and approve. agentd's difference:
harness-agnostic (works from the terminal, not from agent APIs) and self-hosted with no relay.

Verification legend: package facts were checked on pub.dev on 2026-11-13; **(unverified)** marks
items from memory or inference to confirm in F1/F2 spikes.

## 1. Scope v1

### 1.1 Platforms

Linux, macOS, Windows and Android (iOS later). One Dart package `agentd_app` in `app/`. The gateway
serves no UI: real-harness validation (F8) uses the desktop build.

### 1.2 Screens

1. **Hosts**: one row per paired gateway (label, online / offline / auth error / pin mismatch /
   version mismatch, counts by status, attention). Pair (QR on Android, code entry everywhere),
   rename, remove (forgets the token locally; the app says to also run `agentd serve devices revoke`).
2. **Agents** (home): all sessions of all reachable hosts, flat or grouped by host, sorted attention,
   `waiting`, `working`, `idle`, `starting`, `exited`. Row: name, host, status badge, age, one-line
   summary (detected question when waiting, else the `ls` preview), unread-mail dot. Filters: host,
   harness, status. Live via `/v1/events`.
3. **Session view**: reflowed text (`View.lines`, ADR-A02; monospace, selectable, follows the tail,
   "jump to bottom" chip; SGR attributes from spans, no terminal emulation); **option buttons**
   (`View.options`, low confidence styled "suggested", tap = `act choose` with revision + id);
   **composer** (local draft per session, never streamed to the PTY while typing, Send = `act
   prompt`; `View.input` shown above it); **quick keys** (`Esc`, `Interrupt` with second-tap confirm
   while `working`, `Enter`, `Tab`, `↑ ↓`, `y`/`n`; `act keys`/`act interrupt`); header menu: Raw
   screen, Mail, Kill/Remove (`write`, confirmation). **Raw screen** (L0, read-only): `GET
   …/terminal` snapshot with a small ANSI-SGR parser in a horizontally scrollable monospace view;
   debug fallback, not live.
4. **Launch agent** (`write`): host, alias and harness pickers from `GET /v1/info`, optional name
   (default `<harness>@<label>`); no free-form command/env even for `admin`
   ([[design/api/security-remote]] §5.4). Opens the new session (`starting`).
5. **Mailbox**: `human:<user>` inbox per host, detail + reply, compose, per-session peek (`as`).
6. **Settings**: device info (scopes), ntfy topic as text and QR, theme, font size, versions.

### 1.3 Not in v1

Terminal emulation, live L0 stream, any PTY resize (ADR-A02); transcript/adapters (A11); file
browser, diffs, git, editor; own relay, APNs/FCM; multi-user hosts; arbitrary command/env in Launch,
editing config/aliases/profiles; iOS and web builds; pane/split layouts (AGENTS.md; wide windows may
show list + session side by side, which is navigation, not tiling).

## 2. UX state mapping

### 2.1 `View.status` and `attention`

Status values from [[design/api/semantic-model]] §5.

| `status` | Badge | List order | Composer | Option buttons | Notes |
|---|---|---|---|---|---|
| `starting` | grey, pulsing | 5 | disabled | hidden | Becomes `working`/`idle` at the first view |
| `working` | blue spinner | 3 | enabled, hint "agent is busy, text is typed into it now" | hidden | Interrupt prominent |
| `idle` | green | 4 | enabled, focused on open | shown if any | "Done" toast if it was `working` while open |
| `waiting` | amber "needs you" | 2 | enabled | **prominent** | Agent asked a question / shows a menu |
| `exited` | red "exited N" / grey "exited 0" | 6 | disabled | hidden | Final screen viewable; Remove (`write`) |
| unknown value | grey, raw text | 6 | disabled | hidden | Forward compatibility (§4.5) |

`attention = true` adds a red dot on row and host, an app badge (tray/launcher where supported), and
sorts the row first. It clears when the user opens the session (client-local "seen"; see §9 Q1).
`View.input_state`: `draft` shows "Someone is typing in a terminal attached to this session";
`suggestion` shows the ghost text in faint italics with a "use" button that copies it into the
composer (never submits).

### 2.2 Busy and conflicts (409)

Writes can be refused to protect a human's typing or the wrong question ([[design/api/daemon-ops]] §3).
Codes as in [[design/api/http-api]] §2.2:

| Code (HTTP) | Cause | App behaviour |
|---|---|---|
| `human_draft` / `busy` (409) | A local terminal has an unsubmitted draft (< 10 min) | Keep the app draft; banner "Someone is typing on the host — try again when they finish"; manual Send (new key after an edit); explicit Retry (prompt only, same key and text, within 30 s). No auto-retry |
| `stale_revision` / `option_mismatch` / `conflict` (409), `precondition_failed` (412) | The interaction state moved since the view the user saw | Replace the view with the fresh one, "review again", never auto-resubmit |
| `in_progress` (409) | The same `Idempotency-Key` is still being processed | "Still processing"; a prompt may be retried later with the same key |
| unknown 409 code | Anything else | Generic message with the code; no review, no retry |
| `exited` (409) | Exited between view and act | Switch to exited state |
| `starting` (409, retryable) | Not running yet | Wait for the next event, keep draft |
| `paste_unverified` (503, retryable) | Pasted text never appeared; nothing submitted | Error snackbar with Retry; draft stays |

Rules: the app drives agents' terminals, so it never sends an action against a state the user did not
see and never sends twice. Every `act` (`prompt`, `keys`, `interrupt`, `cancel`, `choose`) carries the
`revision` + `epoch` of the view DISPLAYED at tap time (the daemon compares the interaction state, not
the transcript, [[design/api/daemon-ops]] §3.1). A confirm dialog (interrupt, low-confidence option)
captures the view at tap time and sends nothing if the revision changed meanwhile ("review again").
There is no automatic retry. The only repeat is an explicit "Retry" of a refused PROMPT with the same
untouched text and `Idempotency-Key` within 30 s; keys, interrupt, cancel and choose are never retried
(the user taps again on a fresh view). A prompt that succeeded clears the composer; on
`outcome_unknown` the text moves into a "Sent? result unknown" note with an explicit "Send again" (new
key), so a primed Send never holds the same text. Two app clients race like two humans.

See also [[changes/app-review-fixes]].

## 3. Architecture

### 3.1 Layers

```
lib/
  ui/            widgets, screens, navigation (go_router), theme; no networking, no JSON
  state/         Riverpod providers: hosts, sessions, view, mail, notifications
  repositories/  HostRepository, SessionRepository, ViewRepository, MailRepository: merge REST + SSE
                 into domain streams, own caching and offline "last seen" state
  api/           AgentdApi (typed REST), SseClient, HostConnection (pinned HttpClient + token),
                 models (hand-written, §7), errors (ApiException with code)
  platform/      SecretStore, HostStore, Notifier, DeepLinks, Lifecycle/Connectivity: thin interfaces
                 with per-OS implementations, faked in tests
```

Dependencies point downward only. `ui` never imports `api`; `api` never imports Flutter (pure Dart,
so it is testable with `dart test` against a local stub server and reusable by a future CLI tool).

### 3.2 State management: Riverpod

Recommendation: **`flutter_riverpod`** (v3.x, 3.5.0 at time of writing), plain `Notifier` /
`AsyncNotifier` / `StreamProvider`; `riverpod_generator` optional, off by default **(unverified
stability; decide in F1)**. SSE maps to stream providers (`ref.onDispose` closes the connection),
providers are overridable with fakes in tests, families express "view of session X on host Y".
Rejected: `bloc` (ceremony per stream), `provider` (weaker async), signals (smaller ecosystem). Rules:

- One `HostConnection` provider per host id; it owns the pinned `HttpClient`, the token (read once
  from `SecretStore`, never in provider state that is printed or serialised) and the events stream.
- Session list state = merge of per-host snapshots + events; the Agents screen watches one derived
  provider. The open session's `view/stream` is a family provider autodisposed when the screen closes.
- Draft text lives in a `StateProvider`-style notifier keyed by (host, session); survives navigation.

## 4. Networking

### 4.1 HTTP in Dart

`dart:io` `HttpClient` directly, wrapped by `HostConnection`, on desktop and Android alike. Reason:
TLS pinning needs `badCertificateCallback`/`SecurityContext` (§4.2), which the `dart:io` client
exposes and which `package:http`'s `IOClient` can wrap (`IOClient(HttpClient)`) **(unverified for
current `http` major)** but `dio` hides behind adapters. Using one client for REST and SSE keeps
pinning in one place. Rules: `Authorization: Bearer <token>` header on every request (never query
string, never logged: a redacting `toString` on the token type and no request-header logging);
`Content-Type: application/json` on writes; `Idempotency-Key` on `act`/`POST sessions`; connection
timeout 10 s, request timeout 30 s; JSON decoding in a background isolate (`compute`) only for
payloads above ~64 KiB (full views); `Accept-Encoding: gzip` is automatic.

### 4.2 TLS pinning (SPKI)

Pairing QR carries `fp=sha256:<hex of SPKI>` in self-signed mode
([[design/api/security-remote]] §3.2/§4.1). Approach:

```dart
HttpClient pinned(Uint8List spkiSha256) {
  final ctx = SecurityContext(withTrustedRoots: false);   // nothing is trusted by default...
  final c = HttpClient(context: ctx)
    ..badCertificateCallback = (cert, host, port) =>       // ...so every handshake reaches the callback
        constantTimeEq(sha256(spkiOf(cert.der)), spkiSha256);
  return c;
}
```

- With `withTrustedRoots: false` every certificate reaches `badCertificateCallback`, so the pin is
  the only trust anchor (no "valid CA cert, other key" bypass). `cert.der` is the leaf DER; the SPKI
  comes from a minimal ASN.1 walk (hand-written ~40 lines, tested against the gateway's real
  certificate; or `asn1lib`, **unverified**).
- Hosts with publicly trusted certs (Tailscale `tailscale serve`, no `fp`): plain
  `HttpClient()` with the system roots, no callback; `badCertificateCallback` is **never** set to
  return true.
- A mismatch yields a typed `PinMismatchException`: the host shows "certificate changed" with the
  expected and seen fingerprints; no "accept anyway". The fix is re-pairing or comparing
  `agentd serve tls show` (security-remote §4.1).
- Android: `dart:io` brings its own trust handling, so Android's network-security-config does not
  apply; `http://` is refused except loopback/emulator hosts in debug. **(unverified: confirm in F2
  that the pin behaves identically on Android.)** Desktop CI tests pin success and failure (rotated
  key) against a Go-generated certificate.
- Pin is stored per host in `HostStore`, not secret (§5.2). Pin is also checked on SSE connections
  (same `HttpClient`).

### 4.3 SSE client

Hand-written `SseClient` (~100 lines) over the pinned `HttpClient`: `GET` with `Accept:
text/event-stream`, `Authorization`, `Last-Event-ID`; `utf8.decoder` → `LineSplitter` → frame parser
(`id`, `event`, multi-line `data`, `retry`, `: ping` comments reset the liveness timer). No third-party
SSE package: they usually take a URL with no hook for header auth plus a custom `HttpClient`
**(unverified per package)**, and the format is trivial. Unknown event names pass through.

Behaviour (http-api §4.3):

- Liveness: no bytes (not even a ping) for 2 x `sse_heartbeat` (30 s) = dead, close and reconnect.
- Resume: keep the last `id` per stream; reconnect with `Last-Event-ID`. On `resync` (or first
  `hello` with a new `epoch`) refetch `GET /v1/sessions` and the open view, replace, do not merge.
  On view streams a missing/unknown revision yields a full `view`, handled idempotently by `base`:
  apply a `view.patch` only when `base == current.revision`, else refetch the view.
- Backoff 0.5 s → 30 s, full jitter, reset after 10 s of healthy stream. `401` stops retries and
  marks the host "auth error: re-pair"; `403 pairing not approved yet` retries every 3 s with the
  "approve on the host" banner; `421` means a wrong URL/Host config (host error, no retry loop).
- De-duplication: events are applied by id order; an event whose id is <= last applied is dropped,
  so reconnects never duplicate rows (F3 acceptance).
- Streams: one `/v1/events` per host while the app is active; one view stream for the visible session
  only.

### 4.4 Reconnect on resume and connectivity change

- `WidgetsBindingObserver.didChangeAppLifecycleState(resumed)`: reconnect every host in parallel.
  Desktop minimised-to-tray keeps streams alive (§6.1); Android background drops them (§6.2).
- `connectivity_plus` (7.x; Android, Linux, macOS, Windows) `onConnectivityChanged`: force-close and
  reconnect with backoff reset, debounced 500 ms (the stream may emit duplicates). It means "a network
  exists", not "gateway reachable" (Tailscale off), so reachability is judged by the stream itself
  (§9 Q9).
- UI: a thin "reconnecting" bar per host, last known data greyed with "last seen"; no blocking spinner.

### 4.5 Compatibility

`GET /v1/info` returns `api_version` and `features[]` (ADR-A16). The app declares `minApi = "1.0"`;
a host below it shows "update agentd on this host", read-only where possible; missing features hide
their UI (e.g. no `mail` feature, no Mailbox). Clients ignore unknown JSON fields and render unknown
enum values (status, option kind, 409 code, event name) generically: models use a `Unknown(String)`
case instead of throwing.

## 5. Secrets, pairing and host store

### 5.1 Token storage

`flutter_secure_storage` (11.x; Android, iOS, macOS, Windows, Linux): Android Keystore-backed
storage (minSdk 23), macOS Keychain, Windows credential/DPAPI-backed storage (needs the C++ ATL
component in the build tools), Linux libsecret. Key = `token/<host_id>`; value = the token only.
Rules (F2 acceptance): the token is never in `shared_preferences`, files, logs, crash reports,
`toString`s or provider debug output.

macOS: dev builds may fail Keychain access; release builds are signed (F7);
`MacOsOptions(usesDataProtectionKeychain: false)` avoids the provisioning-profile requirement.

**Linux caveats.** Requires the libsecret runtime (`libsecret-1-0`) and a running Secret Service
provider (gnome-keyring, KWallet's bridge, KeePassXC). The build needs `libsecret-1-dev`. Headless or
minimal desktops (tiling WMs without a keyring daemon) have no provider: the app **refuses to store
the token and explains how to start one** instead of falling back to a plaintext file (same stance as
[[design/api/security-remote]] §2.2). The keyring may be locked and prompt at first use (keep the UI responsive). Packaging: the AppImage/tar.gz must document the
libsecret dependency; Flatpak (runtime 25.08+ ships libsecret) is a possible later channel.

### 5.2 Host store

`hosts.json` in the app support dir (`path_provider`): `{id, label, base_url, tls_pin?, device_id,
scopes, api_version, added_at}`. No secrets. Written atomically (temp + rename). The notification
preferences and per-host "seen" markers live in the same dir.

### 5.3 Pairing

Payload `agentd://pair?v=1&u=…&c=…&fp=…&h=…` ([[design/api/security-remote]] §3.2).

- **Android: QR scan** with `mobile_scanner` (7.x; CameraX + ML Kit; Android, iOS, macOS, web).
  Camera permission requested on demand; the scanner is restricted to `BarcodeFormat.qrCode`. ML Kit
  adds ~3-10 MB to the APK; the "unbundled" variant (~600 KB, downloaded via Google Play Services)
  is an option but fails on devices without Play Services, so the **bundled** one is default.
  The system camera also works: the QR opens the app through the deep link (§6.3).
- **Desktop: code entry.** `mobile_scanner` does **not** support Linux or Windows (verified), so
  desktop uses a form: URL, one-time code (grouped, case/dash tolerant), optional fingerprint, or
  pasting the whole `agentd://pair?...` string. Also available on Android (broken camera,
  accessibility). macOS scanning is possible with the same plugin but not a v1 goal.
- Flow: parse (reject unknown `v`; only `https://` base URLs, or `http://` to loopback/emulator hosts
  in debug) → build pinned client from `fp` → `POST /v1/pair {code, name, platform}` → store token
  (§5.1) and host record → show "approve on the host" while the pending token gets `403` → host
  becomes online. Decoded QR/deep-link content is untrusted input: confirm screen shows host label,
  URL and fingerprint before any network call.

## 6. Notifications and deep links

Content policy (security-remote §6): no screen content by default, only "`codex@webapp` on `box` is
waiting"; previews are an opt-in setting.

### 6.1 Desktop

- `flutter_local_notifications` (23.x; Linux via the Desktop Notifications spec, macOS
  UserNotifications, Windows toasts) produces notifications locally from `/v1/events`
  (`status.changed` to `waiting`/`idle` after work, `attention`). Linux capabilities depend on the
  notification server; Windows: some APIs (active-notification queries/cancel) require an **MSIX**
  package **(per package docs; relevant to F7 packaging choice)**. Latest releases track very recent
  Flutter/AGP versions, so pin the version and check minimum SDK before upgrading.
- The app keeps streams while the window is hidden. Tray: `tray_manager` (0.9.x; Linux, macOS,
  Windows; Linux uses a StatusNotifierItem and needs the AppIndicator extension on stock GNOME) with
  window hide-on-close via `window_manager` **(unverified)**. Tray menu: sessions needing attention,
  "Open agentd", "Pause notifications 1 h", Quit. Without tray support the window stays open
  (minimised) and the app says so.
- Click on a notification opens `/h/:host/s/:name`.

### 6.2 Android

- **Foreground (app visible):** SSE as in §4; no system notification for the open session, a snackbar
  instead.
- **Background, v1 default: ntfy.** The gateway posts to ntfy ([[design/api/security-remote]] §6); the
  user installs the ntfy app and subscribes (Settings shows the topic URL as text and QR). Zero
  background work in our app, no battery cost, reliable under Doze.
- **Optional foreground service ("keep connected")**, user-enabled: `flutter_foreground_task` holds
  `/v1/events` and raises local notifications (`flutter_local_notifications`). Android 14+ needs a
  declared `foregroundServiceType` + permission; `dataSync` is capped at 6 h/24 h on Android 15+; OEM
  killers still apply; persistent notification mandatory. **Not in the v1 default**: add only if F6
  testing shows ntfy is insufficient (type choice and Play policy fit unverified).
- Android 13+: runtime `POST_NOTIFICATIONS` permission, requested contextually (first host paired).

### 6.3 Deep links (`agentd://`)

`app_links` (7.x; Android, iOS, macOS, Windows, Linux custom schemes; setup guides per platform) for
`agentd://pair?…` and `agentd://open?h=<host>&s=<session>` (ntfy click actions, notification clicks).
Android: intent filter on the main activity (`singleTop`). Windows/Linux: scheme registration by the
installer (F7); a second launch must forward the URI to the running instance **(unverified: spike in
F6)**. Unknown hosts in `open` links are ignored with a toast. Links never carry tokens.

## 7. API client strategy

Options: (a) generate a Dart client from `docs/api/openapi.yaml` (OpenAPI Generator `dart-dio` via
`openapi_generator` 7.x + `build_runner`; needs Java and a generation step in CI; generated models
tend to throw on unknown enum values, fight with `Unknown(...)` cases and with `view.patch`
semantics); (b) hand-written models and a thin typed client, plus contract tests.

**Recommendation: (b).** The surface is small (about 20 routes, 5 core types), SSE and patch handling
are hand-written either way, the forward-compat rules (§4.5) need custom enums, and it avoids a Java
toolchain. Drift is prevented by tests, not by generation:

1. **Fixtures from the spec.** `app/test/contract/` loads `docs/api/openapi.yaml`, and for each
   documented response/example parses it with the Dart models and asserts a lossless round trip of the
   known fields (unknown fields tolerated).
2. **Route coverage test.** Every `operationId`/path+method in the spec has a client method (a table
   in the test lists the mapping; the test fails when the spec has a route the client lacks, or the
   reverse).
3. **Recorded gateway fixtures**: the Go contract tests (http-api §6) can dump real responses to
   `docs/api/examples/`; the Dart tests parse them. Wiring that dump is a follow-up to API-S16.
4. **Stub server** (`dart:io` `HttpServer`, including a self-signed TLS variant for pinning tests and
   an SSE endpoint that drops, replays and sends `resync`): F1 acceptance, no real agents.
5. Revisit generation if the API grows past ~40 routes **(§9 Q3)**.

Models: immutable classes with hand-written `fromJson`; `freezed` optional (decide in F1).

## 8. Repository layout, CI, coexistence with Go

### 8.1 Layout

```
app/  pubspec.yaml (name: agentd_app), analysis_options.yaml (flutter_lints, strict-casts), README.md
  lib/{ui,state,repositories,api,platform}/ + main.dart       # §3.1
  test/{api,state,ui,contract,fixtures}/   integration_test/ (desktop smoke vs stub gateway)
  android/ linux/ macos/ windows/          # flutter create output; no ios/ until later
```

### 8.2 Go coexistence

Go ignores `app/` (no Go packages in it), so `go build/vet/test ./...` (AGENTS.md) are unaffected;
never commit `.go` files under it. `app/.gitignore` covers `build/`, `.dart_tool/`, `local.properties`,
`key.properties`, keystores. Go contributors need no Flutter; when F1 lands, the AGENTS.md toolchain
section must list Flutter/Dart (version pinned in CI via `subosito/flutter-action` or `.fvmrc`). The
`docs/api/openapi.yaml` file is
consumed by `app/test/contract/` by relative path (`../docs/api/openapi.yaml`).

### 8.3 CI

Path-filtered workflow `.github/workflows/app.yml` (runs on `app/**` and `docs/api/**` changes):

Jobs: `check` (ubuntu: `flutter pub get`, `dart format --set-exit-if-changed`, `flutter analyze`,
`flutter test` incl. contract and stub-server tests); `build-linux` (ubuntu, `libgtk-3-dev
libsecret-1-dev`, `flutter build linux`); `build-macos`; `build-windows`; `build-android` (JDK 17,
`flutter build apk --debug`). PR builds are unsigned.

Release (F7) is tag-driven (`v*`, same tag as the Go release, `docs/release.md`): the matrix builds
release artifacts, signs macOS (Developer ID + notarisation) and Windows (Authenticode) via the
`digio-release-signing` pipeline, signs the Android APK/AAB from CI secrets, and attaches everything
to the GitHub Release. The app version comes from `pubspec.yaml` `version:` set from the tag by a
CI step. Dart pub dependencies are locked with a committed `pubspec.lock` (it is an application).
Flutter SDK is pinned to one stable version in CI; upgrades are explicit PRs.

## 9. Open questions

1. Does the API expose an `attention` ack so opening a session on the phone clears desktop badges, or
   is "seen" client-local (v1 assumption)?
2. Composer: submit-only (v1) or also "type draft into the PTY without submitting"?
3. Generated vs hand-written client: revisit at ~40 routes or with a second Dart consumer.
4. Does `badCertificateCallback` + `SecurityContext(withTrustedRoots:false)` pin identically on
   Android (TLS 1.3, Tailscale certs)? Spike in F2 (**unverified**).
5. Linux packaging (AppImage / deb / Flatpak) given libsecret and `agentd://` registration.
6. Android background: ntfy alone, or the optional foreground service (battery, Play policy)?
7. Windows: MSIX (full toast API) or plain installer with reduced notifications?
8. Is a read-only live L0 view (WebSocket, ADR-A07) needed after F8? Tablet layout default?
9. Warn "connect Tailscale" when a `*.ts.net` host is unreachable; per-host notification prefs in
   the app vs host-side `[api.notify]`; iOS (ATS, Keychain, review) when taken on.
