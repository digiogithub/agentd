# Release pipeline

Releases are built, signed and published by GitHub Actions
([`.github/workflows/release.yml`](../.github/workflows/release.yml)) when a `v*` tag is pushed.
Continuous integration ([`.github/workflows/ci.yml`](../.github/workflows/ci.yml)) runs on pushes
and pull requests to `main`: `gofmt -l`, `go vet`, `go test -race ./...` on Linux and a build on
macOS.

## Cutting a release

```sh
git tag -a v0.1.0 -m "agentd v0.1.0"      # or v0.1.0-rc.1 for a pre-release
git push origin v0.1.0
```

- The version is the tag without the leading `v`. It is injected with
  `-ldflags "-X github.com/digiogithub/agentd/internal/version.Version=<version>"`, so
  `agentd version` prints it (source builds print `0.0.0-dev`).
- A tag with a `-` suffix (`v0.1.0-rc.1`) is published as a pre-release.
- A tag-triggered run uses the workflow file **as committed in the tagged commit**: commit workflow
  changes before tagging.
- To rebuild and re-publish an existing tag (for example after a signing failure), run the workflow
  manually: Actions → Release → Run workflow, `tag` = `v0.1.0` (optionally `draft`), or
  `gh workflow run release.yml -f tag=v0.1.0`. Existing assets of that release are replaced.

## Jobs and artifacts

| Job | Runner | Output artifact | Contents |
|---|---|---|---|
| `linux` (matrix `amd64`, `arm64`) | ubuntu-latest | `dist-linux-<arch>` | `agentd_<v>_linux_<arch>.tar.gz` (binary, README, LICENSE, systemd user unit), `agentd_<v>_<arch>.deb` |
| `macos` | macos-latest | `dist-macos` | `agentd_<v>_darwin_universal.zip` (signed binary), `agentd_<v>_darwin_universal.pkg` (signed, notarized, stapled) |
| `windows` | windows-latest | `dist-windows` | disabled (`if: false`) until Windows is supported |
| `release` | ubuntu-latest | GitHub release | every `dist-*` file + `SHA256SUMS`, generated notes since the previous tag |

Only artifacts named `dist-*` are downloaded by the release job, so an unsigned intermediate can
never be published by mistake.

