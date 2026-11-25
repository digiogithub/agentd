# Push: Android data mode and resolved pushes

2026-11-25. Extends [[push-fcm_plan]] (section "Android data mode") and [[changes/push-fcm]]. Requested by the
agentd-app agent for Android Auto, confirmed by the user.

## What changed

- `[push] android_mode = "notification" | "data"` (default `notification`, behaviour unchanged).
- `push_register` takes `mode` (`notification` | `data`, `data` only for android) and answers `{id, mode, modes}`;
  `push_ls` tokens carry `mode`; `tokens.json` stores it.
- Data-mode tokens get data-only FCM messages (`title`, `body`, `tag` in `data`; no notification blocks; HIGH priority
  and `collapse_key` kept) and a `resolved` data push (NORMAL priority, `resolves`, `tag`, `message_id?`) when the
  question is answered, the attention clears / status leaves `waiting`, the session exits, or its last blocked task
  is unblocked. `push_test` follows each token's mode.
- CLI: `push ls` MODE column, hidden `push register --mode`.

## Files and symbols

- `internal/proto/push.go`: `PushModeNotification`, `PushModeData`, `PushModes`, `PushResolved`,
  `PushRegisterArgs.Mode`, `PushRegisterResult.Mode/Modes`, `PushToken.Mode`.
- `internal/config/push.go`: `PushConfig.AndroidMode`, default + validation, `PushModeNotification/Data`.
- `internal/push/fcm.go`: `Message.DataOnly`, `Message.Normal`, payload; `store.go`: `Entry.Mode`.
- `internal/daemon/push.go`: `daemonPush.open`, `resolve`, `enqueueLocked`, `pushEventResolved`, `pushResolveQuestion`,
  `tokenMode`/`pushTokenMode`, `tag`; `pushMessage`, `pushDeliver`, `pushRegister`, `pushLs`, `pushTest`; bus kinds +
  `status.changed`. `mailboxops.go` `reply` calls `pushResolveQuestion` for a question.
- `cmd/agentd/push.go`; `AGENTS.md`, `README.md`.

## Why

Android Auto shows only notifications the app posts itself (MessagingStyle with a reply action); FCM notification
messages are drawn by the system and duplicated the app's own one. The resolved push lets the app cancel a
notification that is no longer actionable.

## Decisions

- Data mode only for Android; iOS / macOS tokens always get notification messages.
- Resolved pushes only go to data tokens (a system-drawn notification cannot be withdrawn by data) and only for a push
  the daemon actually queued; the debounce is not reset by a resolve (default behaviour unchanged).
- The question tag stays per session (as before); `message_id` in the resolved push says which question.

## Verification

`go vet`, `gofmt -l` clean; `go test -race ./internal/push ./internal/config ./internal/proto ./cmd/agentd`;
`go test -race -run Push -count=3 ./internal/daemon`; full `go test -race ./...`. New tests:
`TestPushAndroidMode`, `TestSendDataOnlyShape`, `TestPushRegisterModes`, `TestPushDataModeAndResolved`,
`TestPushReplyResolvesQuestion`.
