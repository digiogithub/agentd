---
id: AGED-US-0033
type: story
title: App release builds
status: done
priority: medium
parent: AGED-EP-0005
milestone: AGED-M-0003
labels: [app, release, plan:f7]
estimate: 3
created: 2026-11-13T13:55:14Z
updated: 2026-11-13T17:46:58Z
started: 2026-11-13T17:28:28Z
closed: 2026-11-13T17:46:58Z
---

## Description
CI/release jobs for Linux (tar.gz/AppImage), macOS (signed + notarized), Windows (Authenticode) and Android APK, following `digio-release-signing`; tag-driven like the Go release.

## Acceptance Criteria
- Artifacts built from a tag in CI.

## Notes
- Plan: [[plans/api-gateway-plan]]
- [[release]]