- **Linux**: `CGO_ENABLED=0` (the SQLite driver `modernc.org/sqlite` is pure Go), so both
  architectures are cross-compiled on one runner. The `.deb` is built with
  [nfpm](https://nfpm.goreleaser.com/) from [`packaging/nfpm.yaml`](../packaging/nfpm.yaml): it
  installs `/usr/bin/agentd`, `/usr/lib/systemd/user/agentd.service`
  ([`packaging/agentd.service`](../packaging/agentd.service), `ExecStart=/usr/bin/agentd daemon`)
  and `/usr/share/doc/agentd/`.
- **macOS**: the `amd64` and `arm64` builds are merged into one universal binary with `lipo`, then
  signed and notarized with the composite actions from
  [`digiogithub/ci-actions`](https://github.com/digiogithub/ci-actions) (pinned to `@v1`):
  1. `macos-signing-keychain` creates an ephemeral keychain from the signing bundle
     (`env-file: kvagerc`, `keychain-name: agentd-build`, `notary-profile: agentd-notary`);
  2. `macos-codesign` signs the binary with the Developer ID Application identity, hardened
     runtime and secure timestamp (no entitlements needed);
  3. `pkgbuild --sign "$PKG_SIGN_IDENTITY"` builds the installer (`/usr/local/bin/agentd`) with the
     Developer ID Installer identity; `ditto` builds the zip;
  4. `macos-notarize` submits both: the `.pkg` is stapled, the `.zip` is submit-only (a bare Mach-O
     cannot carry a ticket; Gatekeeper checks it online);
  5. `xcrun stapler validate` + `spctl --assess` verify the pkg;
  6. `macos-keychain-cleanup` runs with `if: always()`.

  The job has `timeout-minutes: 120` because notarization waits for Apple.

## Signing configuration

Set on the GitHub repository before the first tag. No value is stored in this repository.

| Kind | Name | Used by | Content |
|---|---|---|---|
| Secret | `MACOS_SIGNING_BUNDLE` | `macos` | base64 of a `.tar.gz` of a directory holding the Developer ID Application and Installer `.p12` files (names matching `*developer*id*application*.p12` / `*developer*id*installer*.p12`) and a `kvagerc` shell file defining `KEYSTORE_PASS`, `MACOS_APP_CERT_PASSWORD`, `MACOS_INSTALLER_CERT_PASSWORD`, `NOTARY_APPLE_ID`, `NOTARY_TEAM_ID`, `NOTARY_APP_PASSWORD` |
| Environment | `release` | `windows` (disabled) | OIDC federation target for Azure |
| Variables | `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID` | `windows` (disabled) | Entra app with a federated credential for `repo:<owner>/<repo>:environment:release` |
| Variables | `AZURE_SIGNING_ENDPOINT`, `AZURE_SIGNING_ACCOUNT`, `AZURE_SIGNING_CERT_PROFILE` | `windows` (disabled) | Azure Artifact Signing endpoint, account and certificate profile |

```sh
tar czf - -C <parent-dir> <signing-dir> | base64 | gh secret set MACOS_SIGNING_BUNDLE --repo digiogithub/agentd
```

The Windows job needs no secret: `azure/login` exchanges the job's OIDC token (it only validates
inside the `release` environment) and `azure/trusted-signing-action` signs with SHA-256 digests and
an RFC 3161 timestamp; the job then fails unless `Get-AuthenticodeSignature` reports `Valid`.

## Windows

Not supported yet: agentd relies on Unix PTYs, `setsid` and peer credentials
(`SO_PEERCRED` / `LOCAL_PEERPID`, `/proc`). The signing job is kept in `release.yml` behind
`if: false` (and an actionlint ignore in `.github/actionlint.yaml`); enable it after the port
described in [ROADMAP.md](../ROADMAP.md).

## Client app

The Flutter client (`app/`) has its own pipeline,
[`.github/workflows/app-release.yml`](../.github/workflows/app-release.yml), triggered by an
`app-v*` tag (independent of the Go `v*` tag). `app.yml` stays the PR/main check.

```sh
git tag -a app-v0.1.0 -m "agentd app v0.1.0"      # or app-v0.1.0-rc.1
git push origin app-v0.1.0
```

- Version: tag without `app-v`. `--build-name` is the numeric core (`0.1.0`, also for
  `-rc.N`), `--build-number` is the workflow run number (monotonic; Android `versionCode`).
- A tag run builds, signs and creates a **draft** GitHub release (review, then publish by hand).
  Manual run (Actions -> App release -> Run workflow, optional `version`) is a **dry run**: unsigned
  artifacts named `dryrun-app-*` (Android `-debugsigned`, macOS `-unsigned`, Windows `-unsigned`),
  no release. Only `dist-app-*` artifacts can reach the release job, which also refuses names
  containing `unsigned`/`debugsigned`.

| Job | Runner | Output |
|---|---|---|
| `linux` | ubuntu-latest | `agentd-app_<v>_linux_x64.tar.gz`, `agentd-app_<v>_amd64.deb` (nfpm, `packaging/app/nfpm.yaml`), `agentd-app_<v>_linux_x86_64.AppImage` (appimagetool, `packaging/app/linux/build-appimage.sh`) |
| `android` | ubuntu-latest | `agentd-app_<v>_android_<abi>.apk` (split per ABI), `agentd-app_<v>_android.aab` |
| `macos` | macos-latest | `agentd-app_<v>_macos_universal.dmg` (app signed + notarized + stapled, DMG signed + notarized + stapled) |
| `windows-build` / `windows-sign` | windows-latest | `agentd-app_<v>_windows_x64_setup.exe` (Inno Setup; app exe/dll and installer Authenticode-signed) |
| `release` | ubuntu-latest | draft release: every `dist-app-*` file + `SHA256SUMS` |

Metadata: Linux `.desktop` / AppStream (`app/linux/packaging/dev.digio.agentd_app.*`, app id
`dev.digio.agentd_app`, `MimeType=x-scheme-handler/agentd;`); macOS `CFBundleURLTypes` (`agentd`) in
`app/macos/Runner/Info.plist`; Windows `agentd://` registered by the installer
(`packaging/app/windows/agentd-app.iss`, `HKA\Software\Classes\agentd`); Android intent filter in
`AndroidManifest.xml`. Windows uses Inno Setup rather than MSIX: MSIX must be signed by a
certificate whose subject equals the manifest `Publisher`, needs extra packaging tooling and
sideloading setup, while Inno gives a single signable `.exe` that registers the URL scheme and
installs per-user without admin.

Secrets and variables (none stored in the repo):

| Kind | Name | Used by |
|---|---|---|
| Secret | `ANDROID_KEYSTORE_BASE64` (base64 of the upload `.jks`), `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD` | `android` (tag runs fail without them) |
| Secret | `MACOS_SIGNING_BUNDLE` (same bundle as the Go release) | `macos` |
| Environment + Variables | `release`, `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID`, `AZURE_SIGNING_ENDPOINT`, `AZURE_SIGNING_ACCOUNT`, `AZURE_SIGNING_CERT_PROFILE` | `windows-sign` (OIDC, no secret) |

Local Android signed build: copy `app/android/key.properties.example` to `app/android/key.properties`
(gitignored); without it (or the `ANDROID_KEY*` env vars) release builds are debug-signed.
Local checks: `flutter build linux --release`, `flutter build apk --release --split-per-abi`,
`actionlint .github/workflows/*.yml`, and the `nfpm package -p deb -f packaging/app/nfpm.yaml` dry run.

## Local checks

```sh
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/release.yml'))"
actionlint .github/workflows/*.yml

# .deb dry run (does not install anything)
go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.48.0
CGO_ENABLED=0 GOARCH=amd64 go build -ldflags "-X github.com/digiogithub/agentd/internal/version.Version=0.0.0" -o build/agentd ./cmd/agentd
mkdir -p dist && VERSION=0.0.0 GOARCH=amd64 nfpm package -p deb -f packaging/nfpm.yaml -t dist/agentd_0.0.0_amd64.deb
dpkg-deb -c dist/agentd_0.0.0_amd64.deb
```
