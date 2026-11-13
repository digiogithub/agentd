---
type: comment
item: AGED-US-0008
author: jose-f-rives-lirola
author_name: Jose F. Rives Lirola
author_email: jose@digio.es
created: 2026-11-13T14:15:02Z
kind: comment
---

Review pass: test hygiene (TestMain unsets AGENTD_TOKEN/ID/SOCKET in daemon, client, mcpserver, cmd tests) so plain go test works inside a session.
