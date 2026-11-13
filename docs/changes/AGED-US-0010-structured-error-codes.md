# AGED-US-0010 Structured error codes in proto.Response

## What
`proto.Response` gains an optional `code` field. Daemon errors can carry a stable class
(`invalid`, `not_found`, `starting`, `exited`, `exists`, `forbidden`, `conflict`, `busy`, `not_ready`,
`verify_failed`, `timeout`, `internal`). Error text is unchanged; old clients read `error` only and old
daemons never send `code` (`""` = unclassified).

## Files & symbols
- `internal/proto/proto.go`: `Response.Code`, `Code*` constants, `CodedError`, `Errorf`, `WithCode`,
  `CodeOf`; `ErrorResponse` uses `errors.As(*CodedError)` (also puts `CodedError.Data` in `data`);
  `RemoteError{Msg, Code, Data}`; `DecodeArgs` errors are `invalid`.
- `internal/client/client.go`: `ErrorCode(err)`, `IsCode(err, code)`.
- `internal/daemon/{handlers,server,mailboxops,identity}.go`: `lookup` -> `not_found`/`starting`; run name
  taken -> `exists`; rm of running -> `conflict`; exited kill -> `exited`; bad args/signal/name/cwd ->
  `invalid`; identity, ACL, agent-type, mailbox ownership -> `forbidden`; shutting down -> `not_ready`.
- Tests: `internal/proto/proto_test.go` (round trips, Data, old client/daemon), `internal/daemon/codes_test.go`.

## Why
Gateway (ADR-A02/A14, [[plans/api-gateway-plan]]) must map errors to HTTP status without parsing
messages ([[design/api/daemon-ops]] 3.5).

## Verified
`gofmt -l`, `go vet`, `go test -race ./internal/proto ./internal/client ./internal/daemon` green
(run with `AGENTD_TOKEN/ID/SOCKET` unset: tests inherit them from an agentd session otherwise).
