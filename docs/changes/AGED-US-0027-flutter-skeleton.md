# AGED-US-0027: Flutter app skeleton and Dart API client

Plan: [[plans/api-gateway-plan]] (ADR-A18, A15, A16). Design: [[design/api/frontend-app]] sections 3, 4, 7, 8.
Builds on the gateway stories [[AGED-US-0018-sse-streams]] and [[gateway-review-fixes]].

## What changed

- New Flutter package `agentd_app` in `app/` (`flutter create --org dev.digio`, platforms linux, macos, windows,
  android; boilerplate and `widget_test` removed). Strict `analysis_options.yaml` (`flutter_lints`, strict-casts,
  strict-inference, strict-raw-types plus a few extra lints). `pubspec.lock` committed.
- Pure-Dart API layer `app/lib/api/` (no Flutter imports): tolerant hand-written models, `AgentdClient`
  (`dart:io` `HttpClient`), SPKI pinning, `SseParser` / `SseClient`, `ViewState`.
- `main.dart`: `ProviderScope` + `MaterialApp` with a placeholder home ("No hosts paired"); empty `ui/ state/
  repositories/ platform/` layer folders for the next stories.
- CI `.github/workflows/app.yml` (path-filtered): `check` (pub get, `dart format --set-exit-if-changed`, analyze,
  test), `build-linux` (apt deps, `flutter build linux --debug`), `build-android` (JDK 17, `continue-on-error`
  until signing, AGED-US-0033).
- `docs/api/openapi.yaml`: `Info` gained optional `api_version` and `features[]` (the gateway already sent them).
- `AGENTS.md`: "Flutter app (`app/`)" toolchain subsection.

## Files and symbols

- `app/lib/api/json.dart` (tolerant accessors, `parseEnum`), `models.dart` (`Info`, `SessionSummary`, `View`,
  `Line`, `Input`, `Option`, `Hint`, `Attention`, `Dialog`, `ViewStreamPatch`, `ViewResult` = `ViewFull` |
  `ViewNotModified` | `ViewPatched`, `ActRequest`, `ActResult`, `TerminalSnapshot`, `Message`, `SendRequest`,
  `PairResult`, `GatewayEvent`, enums with an `unknown` member and the raw string kept next to them),
  `errors.dart` (`ApiException` with code/message/retryable/details/retryAfter/etag, `freshView`,
  `isOutcomeUnknown`, `isConflict`; `PinMismatchException`; `ConnectionException`), `spki.dart` (`SpkiPin`,
  `spkiOf` DER walk), `client.dart` (`AgentdClient`), `sse.dart` (`SseParser`, `SseClient`, `compareEventIds`),
  `view_state.dart` (`ViewState`, `PatchOutcome`), `agentd_api.dart` (barrel).
- `AgentdClient`: `health`, `info`, `pair`, `listSessions` (ETag/304), `getSession`, `runSession`, `killSession`,
  `removeSession`, `getView` (If-None-Match, 304/unchanged/patch), `ackView`, `act` (auto UUIDv4
  `Idempotency-Key`, `If-Match`), `getTerminal`, `listMailbox`, `getMessage`, `sendMessage`, `replyMessage`,
  `markMessageRead`, `eventsStream`, `viewStream`.
- Behaviour: bearer header on every call except health/pair, never in query strings, redacted in `toString`;
  plain http only for loopback / 10.0.2.2 (or `allowInsecureHttp`); redirects not followed; JSON above 64 KiB
  decoded in an isolate; a failure after a side-effecting request was sent is a local `outcome_unknown`
  (before sending: `ConnectionException`, safe to retry). With a pin the client uses
  `SecurityContext(withTrustedRoots: false)` so the pin is the only anchor; mismatch is `PinMismatchException`
  (expected and seen), also terminal for SSE.
- `SseClient`: `id/event/data/retry`, comments as heartbeat, `Last-Event-ID` resume, backoff `retry` hint or 0.5 s
  base, doubling to 30 s with full jitter, reset after 10 s healthy, liveness = 2x heartbeat, dedupe by id
  (`<epoch>:<seq>` and `e<epoch>-i<inst>-r<rev>`; `hello`, `resync`, `bye` and full `view` never dropped),
  401/403/404/421/other 4xx terminal (error then close), 408/429/5xx/network retried honouring `Retry-After`.
- Tests: `app/test/api/{models,view_state,sse,client,pin,contract}_test.dart`, fixtures
  `app/test/fixtures/{a,b}.{cert,key}.pem`, `*.pin` (test-only self-signed EC certs, README inside).

## Why

First of the Flutter stories (F1): a tested, Flutter-free API layer the later screens build on, with drift
against `docs/api/openapi.yaml` caught by tests instead of code generation (frontend-app.md section 7).

## Verification

`cd app && dart format --output=none --set-exit-if-changed . && flutter analyze && flutter test &&
flutter build linux --debug` (all clean; 68 tests). `go vet ./...` clean and the Go OpenAPI tests still pass
after the `Info` additions. See the bottom of this file for the Android build result.

Tests: model parsing incl. unknown enum values/fields and defaults; 300-seed x 30-step property test of
`view.patch` application against fresh views (scroll, splice, optional fields, null clears); SSE parser with
every chunking, CR/CRLF, multi-line data; `SseClient` resume, dedupe, liveness, pings, backoff, retry hint,
terminal vs retried errors; client against a local `HttpServer` (ETag/304, error envelope, 409 fresh view,
412, 502 outcome_unknown, dropped write, idempotency and If-Match headers, SSE resume over HTTP); SPKI pin
accept / reject (rotated key) / no-pin-rejects-self-signed with fixture certs cross-checked against openssl;
contract test over the parsed spec (every operation both ways, query/header params, model fields both ways,
enum values).

## Deviations and notes

- Design said `http` could be avoided: it is (only `dart:io`). Added `crypto` for SHA-256.
- `ViewPatchResponse` (REST patch) is parsed minimally (`ViewPatched`): the client never sends `since`.
- Pairing is a long poll in the shipped gateway (no pending-token 403), so the client's `pair` waits up to 6 min.
- Test private keys are committed in `app/test/fixtures/` (secret scanners may flag them; they protect nothing).
- Riverpod is only wired at the root; no providers yet.

## Android build result

`ANDROID_HOME=/apps/android-sdk flutter build apk --debug` succeeded locally with the system JDK 25
(`build/app/outputs/flutter-apk/app-debug.apk`, ~160 MB, first run 280 s). Gradle auto-installed NDK 28.2.13676358
and Build-Tools 36.0.0 into the SDK on the first build. CI uses JDK 17 (temurin) and stays `continue-on-error`.
