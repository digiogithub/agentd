# Flutter app review fixes, act revision for every action, app navigation

Builds on [[design/api/frontend-app]], [[design/api/daemon-ops]] section 3, [[design/api/http-api]],
[[AGED-US-0013-op-act]], [[ops-review-fixes]] and the app stories [[AGED-US-0027-flutter-skeleton]],
[[AGED-US-0028-hosts-pairing]], [[AGED-US-0029-agent-list]], [[AGED-US-0030-session-view]],
[[AGED-US-0031-launch-mailbox]].

Principle: the app drives agents' terminals. It never sends an action against a state the user did not
see, and it never sends twice. Every behaviour fix has a regression test (Go: `internal/daemon/act_test.go`;
Flutter: `app/test/...`).

## A. Daemon: revision for every act action

- What: `prompt`, `keys`, `interrupt` and `cancel` accept an optional `revision` (+ `epoch`, required with
  it). When set, the daemon compares the **interaction fingerprint** of the current view with the one
  recorded for that revision: status, menu instance and its options (ids, pointer, checks), dialog
  presence/question, pending-draft state. Transcript lines, title, cwd, idle time and a ghost suggestion
  appearing are not part of it. A different fingerprint, an unknown (never seen or evicted) or future
  revision is `conflict` with `data = {revision, view}`; no key is sent (for `prompt` the check runs in the
  inject guard under the input lock, before the paste). `choose` rules are unchanged. Revision stays optional
  for these actions (CLI/back-compat); the app always sends it.
- Files and symbols: `internal/daemon/act.go` (`interactionFP`, `menuTrack.noteInter/interAt`,
  `checkInteraction`, `actKeys` now takes the caller), `internal/daemon/view.go` (`viewState.inter`, recorded
  per revision next to the menu instance). Docs: `AGENTS.md` Act section, `docs/design/api/daemon-ops.md`
  3.1, `docs/design/api/http-api.md` 3.6, `docs/api/openapi.yaml` (`ActRequest.revision`).
- Tests: `TestActRevisionKeysRefusedWhenMenuAppeared` (Enter/interrupt/cancel refused after a menu
  appeared, nothing typed, fresh view in `data`, fresh revision accepted),
  `TestActRevisionPromptRefusedWhenStatusChanged` (idle to waiting), `TestActRevisionTranscriptOnlyChangeAccepted`.
- Verified: `gofmt -l .` clean, `go vet ./...` clean, `go test -race -count=1 ./internal/daemon ./internal/httpapi ./cmd/...` green (OpenAPI contract tests included).

## B. App findings

1. Prompt flow (`app/lib/state/session_view.dart`, `ui/session/composer.dart`): `_autoRetry` removed. `busy`:
   banner "Someone is typing on the host — try again when they finish"; the text stays in the composer; Send
   always uses a new key; the banner's Retry (prompt only) reuses key + text within 30 s
   (`retrySameKeyWindow`, clock seam `sessionClockProvider`). Success clears the composer (also when the
   Retry path succeeds: `sentSeq`/`sentText`). `outcome_unknown`/timeout/504: the text moves into a
   "Sent? result unknown" note (`unknownText`) with "Send again" (`sendAgain`, new key) and Dismiss.
   Tests: `session_view_test.dart` (busy, retry window, unknown + sendAgain), `session_header_test.dart`
   (busy banner, no auto send, Retry clears the field), `session_screen_test.dart` (unknown note).
2. Every act carries `revision` + `epoch` of the displayed view (`ActRequest.keys/interrupt/cancel` gained
   them). 409/412 replace the view and show "review again". Test: "every act carries the revision and epoch".
3. Confirm dialogs: `DialogCard`/`QuickKeys` capture the view at tap time and pass it as `shown`; the
   notifier (`_movedOn`) sends nothing and shows "review again" when the current revision/epoch differ.
   Tests: `session_header_test.dart` (interrupt and low-confidence option, moved on vs unchanged),
   `session_view_test.dart`.
4. `hostClientProvider` is keyed by `HostCredentials` (id, baseUrl, pin, deviceId) through
   `hostsProvider.select`; the token is read once per build. Rename and `markOk` no longer rebuild/close the
   client. The agents feed now uses it (`defaultHostFeed`, `ClientHostFeed.ownsClient = false`); credential
   changes still restart the runner (`_reconcile`). Tests: `test/state/host_client_test.dart`.
