# Wave 1 review fixes

Follow-up to [[plans/api-gateway-plan]] wave 1 ([[design/api/daemon-ops]]); builds on
[[changes/AGED-US-0001-screen-snapshot]], [[changes/AGED-US-0008-fixed-pty-size-policy]],
[[changes/AGED-US-0009-internal-inject]], [[changes/AGED-US-0010-structured-error-codes]] and
[[changes/AGED-US-0011-info-op-run-target-harness]].

## What changed (per review finding)
1. **Alias regression.** `run` (daemon-side target resolution) used the daemon's environment. It now reads
   `AGENTD_ALIASES`, `AGENTD_ALIASES_FILE`, `XDG_CONFIG_HOME` and `HOME` (for `~`) from `RunArgs.env_base`
   when present, else the daemon's own env. Op `info` accepts optional `env_base` (`proto.InfoArgs`) and
   applies the same rule; `agentd aliases` stays local (reads the CLI's own env, which is exactly what `run`
   sends as `env_base`), so the three agree for one caller. Chosen over routing `aliases` through the
   daemon because it needs no daemon and cannot disagree by construction.
2. **Delivery guard refusal** (`errNotIdle`) returns before attempt/cooldown accounting and logs nothing.
3. **Snapshot** copies raw `uv.Cell`s under `outMu.RLock`, converts to strings/colours after unlocking;
   `BenchmarkSnapshot` added (160x50, 100 scrollback rows: ~0.31 ms/op).
4. **OSC 9**: bodies matching `^\d+(;|$)` (ConEmu/WT `9;4`, `9;9;cwd`, `9;12`) are not notifications.
5. **Sanitizing**: `screen.Sanitize` strips C0/C1/DEL and caps length; title/notify 256 runes
   (`screen.MaxTitleLen`), cwd 4096 (`screen.MaxCwdLen`).
6. **Wide cells**: continuation cells stay in `Row.Cells` as `Width:0` (index == column); `Row.Text` skips
   them, `Cell.Continuation()` added; docs and tests updated.
7. **Error codes**: names `invalid`/`exists` kept; design doc 3.5 and gateway mapping amended
   (`invalid`->400, `exists`->409). Daemon `classifyError`: `session.ErrWriteTimeout`->`timeout`,
   `inject.ErrPasteNotVisible`->`verify_failed`, `inject.ErrDraft`->`busy`; purge argument errors ->
   `invalid`, purge permission refusals (other mailbox, non read/replied states, vacuum) -> `forbidden`.
8. **`proto.CodedError`** gains `Err` + `Unwrap()`; `Errorf` supports `%w`; `WithCode` keeps the cause.
9. **inject lock key**: session name, else pointer identity, else one shared lock; no panic on
   non-comparable targets.
10. **`Changed()`** doc: never fires after exit; select on `Done()`.
11. **CLI vs old daemon**: `run` failing with `unknown field "target"` prints a hint to `agentd shutdown`.
12. **Test hygiene**: `TestMain` unsets `AGENTD_TOKEN/ID/SOCKET` in `internal/daemon`, `internal/client`,
    `internal/mcpserver`, `cmd/agentd`.

## Files & symbols
`internal/config/aliases.go` (`AliasesPathEnv`, `EnvLookup`, `LoadAliasesEnv`), `internal/daemon/{info,handlers,
retention,server,main}.go` (`currentAliases(envBase)`, `AliasesLoader(getenv)`, `infoOp(InfoArgs)`,
`classifyError`), `internal/proto/proto.go` (`InfoArgs`, `CodedError`), `internal/client/client.go`
(`InfoFor`), `internal/delivery/delivery.go`, `internal/inject/inject.go` (`lockKey`),
`internal/session/snapshot.go` (`rawRow`, `convertRow`, `conEmuSub`), `internal/screen/screen.go`
(`Sanitize`, `Cell.Continuation`), `cmd/agentd/main.go` (`runHint`), `docs/design/api/daemon-ops.md`,
`AGENTS.md`, `testmain_test.go` files.

## Tests
`TestAliasesFromEnvBase`, `TestLoadAliasesEnvUsesGivenEnvironment`, `TestGuardRefusalIsSilentAndNotAnAttempt`,
`TestSnapshotSanitizesChildStrings`, `TestSnapshotNotifications` (9;4/9;9/9;12), `TestSnapshotContent`
(continuation cell), `TestSanitize`, `TestLockKeyNoPanicOnNonComparable`, `TestCodedErrorWrap`,
`TestClassifyError`, `TestPurgeAndWriteCodes`, `TestRunHint`, `BenchmarkSnapshot`.

## Verified
`gofmt -l .`, `go vet ./...`, `go test -race -count=1 ./...` run inside an agentd session (AGENTD_* set)
without `env -u`. `internal/screen` failures during the run came from other agents' in-progress
`reflow`/`regions` work, not from these changes.
