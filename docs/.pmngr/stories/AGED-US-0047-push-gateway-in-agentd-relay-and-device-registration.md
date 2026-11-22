---
id: AGED-US-0047
type: story
title: Push gateway in agentd-relay and device registration
status: backlog
priority: medium
parent: AGED-EP-0007
labels: [dashboard, relay]
created: 2026-11-22T08:23:38Z
updated: 2026-11-22T08:23:38Z
---

## Goal
Native push (D3 step 2): the relay holds the FCM credential and wakes paired devices.
Plan and contract: [[plans/agentd/dashboard_plan]] (normative). Epic: AGED-EP-0007.

## Acceptance
- agentd-relay [push] (FCM HTTP v1 credentials), endpoint used only over host links, rate limits.
- Device push token registered through the relay tunnel, stored with the device in relay/devices.json.
- Payload encrypted for the device relay key, or content-free in private mode; relay and Google see no text.
- Daemon [notify] gains sink relay_push. Tests with a fake FCM server.
- Blocked on: Firebase project (owner).
