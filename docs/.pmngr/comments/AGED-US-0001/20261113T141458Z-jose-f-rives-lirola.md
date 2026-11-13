---
type: comment
item: AGED-US-0001
author: jose-f-rives-lirola
author_name: Jose F. Rives Lirola
author_email: jose@digio.es
created: 2026-11-13T14:14:58Z
kind: comment
---

Snapshot now copies raw cells under RLock and converts after unlock (+BenchmarkSnapshot); wide-char continuation cells kept as Width:0 (index==column); OSC 9 numeric sub-commands (9;4, 9;9, 9;12) ignored; title/cwd/notify sanitized and capped; Changed() doc says it never fires after exit. See docs/changes/wave1-review-fixes.md
