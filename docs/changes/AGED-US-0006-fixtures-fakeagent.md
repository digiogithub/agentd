# AGED-US-0006: fixtures and fakeagent menu/dialog modes

Plan: [[plans/api-gateway-plan]]. Design: [[design/api/semantic-model]] §8 (test strategy), §6 (menus).

## What changed

- `fakeagent` gained widget and signal modes used by the screen/`view`/`act` tests.
- New package `internal/testutil/screenfix`: fixture loader, PTY-log replay into an x/vt
  `SafeEmulator` producing a `*screen.Snapshot`, and golden-JSON support (`-update`).
- New recorder `internal/testutil/screenfix/record` that records fakeagent runs in a PTY.
- Fixtures committed under `internal/screen/testdata/`.

## fakeagent flags (new; old flags unchanged)

`-menu "A|B|C"`, `-numbered` (+ `-digitmove`), `-yesno "text"`, `-dialog` (+ `-title`), `-box` (with
`-ghost`), `-appcursor`, `-bell`, `-osc9 "text"`, `-wrap` (terminal autowrap), `-wrapat N`
(hard wrap at N, Ink-like), `-loop` (redraw widget after a resolution). Widgets resolve with
`chose: <label>` / `cancelled`, emit the turn-end signals and exit 0. With `-appcursor` only SS3 arrows
move pointers, without it only CSI arrows. `-bell`/`-osc9` also apply to the plain prompt turns.

## Files and symbols

- `internal/testutil/fakeagent/main.go` (hooks), `widgets.go` (`runWidget`, `runMenu`, `runYesNo`,
  `runBox`, `readKey`, `turnEnd`, `writeParagraphs`), `widgets_test.go`.
- `internal/testutil/screenfix/`: `screenfix.go` (`Meta`, `Fixture`, `Load`, `LoadFrom`, `MustLoad`, `Save`,
  `Names`, `Dir`, `Fixture.Replay/ReplayAt/ReplayFrames`, `Replayer`), `golden.go` (`Golden`, `GoldenIn`,
  `Summary`, `Summarize`, `Span`), `cells.go` and `filter.go` (copies of session's unexported
  `copyRow/attrBits/colorString` and `stringFilter`; keep in sync, replace by exports if session gets them),
  tests `screenfix_test.go`, `fakeagent_test.go`.
- `internal/testutil/screenfix/record/main.go`: `go run ./internal/testutil/screenfix/record [names]`.
- Fixtures `internal/screen/testdata/<name>.{log,json,golden.json}`.

## Fixtures

fake-menu, fake-numbered, fake-yesno, fake-dialog, fake-box-ghost, fake-wrap-120 (120x40),
fake-wrap-80 (80x24), fake-wrapat-80, fake-appcursor-menu, fake-bell, plus the converted Claude captures
claude-post-turn-suggestion and claude-post-turn-empty (60x6, from `testutil.ClaudePostTurn*`).
Note: the Claude title `✳` replays as U+FFFD (the string filter's 0x9C fix); that is the live behaviour.

## Why

Layered test strategy of semantic-model §8 (layers 2 and 3), without ever running a real agent CLI.
Values marked † in the semantic model remain to be confirmed against real captures (F8); these fixtures
come from fakeagent and Claude byte fixtures only.

## Verification

`gofmt -l .` clean, `go vet ./internal/testutil/...` clean,
`go test -race ./internal/testutil/... ./internal/session` green (also delivery and daemon). Tests: loader
round trip, deterministic replay (whole vs 7-byte chunks), goldens, fakeagent real-PTY widget behaviour
(arrow/digit/Esc/SS3 vs CSI, bell/OSC 9, box), wrap text invariance across widths.
Goldens: `go test ./internal/testutil/screenfix -update`.
