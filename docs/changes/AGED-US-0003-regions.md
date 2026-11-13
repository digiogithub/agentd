# AGED-US-0003: region split (transcript, input, footer, status, dialog)

Links: [[plans/api-gateway-plan]] [[design/api/semantic-model]] (section 4) [[ghost-text]]

## What changed
`screen.Split(snap, rules) Regions` classifies every screen row and extracts the input line (ghost
text reported separately), footer, status line, dialog and the reflowed transcript.
Ghost-cell classification moved from `internal/session` into `internal/screen` and is shared.

## Files and symbols
- `internal/screen/regions.go`: `Split`, `Regions`, `Region` (`transcript input footer dialog status`
  plus `rule`), `InputInfo`, `DialogInfo`, `InputEmpty/InputSuggestion/InputDraft`.
- `internal/screen/ghost.go`: `GhostColumns(row, curX, cols, GhostRules)`, `GhostText`,
  `WithoutColumns`, `ColorMatches`.
- `internal/screen/rules.go`: `Rules` (ghost rules and prompt marker, `PromptRegex`,
  `InputBgColors`, `RuleChars`, `FooterRegex`, `BusyRegex`, `SpinnerChars`, `NoJoin`, `WrapMargin`,
  `DialogContextRows`, `Menu MenuRules`).
- `internal/session/ghost.go`: `type GhostRules = screen.GhostRules`; `ghostCells` delegates to
  `screen.GhostColumns`; the style/colour helpers were removed (exported API and tests unchanged).
- `internal/config/regions.go` + small hooks in `config.go`: profile keys `rule_chars`,
  `input_bg_colors` (codex built-in `#1e1e1e`), `footer_regex`, `busy_regex`, `spinner_chars`,
  `reflow_break_regex`, `wrap_margin`, `dialog_context_rows` with validation, resolved into
  `Profile.Regions`.
- Tests: `regions_test.go`, `regions_fixture_test.go` (claude post-turn captures, fake-box-ghost,
  fake-dialog, fake-menu), `internal/config/regions_test.go`.

## Algorithm
Rule rows (>= 80 % rule glyphs spanning >= 50 % of the width) and border-only rows are
`rule` rows. Input row, first match: cursor right after the marker; typed draft (cursor on/below
the marker row inside the same box); hidden cursor + `prompt_regex` row that is not a menu pointer
row (aligned sibling check); cursor row fully on an `input_bg_colors` background; no marker
configured: cursor in the bottom third. Box = nearest rules/frame borders (or the bg extent).
Footer = rows below the box (or trailing `footer_regex` rows). Dialog = framed block not holding
the input, else the option list found by `DetectOptions` plus context rows above. Status = busy /
spinner rows reachable from the box going up over blank rows only. Everything else is transcript
(scrollback included on the main screen).

## Verification
`gofmt -l .`, `go vet ./internal/screen ./internal/session ./internal/config`,
`go test -race ./internal/screen ./internal/session ./internal/config`: all green.
