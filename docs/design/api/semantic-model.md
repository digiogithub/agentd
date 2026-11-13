# Design: semantic screen model (`internal/screen`)

Status: **draft** (2026-11-13). Plan: [[plans/api-gateway-plan]] (stories API-S01..S06; ADR-A02,
A03, A04, A05 are binding here). Background: [[ghost-text]], [[spike-vt]], `AGENTS.md` ("Idle
detection", "Ghost text", "Injection"). Consumers: [[design/api/daemon-ops]] (`view`, `act`,
`subscribe`), [[design/api/http-api]].

## 1. Scope and non-goals

`internal/screen` turns a session's terminal state into a **View**: status, reflowed text with role
hints, the input line, detected options and hints. The View never depends on the PTY size: it has
logical lines, not grid rows (ADR-A02). It is the L1 layer of ADR-A03.

In scope:
- An atomic `Snapshot` type taken from `internal/session` (the only new session API, §2).
- Pure functions `Reflow`, `Split`, `Classify`, `DetectOptions`, `Build` over a `Snapshot` and a
  `Rules` value derived from the harness profile. No I/O, no clock reads (time comes in the snapshot).
- A small stateful `History` (transcript accumulator for alternate-screen TUIs, §3.5), owned by the
  daemon per session; its `Merge` is deterministic given its inputs.
- A choose *planner* (§6.5): pure function from (View, option id) to a key plan. Executing the plan
  (writing keys, waiting, re-verifying) is the daemon's `act` op, built on the safe-input path
  extracted from `internal/delivery` (API-S08).

Non-goals:
- **No harness-specific Go code** (ADR-A04). Claude Code, Codex and Pando differ only by profile data
  (§7). A harness with an empty profile still gets text, status from timers, and generic options.
- No structured chat transcript (L2, ADR-A11): no "messages", "tool calls" or "turns" in v1. Roles are
  hints on lines, not a conversation model.
- No pixel/grid fidelity: L0 (`dump --ansi`, raw stream) covers that.
- No PTY resizing (ADR-A02) and no change to `delivery.Idle` semantics: delivery keeps its own gate;
  the classifier reuses its inputs (§5).
- Dependencies: `internal/screen` depends only on the stdlib and `internal/termcolor`. It defines
  the `Snapshot` type; `internal/session` imports `screen` to produce it (§2). Profile → `Rules`
  conversion lives in `internal/daemon` (`screenRules(config.Profile) screen.Rules`), so neither
  `screen` nor `config` import each other. `AGENTS.md`'s contract table gets the new row in the same
  commit.

## 2. Snapshot

One read-locked copy of everything interpretation needs, so every rule sees the same instant.
New method `(*session.Session).Snapshot(opts screen.SnapOpts) *screen.Snapshot`, taken under
`outMu.RLock` (like `Screen()`), so it is consistent with the emulator, the cursor, the title and the
mode table. `internal/session` imports `internal/screen` for the types only.

```go
package screen

type Attr uint16 // Bold, Faint, Italic, Underline, Blink, Reverse, Conceal, Strike

type Cell struct {
	Text  string // grapheme; "" for empty; continuation cells of wide chars are dropped
	Width uint8  // 1 or 2
	Attrs Attr
	Fg, Bg string // same encoding as session.CellInfo: "" default, "N" palette index, "#rrggbb"
}

type Row struct {
	Cells   []Cell // len ≤ Cols; scrollback rows are already trimmed by x/vt
	Wrapped bool   // emulator autowrap continued this row onto the next; always false with x/vt v0.1.0 (§3.1)
}

type Snapshot struct {
	Taken        time.Time
	OutputSeq    uint64 // count of PTY output chunks fed to the emulator (new session counter)
	Cols, Rows   int
	Cursor       Pos    // 0-based, screen coordinates
	CursorHidden bool
	AltScreen    bool
	Screen       []Row  // len == Rows
	Scrollback   []Row  // tail of the main-screen scrollback, oldest first (≤ SnapOpts.ScrollbackTail)
	ScrollbackLen int   // total lines in x/vt scrollback at capture (for History alignment)
	Title        string // OSC 0/2
	Cwd          string // OSC 7 if seen, else /proc/<pid>/cwd (Linux), else Spec.Cwd
	AppCursor    bool   // DECCKM (mode 1): arrows must be sent as SS3 (ESC O A)
	BracketedPaste bool
	// Activity (same sources as delivery.Idle).
	State        string // "starting" | "running" | "exited"
	ExitCode     *int
	StartedAt, LastOutput, LastHuman, DraftSince time.Time
	// Notifications since the session started (monotonic counters + last payload).
	Bells        uint64
	Notifies     uint64    // OSC 9 (excluding 9;4 progress) and OSC 777;notify
	LastNotify   Notify    // {At time.Time; Title, Body string}
}

type SnapOpts struct{ ScrollbackTail int } // default 500 rows, 0 = none
```

