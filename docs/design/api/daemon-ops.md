# Design: daemon ops `view`, `act`, `subscribe` (API gateway, phase 1)

Status: **design** (2026-11-13). Plan: [[plans/api-gateway-plan]] (stories API-S07..S12; the
ADRs in its §3 are binding). Screen interpretation (`internal/screen`, `screen.View`, statuses,
option detection) is specified in [[design/api/semantic-model]]; this page only consumes it.
The HTTP mapping of these ops is [[design/api/http-api]].

## 1. Overview and package contract changes

Three new socket ops, all usable by the CLI, `agentd mcp` and `agentd serve` (ADR-A01, A05):

| Op | Shape | Purpose |
|---|---|---|
| `view` | request/response | Semantic view of one session (L1): status, reflowed lines, input line, options |
| `act` | request/response | Safe, verified write: prompt, choose an option, abstract keys, interrupt, cancel |
| `subscribe` | upgrades the connection to a JSON-lines event stream | Lifecycle, status, view revisions, mail |

Supporting changes: a shared **safe-input** package used by both delivery and `act` (§4), a fixed
PTY size policy (§5), a daemon **event bus** (§6) and a structured error `code` in `Response` (§3.5).

Rows to add / edit in the `AGENTS.md` package table (done in the same commit as the code, per the
AGENTS.md rule; listed here, not applied):

| Package | Responsibility | Depends on |
|---|---|---|
| `internal/screen` *(new)* | Pure interpretation of a session snapshot → `View` (see semantic-model) | session (snapshot types), config, termcolor |
| `internal/inject` *(new)* | Safe input path: draft/human guards, bracketed paste, paste verification, submit, key sequences | session, config |
| `internal/delivery` *(edit)* | Decide when a session is idle and inject queued messages **via `inject`** | session, mailbox, config, **inject** |
| `internal/proto` *(edit)* | … + `View`, `ActArgs`, `Event` wire types, error codes | stdlib |
| `internal/daemon` *(edit)* | … + view cache/revisions, event bus, `view`/`act`/`subscribe` handlers | all above |

Other `AGENTS.md` edits: "Ops" list gains `view`, `act`, `subscribe`; "Wire protocol" gains the
`code` field and the subscribe upgrade; "Key behaviours" gains the size policy (§5); "Injection"
points at `internal/inject`. Dependency direction stays acyclic: `daemon → {delivery, screen,
inject} → session/config`; `delivery → inject`; nothing below `daemon` imports `daemon` or `proto`.

## 2. Op `view`

```go
// proto
const OpView = "view"

type ViewArgs struct {
	Name          string `json:"name"`
	SinceRevision uint64 `json:"since_revision,omitempty"` // 0 = always full
	LinesLimit    int    `json:"lines_limit,omitempty"`    // last N logical lines; 0 = default 500, max 10000
	Scrollback    bool   `json:"scrollback,omitempty"`     // include scrollback before the screen
}

type ViewResult struct {
	Name      string `json:"name"`
	Revision  uint64 `json:"revision"`
	Unchanged bool   `json:"unchanged,omitempty"` // since_revision == revision: View omitted
	View      *View  `json:"view,omitempty"`
}
```

`proto.View` is a **wire mirror** of `screen.View` (same pattern as `proto.Message` vs
`mailbox.Message`: `proto` stays stdlib-only so `internal/httpapi` needs only `client`+`proto`);
the daemon converts with a `toWireView` function. Field list and semantics: semantic-model §2.

```json
{"op":"view","args":{"name":"claude@webapp","since_revision":41,"lines_limit":200}}
{"ok":true,"data":{"name":"claude@webapp","revision":43,"view":{
  "revision":43,"status":"waiting","attention":true,"title":"✳ Fix login",
  "lines":["● I need to edit src/auth.ts.","","Do you want to make this edit?"],
  "lines_truncated":true,
  "input":{"text":"","ghost":"","draft":false},
  "options":[{"id":"o1","label":"Yes","selected":true,"key":"1","confidence":0.95},
             {"id":"o2","label":"Yes, and don't ask again","selected":false,"key":"2","confidence":0.95},
             {"id":"o3","label":"No, and tell Claude what to do differently","selected":false,"key":"3","confidence":0.95}]}}}
```

No identity check (same as `dump`): the socket is already 0600 and per-uid. Exited sessions return
their final view with `status: "exited"`. `starting` sessions → error code `starting`.

