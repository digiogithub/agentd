# AGED-US-0030: Session view (text, prompt box, options, quick keys)

Plan: [[plans/api-gateway-plan]]. Design: [[design/api/frontend-app]] sections 1.2, 2, 4.3; [[design/api/http-api]]
sections 3.5 to 3.6. Builds on [[AGED-US-0027-flutter-skeleton]], [[AGED-US-0028-hosts-pairing]], [[ops-review-fixes]].

## What changed

- `sessionViewProvider(sessionKey(hostId, name))` (`NotifierProvider.autoDispose.family`): `view/stream` through a
  `SessionGateway` seam into `ViewState` (full view + patches); refetch (`GET view?lines=1000`) on `needsRefetch`,
  `resync` and `bye`; `reconnect()` (called on app resume) reopens the stream and refetches; `setVisible()` +
  debounced (500 ms) `view/ack` when the screen is visible and `attention.set`. `sessionGatewayProvider` is the test seam.
- Act flow in the notifier: `prompt`, `choose` (option id + label + current revision + epoch), `keys`, `interrupt`,
  `cancelMenu`, `retry`. One `Idempotency-Key` per user action, reused by Retry and by the single automatic retry.
  - 409 `stale_revision`/`option_mismatch`/`conflict` and 412: view replaced with `details.view` (refetch if absent),
    "The menu changed: review the options and choose again", never resubmitted.
  - `outcome_unknown` (and `timeout`/504): never retried, "Result unknown: check the screen", view refreshed, no Retry button.
  - `busy`/`human_draft`: draft kept, Retry; prompts auto-retry exactly once when the next view reports
    `input_state != draft` (frontend-app 2.2); choices never auto-resubmit. `not_ready`: friendly message, no retry.
    `verify_failed`/`paste_unverified`/`starting`: Retry with the same key. `exited`: refetch.
- UI (`app/lib/ui/session/`): header (name, host label, status badge, attention dot), reconnecting bar, selectable
  monospace transcript (lazy `ListView.builder` inside `SelectionArea`, wraps at any width, never asks for a resize,
  line roles styled, follows the tail, "New output" chip otherwise), dialog card (question + option buttons, selected
  highlighted, confidence < 0.75 labelled "suggested" and needs a confirm dialog, "Dismiss menu" = `act cancel`),
  feedback banner, quick keys (Esc, Interrupt with confirm, up, down, Enter, Tab), multi-line composer (ghost only as
  hint text plus an explicit "Use suggestion" button; disabled with a reason for host draft ("Someone is typing on
  the host", host draft text shown), working (interrupt instead; force is never used), starting, options pending, exited).
  Exited sessions: banner with exit code, no composer, keys or options.
- `/hosts/:id/sessions/:name` now builds `SessionScreen`; `SessionPlaceholder` removed.

## Files

`app/lib/state/session_view.dart`, `app/lib/state/README.md`, `app/lib/ui/session/{session_screen,transcript,
dialog_card,composer,session_status}.dart`, `app/lib/ui/app_shell.dart`; tests `app/test/state/{session_view_test,
session_fakes}.dart`, `app/test/ui/session_screen_test.dart`, `app/test/ui/deep_link_widget_test.dart` (route now
asserts `SessionScreen`, fake gateway).

## Verification

In `app/`: `dart format --output=none --set-exit-if-changed .`, `flutter analyze` (clean), `flutter test` (211 tests, 12
provider + 11 widget tests new), `flutter build linux --debug`, `flutter build apk --debug` all pass.

## Deviations and notes

- `act` sends `revision`/`epoch` in the body, not `If-Match` (the body values are equivalent and the view model does
  not expose the ETag instance part). Prompt and keys carry no revision (the daemon does not require one for them).
- History is the last 1000 lines (`tail`); no further `first_line` paging in v1.
- Raw screen, Mail and Kill header menu items of the design are not part of this story.
- Connectivity-change reconnect (`connectivity_plus`) not added; only app resume.
