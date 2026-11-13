# AGED-US-0033 App release builds

## What

Tag-driven release pipeline for the Flutter client (`app/`), separate from the Go `v*` release:
`app-v*` tag -> build, sign, package, **draft** GitHub release. `workflow_dispatch` is an unsigned
dry run (`dryrun-app-*` artifacts, no release). Nothing is published by this change.

Builds: Linux x64 (tar.gz, .deb via nfpm, AppImage via appimagetool), Android (split-per-ABI APKs +
AAB), macOS (universal .app signed + notarized, DMG signed + notarized + stapled), Windows (Inno
Setup installer; exe/dll and installer Authenticode-signed via Azure Artifact Signing OIDC).
Version from the tag: `--build-name` = numeric core, `--build-number` = run number.
Windows uses Inno Setup, not MSIX (MSIX needs a cert whose subject matches the manifest Publisher
plus sideload setup; Inno is one signable exe that registers `agentd://` per-user without admin).

## Files

- `.github/workflows/app-release.yml` (new); `.github/workflows/app.yml` (path filter + comment)
- `app/android/app/build.gradle.kts`: release signing from `ANDROID_KEYSTORE_FILE|PASSWORD`,
  `ANDROID_KEY_ALIAS|PASSWORD` env or `android/key.properties`; debug-sign fallback; also
  `compileSdk >= 37` and core library desugaring (required by `flutter_local_notifications`, added
  by the notifications work; without them `assembleRelease` failed)
- `app/android/key.properties.example`
- `app/linux/packaging/dev.digio.agentd_app.{desktop,metainfo.xml,png}` (scheme handler
  `x-scheme-handler/agentd`)
- `app/macos/Runner/Info.plist` (`CFBundleURLTypes` agentd), `Release.entitlements`
  (+ `network.client`, needed under the sandbox)
- `packaging/app/nfpm.yaml`, `packaging/app/linux/{postinstall.sh,build-appimage.sh}`,
  `packaging/app/macos/make-dmg.sh`, `packaging/app/windows/agentd-app.iss`
- `docs/release.md` ("Client app"), `AGENTS.md` (note)

## Secrets / variables to configure (not done here)

- `ANDROID_KEYSTORE_BASE64`, `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD`
- `MACOS_SIGNING_BUNDLE` (same as the Go release)
- Environment `release` + variables `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID`,
  `AZURE_SIGNING_ENDPOINT`, `AZURE_SIGNING_ACCOUNT`, `AZURE_SIGNING_CERT_PROFILE`; the Entra app needs
  federated credentials for this repo (`scripts/setup-repo.sh` of the digio-release-signing skill)

## Cut a release

`git tag -a app-v0.1.0 -m "agentd app v0.1.0" && git push origin app-v0.1.0`, then review and publish
the draft. Commit the workflow before tagging (tag runs use the tagged commit's workflow).

## Verification

Local: `flutter build linux --release` OK; `flutter build apk --release --split-per-abi` OK (3 APKs, debug-signed fallback confirmed with apksigner); nfpm `.deb` built and inspected (desktop file, metainfo, icon, symlink, postinst);
tar.gz built; `actionlint .github/workflows/*.yml` clean; YAML parses; AppImage skipped
(`appimagetool` absent, script exits 3 cleanly).

Only in CI (untested): AppImage build, aab/apk signing with real keystore, macOS keychain/sign/
notarize/staple of the `.app` (`deep` + Release entitlements may need tuning for nested frameworks),
DMG creation and Gatekeeper check, Windows Flutter build, Inno compile, Azure signing, release job.
Not verified: appimagetool 1.9.0 download URL, `azure/trusted-signing-action` `files-folder-recurse`.
