---
id: AGED-US-0005
type: story
title: Option detection (menus, numbered lists, yes/no)
status: done
priority: high
parent: AGED-EP-0001
milestone: AGED-M-0001
labels: [screen, plan:api-s05]
estimate: 8
created: 2026-11-13T13:52:41Z
updated: 2026-11-13T14:33:42Z
started: 2026-11-13T14:06:36Z
closed: 2026-11-13T14:33:42Z
---

## Description
Detect numbered lists, pointer menus (`❯`, `>`, `›`), checkboxes, yes/no prompts and footer hints; option model `{id, label, selected, confidence, keys}`; Claude's `❯` disambiguated structurally (input row is never a menu row, menu rows need aligned siblings). New profile keys `menu_*` per semantic-model §7.

## Acceptance Criteria
- Stable option ids across redraws of the same menu (fingerprint).
- Navigation plan: digit if numbered, else arrows from current pointer (SS3 in application cursor mode) + Enter.
- Profile keys validated in `internal/config` with built-in defaults for claude/codex/pando/default.
- `gofmt`/`go vet` clean; `go test -race` green for the touched packages; no real agent CLI in tests (bash/sh/cat/fakeagent only).

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[design/api/semantic-model]] §6-§7
