# AGED-US-0020: L0 raw terminal snapshot endpoint

Plan: [[plans/api-gateway-plan]]. Design: [[design/api/http-api]] 3. Part of [[changes/AGED-US-0017-rest-endpoints]].

## What changed
`GET /v1/sessions/{name}/terminal` [read]: the daemon `dump` as JSON
`{format, text, ghost, ghost_hidden, cols, rows}`. Query: `format=text|ansi` (default text), `scrollback` (bool),
`ghost` (bool: raw screen). `format=ansi` with `scrollback=true` is 400. `Accept: text/plain` (without JSON) returns only
the text. `Cache-Control: no-store`. 404 for an unknown session.

## Files and symbols
`internal/httpapi/terminal.go` (`handleTerminal`), registered in `restRoutes` (`internal/httpapi/sessions.go`);
tests `TestRESTTerminalSnapshot` in `internal/httpapi/rest_test.go`; contract in `docs/api/openapi.yaml`
(`getTerminal`, `TerminalSnapshot`).

## Why
Clients that need the literal terminal (L0) next to the semantic view (L1), e.g. for a "raw" tab. It never resizes the
PTY: the snapshot is rendered at the session's own size (`cols`/`rows` come from `ls`), clients scroll instead.

## Verification
Test compares the endpoint with `client.Dump`/`DumpResult` for text, ghost=true, scrollback, ansi (contains escapes),
text/plain; asserts the session size is unchanged afterwards; 400 and 404 cases. `gofmt`, `go vet`, `go test -race`
clean for the REST tests.
