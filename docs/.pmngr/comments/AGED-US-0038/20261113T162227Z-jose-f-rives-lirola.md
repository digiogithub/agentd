---
type: comment
item: AGED-US-0038
author: jose-f-rives-lirola
author_name: Jose F. Rives Lirola
author_email: jose@digio.es
created: 2026-11-13T16:22:27Z
kind: comment
---

Done. sync2026 history loss was a real bug (reader lag cuts PTY chunks mid-frame so every Snapshot saw SyncOpen): fixed with frame-aligned chunking (internal/session/syncgate.go) + faster sync sampling (screen.SyncSettleMax, view debounce). TestSnapshotConsistentUnderOutput and nosync case were test assumptions: scaled timeouts/child pacing via testutil.Slow/Scale, cheaper script, nosync assertions relaxed (lossy by design). Verified under 32 busy loops (-count=10 x3) and full -race suites. See docs/changes/AGED-US-0038-load-robust-tests.md