Session additions needed: `OutputSeq` (incremented in `handleOutput` under `outMu`), `Bell`
callback, `WorkingDirectory` callback, and OSC handlers registered with
`emu.RegisterOscHandler(9, …)` / `(777, …)` (exported on `vt.Emulator` through its embedded
`handlers`; today x/vt logs both as unhandled). OSC `9;4;…` is ConEmu progress, not a notification,
and is ignored. Copying 40–50 rows of cells is cheap; the scrollback tail is bounded by `SnapOpts`.

## 3. Logical-line reflow (API-S02)

Output of reflow: `[]Line` where `Line{Text string; Rows int; Role Role; Style LineStyle; Src Span}`.
`Src` keeps the grid origin (screen row range or scrollback index) for debugging and for §4/§6; it is
never exposed as layout in the API.

### 3.1 Wrap information: not available from x/vt v0.1.0

Checked in the module cache (`x/vt v0.1.0`, `ultraviolet v0.0.0-20260303162955`): `uv.Line` is
`[]uv.Cell`, `uv.Cell` has `Content, Style, Link, Width` and **no wrap flag**. Autowrap is handled in
`handleGrapheme` (`utf8.go`): with `atPhantom && DECAWM` it calls `e.index()` (the same path as a
linefeed, it does not record the wrap) and then writes at x=0. `Scrollback.Push` trims trailing empty
cells, so scrollback rows also lose their original width. `Emulator.Resize` → `Buffer.Resize`
**truncates or pads** rows; it does not reflow (shrinking a local attach loses the right part of each
row until the TUI repaints). There is no callback that would let `session` observe the wrap either
(`CursorPosition` fires on moves, indistinguishable from CR+LF).

Consequence: `Row.Wrapped` stays in the type (set by a future emulator or an upstream patch, §9) but
reflow must work without it. Moreover the harnesses that matter (Claude Code, Codex: Ink/ratatui TUIs)
**word-wrap themselves** and position every row explicitly, so even a perfect autowrap flag would not
mark their wraps. Joining is therefore heuristic in all cases.

### 3.2 Row → text

Per row: concatenate `Cell.Text` (empty cell = space), drop continuation cells of wide characters
(width comes from `Cell.Width`, column arithmetic uses cells, never rune counts), map U+00A0 to a
space **only for matching** (the text keeps it), trim trailing blanks (space, NBSP, styled blanks).
Ghost cells (§4.2) are removed from the text and reported separately. Box borders of a detected frame
(§4.3: `│ ┃ ║` at the frame's left/right column) are stripped with their padding.

### 3.3 Join rule (soft-wrap heuristic)

Rows r and r+1 of the same region and frame are joined into one logical line when all hold:
1. both are non-blank and `Row(r).Wrapped` is true, **or** the heuristic below holds;
2. *autowrap shape*: row r's last non-blank cell is at column `avail-1` (`avail` = frame inner width
   or `Cols`) → join **without** a separator (character wrap, also long words / URLs); or
   *word-wrap shape*: `len(r) + 1 + width(firstWord(r+1)) > avail - wrap_margin` (the next row's first
   word would not have fit) → join with one space;
3. r+1's indent equals r's **content indent** (r's indent, or r's hanging indent after a list marker
   such as `⏺ `, `● `, `- `, `* `, `• `, `1. `, `│ `);
4. r+1 does not start with a list marker, a prompt marker, a menu pointer, a box-drawing char, or
   match `rules.NoJoin` (profile `reflow_break_regex`);
5. neither row is in a region marked verbatim: rows whose cells share a non-default background over
   their whole width (code blocks, diffs) or that contain diff gutters (`+`/`-` after a line number)
   are kept as-is.

`wrap_margin` (default 2) absorbs TUIs that wrap a few columns early. Wrong joins only merge two
display rows; the original row count is kept in `Line.Rows` and `Line.Joined` (reason code) for
golden-test diagnostics.

