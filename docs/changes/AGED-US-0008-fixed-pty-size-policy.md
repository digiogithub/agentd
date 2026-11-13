# AGED-US-0008 Fixed PTY size policy (API never resizes)

## What
- Profiles gain `cols`/`rows` (built-in default 160x50, validated 1..1000). `run` without an explicit
  size (per dimension) uses the profile size instead of `session.DefaultCols/Rows` (120x40).
- CLI `agentd run` stops sending its terminal size; new `--size COLSxROWS`.
- `[run] restore_size_on_detach` (default true): when the last attached client detaches, the session is
  resized back to its start size. Local attach keeps "most recently active client wins".
- `info` profiles carry `cols`/`rows`.

## Files & symbols
- `internal/config/config.go`: `DefaultCols/Rows`, `MaxPTYDim`, `ProfileConfig.Cols/Rows`,
  `Profile.Cols/Rows`, validation + merge, `RunConfig.RestoreSizeOnDetach`, `RunConfig.RestoreSize()`.
- `internal/daemon/handlers.go` (`run`), `internal/daemon/server.go` (`entry.baseCols/baseRows/restore`,
  `removeClient`), `internal/daemon/info.go`, `internal/proto/proto.go` (`ProfileInfo.Cols/Rows`),
  `cmd/agentd/main.go` (`runCmd`, `parseSize`).
- Tests: `internal/config/size_test.go`, `internal/daemon/size_test.go` (profile size, explicit/partial size,
  two-client attach/detach restore, restore disabled), `cmd/agentd/main_test.go` (`TestParseSize`);
  `daemon_test.go` expectation updated 120x40 -> 160x50.

## Why
The phone/API front end must not shrink a session; ADR-A02 in [[plans/api-gateway-plan]], see
[[design/api/daemon-ops]] section 5.

## Deviations
- Restore target is the size the session started with (profile size unless `run`/`--size` gave one), so an
  explicit `--size` is not discarded on first detach.
- `AttachArgs.Passive` (design section 5) is not part of this story.

## Verified
`gofmt -l .`, `go vet ./...`, `go test -race ./internal/proto ./internal/client ./internal/config
./internal/daemon ./cmd/...` (env `AGENTD_TOKEN/ID/SOCKET` unset).
