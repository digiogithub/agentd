# AGED-US-0041 / AGED-US-0042: op board, relay gate and hc review

Part of [[plans/agentd/dashboard_plan]] (Contract > Board).

## What

- New op `board {since_digest?}`: one call per host with every session's status, attention, turn, preview, full task list and the
  open questions it asked the caller, plus `unread_human` and a `digest` for conditional fetch.
- Humans on the socket only; a session gets `forbidden`. Relay devices: any scope, session globs filter the sessions,
  `questions` and `unread_human` only for owner devices (guests get empty/0).
- CLI `agentd board [--json]`, client `Client.Board(sinceDigest)`.
- AGED-US-0042: `board` added to the relay gate (any scope). `tasks_set`/`task_update` stay denied to devices and a device
  cannot send `kind: "question"`, both covered by tests. The hc translator subscribes only to `session`, `status` and `doc`
  kinds and ignores any other event; a test pins that `task.*` and the `kind` field of mail events are ignored without error.

## Files and symbols

- `internal/proto/board.go` (`BoardArgs`, `BoardSession`, `BoardQuestion`, `BoardResult`), `proto.OpBoard`.
- `internal/daemon/board.go` (`Server.board`), dispatch in `internal/daemon/handlers.go`, `internal/daemon/relaygate.go`.
- `internal/client/client.go` (`Board`), `cmd/agentd/board.go` (`boardCmd`, `printBoard`), registered in `cmd/agentd/main.go`.
- Tests: `internal/daemon/board_test.go`, `cmd/agentd/board_test.go`, `internal/hcompat/events_ignore_test.go`.
- `AGENTS.md`: Ops list and relay gate list.

## Why

Desktop and mobile dashboards must paint a host with one request instead of `ls` + `tasks` per session + inbox.

## Choices beyond the contract

- Sessions still `starting` are not listed (the board reads cached views of registered sessions).
- The digest is computed over what the caller sees (a filtered device gets its own digest).
- Questions are read with one query (`OpenQuestions(ctx, "", caller)`) and grouped by sender.

## Verification

`gofmt -l internal cmd`, `go vet ./...`, `go test -race ./internal/daemon/... ./internal/proto/... ./internal/client/...
./internal/hcompat/... ./cmd/agentd/...` (see the report of the change for results).