### 3.4 Scrollback + screen (main-screen harnesses)

When `AltScreen` is false, the text is `Scrollback ⧺ Screen` reflowed as one stream (a logical line
may start in scrollback and continue on screen). Blank rows below the last non-blank screen row are
dropped. x/vt pushes the whole screen into scrollback on `ED 2` (clear screen), so a TUI that clears
and repaints on the main screen (e.g. `top`) duplicates frames; History dedup (§3.5) removes runs
that equal the previous frame.

### 3.5 Alternate-screen TUIs: the transcript History

Claude Code 2.1.319 and Codex 0.166.0 run in the **alternate screen** ([[ghost-text]]), which has no
scrollback in x/vt: anything the TUI scrolls out of its viewport is gone from the emulator. To return
more than one screen of text, the daemon keeps a per-session `History`:

```go
type History struct {
	Committed []Line // oldest first, capped at rules.HistoryMaxLines (default 5000) / 1 MiB
	prev      []Line // transcript-region lines of the last merged snapshot
	Gaps      int    // discontinuities seen (frames that could not be aligned)
}
func (h *History) Merge(cur []Line) (committed int)
```

Algorithm (only the **transcript region** of §4 is merged; input, footer and dialogs never are):
1. Normalise lines for comparison (strip spinner glyph at line start per `transcript_volatile_regex`,
   collapse runs of digits in elapsed-time counters like `(12s`, trim).
2. Find the largest k such that `prev[k:]` is a prefix of `cur` (allowing the last 3 lines of `prev`
   to differ: the live tail mutates while streaming). Lines `prev[:k]` scrolled out: append them to
   `Committed`. If `k == 0` and the frames are equal or `cur` extends `prev`, nothing to commit.
3. No alignment (full repaint, `/clear`, a dialog replaced everything): commit the `prev` lines that do
   not occur in `cur`, append a `Line{Role: RoleGap}` marker, `Gaps++`.
4. On leaving the alternate screen (`AltScreen` true → false) commit `prev` entirely.

The daemon merges on every **settled frame**: after output has been quiet for 60 ms, and at least
every 250 ms during continuous output (driven from the session output notification, not from API
reads, so history accumulates with no client connected). Cost: one snapshot + reflow per merge on ~50
rows, negligible. The View's text = `Committed ⧺ transcript lines of the current snapshot`.

Honest limits: lines that scroll through faster than the merge cadence between two settled frames
are lost (fallback: L0 log); TUIs that collapse or rewrite earlier output ("ctrl+o to expand",
re-rendered tool blocks) leave both versions in History or are only partially aligned; identical
repeated lines can misalign by a few rows; History is in memory only and restarts with the daemon.
Replaying `logs/<session>.log` into an emulator **of the same size** can rebuild History offline
(used by tests, §8, and optionally at daemon start for sessions whose log survives), but a taller
emulator would not help: the TUI lays out for the PTY size it was told.

## 4. Region split (API-S03)

`Split(snap, rules) Regions` assigns every screen row one of: `transcript`, `input`, `footer`,
`dialog`, `status` (spinner/working line). Scrollback rows are always `transcript`.

### 4.1 Rules (horizontal separators)

A *rule row* is a row whose non-blank cells are ≥ 80 % `rules.RuleChars` (default `─━═╌┄`) and span
≥ 50 % of the width. Rule rows separate regions and never appear in the text.

### 4.2 Input line and ghost text

Candidate input row, first match wins:
1. the cursor row when the text left of the cursor (NBSP and spaces trimmed) ends with
   `prompt_marker` (same check as `GhostRules.RightOfCursor`), cursor visible;
2. the last row matching `prompt_regex` (on the ghost-filtered text) below the last transcript text,
   when the cursor is hidden;
3. the cursor row when its whole width shares a background listed in `input_bg_colors` (Codex: every
   cell of the input row has bg `#1e1e1e`);
4. no profile marker: the cursor row if the cursor is visible and in the bottom third of the screen.

The input **box** extends from the candidate row up/down to the nearest rule rows (Claude Code draws
the box between two `─` rules), so multi-row drafts are included. Ghost text is classified with the
existing `session.GhostRules` logic (moved/shared, not duplicated: `ghostCells` becomes a function over
`[]Row` in `screen`, and `session.ScreenFiltered` calls it). Input = `{text, ghost, draft}`: `text`
is the box content after the marker with ghost cells removed; `draft` is the keystroke-based
`DraftSince` < 10 min (never inferred from the screen); `state` = `draft|suggestion|empty` exactly as
`delivery.InputState`. Known limit kept from [[ghost-text]]: ghost text wrapped onto a second row is
only detected on the cursor row.

