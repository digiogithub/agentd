---
type: comment
item: AGED-US-0037
author: jose-f-rives-lirola
author_name: Jose F. Rives Lirola
author_email: jose@digio.es
created: 2026-11-13T15:44:59Z
kind: comment
---

Root cause: session.handleOutput stamped lastOutput before the emulator write; the view watcher could observe the stale empty screen with lastObserved > LastOutput, so the real frame was never recorded and ls Preview stayed empty. Fix: re-stamp lastOutput after the emulator write (internal/session/session.go). Verified -count=20 GOMAXPROCS=2 race, full daemon pkg, vet/gofmt. See docs/changes/AGED-US-0037-flaky-ls-preview-test.md
