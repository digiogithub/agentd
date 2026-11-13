---
type: comment
item: AGED-US-0028
author: jose-f-rives-lirola
author_name: Jose F. Rives Lirola
author_email: jose@digio.es
created: 2026-11-13T17:27:21Z
kind: comment
---

Review fixes: re-pair keeps base URL and pin unless the user confirms a change in a warning dialog (old vs new); HostStore add/re-pair is transactional (stage metadata, write token, commit, rollback) and remove deletes the token first; pairing link goes through an in-memory provider, not the route; secret-store hint on Android says re-pair; android backup disabled. Details: docs/changes/app-review-fixes.md