### 4.3 Footer, status line, dialogs

- **Footer**: rows below the input box (Claude: `? for shortcuts`, mode lines), or, without a box,
  trailing rows matching `footer_regex`. Footer text feeds hints (§6.4) and `busy_regex` (§5), not the
  transcript.
- **Status line**: rows between the transcript and the input box matching `busy_regex` or starting
  with a glyph in `spinner_chars`; reported as `View.Activity` text, not committed to History.
- **Dialog**: a framed block (`╭…╮`/`╰…╯`, `┌…┐`, or a block bounded by rule rows) **or** the block of
  rows containing a detected option list (§6) plus up to `dialog_context_rows` (default 6) non-blank
  rows above it (the question). When a dialog is present and no input candidate exists, the dialog
  owns the bottom of the screen (Claude replaces its input box with permission menus).
- **Transcript**: everything else above, with rule rows and empty frame borders removed.

## 5. Status classifier (API-S04)

`Classify(snap, regions, opts, rules) Status`. Inputs are the snapshot timers (`LastOutput`,
`StartedAt`, `LastHuman`, `DraftSince`) and the profile durations already used by `delivery.Idle`
(`idle_ms`, `MinBoot` = 3 s). First match wins:

| state | rule |
|---|---|
| `exited` | `State == "exited"` (exit code in `Status.ExitCode`). |
| `starting` | registry `starting`; or no output yet; or `now - StartedAt < max(idle_ms, 3s)`. |
| `waiting` | options detected with confidence ≥ `option_min_confidence` in a dialog/footer, **or** `waiting_regex` matches the filtered screen; and `busy_regex` does not match the status/footer rows; and output quiet ≥ `min(idle_ms, 500ms)` (dialogs are static). |
| `working` | output within `idle_ms`; or `busy_regex` matches status/footer rows (spinner paused but the harness says "esc to interrupt"). |
| `idle` | otherwise (quiet ≥ `idle_ms`). If `prompt_regex` is set and does not match the ghost-filtered screen, still `idle` but `Status.Reason = "no_prompt"` and `Confidence` 0.4 (crashed into a shell, unknown screen). |

Differences from `delivery.Idle`, on purpose: human keystrokes (`human_grace_ms`) and pending drafts do
**not** make the agent `working`; they are reported in `Input` and `Status.HumanActive` instead. The
delivery gate is unchanged and keeps using `delivery.Idle`.

**Attention** (`Status.Attention`, with `Reasons []string` and `Since`): set when (a) the state goes
`working → waiting`, (b) `working → idle` after ≥ `idle_ms` of work (turn finished), (c) `Bells` or
`Notifies` increased (BEL, OSC 9 `9;<text>`, OSC 777 `notify;<title>;<body>`; payload in
`LastNotify`, gated by profile `notify_osc`). It is cleared by any `act` on the session or an explicit
acknowledgement (daemon-ops §3); it is daemon state (transition memory), so `Classify` returns the raw
signals and the daemon's per-session tracker owns the flag. BEL alone during `working` is ignored
(some TUIs beep on invalid keys); a BEL that coincides with `waiting`/`idle` counts.

## 6. Option detection (API-S05)

Scope: dialog rows, else the last 15 non-blank rows above the input/footer. Never the input line.

### 6.1 Numbered lists

Contiguous rows (continuation rows allowed when indented past the label column) matching
`menu_number_regex`, default `^\s*(?:(?P<ptr>[^\w\s])\s*)?(?P<n>\d{1,2})[.)]\s+(?P<label>\S.*)$`,
with numbers **consecutive from 1** and a shared number column. Selected = the row whose `ptr` group is
in `menu_pointers` or whose label cells carry a `menu_selected_styles` attribute. Confidence 0.9 with a
pointer or selected style, 0.7 without (could be a numbered list in prose: in the transcript, without
a pointer, a question line ending in `?` within the context rows is required, else discarded).

### 6.2 Pointer menus and the `❯` ambiguity