### 2.1 Cost, caching and revisions

Interpretation is not free (snapshot under `outMu`, reflow, region split, option regexes), so the
daemon keeps a per-session **view cache** in `entry`:

```go
type viewCache struct {
	mu       sync.Mutex
	gen      uint64      // session.OutputGen() + size + profile epoch the expensive part was computed at
	base     screen.View // screen-only view (no scrollback), expensive part
	view     screen.View // base + cheap fields (status, attention, input.draft) as last published
	revision uint64      // bumps when view's fingerprint changes
	fp       [32]byte    // sha256 of the canonical JSON of view minus revision
	history  [8]struct{ rev uint64; optFP [32]byte } // option fingerprints of recent revisions (§3.3)
}
```

- `session` gains `OutputGen() uint64` (atomic, incremented in `handleOutput` after the emulator
  write and in `Resize`) and `Changed() <-chan struct{}` (capacity-1, coalescing, single consumer:
  the daemon's watcher). No callback into the daemon, so no import cycle.
- **Expensive part** (snapshot → lines/input/options/title) is recomputed only when `gen` changed.
  **Cheap part** (status from timers: idle_ms, human grace, draft; attention) is recomputed on
  every `view` call and on watcher ticks, because `working → idle` happens with *no* output.
- **Revision**: per-session `uint64`, starts at 1 when the session registers, **bumps iff the
  fingerprint of the published view changes** (lines, input, options, status, attention, title).
  Pure output that leaves the reflowed view identical (cursor blink, spinner redraw of the same
  frame) does not bump. Revisions are only meaningful within one daemon lifetime; `ViewResult`
  does not need a daemon epoch because `subscribe` carries one (§6).
- **Watcher** (one goroutine per running session, started in `run`, ends on `Done`): waits on
  `Changed()`, debounces **100 ms** (trailing edge, max latency 250 ms during continuous output),
  recomputes, and if the revision bumped publishes `view.changed` / `status.changed` /
  `input.changed` (§6). It also arms a timer at `LastOutput + idle_ms` (and `+ human_grace_ms`) to
  catch the silent transition to `idle`. Worst case ≈ 4 recomputes/s per busy session, screen only.
- `scrollback: true` and `lines_limit` beyond the screen are computed **on demand** in the request
  goroutine (never by the watcher) and never cached; they reuse the current revision number since
  the revision tracks the screen view. Hard cap: 10 000 logical lines / 4 MiB of JSON
  (`proto.MaxLineSize` is 8 MiB).

CLI: `agentd view NAME [--json] [--scrollback] [-n LINES] [--watch]`. Text output prints the lines,
then a status footer, the input line and numbered options:
`[agentd: waiting · rev 43]`, `options: [1] Yes  [2] Yes, and don't ask again  [3] No…`.
`--watch` uses `subscribe` + `view` to reprint on `view.changed` (no curses).

## 3. Op `act`

```go
const OpAct = "act"

type ActArgs struct {
	Name     string   `json:"name"`
	Action   string   `json:"action"`              // prompt | choose | keys | interrupt | cancel
	Text     string   `json:"text,omitempty"`      // prompt
	OptionID string   `json:"option_id,omitempty"` // choose
	Label    string   `json:"label,omitempty"`     // choose: expected label (recommended)
	Revision uint64   `json:"revision,omitempty"`  // view revision the client acted on; REQUIRED for choose, optional (checked when set) for the rest
	Epoch    string   `json:"epoch,omitempty"`     // daemon epoch of that revision; required whenever revision is set
	Keys     []string `json:"keys,omitempty"`      // keys: abstract key names (§3.2), max 32
	Force    bool     `json:"force,omitempty"`     // override draft / status preconditions
}

type ActResult struct {
	Revision  uint64 `json:"revision"`            // view revision after the action settled (best effort)
	Submitted bool   `json:"submitted,omitempty"` // prompt: submit sequence written
	Warning   string `json:"warning,omitempty"`   // e.g. "human typed during submit delay"
}
```

### 3.1 Actions

| Action | Effect | Preconditions (without `force`) |
|---|---|---|
| `prompt` | `inject.Input` with `Text`, verify (tied to the paste, §4), re-check right before Enter (`BeforeSubmit`), submit (profile `submit`) | running; status `idle` or `waiting` without options; no pending draft (human-typed or pasted-and-not-submitted); empty input line; no human keystroke within `human_grace_ms`; text non-empty (a non-blank line), ≤ 64 KiB |
| `choose` | select option `OptionID` (§3.3) | running; status `waiting` (or `idle` with options shown); no pending draft; no real human keystroke within `human_grace_ms`; option, epoch, revision and menu instance re-verified |
| `keys` | write the mapped byte sequences, 30 ms apart | running; no pending draft; `enter` on an empty input with a ghost suggestion visible and no menu is `not_ready` (never forced); `ctrl-c`/`ctrl-d` need `working` or `waiting` like `interrupt` |
| `interrupt` | profile `interrupt_keys` (default `["ctrl-c"]`; claude, codex `["esc"]`) | status `working` or `waiting` (an interrupt at `idle` may quit the harness: Ctrl-C/Ctrl-D) |
| `cancel` | profile `cancel_keys` (default `["esc"]`): dismiss menu / clear selection | running |

**Revision for every action.** `prompt`, `keys`, `interrupt` and `cancel` accept an optional `revision`
(+ `epoch`, required with it; omitted by the CLI for back-compat, always sent by the app). When set, the
daemon compares the **interaction fingerprint** of the current view with the one recorded for that
revision (`menuTrack.noteInter`, same 256-revision history as the menu instances): status, menu instance
and its options (ids, pointer, checks), dialog presence/question, and pending-draft state. Transcript
lines, title, cwd, idle time and a ghost suggestion appearing are NOT part of it, so transcript-only
changes pass. A different fingerprint, an unknown (never seen / evicted) revision or a revision from the
future is `conflict` with `data = {revision, view}` and **no key is sent** (for `prompt` the check runs
in the inject guard under the input lock, before the paste). `choose` rules are unchanged.

`force` skips status and draft/human checks only; it never skips paste verification, option
re-verification or permissions. Every `act` is logged to `daemon.log` (`act`, session, action,
caller, revision) — the gateway's audit log (security-remote §5) builds on it.

### 3.2 Abstract key names

Names are lowercase; modifiers `ctrl-`, `alt-`, `shift-` (only where listed). Cursor keys honour
DECCKM: the session already tracks DEC private modes (`decModes`); with mode 1 set, arrows use SS3.

| Name | Bytes | Name | Bytes |
|---|---|---|---|
| `enter` | profile `submit` (default `\r`) | `tab` / `shift-tab` | `\t` / `ESC [ Z` |
| `esc` | `\x1b` | `backspace` | `\x7f` |
| `up` `down` `right` `left` | `ESC [ A/B/C/D` (DECCKM: `ESC O A/B/C/D`) | `home` / `end` | `ESC [ H` / `ESC [ F` |
| `pageup` / `pagedown` | `ESC [ 5 ~` / `ESC [ 6 ~` | `delete` | `ESC [ 3 ~` |
| `ctrl-a` … `ctrl-z` | `0x01` … `0x1a` | `alt-<x>` | `\x1b` + `<x>` |
| `space` | ` ` | `char:<c>` | one printable rune (e.g. `char:y`, `char:1`) |

Unknown names fail the whole request with `invalid` before anything is written. The table lives
in `internal/daemon/keys.go` (`resolveKeys`). Draft-neutral keys (escapes, Enter, Ctrl-C) are written with
`Session.WriteAct` (activity recorded in `LastActInput`, never in `LastHumanInput`); printable keys are
plain writes so they never create a "draft".

### 3.3 `choose`: resolution and re-verification

Unit of identity: the menu **instance** (`viewState.instanceFor`) = menu fingerprint (kind, question,
labels) + `screen.MenuContext` (hash of up to 8 non-blank rows above the dialog question) + transcript
length and tail. Option ids stay fingerprint-based (stable across redraws); every conflict check uses the
instance, so an identical-looking dialog for another command is a different instance.

1. `revision` and `epoch` are REQUIRED (missing → `invalid`); an `epoch` other than the daemon's →
   `conflict`. (Any act that carries a `revision` must carry its `epoch`.)
2. Force a view refresh, take a fresh snapshot and re-run option detection (never trust cached
   coordinates; a local attach may have resized, plan §6). The debounced watcher is not relied on.
3. Find `OptionID` in the fresh options; missing → `conflict` ("option gone").
4. If `Label` is set and differs from the fresh label → `conflict`.
5. `Revision` must be the current revision, or the menu instance recorded for it (`menuTrack`, keyed by
   revision) must equal the current instance; otherwise `conflict` (text output changes alone do not
   invalidate a still-identical menu instance).
6. Plan (`screen.PlanChooseKeys`) per profile key `menu_digit_action`:
   - numbered option with a digit, `select`: the digit ONLY, never an Enter afterwards; verified by the
     instance disappearing or changing. `move`: the digit, then Enter only if the same instance is still
     on screen and the pointer moved to the target.
   - no digit (or `menu_select = "arrows"`): arrows from the selected option, each verified, then Enter.
   - yes/no: the key (word + Enter in line mode).
   Built-in defaults: `claude`, `codex` = `select` (to confirm against the real TUIs, F8,
   semantic-model §7); `default`, `pando` = `move`.
7. Executor contract: after every key wait for new output and a changed screen, then judge `Expect` only
   against the menu with the plan's fingerprint AND instance. **Every step after the first requires the
   same instance on screen before its key is written** (yes/no word + Enter included); otherwise
   `conflict` and no key (an `IfNotGone` step ends the plan as done when the menu is gone). A pointer that
   did not land → `conflict`, no Enter.
8. The conflict error carries the fresh view so the client can redraw without a second round trip:
   `{"ok":false,"code":"conflict","error":"option o2 changed","data":{"revision":44,"view":{…}}}`. The view
   is built for the real caller: a session without view permission gets no `data`.

### 3.4 Identity and permissions

`act` resolves the caller like `type` (it is on the identity-dependent list). Rule, consistent with
`type`: **humans may act on any session; a session may act only on itself unless `[security]
agent_type = true`.** The gateway is always `human:<user>` (ADR-A06). `view` and `subscribe` need
no identity (like `dump`/`ls`); restrictions for agents are applied at the MCP layer (§7).

### 3.5 Structured error codes (backward compatible)

```go
// proto
type Response struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`  // may also accompany an error (conflict view)
	Error string          `json:"error,omitempty"`
	Code  string          `json:"code,omitempty"`  // new; absent = "" = unclassified
}

