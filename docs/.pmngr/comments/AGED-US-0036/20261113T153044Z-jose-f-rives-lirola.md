---
type: comment
item: AGED-US-0036
author: jose-f-rives-lirola
author_name: Jose F. Rives Lirola
author_email: jose@digio.es
created: 2026-11-13T15:30:44Z
kind: comment
---

Implemented: session tracks DEC 2026 (InSyncUpdate/SyncFrames, Snapshot.SyncOpen/SyncSince/SyncFrames, 1s stuck-BSU timeout); watcher never observes inside an open sync update; fallback uses History.ObserveForced (transient rows via HistoryOpts.Transient dropped, unaligned forced frames deferred up to 3x). fakeagent -redrawnosync/-redrawdelay. Tests: screen history, session sync (chunk split), daemon TestViewRedrawCleanHistory (sync + nosync). gofmt/vet clean, go test -race ./internal/screen ./internal/session ./internal/daemon green. Notes: docs/changes/AGED-US-0036-history-sampling.md