A block of ≥ 2 rows with the same text column where exactly one row has a glyph from `menu_pointers`
(default `❯ › > ▶ ▸ ➜ →`) at the pointer column and the others have a blank there (or one row is
`reverse`/selected-bg styled and its siblings are not). Claude Code uses `❯` both as prompt marker and
as menu pointer ([[ghost-text]]), disambiguated structurally, not by glyph:
- the row chosen as input line in §4.2 is never a menu row;
- a pointer row needs at least one **aligned sibling** row (same label column, pointer column blank);
  the prompt row has none (rule row above/below instead);
- a menu row's label is not faint and the cursor is not right after its marker (the input candidate
  test 4.2-1 fails), or the cursor is hidden.
Checkbox lists (`[ ]`, `[x]`, `◯`, `◉`) are pointer menus with `Option.Checked` set; `choose` toggles
(space, `menu_toggle_key`) and a separate confirm is needed (hint-driven, §6.4).

### 6.3 Yes/no prompts

The last non-blank row matching `(?i)[\[(]\s*(y(?:es)?)\s*/\s*(n(?:o)?)\s*[\])]` → two options; an
upper-case letter (`[Y/n]`) marks the default as `Selected`. Keys: the letter; plus `submit` when the
cursor sits on that row right after the prompt (line-mode `read`), otherwise the bare letter.
Confidence 0.8.

### 6.4 Hints

Footer/dialog text matching `(?i)\b(esc|enter|tab|shift\+tab|ctrl\+[a-z]|space|↑/↓|←/→)\s+(?:to\s+)?([a-z][\w '-]{1,30})`
gives `Hint{Key, Action}` ("Esc to cancel" → `{esc, cancel}`). Hints drive `act cancel` (prefer the
hinted key over `cancel_keys`), the confirm key for checkbox menus, and are returned to clients
verbatim. Profile `hint_regex` replaces the default.

### 6.5 Option model and `choose`

```go
type Option struct {
	ID         string  // "o" + 8 hex of sha1(dialog question ⧺ index ⧺ label); stable across revisions
	Index      int     // 1-based display order
	Label      string  // reflowed, continuation rows joined
	Kind       string  // "numbered" | "pointer" | "checkbox" | "yesno"
	Selected   bool
	Checked    *bool
	Keys       string  // direct key, e.g. "2" or "y" (empty: navigation only)
	Confidence float64 // 0..1
}
type Plan struct{ Steps []Step } // Step{Keys string; Expect Expect}; Expect{SelectedID string; Gone bool}
func PlanChoose(v *View, optionID string, rules Rules) (Plan, error)
```

Execution (daemon `act choose {name, revision, option_id, label}`), harness-agnostic:
1. Take a fresh snapshot and re-run detection; the option must exist with the same id **and** label,
   else 409 (plan §6 risk). The safe-input preconditions apply (no human keystroke within
   `human_grace_ms`, no pending draft), as for delivery.
2. Plan: if `Kind == numbered` and `menu_select != "arrows"`: send the digit; then per
   `menu_digit_action`: `select` = the digit is the whole plan (Expect: the menu *instance* is gone or
   changed; NEVER an Enter afterwards, it could confirm the next dialog); `move` = Expect `Gone` **or**
   `SelectedID == target` and then `menu_submit`, sent only if the same menu instance (fingerprint +
   screen context above the question + transcript tail) is still up with the pointer on the target. If
   `yesno`: the letter (+ submit per §6.3). Otherwise arrows: `delta = target.Index - selected.Index`,
   one `menu_nav_down`/`menu_nav_up` per step (default `ESC[B`/`ESC[A`, `ESC O B`/`ESC O A` when
   `AppCursor`), each followed by Expect `SelectedID == next`, then `menu_submit` (default the profile
   `submit`) with Expect `Gone` (the option list disappeared or its question changed).
3. After every step, poll snapshots (25 ms, up to 750 ms) until the Expect holds; re-check human input
   before each key. Any mismatch (pointer did not move, moved to the wrong label, dialog changed) stops
   immediately with no further keys and reports the observed View; nothing is "retried blindly".
4. No current selection known (no pointer, no style) and no digit: refuse with `not_navigable`; the
   client may fall back to `act keys`.

## 7. Profile rule additions

New keys under `[profiles.<name>]`, resolved like the existing ones (built-in default → built-in
harness → user default → user harness). All are data (ADR-A04).

