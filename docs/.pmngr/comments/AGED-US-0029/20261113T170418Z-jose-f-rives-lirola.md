---
type: comment
item: AGED-US-0029
author: jose-f-rives-lirola
author_name: Jose F. Rives Lirola
author_email: jose@digio.es
created: 2026-11-13T17:04:18Z
kind: comment
---

Implemented agentsProvider (per-host list + /v1/events SSE, resync/epoch refetch, resume/connectivity reconnect, offline hosts) and the agents list UI (filters, badges, attention sort, empty states, navigation). Verified in app/: format, analyze, flutter test (211 pass), build linux --debug, build apk --debug. Notes in docs/changes/AGED-US-0029-agent-list.md.
