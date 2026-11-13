# AGED-US-0023: TLS modes (self-signed with pin, files, Tailscale)

Builds on [[plans/api-gateway-plan]] (ADR-A08, A15), [[design/api/security-remote]] section 4 and
[[design/api/frontend-app]] 4.2; continues [[AGED-US-0016-serve-skeleton]].

## What changed
- New package `internal/tlsutil` (stdlib only): persisted self-signed ECDSA P-256 certificate, `files`
  mode with live reload, SPKI pin, pin-verifying client callback.
- `[api] tls = "self-signed"` now works; new `[api] tls_hosts = []` (extra SANs).
- `agentd serve tls [show|rotate]`. `agentd serve` logs the pin and reloads `files` certs on SIGHUP.
- Docs: README "Remote access", security-remote 4.1/4.2, AGENTS.md (contract row, paths, `[api]` keys).

## Files and symbols
- `internal/tlsutil/tlsutil.go`: `Open`, `Load(mode, cfg) (*tls.Config, pin, err)`, `CurrentPin(mode, cfg)`,
  `Source` (`TLSConfig`, `Pin`, `Info`, `Reload`), `Generate`, `SANs`, `Describe`, `Pin`, `PinBase64URL`,
  `Verifier`. Tests in `tlsutil_test.go`.
- `internal/config/api.go`: `APIConfig.TLSHosts`.
- `cmd/agentd/serve.go`: `serveTLS`, `tlsConfigFor`, `serveOptions.TLSDir`, SIGHUP reload.
- `cmd/agentd/tlscmd.go`: `tlsCmd`, `tlsShow`, `tlsRotate`. `cmd/agentd/tls_test.go`.
- `cmd/agentd/pair.go`: `spkiPin` now delegates to `tlsutil.CurrentPin` (so self-signed works for pairing).

## For the pairing code
Use `tlsutil.CurrentPin(api.TLS, cfg)` (via `tlsConfigFor(api, "")`); it returns `""` for `off`, generates
the self-signed cert if `serve` never ran. Format is `sha256:<hex>` (unchanged from pair.go and
frontend-app 4.2); `tlsutil.PinBase64URL` gives the unpadded base64url form of the same digest if wanted.

## Decisions
- Storage `<state>/api/tls/`; key written first, atomically, 0600; dir 0700.
- Existing valid pair is always reused (a changed `tls_hosts` needs `tls rotate`, so the pin never changes
  silently); corrupt state is an error; expired is regenerated.
- TLS 1.2 minimum for older Android/embedded clients, ECDHE+AEAD suites only; 1.3 when offered.
- No external command (no `tailscale status`): MagicDNS names go in `tls_hosts`.
- `rotate` affects a running gateway only after restart.

## Verification
`gofmt`, `go vet ./...`, `go test -race -count=1 ./internal/tlsutil ./internal/config ./cmd/...` pass.
Tests: generate/persist/reload same pin, perms, SANs, rotate, expiry, corrupt state, files reload (and
broken replacement keeps old cert), pinned client ok / wrong pin fails / TLS 1.1 refused, end-to-end
`/v1/health` over HTTPS on a `0.0.0.0` bind with self-signed, `tls show|rotate`.
