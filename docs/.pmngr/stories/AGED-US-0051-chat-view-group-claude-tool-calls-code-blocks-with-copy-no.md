---
id: AGED-US-0051
type: story
title: "Chat view: group Claude tool calls, code blocks with copy, no repeated transcript"
status: backlog
priority: high
created: 2026-11-23T12:11:30Z
updated: 2026-11-23T12:11:30Z
---

Tool calls and their output leak into the chat prose (multi-line calls, bare headers, MCP calls), code is not shown as a code block with a copy button, and the transcript repeats blocks after a gap. Daemon (history, info_regex, code lines) + desktop parser; mobile port in agentd-app.
