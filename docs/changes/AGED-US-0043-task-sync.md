# AGED-US-0043: `agentd task sync` hook and install snippets

Builds on [[plans/agentd/dashboard_plan]] (Tasks contract), the per-session task lists (AGED-US-0039)
and mailbox questions (AGED-US-0040).

## What changed
- `agentd task sync [--from claude] [--verbose]`: reads one Claude Code PostToolUse payload from stdin and
  mirrors the harness todo list into the calling session's task list (`tasks_set`).
  - `TodoWrite` (`tool_input.todos[{content,status,activeForm}]`): whole list; completed -> done; id =
    `td-` + first 8 hex of SHA-1 over the whitespace-normalised text (stable across status changes and
    reordering; identical texts get `-2`, `-3`).
  - `TaskCreate` (`tool_input.subject`, id from `tool_response.task.id`) and `TaskUpdate`
    (`tool_input.taskId`, `status`, `subject`; `deleted` removes): applied to the current list; id = `c<harness id>`.
    Unknown task ids are ignored; a TaskCreate response without an id falls back to `tc-<hash>`.
  - Silent and safe: no stdout, exit 0 on unknown tool, bad JSON, no daemon, human caller; never autostarts the
    daemon (`client.Dial`); whole run capped at 3 s; title clipped to 200 runes, list to 50.
- `agentd hooks install --print`: settings.json PostToolUse snippet (matcher `TodoWrite|TaskCreate|TaskUpdate`) plus an
  agent instruction block (`agentd_tasks_set`, `agentd_task_update`, `agentd_ask`). Separate from
  `mcp install` because hooks are harness settings, not MCP servers.

## Files
`cmd/agentd/tasksync.go`, `cmd/agentd/hooks.go`, `cmd/agentd/task.go` (registers `sync`), `cmd/agentd/main.go`
(registers `hooks`), `cmd/agentd/tasksync_test.go`, `cmd/agentd/testdata/hook_*.json`, `README.md`, `AGENTS.md`.

## Why
Agents that already plan in their harness should populate the dashboard without prompt changes.

## Docs status of the payload shapes
Confirmed: PostToolUse stdin fields and settings.json format (code.claude.com/docs/en/hooks), `TodoWrite` /
`TaskCreate` / `TaskUpdate` input and output types (code.claude.com/docs/en/agent-sdk/typescript, tools-reference).
Guess: the hook's `tool_response` for `TaskCreate`/`TaskUpdate` is assumed to equal the documented tool output
(`{task:{id,subject}}`); the hooks page only says the schema "depends on the tool".

## Verification
`gofmt -l internal cmd`, `go vet ./...`, `go test -race ./cmd/agentd/... ./internal/mcpserver/...`; end-to-end
test runs a `sh` session on a temp-socket daemon and authenticates with its `AGENTD_TOKEN`.
