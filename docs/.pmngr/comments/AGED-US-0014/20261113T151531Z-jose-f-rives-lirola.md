---
type: comment
item: AGED-US-0014
author: jose-f-rives-lirola
author_name: Jose F. Rives Lirola
author_email: jose@digio.es
created: 2026-11-13T15:15:31Z
kind: comment
---

Code-review fixes applied (18 items, see docs/changes/ops-review-fixes.md). Key points: menu_digit_action (select|move) + menu-instance identity (fingerprint + screen context + transcript tail) for choose; revision+epoch required; every later plan step needs the same instance; paste verification tied to the paste; BeforeSubmit re-check; unsubmitted paste is a draft; WriteAct separates act input from real human input; subscribe since_seq needs epoch, snapshot ordering, identity subscriptions end on rm, client closes conn on bye; session.created only after start, exited before removed. gofmt/vet clean, go test -race ./... green.
