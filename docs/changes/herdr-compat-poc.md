# Herdr compatibility PoC; Flutter app removed; gateway frozen

Builds on [[herdr-compatibility-analysis]] and [[agentd/plans/herdr-compat_plan.md]]. Supersedes the client
side of [[plans/api-gateway-plan]] (the `agentd serve` gateway is frozen, not removed).

## What changed

- **Removed** the Flutter client: `app/`, `.github/workflows/app.yml`, `.github/workflows/app-release.yml`,
  `packaging/app/`; the "Flutter app" section of `AGENTS.md` and "Client app" of `docs/release.md`. Historic
  change records (`docs/changes/AGED-US-0026..0033`, `app-*.md`) are kept as history.
- **New package `internal/hcompat`**: herdr-core protocol generation 1 over the daemon socket.
  - `wire.go`: frames `[channel u32 LE][kind u8][len u32 LE]`, kinds DATA/CREDIT/CLOSE/RESIZE/FOCUS/FACTS/
    AUTOMATIC_INPUT, 16 MiB cap, 256 KiB initial credit.
  - `server.go`: `Server.Serve` (plain JSON-RPC lines until `hello {framed:true}`, then frames; channel-0 calls
    concurrent), credit-aware `stream`, shared label/keep/turn/doc state, `record` (SessionInfo -> hc Terminal).
  - `methods.go`: `hello`, `server.status`, `terminal.list/get/run/kill/rm/label/keep/read`, `doc.get/set`
    (versioned, `conflict` on stale), `machine.list`, `templates.list`.
  - `attach.go`: one daemon attach per channel; first paint = CAN + daemon redraw + `CSI ?9001l`; DATA/RESIZE
    forwarded; CLOSE answered empty; exit -> CLOSE `{"reason":"exited","exit_code":N,"signal":null}`.
  - `events.go`: daemon `subscribe` (session, status) -> hc `event` (created, status, turn, finished, removed,
    labels, keep, doc), `heartbeat`, `missed` on resync; filters terminal / labels / docs.
  - `types.go`: hc records, error kinds (`fromDaemon`), default labels, status mapping.
- **`cmd/agentd/hc.go`**: argv[0] dispatch (`herdr-core`, `hc`) and `agentd hc ...`: `--version`, `connect`,
  `machine ls --json`, refusal of `update`; `install-shim [--dir]`; persisted core id.
- Docs: `AGENTS.md` (package table, "Herdr compatibility" behaviour, gateway frozen), `ROADMAP.md` (Phase 7,
  app dropped), plan file, analysis.

## Why

The bespoke HTTP API + Flutter app was weak; Herdr clients already exist and speak a small, documented
protocol (herdr-core OpenRPC). agentd keeps its mailbox/MCP/identity on top.

## Verification

- `gofmt -l` clean, `go vet ./internal/hcompat ./cmd/...` clean.
- `go test -race -count=5 ./internal/hcompat` ok; `go test -race -count=1 ./...` all packages ok.
- Tests drive `Serve` like Herdr Desktop against a real daemon: hello gate (generation, version, Desktop's
  required methods, `installed_by`), generation 2 rejected, plain-mode list (labels, no `/` in addresses),
  subscribe + attach (CAN first paint, resize, input echo through `cat`, empty CLOSE on detach, kill ->
  `finished` event), attach to finished / unknown, doc versioning + `doc` event, run name collision + label merge.
- Manual: built binary, `agentd hc install-shim`, `herdr-core --version` = `hc 0.1.21`, `herdr-core connect`
  answered hello + `terminal.list` with an agentd session; `hc update` refused (exit 2).
- **Not verified**: the real Herdr Desktop GUI (not installed on the dev machine).

Note: the KB / code-index tools required by `AGENTS.md` were unavailable in this session; this file is the record.
