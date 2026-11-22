---
id: AGED-US-0048
type: story
title: "Mobile: FCM/APNs push client, notification actions, private mode"
status: backlog
priority: medium
parent: AGED-EP-0007
labels: [dashboard, mobile]
created: 2026-11-22T08:23:39Z
updated: 2026-11-22T08:23:39Z
---

## Goal
Phone receives push even when the app is closed.
Plan and contract: [[plans/agentd/dashboard_plan]] (normative). Epic: AGED-EP-0007.

## Acceptance
- firebase_messaging (Android, iOS via APNs), token registration over the relay, decrypt payload locally.
- Actions: question options and Responder (answer via reply); terminal menus only open the app.
- Private mode: content-free notification.
- Blocked on: Firebase project (owner) and the relay push gateway story.
