---
type: comment
item: AGED-US-0033
author: jose-f-rives-lirola
author_name: Jose F. Rives Lirola
author_email: jose@digio.es
created: 2026-11-13T17:35:51Z
kind: comment
---

Pipeline prepared (nothing published): .github/workflows/app-release.yml (app-v* tags, dispatch dry run), Android signing config, Linux/macOS/Windows packaging metadata, docs/release.md Client app, docs/changes/AGED-US-0033-app-release.md. Verified locally: linux release build, apk release split-per-abi (debug-signed fallback), nfpm deb, tar.gz, actionlint. CI-only: AppImage, signing/notarization, Windows, release job.
