# Herdr compatibility phase 1: hc state in the daemon

Continues [[herdr-compat-poc]]; plan [[agentd/plans/herdr-compat_plan.md]]; analysis [[herdr-compatibility-analysis]].

## What changed

- **proto**: `OpLabel`/`OpDocGet`/`OpDocSet`, `LabelArgs/Result`, `DocGetArgs/Result`, `DocSetArgs/Result`,
  `RunArgs.Labels`, `SessionInfo.Labels/Turn`, `StatusChangedData.Turn` (omitempty), events `session.labels`
  (`LabelsData`) and `doc.changed` (`DocChangedData`).
- **config**: `DocsPath()` (`$STATE/docs.json`).
- **daemon** (`meta.go`): label validation/merge (`mergeLabels`), op `label` (identity rule of `type`), document
  store `docStore` (lazy load, compacted JSON, atomic write 0600, broken file -> `.bad`), `docGet`, `docSet`
  (humans only, optimistic version, null deletes), event kinds `doc`; `run` validates and keeps labels;
  `info` reports labels; `view.go` counts `turn` when the published status leaves `working`.
- **client**: `Label`, `DocGet`, `DocSet`.
- **hcompat**: `Server` holds no state; `visibleLabels` + reserved labels `agentd.hc.keep` / `agentd.hc.labels`;
  label/keep/run/doc methods call the daemon; `events.go` translates `session.labels` (labels + keep events,
  label-filter enter/leave with `record`) and `doc.changed`; `turn` from the daemon. `attach.go` rewritten: a
  reader always drains the daemon stream into a bounded backlog, a sender honours credit, overflow beyond
  `Server.MaxPending` (4 MiB) reopens the daemon attach and the redraw becomes a CAN repaint.
- Tests: `internal/daemon/meta_test.go` (labels via run/label, events, session permissions, documents: versions,
  conflict, names, size, null delete, persistence across a daemon restart, broken file); turn assertion in
  `TestViewStatusTransitionsAttentionAndRevisions`; `internal/hcompat/schema_test.go` (mini JSON Schema validator
  over vendored `testdata/herdr-core-released.openrpc.json`, advertised methods exist upstream) and result checks
  in every hcompat test; `phase1_test.go` (state shared across two connections, default-label deletion sticks,
  reserved label refused, slow viewer gets a repaint and stays interactive, turn events).
- Docs: `AGENTS.md` ops/events/paths/hc paragraph, plan, this record.

## Why

The PoC kept labels, keep, turns and Desktop's `herdr-app` layout document in the `hc connect` process: lost on
every Desktop restart and invisible to other connections. A stalled viewer could also block the daemon attach
past its 10 s write deadline.

## Verification

- `gofmt -l` clean, `go vet ./...` clean.
- `go test -race -count=3 ./internal/hcompat` ok; `go test -race -count=1 ./...` ok (after making
  `StatusChangedData.Turn` omitempty, which `proto` golden test `TestEventJSONShape` required).
- Not verified: real Herdr Desktop / Whip GUIs.

Known gap: the frozen gateway forwards daemon events it subscribes to; `session.labels` / `doc.changed` carry no
screen content (labels and document names/versions only).

Note: KB tools unavailable in this session; this file is the record.
