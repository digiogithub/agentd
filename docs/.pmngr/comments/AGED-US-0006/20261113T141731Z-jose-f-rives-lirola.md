---
type: comment
item: AGED-US-0006
author: jose-f-rives-lirola
author_name: Jose F. Rives Lirola
author_email: jose@digio.es
created: 2026-11-13T14:17:31Z
kind: comment
---

screenfix now wraps session.Replayer (exported from internal/session); deleted its copies of stringFilter/copyRow/attrBits/colorString (cells.go, filter.go). No golden changes. See docs/changes/screenfix-session-replayer.md
