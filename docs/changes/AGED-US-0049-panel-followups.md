# AGED-US-0049: Panel follow-ups (quiet reply, answer layout)

Part of [[plans/agentd/dashboard_plan]]. Builds on [[changes/AGED-US-0045-desktop-panel]] and
[[changes/AGED-US-0040-questions]]. Date: 2026-11-22. (KB unavailable: recorded here.)

## What

- Op `reply` gains `notify` (`*bool`, default true). With `notify: false` the reply is stored as `delivered`, so the
  delivery loop (which only reads `queued`) never types it into the asking session; no `mail.queued`, no kick, the daemon
  publishes `mail.delivered`. The original message becomes `replied` as usual, so a question closes in `board`.
  Permissions and the relay gate are unchanged.
- Desktop Panel: answering a question through Prompt (act `prompt`) now closes it: after a successful act the Panel calls
  `Backend.Reply(..., notify=false)` with the same text, so the answer is on record without being injected twice. If that
  reply fails, a toast is shown and the prompt result is kept.
- Layout: the free-text answer field is full width; the Buzon/Prompt toggle and Enviar are on their own row below
  (44 px field and toggle).
- CLI: `agentd reply ID TEXT --no-notify`.

## Files and symbols

- `internal/mailbox/mailbox.go`: `Store.ReplyNotify` (`Reply` = notify true).
- `internal/proto/proto.go`: `ReplyArgs.Notify`; `internal/client/client.go`: `Client.ReplyQuiet`.
- `internal/daemon/mailboxops.go`: `reply`.
- `cmd/agentd/main.go`: `replyCmd` `--no-notify`.
- `internal/desktopui/backend` (`Backend.Reply(..., notify bool)`), `backend/fake/board.go` (`ReplyCall.Notify`, state),
  `core/board.go`, `app/panel/panel.go` (`promptAnswer`, answer row).
- `AGENTS.md`: Ops `reply`, Identity `reply`, package contract, Panel bullet.

## Verification

`go test -race` of mailbox, daemon, client, cmd/agentd and desktopui: new tests `TestReplyNotifyFalseStoresDelivered`,
`TestReplyNotifyFalse` (a `cat` session, state delivered, nothing injected, question closed in `board`), panel
`TestFreeTextToPromptForcesWhileWorking` (Act then quiet Reply), core `TestWatchBoard` reply args (`notify:false` on the wire).
