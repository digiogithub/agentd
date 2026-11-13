# AGED-US-0019 OpenAPI contract tests

Plan: [[plans/api-gateway-plan]]. Design: [[design/api/http-api]] (section 6). Follows [[changes/gateway-review-fixes]].

## What changed

CI now fails when the `agentd serve` handlers drift from `docs/api/openapi.yaml`.

- **Validator**: `github.com/pb33f/libopenapi` + `github.com/pb33f/libopenapi-validator` (OpenAPI 3.1;
  fetched with `go get`, test-only, listed in `go.mod` and in the AGENTS.md pinned deps with justification).
  kin-openapi was rejected: the spec uses 3.1 features (`const`, `examples`, `if/then/else`,
  `type`-level 3.1 JSON Schema) that it does not fully support. No Node/Java tooling.
- `internal/httpapi/contract_test.go`: `TestContractREST` drives every operation through the real
  handler (temp daemon, `sh` and fakeagent sessions, test authenticator, httptest) with a happy path plus
  documented errors (400/401/403/404/409/412/413/415/422/503, 304, 204) and validates request, status,
  content type and body against the spec; common headers (`nosniff`, `Referrer-Policy`, JSON `Cache-Control:
  no-store`) are asserted on every response; `assertCoverage` fails if an operation lacks a 2xx/3xx and an
  error status. `TestContractSSEEvents` / `TestContractSSEViewStream` validate each frame's `data` JSON against
  the schema named by the spec's `x-sse-events` (`*` = fallback) and require hello, resync, bye,
  view, view.patch, status.changed, session.exited to be seen.
- `internal/httpapi/openapi_lint_test.go`: `TestOpenAPILint` (parses as 3.1, every `$ref` resolves, no dead
  components, unique `operationId`, `x-agentd-scope` valid (`x-agentd-pairing` for pairing), scope `none` iff
  `security: []`, path params declared, every 4xx/5xx JSON response uses the `Error` schema, SSE schema names exist).
- `contract_helpers_test.go`: spec loading, validator wiring, `contractEnv`. Existing `TestRoutesMatchOpenAPI`
  still checks paths and scopes.
- `.github/workflows/ci.yml`: a named fail-fast step `go test -race -run 'Contract|OpenAPI' ./internal/httpapi`
  (the full `go test -race ./...` already ran them).

## Drift fixed

Spec (implementation matched the design docs):
- 415 was documented only on `act`; added to `runSession`, `killSession`, `sendMessage`, `replyMessage`.
- Missing 400: `removeSession` (`force=maybe`), `getView` (bad `lines`), `getMessage` / `markMessageRead` (bad id).
- Missing 403 on `listMailbox` (`mailbox=` without admin).
- `streamView` `x-sse-events` lacked `session.removed`.

Handler:
- `internal/httpapi/idempotency.go`: an idempotent replay lost `Cache-Control: no-store` (spec: JSON responses
  carry it); added to `replayedHeaders`.

Not covered here: `pairDevice` (mounted by `agentd serve` via `AddRoute`, lives in `internal/apitoken`, which
imports `httpapi`, so it cannot be driven from this package; it is still linted and exercised by
`cmd/agentd/pair_test.go`). The spec still describes `session.removed` as an event; in practice a stream of a
removed session normally ends with `session.exited` + `bye`.

## Files

`internal/httpapi/{contract_test.go,contract_helpers_test.go,openapi_lint_test.go,idempotency.go}`,
`docs/api/openapi.yaml`, `.github/workflows/ci.yml`, `go.mod`, `go.sum`, `AGENTS.md`.

## Verification

`gofmt -l .` clean, `go vet ./...` clean, `go test -race -count=1 ./internal/httpapi` ok. Mutation checks:
adding a bogus `required` to `Message` / `Event`, removing an `operationId`, or a dangling `$ref` makes the
tests fail.
