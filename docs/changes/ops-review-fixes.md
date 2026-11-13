# Ops act / view / subscribe: code-review fixes

Builds on [[plans/api-gateway-plan]], [[design/api/daemon-ops]], [[AGED-US-0012-op-view]],
[[AGED-US-0013-op-act]], [[AGED-US-0014-op-subscribe]].

Principle: `act` types into agent terminals; when evidence is ambiguous it refuses (`conflict` /
`timeout` / `busy`) instead of sending a key. Every item has a regression test (mostly
`internal/daemon/review_fixes_test.go`, bash scripts and the fake agent; no real agent CLIs).

Verification (all items): `gofmt -l .` clean, `go vet ./...` clean, `go test -race -count=1 ./...` green.
The HIGH-1 and HIGH-2 tests were also run against a build with the instance checks disabled and failed
there (the second dialog got the key), so they do detect the bug.

## 1. HIGH: choose digit + Enter could approve the next dialog
- What: new profile key `menu_digit_action = "select" | "move"`. `select` = the digit only, never an
  Enter after it (verified by the menu instance disappearing/changing); `move` = digit, then Enter only if
  the same menu INSTANCE is up and the pointer is on the target. Built-ins: claude/codex `select` (to
  confirm, F8), default/pando `move`. Menu instance = fingerprint + `screen.MenuContext` (hash of up to 8
  non-blank rows above the question) + transcript length/tail (`viewState.instanceFor`). `Expect.Instance`
  added; `satisfied`/`sameMenu`/`otherMenu` compare instance, not only fingerprint. `revision` and `epoch`
  are REQUIRED for choose (`invalid` when missing); accepted if equal to the current revision or if the
  instance recorded for it (`menuTrack`, per revision, now keyed by instance) equals the current one. The
  refresh is forced inside `actChoose` (options, instance and revision come from one `refreshLocked`).
  Arrows + Enter only for options without a digit (or `menu_select = "arrows"`).
- Files and symbols: `internal/config/menu.go` (`MenuDigitAction`, `MenuProfile.DigitAction`),
  `internal/config/config.go` (built-ins), `internal/screen/options.go` (`ChooseKeys.DigitAction`,
  `Expect.Instance`, `planChooseKeys`, `MenuContext`), `internal/daemon/view.go` (`instanceFor`,
  `menuCtx`, `instance`), `internal/daemon/act.go` (`menuTrack`, `actChoose`, `satisfied`),
  `internal/daemon/view_rules.go` (`chooseKeys`), `cmd/agentd/act.go` (`--epoch`).
- Tests: `TestReviewChooseNeverApprovesIdenticalNextDialog` (select and move, identical dialog for another
  command), `TestReviewDigitSelectSendsNoEnter`, `TestReviewChooseRevisionAndEpochRules`,
  `TestMenuContext`, `TestPlanChoose`, `TestMenuDigitAction`, `TestSatisfiedRequiresInstance`.

## 2. HIGH: later plan steps need the same instance
- What: before writing any step after the first, the same instance must still be on screen (yes/no word +
  Enter included); otherwise `conflict`, no key (an `IfNotGone` step ends the plan as done).
- Files: `internal/daemon/act.go` (`actChoose` loop). Test: `TestReviewLaterStepNeedsSameInstance`.

## 3. HIGH: paste verification tied to the paste
- What: needle = first non-blank line of the paste-safe text (none = `ErrEmptyNeedle`, `invalid`, nothing
  written). Visible only if its occurrence count on the ghost-filtered screen grew versus a baseline just
  before the paste, or it appears in the input region (`Request.InputRegion`, `screen.Split`) where it was
  not before. Mailbox notices keep their unique needle.
- Files: `internal/inject/inject.go` (`Input`, `DefaultNeedle`, `countVisible`, `ErrEmptyNeedle`),
  `internal/daemon/act.go` (`InputRegion`). Delivery test fake now appends pastes like a terminal.
- Tests: `TestPasteVerificationTiedToPaste`, `TestEmptyNeedleRefused`; delivery tests unchanged and green.

## 4. MED: BeforeSubmit
- What: `inject.Request.BeforeSubmit`; act prompt re-checks alive, draft, real human keystroke and a menu
  that appeared since the guard right before Enter. Failure: no submit, text stays pasted and is recorded
  as a draft, answer `conflict`.
- Files: `internal/inject/inject.go`, `internal/daemon/act.go` (`actPrompt`).
- Tests: `TestBeforeSubmitAbortLeavesDraft`, `TestReviewPromptBeforeSubmitRecheck`.

## 5. MED: unsubmitted text is a draft
- What: `Session.MarkDraft` / `ClearDraft`; inject marks a draft after a verified paste that is not
  submitted (submit:false, BeforeSubmit veto, aborted delay) and clears it after a submit. Prompt is `busy`
  while `view.input.text` is non-empty (force does not override). Delivery/idle detection already treats a
  draft as busy. A forced interrupt (Ctrl-C) is the way to clear it through the API.
- Files: `internal/session/draft.go`, `internal/inject/inject.go`, `internal/daemon/act.go`.
- Tests: `TestDraftMarkedWhenNotSubmitted`, `TestReviewPastedPromptIsADraft`, `TestWriteActIsNotHumanInput`.

