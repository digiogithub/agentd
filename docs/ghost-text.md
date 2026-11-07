# Ghost text: placeholders and suggested prompts in harness input lines

Harnesses draw text in their input line that is **not user input**: a startup placeholder
(`Try "how does <filepath> work?"`, `Ask Codex to do anything`) and, after a turn, a *suggested next
prompt* (e.g. `❯ He creado el repo de GitHub, mergea crawl/repo`). Agents that read the screen
(formerly via zellij dumps) mistook it for a human draft every time. agentd calls it *ghost text*,
hides it from `dump`, never counts it as a draft, and never lets delivery submit it.

## Captures (2026-11-07)

Method: a throwaway Go program started each TUI once in a PTY (120x40, `TERM=xterm-256color`,
`COLORTERM=truecolor`, `CLAUDECODE*`/`AGENTD_*` stripped) fed into an `x/vt` emulator (with the
emulator's query answers written back, like `internal/session`), waited 8 s, recorded every cell of
the screen (content, fg/bg, attributes) and the cursor, then killed the process group. **Nothing was
typed or submitted.** cwd was a fresh empty temp dir (for Claude Code, one inside a directory it
already trusts, so the trust dialog did not appear; for Codex the temp dir was marked trusted with
`-c projects."<dir>".trust_level="trusted"`, a CLI override that writes nothing).

For Claude Code, the post-turn suggested prompt has since been checked on a live session; see
"Post-turn suggestions" below. For Codex it is still assumed to look like the placeholder.

### Claude Code 2.1.319

Alternate screen. Input box between two `─` rules (fg `#888888`). Input row 35:

| x | content | fg | bg | attrs |
|---|---|---|---|---|
| 0 | `❯` | default | default | — |
| 1 | U+00A0 (no-break space) | default | default | — |
| 2–32 | `Try "how does <filepath> work?"` | default | default | **faint** (SGR 2) |
| 33–119 | spaces | default | default | — |

Cursor: visible, at (x=2, y=35) = **first cell of the ghost text**, right after `❯` + NBSP.
Raw bytes: `ESC[39m❯\xa0ESC[2mTry "how does <filepath> work?"…ESC[22m`. Secondary UI text uses
fg `#999999` (not faint), so faint alone identifies the placeholder. Notes: the marker is followed
by a **no-break space**, not a space (a `prompt_regex` requiring `❯ ` would never match); the same
`❯` glyph is used as the selection pointer in Claude Code menus (permission/trust dialogs), so a
`prompt_regex` on `❯` alone does not distinguish them; the input box is also visible while Claude
works, so `prompt_regex` cannot express "idle" either.

### Post-turn suggestions (verified on Claude Code 2.1.319)

Method (2026-11-07, read-only): the PTY log of a live `claude@<project>` session (`logs/<session>.log`,
120x40) was replayed into a fresh `x/vt` emulator; that reproduced the daemon's screen, cursor
and title exactly. Two turns were checked. Input row 36, after the turn:

| x | content | fg | bg | attrs |
|---|---|---|---|---|
| 0 | `❯` U+276F | default | default | — |
| 1 | U+00A0 | default | default | — |
| 2–22 | `Did you get the pong?` | default | default | **faint** |
| rest | blank | default | default | — |

Cursor: (x=2, y=36), the first cell of the suggestion. So it looks just like the startup
placeholder, and the existing rules (faint + right of `❯`) classify it. Raw bytes of the last
frames: `…ESC[37;3H` (the input line was drawn earlier as `❯\xa0 ` + `ESC[K`), then
`ESC[2mDid you get the pong?ESC[22m ESC[40;1H ESC[37;3H`. Claude Code does not erase after the
suggestion; it relies on the line being empty. The other turn showed no suggestion at all: the
input line was `❯\xa0` with the cursor at x=2.

**What looked like an unhidden suggestion was an emulator bug.** Before the fix, the live rows
read `❯\xa0 agentd test` (no suggestion) and `❯\xa0Did you get the pong? agentd` (with
`agentd` not faint and not hidden), and `ls` showed the title as `�`. During a turn Claude Code
sets the window title to `<spinner> <topic>`, e.g. `ESC]0;✳ agentd ping test BEL`, while
the cursor is parked on the input line. `✳` is U+2733 = `E2 9C B3`, and the x/ansi parser
(v0.11.7, also v0.11.8) ends OSC/DCS/SOS/PM/APC strings at **any** `0x9C` byte (8-bit ST), even a
UTF-8 continuation byte. So the title became `"\xe2"`, and ` agentd ping test` was
printed at the cursor, into the input line. The faint suggestion overwrote x=2–19 and left
` agentd` behind. A real UTF-8 terminal shows neither.

Fix (`internal/session/oscfix.go`): before the emulator, a streaming filter replaces each
complete UTF-8 rune that contains `0x9C` inside an ESC-introduced control string with U+FFFD. A
lone `0x9C` still terminates the string. The raw stream (log, ring buffer, attached clients) is
not touched. Titles now read `� agentd ping test`. The fixtures are in
`internal/testutil/claude_fixtures.go`.

Diagnostics: `agentd dump <name> --cells [--row N]` prints the cursor and every non-blank cell
of a row (rune, fg, bg, attributes, width). Without a live daemon, replay
`logs/<name>.log` into an `x/vt` emulator of the session's size.

### Codex CLI 0.166.0

Alternate screen. The input row 36 has background `#1e1e1e` on every cell:

| x | content | fg | bg | attrs |
|---|---|---|---|---|
| 0 | `›` (U+203A) | default | `#1e1e1e` | bold |
| 1 | space | default | `#1e1e1e` | — |
| 2–25 | `Ask Codex to do anything` | default | `#1e1e1e` | **faint** |
| 26–119 | spaces | default | `#1e1e1e` | — |

Cursor: visible, at (x=2, y=36) = first cell of the ghost text, right after `› `. Codex also draws a
lot of ordinary text faint elsewhere (version, cwd, help lines `/init - create an AGENTS.md…`),
so faint cells must only be considered on the input line.

### Pando v1.5.3

In a directory without `.pando.toml` Pando generates one and opens its **first-run Settings screen**
(alternate screen, cursor hidden at the bottom row): no chat input line was on screen, so there is
nothing to classify yet. Pando keeps the generic defaults until a capture of its chat input exists.

## Rules implemented

Scope is only the **input line**: the cursor's row, from the cursor column to the right edge.
Profile settings (`config.toml`, `[profiles.<name>]`):

| key | default | meaning |
|---|---|---|
| `ghost_faint` | `true` | faint (SGR 2) cells in scope are ghost |
| `ghost_colors` | `[]` | fg colours marking ghost cells: palette index `"8"`, `"244"` or `"#RRGGBB"` |
| `ghost_right_of_cursor` | `false` (claude/codex: `true`) | ghost only when the text left of the cursor ends with `prompt_marker` (spaces/NBSP ignored); if no style rule is set, every non-blank cell in scope is ghost, otherwise a cell must match a style rule too |
| `prompt_marker` | `""` (claude `❯`, codex `›`) | the harness's prompt glyph |

Built-in `prompt_regex` (matched on the ghost-filtered screen, trailing spaces trimmed):
claude `(?m)^❯([ \x{a0}]|$)`, codex `(?m)^›( |$)`, pando none. They only check that the harness's
input line is on screen (e.g. not a crashed shell); idle still comes from output quiescence.

Typed text is never faint in either harness, and once a human types the ghost text disappears, so
`ghost_right_of_cursor` + `ghost_faint` keeps a draft whose cursor was moved back (Home) visible.

## Delivery safety

1. `prompt_regex` runs on the screen with ghost text blanked (`delivery.GhostOpts(profile)`).
2. Drafts are keystroke-based (`session.HumanDraftSince`): ghost text is child output and never
   counts.
3. Before pressing Enter the loop checks the paste landed: it polls the filtered screen for up to
   1 s for `[agentd msg #<id> from` (whitespace-insensitive, so wrapped/indented notices match). If
   it is not there, **no submit** is sent (Enter on an empty Claude Code input may submit the
   suggestion), the message stays queued and is retried after the cooldown.

Known limits: a paste longer than Claude Code's paste-collapse threshold (not measured here; believed
to be several hundred characters) shows as `[Pasted text …]` and would fail verification, so the
message would never be submitted. Notices are single-line and capped (`inline_max`, default 400, plus
a ~150–200 char header); lower `inline_max` if a harness collapses them. A paste that renders only after the 1 s window stays in
the input line un-submitted until the retry pastes the notice again. Multi-row ghost text (a
suggestion wrapped onto the next row) is only hidden on the cursor's row.

## Reproducing

The capture program is not part of the repo (it runs paid harnesses). Equivalent manual check with
the spike probe, which logs the screen but not cell attributes:

```sh
AGENTD_SPIKE_SETTLE=8s AGENTD_SPIKE_CMD="claude" go test -run TestSpikeRealTUI -v ./internal/session/
```

For a live session: `agentd dump <name> --ghost` (raw) vs `agentd dump <name>`, and
`agentd ls --json | jq '.[].input_state'`.