| key | default | claude | codex | pando |
|---|---|---|---|---|
| `busy_regex` | `""` | `(?i)esc to interrupt` † | `(?i)esc to interrupt` † | `""` |
| `waiting_regex` | `""` | `""` | `""` | `""` |
| `spinner_chars` | `""` | `"✻✳✢✶✽·*"` † | `""` | `""` |
| `rule_chars` | `"─━═╌┄"` | = | = | = |
| `input_bg_colors` | `[]` | `[]` | `["#1e1e1e"]` | `[]` |
| `footer_regex` | `""` | `(?i)for shortcuts` † | `""` | `""` |
| `reflow_break_regex` | `""` | `^[⏺●⎿] ` † | `""` | `""` |
| `wrap_margin` | `2` | = | = | = |
| `transcript_volatile_regex` | `""` | spinner line † | `""` | `""` |
| `menu_pointers` | `["❯","›","▶","▸","➜","→"]` | `["❯"]` | `["›"]` | default |
| `menu_number_regex` | §6.1 | = | = | = |
| `menu_selected_styles` | `["reverse"]` | = | = | = |
| `menu_select` | `"auto"` (`auto`\|`digit`\|`arrows`) | = | = | = |
| `menu_digit_action` | `"move"` (`select`\|`move`) | `select` (to confirm, F8) | `select` (to confirm, F8) | `move` |
| `menu_nav_up` / `menu_nav_down` | `"auto"` (CSI or SS3 per DECCKM) | = | = | = |
| `menu_submit` | `""` = `submit` | = | = | = |
| `menu_toggle_key` | `" "` | = | = | = |
| `cancel_keys` | `"\x1b"` | = | = | = |
| `interrupt_keys` | `"\x03"` | `"\x1b"` | `"\x1b"` | `"\x03"` |
| `hint_regex` | §6.4 | = | = | = |
| `option_min_confidence` | `0.6` | = | = | = |
| `dialog_context_rows` | `6` | = | = | = |
| `history` / `history_max_lines` | `true` / `5000` | = | = | = |
| `notify_osc` | `true` | = | = | = |

† Provisional, from observation of the live harness, **not yet in a recorded capture**; API-S06 must
confirm or drop each one before it ships as a built-in. (Claude Code's `Ctrl-C` twice exits, so its
interrupt is Esc.) The fixed default size `cols`/`rows` (160x50) is specified in
[[design/api/daemon-ops]] §5.

Validation (in `ProfileConfig.validate`, error names the key): every `*_regex` compiles (RE2) and is
not empty-matching where a match means a state (`busy_regex`, `waiting_regex`, `footer_regex`:
reject a regex that matches `""`); `rule_chars`, `spinner_chars`, `menu_pointers` entries are
non-blank, single grapheme, width 1, not alphanumeric; colours parse with `termcolor.ParseAll`;
`menu_select` and `menu_selected_styles` are from their enums; key strings are non-empty, ≤ 32 bytes,
no `\x00`, and none contains the paste markers; `wrap_margin` 0..8; `option_min_confidence` 0..1;
`dialog_context_rows` 0..20; `history_max_lines` 0..100000 (0 = no History).
The daemon's `screenRules(config.Profile) screen.Rules` compiles everything once per resolved
profile (cached with it).

## 8. Test strategy (API-S06)

Never run a real agent CLI in tests (`AGENTS.md`). Four layers:

1. **Unit tests on synthetic grids**: `[]Row` built in Go (like `ghost_test.go`'s `cellGrid` fakes) for
   reflow join rules, wide characters (CJK, emoji, ZWJ: column math by cells), NBSP, trailing styled
   blanks, frames, rule rows, `❯` prompt vs `❯` pointer, yes/no, hints.
2. **Recorded captures → golden files**: `internal/screen/testdata/<harness>-<version>-<case>.log`
   holds raw PTY bytes (+ a sidecar `.json`: cols, rows, profile, timestamps of frame boundaries).
   Captured by hand with the throwaway method of [[ghost-text]] (fresh temp dir, nothing typed unless
   the case needs it, logs scrubbed of paths/usernames/secrets before commit). The test replays the
   bytes into an `x/vt` emulator through `session`'s `stringFilter` (exported test helper, so the OSC
   `0x9C` fix applies), builds a `Snapshot` at each frame boundary, feeds History, and compares the
   final `View` (JSON, timers frozen) with `<case>.golden.json`; `-update` rewrites goldens. Required
   cases: claude idle/placeholder, post-turn suggestion (from `testutil.ClaudePostTurn*`), working with
   spinner, permission dialog (numbered + `❯`), trust dialog, long answer scrolled out of the alt
   screen (History), codex idle, codex working, codex approval menu; pando chat input when available.
