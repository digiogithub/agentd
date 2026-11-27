# Site: push, backlog, relay pairing, voice mode, Android Auto, uploads

Continues [[site-work-dashboard]] and the apps section ([[relay-transport]]).

## What changed
- Work dashboard: four cards (`.cards.four`: 4 columns, 2 below 1180 px, 1 below 640 px); new card "A backlog you
  hand out" (`agentd note add` / `note assign`), the notifications card now tells FCM push from each host
  (question / permission, cleared once answered) besides ntfy and webhooks; snippet gains `agentd note`.
- Apps: pairing by scanning the QR of `agentd relay invite` and revoking devices from the apps; desktop list adds
  Backlog and Settings > Devices; Android list adds push (agentd 0.10+), image attachments on relay hosts
  (agentd 0.12+, op `upload`), voice mode, Android Auto, backlog/QR/revocation, color schemes and battery saver.
- English and Spanish pages kept in sync.

## Files
`pages/index.html`, `pages/es/index.html`, `pages/assets/site.css`.

## Verification
HTML tag balance checked; headless Chrome screenshots of the dashboard (1440 px, 1100 px) and apps (1440 px,
420 px) sections from a local copy.
