# AGED-US-0005 Option detection (menus, numbered lists, yes/no)

Plan: [[plans/api-gateway-plan]]. Design: [[design/api/semantic-model]] §6-§7.

## What changed
Pure option detection on a `screen.Snapshot` plus a pure key plan for choosing an option, and the
`menu_*` profile keys. No daemon wiring yet (op `act choose` verifies and executes the plan later).

- `screen.DetectOptions(s, inputRow, rules) []Option`: numbered lists (`1.`, `1)`, `[1]`), pointer menus,
  checkbox lists, yes/no prompts. Returns the lowest menu on screen (lists before yes/no), filtered by
  `MinConfidence`. `inputRow` (-1 unknown) and rows below it are ignored.
- `screen.DetectHints(s, inputRow, rules) []Hint`: "Esc to cancel" -> `{esc, cancel}`.
- `screen.PlanChoose(opts, target, appCursor) []KeyStep` / `PlanChooseKeys(..., ChooseKeys)`.
- Config: `menu_pointers`, `menu_number_regex`, `menu_selected_styles`, `menu_select`, `menu_nav_up`,
  `menu_nav_down`, `menu_submit`, `menu_toggle_key`, resolved into `Profile.Menu` (`MenuProfile`).
  Built-ins: default = all glyphs; claude `["❯"]`; codex `["›"]`; pando = default.

## Rules
- Claude's `❯`: the caller's input row is never a menu row; a pointer row needs aligned siblings; the
  block is discarded if the cursor is visible on one of its rows; a 2-row pointer block with a visible
  cursor gets 0.5 (< `option_min_confidence` 0.6).
- Numbered list with pointer or exactly one reverse-styled row: 0.9. Without either: 0.7 only if a line
  ending in `?` is within `ContextRows` (6) above or a hint row follows, else discarded (prose).
  Numbers must be consecutive from 1, share a column, >= 2 items; continuation rows join the label.
- Checkboxes (`[ ] [x] ◯ ◉ ☐ ☑`, optional pointer prefix): 0.9 with pointer/style, else 0.7.
  Markdown `- [ ]` is not a checkbox list.
- Yes/no: `(y/n)`, `[Y/n]`, `(yes/no)`; 0.8; upper-case = Selected; `Keys` = the token as shown;
  `Submit` when the cursor is right after the prompt.
- Cell index == column (wide-char `Width:0` continuation cells are kept and skipped in labels).
- Deviation: option `ID` = `"o"+8hex(sha1(kind + all normalised labels, index, label))`; the dialog
  question is not hashed because it can re-wrap with the terminal size. Pointer moves do not change IDs.
- `PlanChoose`: digit (+ Enter marked `IfNotGone`) for numbered; letter (+ submit) for yes/no; arrows
  from the selected option (SS3 when `appCursor`) then Enter (`Expect.Gone`); checkboxes end with the
  toggle key instead of Enter; `nil` = not navigable (unknown target, no known selection, no digit).

## Files & symbols
`internal/screen/options.go` (Option, Hint, MenuRules, DefaultMenuRules, DetectOptions, DetectHints,
Expect, KeyStep, ChooseKeys, PlanChoose, PlanChooseKeys), `internal/screen/options_test.go`,
`internal/config/menu.go` (MenuConfig, MenuProfile), `internal/config/menu_test.go`,
`internal/config/config.go` (embedded `MenuConfig` in ProfileConfig, `Profile.Menu`, defaults, merge,
validate, resolve, copy).

## Verification
`gofmt -l .` clean; `go vet ./internal/screen ./internal/config`; `go test -race ./internal/screen
./internal/config` pass (table tests on synthetic grids; no agent CLIs).
