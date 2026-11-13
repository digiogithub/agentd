# AGED-US-0026: rewrite the frontend-app design for Flutter

Links: [[plans/api-gateway-plan]] (ADR-A18, F1..F8), [[design/api/frontend-app]], [[design/api/security-remote]].

## What changed
- `docs/design/api/frontend-app.md` rewritten for ADR-A18: Flutter app (Linux/macOS/Windows + Android)
  in `app/` (package `agentd_app`), no embedded web UI. Product/UX content (screens, status mapping,
  409 handling) kept; Tauri, Preact, `go:embed` and the `Platform` adapter dropped. New sections: 1 scope,
  2 UX mapping, 3 architecture (Riverpod, layers), 4 networking (dart:io HttpClient, hand-written SSE,
  Last-Event-ID, SPKI pinning with `SecurityContext(withTrustedRoots:false)` + `badCertificateCallback`),
  5 secrets/pairing (`flutter_secure_storage`, `mobile_scanner` on Android, code entry on desktop),
  6 notifications and `agentd://` deep links, 7 API client strategy (hand-written + contract tests),
  8 repo layout/CI/Go coexistence, 9 open questions.
- `docs/design/api/security-remote.md`: Tauri/embedded-UI mentions replaced by Flutter equivalents
  (sections 2.1, 2.5, 4.1, 5.1); section numbers unchanged.

## Why
ADR-A18 (maintainer decision 2026-11-13) supersedes ADR-A10/A12.

## How verified
Docs only. Package facts checked on pub.dev (flutter_secure_storage 11.x, mobile_scanner 7.x: no Linux/Windows,
flutter_local_notifications 23.x, app_links 7.x, connectivity_plus 7.x, tray_manager 0.9.x, riverpod 3.5,
flutter_foreground_task, openapi_generator 7.x). Unverified items are marked in the doc (Android pin behaviour,
go_router, window_manager, asn1lib, app_links desktop single-instance). grep shows no Tauri/Preact leftovers in
the two files.

## Follow-ups
`docs/design/api/http-api.md` still cites "frontend-app §3.2" (line ~87) and ADR-A10 in section 6; not edited here.
