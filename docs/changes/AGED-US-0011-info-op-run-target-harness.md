# AGED-US-0011 Op info and run with target + harness resolved in the daemon

## What
- New op `info` -> `proto.InfoResult{host, version, pid, epoch, harnesses[], profiles[{name,builtin}],
  aliases[{name,dir,source}]}`. Harnesses = every profile except `default`.
- `ping` returns `epoch` (`<unix>-<pid>-<rand>`, unique per `Server`).
- `run` accepts `target` + `harness` (`proto.RunArgs.Target/Harness`): the daemon resolves with the CLI's
  rules. A relative target is resolved against `cwd` (the caller's absolute cwd; required in that case).
  Without `target`, behaviour is unchanged (`harness` only supplies default cmd/profile).
- `SessionInfo` gets placeholder summary fields `status`, `attention`, `revision`, `preview` (omitempty,
  empty until API-S09).
- CLI `agentd run` now sends `target`, `harness` and its cwd; no local resolution.

## Files & symbols
- `internal/config/aliases.go`: `ResolveTargetIn(arg, base)` (`ResolveTarget` delegates).
- `internal/config/runspec.go`: `RunRequest`, `RunSpec`, `(*Aliases).ResolveRun` (single place for default
  harness/cmd/name/profile rules, moved out of the CLI). `config.go`: `Config.ProfileNames`.
- `internal/proto/proto.go`: `OpInfo`, `InfoResult`, `ProfileInfo`, `AliasInfo`, `PingResult.Epoch`,
  `RunArgs.Target/Harness`, `SessionInfo` placeholders. `internal/client`: `Client.Info`.
- `internal/daemon/info.go` (new): epoch, `ping`, `infoOp`, `resolveRun`, `currentAliases`;
  `Server.AliasesLoader` (set by `daemon.Main` to `config.LoadDefaultAliases`: aliases re-read per
  `run`/`info`, so editing `aliases.toml` still works without restarting the daemon, as it did when the CLI
  read it); `handlers.go` dispatch + `run`. `cmd/agentd/main.go` `runCmd`.
- Tests: `internal/config/runspec_test.go` (parity with `ResolveTarget`, defaults), `internal/daemon/info_test.go`
  (ping epoch, info, run alias/relative/errors/duplicate, aliases reload).

## Why
ADR-A14 ([[plans/api-gateway-plan]]): the gateway must not read aliases itself; one resolution path
([[design/api/daemon-ops]], http-api 3.1 and 8).

## Notes / deviations
`AGENTD_ALIASES` of the CLI's own environment no longer influences `run` (the daemon's environment does;
`DaemonEnv` already passes it at autostart). `run` errors from resolution carry code `invalid`.

## Verified
`gofmt -l .`, `go vet`, `go test -race ./internal/config ./internal/daemon ./cmd/...` green (env
`AGENTD_TOKEN/ID/SOCKET` unset).
