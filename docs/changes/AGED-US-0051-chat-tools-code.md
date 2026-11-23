# AGED-US-0051: chat groups tool calls, shows code blocks, stops repeating transcript

Implements [[plans/agentd/chat-tools-code_plan]]. Builds on [[agentd/changes/AGED-US-0036-history-sampling]] and
[[plans/agentd/desktop_plan]]. Date: 2026-11-23. (KB unavailable: recorded here.)

## What

- **History**: rows matching the new profile key `info_regex` directly above the input box are chrome
  (`screen.RegionInfo`): out of the transcript and the history, not a busy sign. Built-in for `claude`
  (`config.ClaudeInfoRegex`): spinner row, context counter, tip. `screen.DefaultTailSlack` 3 -> 8 and a frame also
  aligns when at least 3 non-blank lines match (`tailAnchors`), so a tool header expanding into arguments and output
  no longer stores the frame again after a gap.
- **Code lines**: profile key `code_ansi_colors` (claude: true). With `markup:true`, a transcript line with a
  non-bold run of letters/digits in an ANSI palette colour is `ViewLine.code = true` with no `md`
  (`screen.CodeLine`). Optional wire field, omitted when false.
- **Desktop chat** (`session.Items`): a tool call header is a marked line whose markup starts with a bold name
  followed by `(` or nothing (`boldCall`), so multi-line calls, bare headers and MCP calls are tool cards; lines before
  the `⎿` mark continue the call (`Item.Call`, shown at the top of the expanded card), the rest are output. Runs of
  code lines become one fenced block, which MyGo's Markdown draws with a copy button. Indented lines after a prompt
  stay in the prompt; `✻ Brewed for 8s` is a status note.

## Files and symbols

- `internal/config/regions.go` (`RegionConfig.InfoRegex`, `CodeANSIColors`; `RegionProfile.Info`, `CodeANSIColors`),
  `internal/config/config.go` (`ClaudeInfoRegex`, claude built-in).
- `internal/screen/rules.go` (`Rules.InfoRegex`, `CodeANSI`), `regions.go` (`RegionInfo`, status loop in `Split`),
  `history.go` (`DefaultTailSlack`, `tailAnchors`, `matches`), `spans.go` (`CodeLine`).
- `internal/daemon/view_rules.go`, `view.go` (`transientFn`, `ViewLine.Code` in the view op).
- `internal/proto/proto.go` (`ViewLine.Code`).
- `internal/desktopui/app/session/chat.go` (`Items`, `boldCall`, `Item.Call`, tool card body).
- Tests: `regions_test.go`, `spans_test.go`, `history_test.go`, `config/regions_test.go`, `session_test.go`.
- `AGENTS.md`, `docs/design/api/semantic-model.md`.

## Why

Reported on 2026-11-23: tool calls and output inside the chat prose, no code blocks, on desktop and mobile.

## Verification

- `go test` of `internal/screen`, `internal/config`, `internal/proto`, `internal/httpapi`, `internal/mcpserver`,
  `internal/desktopui/...`, `cmd/...`: pass. `internal/daemon`: pass except
  `TestHCListenerLifecycleAndIdentity`, which fails or passes from run to run with and without this change (the probe
  program can reach the hc socket before its session is registered, so the refusal names the token instead of the
  session); not addressed here.
- Replay of a real Claude session log (last 3 MB, sampled every third synchronized frame): 111 gaps / 4267 history
  lines before, 39 gaps / 1717 lines after. The log was replayed at one size while the session had been resized,
  which accounts for part of the remaining gaps.
- The same replay through `session.Items`: Bash, Read, CronCreate/CronDelete calls come out as tool items with their
  output; a multi-line prompt is one user entry.
- Not verified: the desktop window itself (no screenshot taken), a code block without syntax colours (not detectable),
  other harnesses' tool headers beyond the existing fallback patterns.