## 6. MED: conflict view uses the real caller
- What: `conflict(e, who, ...)` builds the view with the real caller; no `agent_view` = no view in `data`.
- Files: `internal/daemon/act.go`. Test: `TestReviewConflictViewRespectsPermissions`.

## 7. MED: ls preview
- What: `ls` resolves the caller; a session without `agent_view` gets other sessions' `preview` blanked
  (status/attention kept; unresolvable identity is treated the same). `dump` is unchanged (pre-existing
  feature) and noted in AGENTS.md as a known gap.
- Files: `internal/daemon/handlers.go` (`dispatch`, `ls`). Test: `TestReviewLsStripsPreviewForSessions`.

## 8. MED: title and the revision
- What: the revision fingerprint uses the title without leading spinner/braille glyphs
  (`stableTitle`); the view still returns the raw title.
- Files: `internal/daemon/view.go`. Tests: `TestStableTitle`, `TestFingerprintIgnoresTitleSpinner`.

## 9. LOW: session.created after a successful start
- What: `session.created` is published together with `session.started`, only after `Start` succeeded and
  the daemon is not closing; no `session.removed` is owed on failure paths.
- Files: `internal/daemon/handlers.go` (`run`). Test: `TestReviewNoCreatedForFailedStart`.

## 10. LOW: exited before removed
- What: `entry.exitPub` is closed after `session.exited` is published; `rm` waits for it (5 s cap) outside
  the registry lock and re-checks the entry.
- Files: `internal/daemon/server.go`, `internal/daemon/handlers.go` (`run`, `rm`).
  Test: `TestReviewExitedBeforeRemoved`.

## 11. LOW: act marks vs real human input
- What: `Session.WriteAct` records `LastActInput`; `LastHumanInput` is real attach-client input only;
  `LastInput` (max) feeds delivery idle detection and the status classifier (`snap.LastHuman`);
  `humanRecent` uses the real one. The global `actMarks` map is gone (nothing to delete on `rm`).
- Files: `internal/session/session.go`, `snapshot.go`, `internal/delivery/delivery.go` (`lastInput`),
  `internal/daemon/act.go` (`writeKeys`, `humanRecent`).
- Tests: `TestWriteActIsNotHumanInput`, `TestReviewActIsNotHumanAndChooseHonoursGrace`.

## 12. LOW: choose and human grace
- What: choose is `busy` within `human_grace_ms` of a real keystroke unless `force`.
- Test: `TestReviewActIsNotHumanAndChooseHonoursGrace`.

## 13. LOW: keys guards
- What: `enter` reached before any text-typing key, with no menu, an empty input and a ghost suggestion
  visible = `not_ready` (force does not override); keys with `ctrl-c`/`ctrl-d` get the interrupt status
  guard (force overrides).
- Files: `internal/daemon/act.go` (`actKeys`). Test: `TestReviewKeysGhostEnterAndStopKeys`.

## 14. LOW: epoch
- What: `ActArgs.Epoch`; required when `revision` is set (`invalid`), mismatch = `conflict`. `view`
  `since_revision` returns `not_modified` only with the matching epoch (missing/foreign never). CLI:
  `agentd view --epoch`, `agentd act choose --epoch`.
- Files: `internal/proto/act.go`, `internal/daemon/act.go`, `internal/daemon/view.go`, `cmd/agentd`.
- Tests: `TestReviewViewNotModifiedNeedsEpoch`, `TestReviewChooseRevisionAndEpochRules`.

## 15. LOW: act context tied to the connection
- What: for `act`, `handleConn` derives the request context from a watcher (`cancelOnDisconnect`: a Peek
  on the codec reader, so pipelined requests are not consumed); a disconnect cancels the act (input-lock
  wait, key plan). `dispatch` now takes the context.
- Files: `internal/daemon/handlers.go`. Test: `TestReviewActCancelledOnDisconnect`.

## 16. LOW: subscribe
- What: `since_seq` needs `epoch` (`invalid`). The subscriber registers first, `seq` is read, then the
  snapshot is built; queued events with `seq` <= it are skipped (a `since_seq` replay is not filtered).
- Files: `internal/daemon/events.go`. Test: `TestReviewSubscribeSinceNeedsEpochAndSnapshotOrdering`.

## 17. LOW: identity subscriptions end on removal
- What: `subscriber.end` is closed when `session.removed` for the subscriber's own session is published
  (any `kinds` filter); the loop flushes and sends `bye`.
- Files: `internal/daemon/events.go`. Test: `TestReviewSubscriptionEndsWhenSessionRemoved`.

## 18. LOW: client closes the conn on bye
- Files: `internal/client/subscribe.go`. Test: `TestSubscriptionClosesConnOnBye`.

## Docs
`AGENTS.md` (Act, Semantic view, Event bus, Injection, Idle detection, ops list), `docs/design/api/daemon-ops.md`
(§3 act/choose, §4 inject, §6 `kind` field and the real event kinds), `docs/design/api/semantic-model.md`
(`menu_digit_action`).
