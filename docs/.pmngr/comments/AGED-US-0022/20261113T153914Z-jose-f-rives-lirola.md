---
type: comment
item: AGED-US-0022
author: jose-f-rives-lirola
author_name: Jose F. Rives Lirola
author_email: jose@digio.es
created: 2026-11-13T15:39:14Z
kind: comment
---

Implemented: agentd serve pair (QR via skip2/go-qrcode, added to AGENTS.md pinned deps), apitoken pairing store (api/pairing.json, flock), POST /v1/pair long-poll handler, httpapi.Server.AddRoute, openapi entry. Tests green with -race (apitoken, httpapi, cmd). See docs/changes/AGED-US-0022-qr-pairing.md.
