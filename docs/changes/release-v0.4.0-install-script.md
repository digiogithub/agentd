# Release v0.4.0 and the install script

Builds on [[herdr-compat-phase3]] and the release pipeline in `docs/release.md`.

## What changed

- `install.sh` (repo root): POSIX sh installer for Linux amd64/arm64 and macOS universal. Resolves the latest
  tag from the `/releases/latest` redirect (or `AGENTD_VERSION`), downloads the tar.gz/zip and `SHA256SUMS`,
  verifies the checksum, installs `agentd` into `~/.local/bin` (`/usr/local/bin` as root, `AGENTD_INSTALL_DIR`)
  through a temporary name, checks it runs, and on Linux writes the systemd user unit (ExecStart rewritten to
  the install path) unless one exists.
- `.github/workflows/release.yml`: the release job syntax-checks `install.sh`, attaches it as a release asset and
  lists it in `SHA256SUMS`; release notes show the one-liner. Windows stays disabled (`if: false`): agentd does
  not compile for Windows (Unix PTY, signals, ioctl); decided with the user to release without it.
- `README.md` (Install script section), `docs/release.md` (Install script section, jobs table).
- Version v0.4.0 chosen by the user.

## Why

Requested: a release with a bash installer and .deb, macOS signed and notarized with the Digio Developer ID
(already configured: `MACOS_SIGNING_BUNDLE`, Azure variables and federated credentials, environment `release`).

## Verification

- `sh -n install.sh`; installed the published v0.1.0 into a temp dir (latest resolution, checksum, unit with the
  rewritten ExecStart); a missing version fails with the download URL; the workflow parses (jobs linux, macos,
  windows, release). The v0.4.0 pipeline run itself is checked after tagging.

## Outcome (2026-11-14)

- Tag `v0.4.0` pushed; release run 43648620884: Linux amd64/arm64 ok, Windows skipped, macOS failed at
  `macos-signing-keychain` (`notarytool store-credentials`): Apple `HTTP 403. A required agreement is missing or
  has expired` (the Digio developer account must accept the pending agreement; affects every Digio repo).
- With the user's approval the Linux part was published by hand: the run's `dist-linux-*` artifacts, `install.sh`
  from the tag and a fresh `SHA256SUMS` (`gh release create v0.4.0 --verify-tag`, notes generated since v0.1.0).
  Checked: `agentd version` = 0.4.0, `.deb` metadata, and the published one-liner
  `curl -fsSL …/releases/latest/download/install.sh | sh` installs 0.4.0.
- To add macOS after the agreement: `gh workflow run release.yml -f tag=v0.4.0` (it rebuilds every asset and
  replaces them, including `SHA256SUMS`).
