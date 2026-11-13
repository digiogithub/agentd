---
type: comment
item: AGED-US-0014
author: jose-f-rives-lirola
author_name: Jose F. Rives Lirola
author_email: jose@digio.es
created: 2026-11-13T14:40:00Z
kind: comment
---

Implemented op subscribe + daemon event bus (internal/daemon/events.go), proto Event types, client.Subscribe, agentd events, delivery OnDelivered hook. -race tests green for daemon, proto, client, delivery, cmd. See docs/changes/AGED-US-0014-op-subscribe.md.