type CodedError struct { Code, Msg string; Data any; Err error } // Err = wrapped cause
func (e *CodedError) Error() string { return e.Msg }
func (e *CodedError) Unwrap() error { return e.Err }
func Errorf(code, format string, a ...any) error // supports %w (Err = the wrapped error)

// RemoteError gains Code and Data; ErrorResponse uses errors.As(err, *CodedError).
```

Codes: `invalid` (malformed or invalid arguments; the name `bad_request` of the first draft was
not adopted), `not_found`, `starting`, `exited`, `exists` (name already taken), `forbidden`, `conflict` (option
moved/changed, stale revision), `busy` (pending human draft or recent keystroke), `not_ready`
(status precondition), `verify_failed` (paste/selection not visible; nothing submitted), `timeout`
(PTY write timeout), `internal`. Old clients ignore `code`; old daemons never send it, so clients
must treat `""` as `internal`. Existing ops adopt codes opportunistically (`lookup` → `not_found` /
`starting`, identity errors → `forbidden`). Gateway mapping: `conflict`, `busy`, `not_ready` → 409,
`exists` → 409, `not_found` → 404, `forbidden` → 403, `verify_failed` → 422, `timeout` → 504,
`invalid` → 400. Daemon mapping: `session.ErrWriteTimeout` → `timeout`, `inject.ErrPasteNotVisible` →
`verify_failed`, purge argument errors → `invalid`.

## 4. Shared safe-input path (`internal/inject`)

Today the safe path is private to `delivery.Loop.inject`. It moves to a new package that both
`delivery` and `daemon` import (it must not live in `daemon`, which `delivery` cannot import, nor
in `session`, which must stay free of profiles/policy).

```go
package inject

