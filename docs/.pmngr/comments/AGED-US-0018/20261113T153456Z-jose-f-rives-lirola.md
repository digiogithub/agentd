---
type: comment
item: AGED-US-0018
author: jose-f-rives-lirola
author_name: Jose F. Rives Lirola
author_email: jose@digio.es
created: 2026-11-13T15:34:56Z
kind: comment
---

SSE streams implemented: internal/httpapi/{sse,events,viewstream}.go + sse_test.go; hub with one daemon subscription, Last-Event-ID resume/resync, view.patch. See docs/changes/AGED-US-0018-sse-streams.md. go test -race ./internal/httpapi green.
