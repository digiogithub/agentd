# act prompt: collapsed pastes and pointer input made desktop chat prompts `busy`

Builds on [[design/api/daemon-ops]] (§4 "Paste collapsing", planned there but never implemented),
[[ops-review-fixes]] and [[plans/agentd/desktop_plan]].

## Symptom

agentd-desktop Chat tab with Claude Code sessions: prompts failed with "agente ocupado o borrador
pendiente". daemon.log: a 1243-char prompt to `claude@radar` was `verify_failed`, every later prompt
`busy`; prompts to `claude@agentd` were `busy` while the desktop Terminal tab was attached.

## Causes

1. Claude Code (v2.1.x) collapses a long or multi-line bracketed paste into `[Pasted text #N]` /
   `[Pasted text #N +M lines]` (seen in `logs/claude@radar.log`). The paste verification looks for the
   first 48 runes of the text, never sees them: `verify_failed`, no Enter, but the placeholder stays in
   Claude's input box. The act guard then refuses every later prompt because the input line is not
   empty (`busy`) until the user clears it in a terminal.
2. Claude Code enables any-event mouse tracking (DEC 1003 + 1006), focus reports (1004) and colour-scheme
   reports (2031). The desktop Terminal tab (MyGo libghostty terminal) writes every pointer move over
   the view, focus changes and `CSI ?997;n n` as attach `D` data, which the daemon counted as human
   keystrokes (`LastHumanInput`): `busy` for `human_grace_ms` (5 s) after any pointer move.

## Changes

- `internal/config/config.go`: profile key `paste_placeholder_regex` (`Profile.PastePlaceholder`);
  built-in claude `\[Pasted text #\d+( \+\d+ lines)?\]`.
- `internal/inject/inject.go` (`Input`): one more placeholder match than in the pre-paste baseline (on
  the ghost-filtered screen or in the input region) verifies the paste. Applies to delivery too.
- `internal/proto/act.go`: `ActBusy{Reason}` + `BusyDraft/BusyHuman/BusyInput/BusyLock`.
- `internal/daemon/act.go`: every `busy` carries `ActBusy` data (`busyErr`); clearer input-line and
  `verify_failed` messages.
- `internal/daemon/termreply.go`: `CSI … n` (DSR, `?997;1n`) and `CSI … t` (XTWINOPS) are terminal
  replies; `ambientOnly` (focus, pointer motion with no button, wheel; SGR/urxvt/X10).
- `internal/daemon/attach.go`: ambient-only chunks are written as non-human input, no turn origin, no
  activation.
- `internal/session/draft.go` (`skipEscape`): X10 mouse `CSI M` + 3 bytes is not draft text.
- `internal/desktopui/core/term.go`: attach `passive`; focus reports sent as `F` frames.
- `internal/testutil/fakeagent`: `-collapse N` (line prompt and `-box`).
- AGENTS.md (Idle detection, Injection, Act prompt, Desktop transport), README, docs/ghost-text.md.

Not changed: an unverified paste is still not recorded as a draft (agentd cannot tell whether it
landed); a visible leftover is caught by the input-line check (`busy`, reason `input`).

## Verification

Tests: `internal/inject` (placeholder verifies, pre-existing placeholder does not, no rule fails),
`internal/config` (built-ins, invalid regex), `internal/daemon/act_paste_test.go` (fakeagent `-collapse`:
verified with the rule in line and box mode; without it `verify_failed` then `busy`/`input`; pointer and
focus attach input does not make a prompt busy, a cursor key does: this test fails on the old
`attach.go`), `TestAmbientOnly`, `TestSplitTermReplies`, `TestDraftEffect`, desktop
`TestAttachPTYFocusFrames`. gofmt, go vet, `go test -race` of the touched packages and `go test ./...`.