// Target is the subset of *session.Session the injector needs (delivery.Target embeds it).
type Target interface {
	State() string
	LastHumanInput() time.Time
	HumanDraftSince() time.Time
	ScreenFiltered(opts session.ScreenOpts) (screen, ghost string)
	Paste(text string) error
	Write(p []byte, human bool) (int, error)
}

type Request struct {
	Text   string        // pasted as bracketed paste (session.PasteSafe applies)
	Needle string        // tied to the paste (see below); "" = first 48 runes of the first non-blank line of the paste-safe Text; none = ErrEmptyNeedle
	Submit bool          // write the profile submit sequence after the delay
	Guard  func() error  // run right before the paste; non-nil error aborts (nothing written)

	InputRegion  func() string // optional: text of the input region (screen.Split) for verification
	BeforeSubmit func() error  // optional: runs right before the submit; error = no submit, text stays pasted (draft), error returned
}

type Result struct { Pasted, Submitted, HumanRaced bool }

var (
	ErrDraft          = errors.New("pending human draft")
	ErrPasteNotVisible = errors.New("pasted text not visible on screen; not submitted")
	ErrEmptyNeedle     = errors.New("nothing to verify the paste with (no non-blank line)")
)

func Input(ctx context.Context, t Target, p config.Profile, r Request) (Result, error)
func GhostOpts(p config.Profile) session.ScreenOpts          // moved from delivery
func DraftPending(t Target, now time.Time) bool              // < DraftTimeout
func PasteVisible(screen, needle string) bool                 // moved from delivery
```

`Input` = exactly today's `inject`: snapshot `LastHumanInput`, `Guard`, paste, poll every 25 ms up
to `PasteVerifyTimeout` (1 s), sleep the remainder of `submit_delay_ms` measured from the paste,
note `HumanRaced` if a keystroke arrived, write submit. Review amendments: the needle is the first
non-blank line (none = `ErrEmptyNeedle`, nothing written); verification is tied to the paste (needle
occurrences on the ghost-filtered screen grew versus a baseline taken just before the paste, or it appears
in the input region where it was not before); `BeforeSubmit` can veto the Enter (text stays, recorded
as a draft with `Session.MarkDraft`; a no-submit paste is a draft too, a submit clears it). Delivery becomes:

```go
res, err := inject.Input(ctx, tgt, prof, inject.Request{
	Text: FormatNotice(m, prof), Needle: noticePrefix(m), Submit: true,
	Guard: func() error { if !Idle(tgt, prof, l.now()) { return errNotIdle }; return nil },
})
if res.HumanRaced { log.Warn("delivery: human typed during the submit delay; submitting anyway") }
```

`delivery.GhostOpts`, `delivery.PasteVisible`, `delivery.ErrPasteNotVisible` stay as thin aliases
(`var ErrPasteNotVisible = inject.ErrPasteNotVisible`) so `daemon` and existing tests compile
unchanged; the delivery tests (`delivery_test.go`, `daemon/delivery_e2e_test.go`) must pass
untouched — that is the acceptance criterion of API-S08. `act prompt` passes a `Guard` that checks
`DraftPending` (→ `busy`), human grace (→ `busy`) and status (→ `not_ready`) unless `force`.

**Paste collapsing.** Claude Code replaces long pastes with `[Pasted text #1 +N lines]`, so the
prefix needle never shows up and verification would fail for long prompts. Add profile rule
`paste_placeholder_regex` (claude built-in `\[Pasted text #\d+`): a match that appears in the input
region after the paste also counts as visible. Delivery notices are short enough not to trigger it.

Serialization: `act` and delivery must not interleave on one session. `inject` keeps a per-target
mutex (`inject.Lock(name)` keyed map) held across guard→paste→verify→submit; delivery and `act`
both take it. A held lock → `act` waits up to 2 s, then `busy`.

## 5. Fixed PTY size policy (ADR-A02)

- Profile gains `cols`/`rows` (built-in default **160x50**; `[profiles.X] cols = …`).
  `session.DefaultCols/Rows` (120x40) remain only as the library fallback.
- `run`: `RunArgs.Cols/Rows` are still honoured when non-zero (explicit request), else the profile
  size. The CLI **stops sending its terminal size** by default; `agentd run --size 200x60` sets it
  explicitly. The gateway never sends sizes.
- API clients never resize: `view`, `act`, `subscribe` have no size fields; the future L0 stream
  uses `attach` with `"passive": true` (new `AttachArgs.Passive`): read-only, never `activate`d,
  `R` frames ignored, input frames rejected.
- Local `agentd attach` keeps today's behaviour (most recently active client's size wins).
  When the **last** non-passive client detaches, the daemon resizes back to the profile size
  (`[run] restore_size_on_detach`, default `true`) so API views are deterministic again.
