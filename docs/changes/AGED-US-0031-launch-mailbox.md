# AGED-US-0031: Launch agents, kill/rm, mailbox (Flutter app)

Implements frontend-app.md section 1.2 screens 4 and 5 plus kill/remove, over http-api.md 3.3, 3.4, 3.7.

## What changed
- State: `app/lib/state/launch.dart` (`hostInfoProvider`, `LaunchController`, `IdempotencyKeys`, label/default-name
  preview mirroring `ResolveTarget`), `session_actions.dart` (`sessionActionsProvider`), `mailbox.dart`
  (`MailboxFeed`, `MailboxActions`, message/recipients/peek providers).
- UI: `ui/launch/launch_screen.dart`, `ui/launch/session_actions_menu.dart` (reusable `SessionActionsMenu` + confirm
  dialogs for the session-view header), `ui/mailbox/{mailbox,message,compose,peek}_screen.dart`, `format.dart`.
- Routes in `ui/app_shell.dart`: `/hosts/:id/launch`, `/mailbox`, `/mailbox/compose`, `/hosts/:id/mailbox/:msgId`.
- README of `app/lib/state` extended.

## Behaviour
- Launch: aliases (with dir) from `/v1/info` or an absolute path; harness chips from `info.harnesses`; free-text harness
  only when `info.canAdmin`; no cmd/env/args. Read-only devices see the form disabled. 201 navigates to the session.
  `already_exists`, `forbidden`, invalid target/alias mapped to friendly text. `outcome_unknown` is never retried
  automatically: the UI offers "Refresh session list" (finds the expected name, otherwise allows relaunch).
- Idempotency: one key per logical write (launch, reply per message, send per host) kept while the payload is
  identical, regenerated when it changes, dropped after success.
- Kill (TERM default, KILL option) and Remove (force when running) behind dialogs naming session and host.
- Mailbox: all hosts merged newest first, unread = `queued`/`delivered`, per-host `before` paging via "Load more",
  per-host errors shown inline; detail uses `GET` (no state change), "Mark read" is explicit; reply; compose with
  session chips and the host's `human:` identity. Peek (`?mailbox=`) only offered for hosts paired with admin scope.

## Verification
`dart format`, `flutter analyze`, `flutter test` (new tests: `test/state/{launch,session_actions,mailbox}_test.dart`,
`test/ui/{launch_screen,mailbox_screens}_test.dart`, fake client in `test/state/launch_support.dart`).
Build results are in the story comment.

## Deviations and notes
- No entry points yet: the agents list / hosts / session view (other stories) must link to `/hosts/:id/launch`,
  `/mailbox` and embed `SessionActionsMenu`.
- Riverpod 3 retries failing providers: tests use `retry: (_, _) => null` so error paths do not hang.
- Listing the inbox with `all=true` marks messages `delivered` on the daemon (daemon semantics); `read` stays explicit.
- Host-level scope for peek comes from the stored `Host.scope`; launch gating uses `info.scopes`.
