# Chat view: tool calls, code blocks, repeated transcript (AGED-US-0051)

Builds on [[plans/agentd/desktop_plan]] and [[agentd/changes/AGED-US-0036-history-sampling]]. Date: 2026-11-23.
(KB unavailable: recorded here.)

Report (2026-11-23): in the Chat view of Claude sessions, tool calls and their output show inside the prose, code is
not shown as a code block with a copy button, on desktop and mobile.

Findings, from this repo's own Claude session (view op + a replay of its PTY log):

1. The history repeats blocks after a gap. Claude Code keeps a spinner row and a context counter between the
   transcript and its input box; both change on every frame, so with the rows a tool call rewrites they exceeded the
   tail slack (3) and the frame was stored again after a gap.
2. The clients recognise a tool call only as one line `Name(args)`. Multi-line calls, bare headers (`● Bash`) and
   MCP calls are taken for prose, and their indented lines follow.
3. Claude Code draws fenced code blocks without fences; only the syntax colours (ANSI palette) tell them apart. The
   markup turned each coloured token into inline code.

Phases (each with its tests):

- P1 daemon, history: profile key `info_regex` (chrome rows next to the status rows, not busy), built-in for claude;
  tail slack 8 and alignment on 3 matching lines.
- P2 daemon, code lines: profile key `code_ansi_colors`, `screen.CodeLine`, `ViewLine.code`.
- P3 desktop: `session.Items` (bold-name tool headers, call continuation vs output, fenced code, multi-line prompt).
- P4 mobile (agentd-app, its own agent): the same client rules in Dart, selectable chat text, release.

Status: P1 to P3 done. P4 delegated (mailbox #777, #780).
