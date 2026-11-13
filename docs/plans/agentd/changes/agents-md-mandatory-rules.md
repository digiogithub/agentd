---
created_at: 2026-11-13T12:44:32.365928519Z
updated_at: 2026-11-13T12:44:32.365928519Z
tags:
    - change
    - docs
    - agents
---
# AGENTS.md: mandatory operating rules for AI agents

Related: [[AGENTS.md]] · [[api-gateway-plan]] · [[plans/api-gateway-plan]]

## What changed

Merged the canonical MANDATORY operating-rules checklist into `AGENTS.md` as one coherent section, written in the document's voice and adapted to this repo. Not a verbatim paste. Existing project content was left intact.

The new section sits after "What this is" and before "Toolchain", so agents see the rules before architecture. It covers, each labelled **MANDATORY**:

1. Gather context before any task (`kb_search_documents` without a tags filter, then `kb_related_documents` / wiki links, then `code_get_symbols_overview` / `code_find_symbol` / `code_hybrid_search` / `code_search_pattern`; `recall` only for a known key).
2. External research when the answer is not in the repo (Context7 `c7_resolve_library_id` + `c7_get_library_docs` for pinned deps; web search + `fetch`; browser tools for UI).
3. Plan before non-trivial work, saved with `kb_add_document` under `agentd/plans/…`; do not fork the existing `api-gateway-plan.md`.
4. Small verified increments: `gofmt`, `go vet`, `go test ./...` (`-race` for session/daemon/delivery); no real agent CLIs in tests.
5. Document every change with `kb_add_document` (`agentd/changes|fixes|features/<slug>.md`), including wiki links; defer the write while plan mode blocks it.
6. `remember`/`recall` only for short keyed facts; KB for anything extensive.
7. English, parallelize without losing context, confirm hard-to-reverse or outward-facing actions.

## Files & symbols touched

- `AGENTS.md` — inserted "## MANDATORY operating rules for AI agents" (clauses 1–7). Toolchain, Rules, Architecture, package contracts, key behaviours, Ops, and Identity were not rewritten.
- `CLAUDE.md` — unchanged symlink to `AGENTS.md`, so it picks up the same text.

## Why

The previous Rules section covered English, tests, package contracts, no tiling UI, and conventional commits, but did not require context recovery, external research, written plans, KB documentation of every change, or memory-tool choice. Those were missing relative to the canonical checklist and had to be added without dropping project-specific contracts.

## How it was verified

- Re-read the inserted section (lines 15–153) and the heading outline. All seven clauses are present and labelled MANDATORY.
- Heading grep confirms Toolchain, Rules, Architecture, Package contracts, Key behaviours, Ops, and Identity still follow the new section.
- `CLAUDE.md` confirmed as a symlink to `AGENTS.md` before editing, so one write covers both.
- No code change; `go test` not applicable. Markdown structure checked by re-read.
