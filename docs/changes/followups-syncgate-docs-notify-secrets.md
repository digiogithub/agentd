# Follow-ups: syncgate docs/tests and notification secrets

Builds on [[AGED-US-0038-load-robust-tests]] and [[AGED-US-0025-notifications]].

## What
1. AGENTS.md now documents frame-aligned PTY chunking (contract row for `internal/session` and a
   new "Frame-aligned PTY chunking" bullet): trailing open DEC 2026 frames are held until ESU, released
   after 100 ms or when >= 256 KiB, and at EOF. Exact scope per the code: the held bytes are withheld from
   all of `handleOutput` (log, ring buffer, attached clients, emulator, `lastOutput`, `OutputGen`/`Changed`),
   not only the emulator; the bytes before the BSU are processed immediately. Sync-aware history sampling:
   `screen.SyncSettleMax` 50 ms, `viewSyncDebounce` 40 ms.
2. `[api.notify]` gets `ntfy_token_env` / `webhook_secret_env` (environment variable name read at
   `agentd serve` start; env wins over the plain value; named-but-unset is an error unless a plain value
   exists). When a plain secret is present in config.toml, `serve` refuses to start unless the file is a
   regular file owned by the user with no group/other bits. Secret values never appear in errors.

## Files and symbols
- `AGENTS.md` (session contract row, new Key behaviours bullet, gateway notifications bullet)
- `docs/design/api/security-remote.md` section 6 (secrets paragraph)
- `internal/config/api.go`: `NotifyConfig.NtfyTokenEnv/WebhookSecretEnv`, `ResolveSecrets`,
  `resolveEnvSecret`, `checkPrivateFile` (uses `ownerUID`), env-name validation
- `cmd/agentd/serve.go`: `serveOptions.ConfigPath/Getenv`, `runServe` calls `ResolveSecrets` right after `Validate`
- Tests: `internal/session/syncgate_test.go` (`TestOpenSyncTail`, `TestFeedHoldsOpenFrame`: split inside BSU,
  complete frames pass, 100 ms timeout release, 256 KiB size release), `internal/config/notify_test.go`
  (`TestNotifyResolveSecrets`), `cmd/agentd/serve_test.go` (`TestRunServeRefusesPlainNotifySecretInOpenConfig`)

## Why
Normative docs lagged the code (AGED-US-0038); plain notification secrets in a possibly world-readable
config.toml were the one gap against security-remote section 6.

## Verification
`gofmt -l .`, `go vet ./...`, `go test -race -count=1 ./internal/session ./internal/config ./internal/httpapi ./cmd/...`
(results in the hand-off report).
