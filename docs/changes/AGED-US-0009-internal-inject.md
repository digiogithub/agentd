# AGED-US-0009: extract the safe-input path into internal/inject

Plan: [[plans/api-gateway-plan]] (ADR-A13), spec [[design/api/daemon-ops]] section 4.

## What changed
New package `internal/inject` holds the injection logic that used to be private to
`delivery.Loop.inject`: Guard before the paste (abort, nothing written), `session.PasteSafe` +
bracketed paste, paste verification (poll the ghost-filtered screen every 25 ms up to 1 s,
whitespace-insensitive needle, no submit when not visible), submit delay measured from the paste,
human re-check before submit (submit anyway, `Result.HumanRaced`), plus a per-target input lock
so two callers cannot interleave pastes. `delivery` now calls `inject.Input`; behaviour unchanged.

## Files and symbols
- `internal/inject/inject.go`: `Input`, `Request`, `Result`, `Target`, `Lock`, `GhostOpts`,
  `DraftPending`, `PasteVisible`, `DefaultNeedle`, `ErrDraft`, `ErrPasteNotVisible`,
  `DraftTimeout`, `PasteVerifyTimeout`.
- `internal/inject/inject_test.go`: submit, guard abort, verification failure, human race,
  lock serialization, lock cancellation.
- `internal/delivery/delivery.go`: `Loop.inject` uses `inject.Input`; `GhostOpts`, `PasteVisible`,
  `ErrPasteNotVisible`, `DraftTimeout`, `PasteVerifyTimeout` are thin aliases.
- `AGENTS.md`: package-contract table (new `inject` row, `delivery` deps).

## Why
`act` (gateway) must reuse the exact safe path and not interleave with delivery on one session.

## Verified
`go vet`, `gofmt -l` clean; `go test -race ./internal/inject ./internal/delivery` and
`go test -race ./internal/daemon -run 'Deliver|Inject|Ghost|Paste'` pass, existing tests unchanged.