- `SessionInfo.cols/rows` already report the live size; `View` carries no geometry beyond what the
  semantic model needs.

## 6. Op `subscribe` and the event bus

```go
const OpSubscribe = "subscribe"

type SubscribeArgs struct {
	Sessions    []string `json:"sessions,omitempty"`     // exact names; empty = all
	Kinds       []string `json:"kinds,omitempty"`        // prefixes: "session", "status", "view", "input", "mail"; empty = all
	SinceSeq    uint64   `json:"since_seq,omitempty"`    // replay from the daemon ring if possible; REQUIRES epoch (else invalid)
	Epoch       string   `json:"epoch,omitempty"`        // epoch the since_seq belongs to
	HeartbeatMS int      `json:"heartbeat_ms,omitempty"` // default 15000, min 1000
}

type SubscribeResult struct {              // the OK response line, before the upgrade
	Epoch    string        `json:"epoch"`    // random per daemon start; seq/revisions reset with it
	Seq      uint64        `json:"seq"`      // last seq reflected in Sessions: no later event predates the snapshot
	Sessions []SessionInfo `json:"sessions"` // snapshot (filtered), so clients start consistent
}

type Event struct {
	Seq     uint64          `json:"seq"`               // monotonic per epoch (0 for control events: hello, resync, heartbeat, bye)
	Epoch   string          `json:"epoch"`
	Time    time.Time       `json:"time"`
	Kind    string          `json:"kind"`              // the wire field is "kind", not "type"
	Session string          `json:"session,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}
