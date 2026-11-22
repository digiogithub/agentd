# AGED-US-0040: agent questions to the human

Links: [[plans/agentd/dashboard_plan]] (contract "Questions")

Note: the KB tools (`kb_add_document`, `kb_search_documents`, code index) were unavailable in this environment, so this
summary lives in the repository and the plan was read from `docs/plans/agentd/dashboard_plan.md`.

## What changed
A session can ask the human a question, with optional answer options, as a mailbox message. The human's answer is an
ordinary `reply`, injected back by the existing delivery loop. No second store.

## Files and symbols
- `internal/mailbox/mailbox.go`: migration v4 (`ALTER TABLE ... ADD COLUMN kind/options/task`, partial index), `Message.Kind/Options/Task`,
  `KindQuestion`, `selectCols`/`scanMessage`/`insert` carry the new columns, `Send` rejects unknown kinds and options/task without kind.
- `internal/mailbox/questions.go`: `Store.OpenQuestions(ctx, sender, recipient)`, `ValidTaskID`.
- `internal/proto/proto.go`: `SendArgs.Kind/Options/Task`, `Message.Kind/Options/Task`, `MailEventData.Kind`.
- `internal/daemon/mailboxops.go`: `checkQuestion` (session caller, `human:<name>` recipient, options <= 8 of 1..80 runes via `screen.Sanitize`,
  task id), `send` uses it and publishes `mail.queued` with `kind`; `events.go`: `publishMailKind`; `handlers.go`: `toWire`.
- `cmd/agentd/ask.go` (`agentd ask`, `questionSuffix`), `cmd/agentd/main.go` (registration, `inbox`/`read` show kind, task, options).
- `internal/mcpserver/mcpserver.go`: tool `agentd_ask`, `Message.Kind/Options/Task`.
- `AGENTS.md`: Mailbox "Questions", Ops `send`, MCP tools.

## Why
Agents blocked on a human decision need a structured, answerable channel for the dashboard (board, notifications) without a new store.

## Choices beyond the contract
- Default recipient: the daemon fills an empty `to` or `human:` with `human:<daemon user>` (`humanAddr(os.Getuid())`).
- v4 uses plain `ADD COLUMN` (no CHECK change needed), so no table rebuild; ids/AUTOINCREMENT are untouched by construction.
- Humans and relay devices asking get `invalid` (session callers only); options are trimmed and sanitized before storage.

## Verification
`gofmt -l .`, `go vet ./...`, `go test -race ./internal/mailbox/... ./internal/daemon/... ./internal/mcpserver/... ./cmd/agentd/... ./internal/client/...`.
Tests: `TestMigrateV3Database`, `TestQuestions`, `TestValidTaskID` (mailbox), `TestQuestionFlow` (daemon, validation, event kind, reply closes),
`TestAskCmd`, `TestQuestionSuffix` (CLI), `TestAskTool` and the tool list (MCP).
