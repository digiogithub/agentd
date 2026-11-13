# Wave 2 review fixes

Follow-up to [[plans/api-gateway-plan]] wave 2, the semantic screen layer ([[design/api/semantic-model]] §3-§7).
Builds on [[changes/AGED-US-0002-reflow]], [[changes/AGED-US-0003-regions]],
[[changes/AGED-US-0004-status-classifier]], [[changes/AGED-US-0005-option-detection]] and
[[changes/AGED-US-0006-fixtures-fakeagent]]. Safety principle: a FALSE option is dangerous (op `act choose`
types keys into the agent), so every heuristic prefers a missed option to an invented one.
Regression tests: `internal/screen/review_test.go`, `internal/config/menu_test.go`,
`internal/session/snapshot_test.go`, `internal/session/ghost_test.go`.

## Per item

1. **Unstyled numbered prose lists** (`internal/screen/options.go`, `detectNumbered`, `tailClear`,
   `questionAdjacent`, `optLine.hintOnly`). An unstyled list is an option list only when it is the last content
   block (only blanks, rules/borders and hint-only rows below it, up to the input row or screen bottom) AND a
   `?` line is directly above (at most one blank row). A hint row alone no longer qualifies. With an input row
   visible it gets 0.5 (< MinConfidence). Same position + question rule for unstyled checkbox lists (`●`/`○`
   bullets are prose as often as checkboxes). Why: "Steps:" lists next to a `?` or an "enter the value" sentence
   became options at 0.7. The old test `TestInputRowExcluded` encoded the bug (unstyled list above an input row
   was detected) and was changed to a pointer list plus an "unstyled above input is none" assertion.
2. **Dialogs from low-confidence lists** (`regions.go` `findDialog`, `realDialogOptions`). Without a visible
   input prompt a list needs a pointer / selected style (or a cursor on a yes/no prompt) to become a dialog;
   unclaimed rows stay transcript. Also: box tables (column junctions `┬┴┼` on the frame) are no longer taken
   for framed dialogs (`tableFrame`).
3. **Option identity and choose safety** (`options.go` `finish`, `Option.MenuFingerprint`, `Expect.Fingerprint`,
   `PlanChooseKeys`). IDs are `sha1(fingerprint, index, label)`; the fingerprint hashes kind + normalised
   (whitespace/case-insensitive) question + labels. Plans put the fingerprint on every step. `KeyStep`/`Expect`
   docs now bind the executor: require a newer `OutputSeq` and a changed screen after each key before
   evaluating `Expect`; evaluate `SelectedID`/`Checked` only on a menu with the same fingerprint; send the
   `IfNotGone` Enter only if the same-fingerprint menu is still present and its pointer is on the target, never
   for a different or identical-looking new dialog. Limitations (doc in options.go): a question wrapping into
   more than ContextRows rows, scrolled menus and menus whose labels truncate differently change the ID.
4. **Pointer menus** (`pointerMenuPlausible`). Glyph/style pointer menus need: pointer line <= 80 runes of
   label, no row starting with `⎿ ● ⏺`, and last-content-block position or a frame. `>` removed from
   `screen.DefaultMenuPointers` and the config default (`internal/config/menu.go`, design doc table updated);
   still configurable. `TestMenuPointersMatchScreenDefaults` cross-checks both.
5. **Input vs menu rows** (`regions.go` `findInput`, `menuRow`, `hasMenuSibling`, `numberedPointer`). Rules
   1/1b/2 never pick a row matching `^\s*❯\s*\d+[.)]\s` or with an aligned sibling row; continuation rows of a
   multi-row draft, hint rows and footer-regex rows are not siblings.
6. **Ghost in `Input.Text`** (`buildInput`, `ghostCursorX`). With a hidden or parked cursor a synthetic cursor
   at the first non-blank cell after the marker feeds `GhostColumns`. Rules with no style rule (Codex: every
   cell right of the marker is ghost) skip this while a keystroke draft is pending, since the text could be it.
7. **Reflow** (`reflow.go` `canJoin`, `codeish`, `startsStructural`, `fenceMask`). No joins for code-ish rows
   (indent >= 4 that is not a hanging indent; `;{}()` at row end on non-full rows), inside ``` fences, or when
   the next row starts with punctuation/bullet/number marker (opening quotes/brackets/backticks excepted). The
   exact-width autowrap join needs width >= 20 and a continuation at the same indent; one extra blank column is
   still accepted (the blank the cut fell on), which the fake-wrap-80/120 goldens require. `Row.Wrapped` joins
   skip the heuristics. Goldens unchanged.
8. **Frames** (`ReflowOpts.StripFrames`, `newRowViewOpt`). `│` borders are stripped only for Dialog (and
   question) reflow; transcript rows (tables) keep them and never join. `TestReflowFrame` sets the option.
9. **Yes/no** (`detectYesNo`). Keys = first letter (`y`/`n`); in line mode (cursor on the prompt row after it)
   the shown word (`yes`/`no` for `(yes/no)`) plus Submit. The prompt must be on the cursor row or be the last
   content row above the input row/bottom (rules skipped) and not start with `⎿ ● ⏺`.
10. **ID limitations** documented in the options.go header (see item 3).
11. **Labels** from full `Cell.Text` (`optLine.sub`): combining marks and ZWJ sequences survive; matching still
    uses one rune per cell.
12. **Hints**: `DefaultHintPattern` requires `to` and has no leading `\b` (so `↑/↓ to select` works);
    "hint-only" rows are anchored and <= 12 words.
13. **Snapshot perf** (`internal/session/snapshot.go`). Only the requested `ScrollbackTail` rows were already
    copied under the lock (`Scrollback.Lines()` returns the slice without copying); the zero `SnapOpts` is 0 =
    none, now documented. New per-session cache keyed by (OutputSeq, resolved tail), reset on `Resize`; the
    returned `*Snapshot` is a fresh copy with current activity fields, sharing the immutable Screen/Scrollback
    (documented on `screen.Snapshot`).
14. **`ghostCells`** rejects (cursor out of range, no visible cell at or right of the cursor) before copying the
    row.
15. **Config**: `reflow_break_regex` matching `""` is rejected; `menu_number_regex` must match a sample
    (`1. x`, `1) x`, `[1] x`, `❯ 1. x`) with n = 1 and a non-empty label.
16. **Menu submit**: `menu_submit = ""` (default) resolves to `Profile.Submit` when the profile is built
    (`config.go`), so `Profile.Menu.Submit` is never empty.

## Verification
`gofmt -l .` clean, `go vet ./...` clean, `go test -race -count=1 ./...` all green. Goldens untouched.
