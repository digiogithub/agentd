# AGED-US-0022: QR pairing flow

Plan: [[plans/api-gateway-plan]] (ADR-A09). Design: [[design/api/security-remote]] 3. Builds on
[[AGED-US-0021-device-tokens]].

## What changed
`agentd serve pair` creates a one-time code, shows a terminal QR + URL + code, and waits for a device to claim it
through the running gateway; the host approves with `Approve? [y/N]` (default no) and the device receives a token.

## Flow
1. CLI: `Pairing.Issue` stores a salted SHA-256 of the 12-symbol Crockford code (60 bits, shown `XXXX-XXXX-XXXX`) in
   `api/pairing.json` (0600, dir 0700, flock `api/pairing.lock`), TTL 5 min (max 30), at most 3 live codes (oldest
   evicted). It prints the QR (half-block characters), URL, code, optional pin and the `agentd://` link.
2. Device: `POST /v1/pair {code, name, platform}` (unauthenticated). The gateway checks the code against every record
   in constant time, marks it `claimed` (single use, under the flock) and holds the request open (long poll).
3. CLI polls the file, sees the claim, prints `Device "N" (platform, addr) wants write access. Approve? [y/N]`.
   `y`/`yes` approves; anything else, EOF or timeout denies. `--no-approve` records the code as approved at claim time.
4. Gateway: `Take` (atomic approved -> done, so exactly one request issues) then `Store.Create(paired_via "qr",
   scope of the code)`; answers `201 {token, device_id, scope}` with `Cache-Control: no-store`. The clear token is
   never written to disk. Denied: 403 `pairing_denied`; expired while waiting / cancelled / client gone: 410
   `pairing_expired`; wrong, malformed, used, expired or burned code: 403 `pairing_invalid` (indistinguishable).

## Payload
`agentd://pair?v=1&u=<url-escaped gateway URL>&c=XXXX-XXXX-XXXX&fp=sha256:<hex SPKI>&h=<hostname>` (fixed key order).
`u` = `--url` or scheme (tls off -> http, else https) + `[api] listen` (unspecified host replaced by the hostname;
warning on a loopback URL). `fp` only with `tls = "files"` (self-signed is not implemented yet) and not `--no-pin`;
omitted otherwise. `h` = hostname.

## Limits
- 5 failed attempts burn the live pending codes: the presented code is unknown on failure, so each failure counts
  against every pending code (documented trade-off: an attacker can burn codes, never guess one).
- 10 requests/min per remote address (`RemoteAddr` only, forwarded headers ignored), 429 + Retry-After.
- 20 failures in 10 min lock pairing for 10 min (429). Malformed bodies (400) do not consume the code or attempts.
- Names/platforms must be printable (no control or bidi/format characters: they are shown on the host terminal).
- A client that disconnects while waiting burns the code. Unsafe/malformed `pairing.json` -> 503, CLI refuses.
- Codes and tokens are never logged (logs carry pairing id, device id, address).

## Files and symbols
- `internal/apitoken/pairing.go`: `Pairing`, `OpenPairing`, `Issue`, `ClaimCode`, `Get`, `Decide`, `Cancel`, `Take`,
  `SetDevice`, `Pending`, `Claim`, `NormalizeCode`, `FormatCode`, states `Pair*`.
- `internal/apitoken/pairhttp.go`: `PairRoute`, `PairHandlerOptions`, rate limit and lockout.
- `internal/httpapi/extra.go`: `Server.AddRoute` (additive; before the handler is built).
- `cmd/agentd/pair.go`: `pairCmd`, `runPair`, `pairURL`, `gatewayURL`, `spkiPin`; `serve.go` registers the command and
  mounts the route before `WrapHandler`.
- `docs/api/openapi.yaml`: `POST /v1/pair` (marked `x-agentd-pairing`, no `x-agentd-scope`, because
  `TestRoutesMatchOpenAPI` only knows base routes).
- `go.mod`/`AGENTS.md`: new dependency `github.com/skip2/go-qrcode` (justification in the pinned deps list).
- Tests: `internal/apitoken/pairing_test.go`, `cmd/agentd/pair_test.go`.

## Verification
`gofmt -l .` and `go vet ./...` clean; `go test -race -count=1 ./internal/apitoken ./internal/httpapi ./cmd/...` green:
full flow with two Pairing/Store instances (CLI and gateway) and the token used on `/v1/info`, deny (and default-no
for empty/EOF/garbage input), timeout while waiting and expired code, 5 wrong attempts burn the code (right code
then refused), replay of a used code, concurrent claims issue one token, abandoned client burns the code, eviction
beyond 3 codes, malformed/control/bidi input, per-address limit and sliding window, global lockout, unsafe file
mode refused, no code or token in logs or on disk, URL/QR payload format, CLI end to end against the real handler.
