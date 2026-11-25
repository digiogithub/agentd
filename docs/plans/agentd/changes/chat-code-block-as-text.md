# Chat: show a code block as normal text (desktop)

2026-11-25. Continues [[backlog-notes-panel-detail-revoke]] and AGED-US-0051 (code lines, `ViewLine.code`).

## What changed

Every code block of the desktop chat has a second button next to "Copy": "Mostrar como texto" (icon `wrap-text`),
which draws the block as normal text (chat font, word wrapped) and becomes "Mostrar como código" (icon `code`).
Client only: no daemon or wire change, the detection heuristics are untouched.

## Why

The code-line detection (a non-bold run in an ANSI palette colour) sometimes takes prose for code. A code block does
not wrap, so a paragraph shown as code is unreadable. The user asked for a per-block toggle in both apps.

## Files and symbols

- `internal/desktopui/app/session/codeblock.go`: `View.codeBlock` (hook `markdown.Options.CodeBlock`), `codeKey`
  (first two lines of the block), `View.asText` (in memory, per session view).
- `internal/desktopui/app/session/richtext.go`: `markdownOpts` sets the hook.
- `internal/desktopui/chatfmt/reflow.go`: `Reflow` (indentation dropped, wrapped lines joined, list items on their
  own line, blank line = paragraph).
- `internal/desktopui/kit/icons/{code,wrap-text}.svg`, `kit.IconCode`, `kit.IconWrapText` (lucide).
- `internal/desktopui/backend/fake/fake.go`: `claude@docs` has two prose lines marked as code.
- `AGENTS.md`: "Chat markup" bullet.

## Verification

`gofmt`, `go vet ./internal/desktopui/...`, `go test ./internal/desktopui/...` (new `TestCodeBlockAsText`,
`TestReflow`), headless capture of the chat with the fake backend. Mobile: delegated to the agentd-app agent.