3. **fakeagent extensions** (`internal/testutil/fakeagent`, real PTY, `-race`): `-menu "A|B|C"`
   (pointer menu, arrow keys move `❯`, Enter prints `CHOSE: B`), `-numbered` (digits select;
   `-digitmove` makes a digit only move the pointer), `-yesno "Proceed?"`, `-dialog` (boxed `╭│╰` frame
   + `Esc to cancel` footer), `-box` (input between `─` rules with `❯ ` prompt), `-appcursor`
   (DECCKM on: only SS3 arrows work), `-bell`/`-osc9 "text"` after each turn, `-wrap N` (word-wrap at N
   columns like Ink), `-alt` long outputs (History). These drive end-to-end `view`/`act choose` tests,
   including refusals (option changed between view and act, human typing, pointer that does not move).
4. **Property tests**: reflow is idempotent across sizes for fakeagent `-wrap` output replayed at
   80/120/160 columns (same logical text); History never loses a line in a slow-scroll replay.

## 9. Open questions

1. **Wrap flags upstream?** Propose a per-line "wrapped" bit to `x/vt` / `ultraviolet` (set in
   `handleGrapheme` before `index()`, carried into `Scrollback.Push`)? It only helps main-screen
   output (plain CLIs, shells), not Ink/ratatui TUIs. Needs an `AGENTS.md` dependency note if pinned to
   a fork.
2. **History persistence**: rebuild from `logs/<session>.log` at daemon start (costly for 50 MB logs,
   and log rotation drops the head) or accept loss on restart?
3. **Merge cadence vs cost** with many sessions streaming at once (50 sessions × 4 merges/s): share
   one ticker, or merge lazily on `view` plus on every alt-screen exit / large scroll?
4. **Attention acknowledgement**: per client/device or global per session? (Global is simpler; two
   phones would clear each other's badge.)
5. **Digit semantics** across harnesses: does Codex's approval menu select on digit, or only move? The
   Expect step handles both, but the built-in `menu_select` and `menu_digit_action` (claude, codex = `select`, unverified guess) should match captures: F8.
6. Should `waiting` also cover "harness asks a free-text question" (no options, prompt visible, last
   transcript line ends with `?`)? Today that is `idle` + attention; a heuristic risks false waits.
7. Dialogs larger than the screen (long diffs in a permission prompt): options may be off-screen;
   report `View.Truncated` and leave scrolling to `act keys`? More `Line.Role` values (`code`,
   `diff`, `tool`) would help UIs but drift toward L2 (ADR-A11).

## Appendix: View (returned by op `view`)

```go
type View struct {
	Session   string
	Revision  uint64    // per session; +1 whenever the View content hash changes (daemon-owned)
	OutputSeq uint64    // snapshot's output counter (debugging, cheap "changed?" check)
	Taken     time.Time
	Status    Status
	Title     string    // window title (OSC 0/2), e.g. Claude's "✳ <topic>"
	Cwd       string
	Lines     []Line    // History.Committed ⧺ current transcript, tail-limited by the op's `lines` arg
	FirstLine int64     // absolute index of Lines[0] in the session's line stream (for SSE diffs)
	Activity  string    // status-line text ("✻ Thinking… (12s · esc to interrupt)")
	Dialog    *Dialog   // {Question string; Lines []Line; Options []Option; Hints []Hint}
	Input     Input     // {Text, Ghost string; State: "empty"|"suggestion"|"draft"; Draft bool; DraftSince time.Time}
	Options   []Option  // = Dialog.Options, or yes/no options outside a dialog
	Hints     []Hint    // footer + dialog hints
	Footer    []string
	Truncated bool      // History cap reached or dialog extends off-screen
}

type Status struct {
	State      string    // starting | working | idle | waiting | exited
	Since      time.Time // last state change (daemon tracker)
	Reason     string    // "", "no_prompt", "busy_regex", "options", "waiting_regex"
	Confidence float64
	Attention  bool; AttentionReasons []string // "turn_done", "waiting", "bell", "notify"
	Notify     *Notify
	HumanActive bool     // keystroke within human_grace_ms
	ExitCode   *int
}
// Line: {Text; Role transcript|status|dialog|gap; Rows int; Joined ""|autowrap|wordwrap|flag (debug)}
```
