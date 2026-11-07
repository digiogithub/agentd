# Spike: `github.com/charmbracelet/x/vt` as the headless screen

Version: `x/vt v0.1.0` (with `ultraviolet v0.0.0-20260303162955`, `x/ansi v0.11.7`).
Date: 2026-11-06. Probed through `internal/session` (PTY + `SafeEmulator`), 100x30.

## What it supports

| Feature | Status | Notes |
|---|---|---|
| Main + alternate screen | yes | `?1047`, `?1049` (saves/restores cursor), `IsAltScreen()`, `AltScreen` callback. Alt screen is cleared on entry. |
| Scrollback | yes, main screen only | 10 000 lines (`DefaultScrollbackSize`), `Scrollback().Lines()`; lines are pushed on scroll at top of region and on `ED 2` (clear screen pushes the visible content into history). Alt screen has none. |
| Terminal replies | yes, via `Read()` | Replies are written to an **unbuffered `io.Pipe`** while holding the emulator lock. Someone must drain `Read()` continuously, or `Write()` blocks forever. `internal/session` pumps it into a bounded queue and then into the PTY. |
| DA1 `CSI c` | yes | `ESC[?62;1;6;22c` |
| DA2 `CSI > c` | yes | `ESC[>1;10;0c` |
| CPR `CSI 6n` / DECXCPR `CSI ?6n` | yes | correct, 1-based |
| DSR `CSI 5n` | **buggy** | answers `ESC[?0n` instead of `ESC[0n` |
| DECRQM `CSI ? n $p` | yes | 2004/1049 report `2` (reset) by default; unknown modes report `0` |
| OSC 10/11/12 color queries | yes | fixed defaults: fg white `rgb:ffff/ffff/ffff`, bg black `rgb:0000/0000/0000` |
| Kitty keyboard query `CSI ?u` | no reply | apps that send it followed by DA1 fall back correctly |
| XTVERSION `CSI >q`, XTGETTCAP `DCS +q`, XTWINOPS `CSI 14t/18t` | no reply | apps relying on them must time out |
| In-band resize (mode 2048) | yes | reported on resize |
| Synchronized output (mode 2026) | not implemented | DECRQM reports `0` (not recognized), so well-behaved apps don't use it. Setting it is stored and fires `EnableMode`, but rendering is not deferred. Raw bytes are passed to attached clients, so their real terminal still honours it. |
| Bracketed paste (2004) tracking | yes | mode table + `EnableMode`/`DisableMode` callbacks (no public getter: `session` tracks it via callbacks, `BracketedPasteEnabled()`). |
| Other mode tracking | via callbacks | callbacks fire for every DECSET/DECRST, including unknown modes; `session` keeps the DEC modes it re-applies on redraw (1, 7, 1000–1006, 1015, 2004). |
| Window title (OSC 0/2) | yes | `Title` callback → `Session.Title()` |
| SGR colors/attrs, OSC 8 hyperlinks | yes | `Render()` emits per-line SGR diffs and links, resetting at end of each line |
| Wide chars, emoji, ZWJ sequences | yes | CJK, skin-tone and ZWJ family emoji render and replay correctly (grapheme width mode `?2027` supported) |
| Cursor style / visibility | yes | `CursorStyle()`, `CursorHidden()` |

## Render fidelity

`Render()` joins lines with bare `\n` and omits trailing blank cells, so it
cannot be written as-is to a raw-mode terminal. `Session.Redraw()` instead
emits: `ESC[?2026h`, `ESC[?1049h` if in alt screen, `ESC[0m ESC[r ESC[H ESC[2J`,
each non-empty row positioned with `ESC[<row>;1H`, tracked modes, DECSCUSR,
cursor position and visibility, `ESC[?2026l`.

Verified by replaying `Redraw()` into a fresh emulator and comparing `String()`
and cursor position against the session (`TestSpikeRealTUI`, `TestAltScreenRedraw`,
`TestSubscribeWithRedrawNoGap`): identical for **htop** (alt screen, colors, meters),
**less** (alt screen), **nano** (alt screen, bracketed paste enabled, reverse video
bars), **top** (main screen, full redraws) and the fake agent.

## Limitations found

1. **Thread-safety gaps in `SafeEmulator`**: `String()`, `Scrollback()` (returns the
   live buffer), `SetCallbacks`, `Close()` vs `Read()` (`closed` flag) are not
   synchronized. `session` serializes every emulator mutation under its own
   `outMu` and reads under the read lock, never calls `emu.Close()` (it closes the
   reply pipe writer instead).
2. **State not exposed, so not restorable on redraw**: scroll region (DECSTBM),
   current SGR pen, origin mode, G0–G3 charsets, keypad mode, kitty keyboard
   flags, tab stops. A TUI that set a scroll region before a client attached will
   keep scrolling within it, but the client terminal doesn't know about it until
   the TUI re-emits it (most full-screen TUIs repaint often enough). A SIGWINCH
   nudge (resize) after attach forces most TUIs to repaint completely.
3. **Duplicate terminal replies when a client is attached**: the emulator answers
   queries and the raw stream also reaches the client's real terminal, which
   answers too. The second answer arrives as "typed" input. `internal/attach`
   should filter terminal responses (DA, CPR, DECRPM, OSC 10/11 replies) from the
   client's stdin, or the daemon should drop emulator replies while a client is
   attached. To decide in Phase 2.
4. **Fixed default colors**: OSC 11 always reports a black background, so agents
   that auto-detect light/dark theme will pick dark. `SetDefaultBackgroundColor`
   could be made configurable if needed.
5. DSR 5 reply bug (above); no XTVERSION/XTGETTCAP/kitty keyboard answers.
6. Package doc in v0.1.0 still says "SKIP: Fix typecheck errors"; API is young,
   pin the version.

## Trying a real TUI manually

The probe test is skipped unless `AGENTD_SPIKE_CMD` is set. It runs the command
under `sh -c` in a 100x30 session, waits `AGENTD_SPIKE_SETTLE` (default 2s),
optionally types `AGENTD_SPIKE_KEYS`, freezes the child with SIGSTOP, logs the
emulated screen/mode state and checks that `Redraw()` reproduces it.

```sh
export PATH=$HOME/.go/bin:$PATH
AGENTD_SPIKE_CMD="htop" go test -run TestSpikeRealTUI -v ./internal/session/
AGENTD_SPIKE_CMD="vim -u NONE /etc/hosts" AGENTD_SPIKE_KEYS=$'jjdd' go test -run TestSpikeRealTUI -v ./internal/session/
# Real agents consume paid usage: only run them by hand, never in CI.
AGENTD_SPIKE_SETTLE=8s AGENTD_SPIKE_CMD="claude" go test -run TestSpikeRealTUI -v ./internal/session/
```

Probe terminal replies with a shell (note `timeout --foreground`, otherwise `cat`
runs in a background process group and gets SIGTTIN):

```sh
AGENTD_SPIKE_CMD='f=$(mktemp); stty raw -echo; printf "\033[c"; timeout --foreground 1 cat > "$f"; stty sane; od -c "$f"' \
  go test -run TestSpikeRealTUI -v ./internal/session/
```

Pending for Phase 4 (manual, paid): Claude Code, Codex and Pando TUIs — check
startup queries (they send DA1 / OSC 11 / kitty keyboard queries), synchronized
output usage, and that `prompt_regex` matches `Session.Screen()`. Startup captures
of the input line (ghost text, prompt markers, cursor) are in `docs/ghost-text.md`:
Claude Code 2.1.319 and Codex 0.166.0 both run in the alternate screen and render
correctly in `x/vt`.
