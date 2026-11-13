---
type: comment
item: AGED-US-0031
author: jose-f-rives-lirola
author_name: Jose F. Rives Lirola
author_email: jose@digio.es
created: 2026-11-13T17:04:04Z
kind: comment
---

Implemented launch screen, kill/remove dialogs (SessionActionsMenu), unified mailbox (list/detail/reply/compose, admin peek). Verified in app/: dart format clean, flutter analyze clean, flutter test 211 passed, flutter build linux --debug and apk --debug OK. Notes: docs/changes/AGED-US-0031-launch-mailbox.md. Entry points from agents/session screens still to be wired by their owners.