5. Android: `allowBackup=false`, `fullBackupContent=false`, `dataExtractionRules` (new
   `res/xml/data_extraction_rules.xml`) excluding every domain for cloud backup and device transfer.
   `secretStoreHint(android:)`: on Android a key-unwrap error says to pair again, not "install
   gnome-keyring". Tests: `test/platform_hardening_test.dart`.
6. `10.0.2.2` is loopback only in debug builds (`app/lib/api/loopback.dart`, `isDebugBuild`; `parseBaseUrl`
   and `AgentdClient` use it). Tests: `platform_hardening_test.dart`.
7. `agentd://open`: `resolveHosts` (id first, then every label match, then every hostname match); more than
   one match shows a host picker (`DeepLinkChoose`). The session screen shows the host label prominently
   (bold, primary colour, `Key('host-label')`). Attention is acked only after the screen was visible for
   1 s (`minVisibleBeforeAck`). Tests: `deep_link_router_test.dart`, `deep_link_widget_test.dart`,
   `session_view_test.dart`.
8. `DeepLinkDeduper`: the same Uri within 2 s is handled once (app_links initial + stream). Tests as above.
9. Re-pair keeps base URL and pin: `PairingController.start(allowEndpointChange)` refuses (nothing sent,
   `PairFailureKind.endpointChanged`) when the link differs from the host; the screen shows a warning dialog
   with old vs new URL and fingerprint (explicit text when the pin would be dropped). The screen also
   prefills when the hosts list loads after the screen opened. Tests: `pairing_controller_test.dart`,
   `pairing_screen_test.dart`.
10. Covered by 1.
11. `in_progress` is "Still processing" (prompt may retry the same key later); an unknown 409 code is a
    generic error with the code, no review, no retry. Test: `session_view_test.dart`.
12. Retry exists for prompts only; keys, interrupt, cancel and choose never get one. Test: "never offered a Retry".
13. `LaunchController.checkOutcome` matches name AND harness (profile) AND target (alias dir or the same
    absolute path) AND `started_at` >= request time (clock seam `launchClockProvider`; the phone clock is
    compared with the host's `started_at`, so a skewed phone clock can only cause a false "not visible yet",
    never a false success). When absent: stays `outcomeUnknown`, key kept, "Not visible yet", nothing cleared;
    "Edit and launch again" resets the form. Tests: `launch_test.dart`, `launch_screen_test.dart`.
14. `mailboxActionsProvider` is auto-disposed and watched by the compose and message screens, so keys die
    with the screen; after a lost answer compose offers "Send again (new message)" (`send(fresh: true)`).
    Tests: `mailbox_test.dart`, `mailbox_screens_test.dart`.
15. `HostStore.add` stages the metadata in a temp file, writes the token, then commits (rename); a failure
    rolls back (new token deleted or previous token restored, temp file discarded). `remove` deletes the
    token first, then the metadata. Failure injection through a `rename` seam and `MemorySecretStore`. Tests:
    `host_store_test.dart`.
16. Deep link handling guards `hostsProvider.future` (snackbar "Cannot read the paired hosts"); the agents
    screen mailbox refresh and `MailboxFeed.refresh` no longer throw on an unreadable store. Test:
    `deep_link_widget_test.dart`.
17. The pairing link travels through `pendingPairLinkProvider` (a plain in-memory holder taken once by the
    screen), never in the route. Test: `deep_link_widget_test.dart`.

## C. Navigation wiring

- Agents screen app bar: Mailbox (badge from `mailboxUnreadProvider`; the feed is loaded on first show) and
  Hosts; a Launch FAB (one host goes straight to its launch screen, several ask which host). Hosts screen:
  per-host "Launch agent" menu item.
- Session screen header: "Raw screen" (`RawScreenDialog`, `GET .../terminal`, read-only monospace,
  `SessionGateway.terminal`), "Send mail to this session" (compose with `host` and `to` prefilled) and
  `SessionActionsMenu` (kill/remove; remove pops the screen; disabled for read scope).
- Tests: `agents_screen_test.dart`, `hosts_screen_test.dart`, `session_header_test.dart`.

## Verification

`cd app && dart format --output=none --set-exit-if-changed . && flutter analyze && flutter test`: formatted,
no issues, 270 tests pass. `flutter build linux --debug` and `flutter build apk --debug`: see the report.