```

After the OK line the connection carries only server→client JSON lines (like `attach` upgrades to
frames); the client closes it to unsubscribe. Anything the client writes is discarded.

| Kind | Data |
|---|---|
| `hello` | `{"epoch","seq"}` (first line, control) |
| `session.created` / `session.started` | `SessionInfo`; both are published only after the process really started (a failed `run` emits nothing) |
| `session.exited` | `{"exit_code":0}` |
| `session.removed` | — (always after `session.exited`; ends the subscriptions bound to that session identity with `bye`) |
| `status.changed` | `{"from":"working","status":"waiting","reason":"…","attention":true,"revision":44}` |
| `view.changed` | `{"revision":44}` (no content: clients call `view` with `since_revision` + `epoch`) |
| `input.changed` | `{"state":"draft"\|"suggestion"\|"empty"}` |
| `attention.changed` | `{"attention":true,"reason":"…"}` |
| `mail.queued` / `mail.delivered` / `mail.read` | `{"id":12,"from":"codex@api","to":"claude@webapp"}` (no body) |
| `resync` | `{"reason":"overflow"\|"since_seq_too_old"\|"epoch_mismatch"}`: refetch `ls` + views |
| `heartbeat` | — (control) |
| `bye` | — (control; daemon shutdown or the identity's session was removed; the client closes the connection) |

```json
{"op":"subscribe","args":{"kinds":["status","mail"]}}
{"ok":true,"data":{"epoch":"7f3a…","seq":1041,"sessions":[…]}}
{"seq":1042,"epoch":"7f3a…","time":"2026-11-13T10:02:11Z","kind":"status.changed","session":"claude@webapp","data":{"from":"working","status":"waiting","attention":true,"revision":44}}
{"seq":0,"epoch":"7f3a…","time":"2026-11-13T10:02:26Z","kind":"heartbeat"}
```

**Bus** (`internal/daemon/events.go`, owned by `Server`):

```go
type bus struct {
	mu   sync.Mutex
	seq  uint64
	ring [1024]proto.Event // replay buffer for since_seq
	subs map[*subscriber]struct{}
}
type subscriber struct {
	filter   filter
	ch       chan proto.Event // capacity 256
	overflow atomic.Bool
}
func (b *bus) publish(typ, session string, data any) // never blocks
```

`publish` assigns `seq`, appends to the ring, and does a non-blocking send per matching
subscriber; on a full channel it sets `overflow` and drops. The subscriber's writer goroutine, after
draining, emits a single `resync` when `overflow` was set (so a slow client loses detail, never
consistency). Each line is written under the attach write deadline (10 s); a client that misses it
is disconnected. `since_seq` older than the ring (or another epoch) → `resync` first. Snapshot ordering: the
subscriber is registered, `seq` read, then the `sessions` snapshot built; queued events with `seq` <= the
reported one are skipped (a `since_seq` replay is not filtered).

Publishers, with no new import edges: `daemon` handlers (`run`, exit watcher goroutine, `rm`,
`send`, `reply`, `inbox` marking delivered, `read`), the view watcher (§2.1), and delivery through
a hook field `delivery.Loop.OnDelivered func(m mailbox.Message)` set by `Server.NewDeliveryLoop`
(delivery does not import daemon). Sessions publish nothing themselves; they only expose
`Changed()`/`OutputGen()`.

## 7. MCP: `agentd_view` for agents?

**Recommendation: do not ship it by default; add it behind `[security] agent_view = false`**
(story API-S12). When enabled, a read-only `agentd_view {name, lines_limit≤200}` returning status,
the last lines and options, wrapped in the same "untrusted peer content" note as mailbox messages;
no `scrollback`, no `act` tool (`act` over MCP is out of scope, like `type`).

Risks: (1) **prompt injection amplification**: a peer's screen contains whatever it read (web
pages, files, tool output), and handing it raw to another agent is a far larger untrusted channel
than an explicit message the peer chose to send; (2) **data leakage across projects/ACLs**: the
mailbox has `[acl]`, a screen read bypasses it — so when enabled, apply the ACL inverted (caller
must be allowed to *message* the target); (3) agents polling views instead of messaging, burning
tokens. The mailbox remains the sanctioned agent↔agent channel; `agentd_peers` may expose the
cheap `status` field (no screen content) since that is low-risk and useful for "is X busy?".

## 8. Test plan

All with `bash`/`sh`/`internal/testutil/fakeagent`, never real harnesses; `-race` for daemon,
session, delivery, inject.

- **fakeagent extensions**: `-menu "Yes|No|Maybe"` draws a pointer menu (`❯`) moved by arrows,
  `-numbered` for `1.`-style shortcuts, `-collapse N` replaces pastes longer than N bytes with
  `[Pasted text #1 +K lines]`, `-busy` already simulates `working`.
- `inject`: unit tests with a fake `Target` moved from `delivery` (verify timeout, human race,
  guard abort, placeholder regex); delivery tests unchanged and green (API-S08 gate).
- `view`: revision stable under identical redraws, bumps on content change, bumps on silent
  `working→idle` (fake clock), `since_revision` → `unchanged`; `lines_limit`, scrollback cap.
- `act`: prompt rejected with `busy` while a human draft is pending (attach client types without
  Enter), accepted with `force`; `not_ready` while `-busy`; `verify_failed` with `-droppaste`;
  `choose` happy path (numbered and arrow menus), `conflict` when the menu changed between `view`
  and `act`, `conflict` on label mismatch; key table golden test incl. DECCKM; permission matrix
  (human / self / other session ± `agent_type`).
- Concurrency (`-race`): delivery and `act prompt` on the same session never interleave pastes;
  100 parallel `view` calls during output; session exit during `act`.
- `subscribe`: ordering and seq monotonicity, filters, slow consumer → exactly one `resync`,
  heartbeat with fake clock, disconnect on write deadline, `since_seq` replay and too-old resync,
  shutdown closes streams.
- Size: profile size applied, CLI no longer sends size, restore on last detach, passive attach
  never resizes.
- Wire: `Response.code` round-trip; an old-format response (no code) decodes as before.

## 9. Migration, compatibility, open questions

- All additions are new ops/fields; existing clients and `agentd mcp` keep working. `Response.Code`
  is `omitempty`. The CLI no longer sending its terminal size on `run` is a visible change: new
  sessions start at 160x50 instead of the caller's size (release note; `--size` restores it).
- `delivery` public API kept via aliases; can be removed one release later.
- Revisions and seqs reset on daemon restart; clients detect it via `subscribe` `epoch`.

Open questions:

1. `interrupt` keys per harness: is Esc correct for Codex while working, and is Ctrl-C ever safe
   at `waiting`? Needs captures (API-S24).
2. `restore_size_on_detach`: Claude Code re-renders its transcript on SIGWINCH, which may duplicate
   scrollback; measure before keeping `true` as default (ADR-A02 suggests it).
3. Should `act prompt` be allowed at status `working` with `force` (queueing into the harness's own
   input), or always refused? Current design: allowed with `force`.
4. View fingerprint excludes spinner glyphs? Depends on semantic-model footer handling; otherwise
   a spinner may bump revisions several times per second (debounce bounds it to ~4/s).
5. Is a 1024-event replay ring in the daemon worth it, or should replay live only in the gateway?
6. Should `view`/`subscribe` for sessions (non-human callers) honour `[acl]` even outside MCP?
