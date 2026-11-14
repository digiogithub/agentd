# Herdr compatibility phase 2: interactive attach

Continues [[herdr-compat-phase1]]; plan [[agentd/plans/herdr-compat_plan.md]].

## What changed

- **proto**: frame types `FrameAuto` ('A') and `FrameFocus` ('F'); `AttachArgs.Observe/Passive/History`,
  `MaxAttachHistory` (10000).
- **session**: `DECMode(m)`, `SubscribeWithHistory(n)` (scrollback lines rendered with `uv.Line.Render`, printed
  and scrolled off the top, then the normal redraw; main screen only).
- **daemon** (`attach.go`, `server.go`): `attachClient.observe/passive`; observers are never active and their
  D/A/F/R frames are ignored; passive clients become active on input/resize/focus; `A` goes to the non-human
  reply queue from any client; `F` activates on gain and forwards `CSI I`/`CSI O` when the child set 1004 and the
  client is active; `history` uses `SubscribeWithHistory`.
- **client**: `AttachWith(proto.AttachArgs)` (`Attach` wraps it).
- **hcompat**: feature `automatic_input` in hello, `automatic_input: true` in attach results; kind 6 → `A`,
  FOCUS → `F`; attach params `observe`, `resumed` (→ passive), `history`; repaint reattaches are passive so a
  lagging viewer never steals the size; FACTS ignored (documented).
- Tests: `internal/daemon/attach_options_test.go` (observe never types/resizes but sees output; passive takes
  the size on focus; automatic input reaches the child without a human draft; focus reports only with 1004;
  history paints scrollback only when asked); `internal/hcompat/phase2_test.go` (negotiation, observe and
  resumed, history 2000, focus forwarded, two terminals on one connection without crossing input, reconnect on a
  new connection with a CAN first paint).
- Docs: `AGENTS.md` (attach frames/options, hc mapping), plan.

## Why

Herdr Desktop sends AUTOMATIC_INPUT for VT query replies, FOCUS on window focus, `history: 2000` on attach;
Whip sends `resumed`. Without them query replies looked like typing (drafts block delivery), focus-aware TUIs
never saw focus, scrollback was missing, and reconnecting viewers resized sessions.

## Verification

- `gofmt -l` clean, `go vet ./...` clean, `go test -race -count=2 ./internal/hcompat` ok,
  `go test -race -count=1 ./...` ok.
- Not verified: real Herdr Desktop / mobile / Whip GUIs.

Note: KB tools unavailable in this session; this file is the record.
