# Push notifications through FCM (P1-P4)

2026-11-25. Implements phases P1-P4 of [[push-fcm_plan]]; continues [[backlog-notes-panel-detail-revoke]] (relay
devices, revoke) and the daemon `[notify]` tap.

## What changed

- `[push]` config, ops `push_register` / `push_unregister` / `push_ls` / `push_test`, a bus tap that sends
  `question` and `permission` (optionally `waiting`, `blocked`, `exited`, `turn_done`) to every registered FCM token, and
  the CLI `agentd push ls|test|rm` (+ hidden `register`). Revoking a relay device removes the tokens it registered.

## Files and symbols

- `internal/proto/push.go`: op names, arg/result types, limits.
- `internal/config/push.go`, `config.go`, `paths.go`: `PushConfig`, defaults, `on` validation, `CredentialsPath`,
  `CheckCredentialsFile`, `PushDir`.
- `internal/push`: `Credentials`, `TokenSource` (RS256 JWT bearer grant), `Client.Send`, `ErrUnregistered`, `Retryable`, `Store`.
- `internal/daemon/push.go`: `startPush`/`stopPush`, `pushEventJob`, `pushDialog`, ops, `pushRemoveDevice`; wiring in
  `handlers.go`, `server.go` (`PushStore`, `PushEndpoint`, `PushBackoff`), `main.go`, `relaygate.go`, `relay.go` (`relayRevoke`).
- `internal/client/client.go` (`PushRegister`, `PushUnregister`, `PushLs`, `PushTest`), `cmd/agentd/push.go`.
- `AGENTS.md` (package row, Key behaviours bullet, paths, ops), `README.md` (Push notifications).

## Why

An agent blocked on a question or a permission dialog should reach the human's phone while the app is closed. Direct
FCM from each daemon keeps the relay out of it and works over SSH and relay hosts.

## Decisions on ambiguities

- Own queue/worker in the daemon (not a `notify` sink hook): `internal/notify` is shaped around ntfy/webhook payloads.
- Token ops work while push is off (apps can register before it is enabled); `push_test` needs push on (`not_ready`).
- Retries live inside `Client.Send` (3 tries, 1 s doubling); one refresh of the access token on a 401.
- Debounce is per session+reason for every reason, questions included, as in the plan.
- `push_unregister` of a token a device does not own answers `removed: 0`, like an absent one.
- The store is opened by `daemon.Main` into `Server.PushStore`; a corrupt `tokens.json` is renamed `.bad`.
- `push_ls` for a relay device lists only its own entries; guests are refused by `relayGate`.

## Verification

`go test ./internal/config ./internal/proto`, `go test -race ./internal/push ./internal/daemon ./cmd/agentd ./internal/client`,
`go vet ./...`, `gofmt -l .` empty, no MyGo in `./cmd/agentd` deps. Tests use httptest fakes for OAuth and FCM, a
generated RSA key and temp state dirs; no real FCM, no real agent CLI.
