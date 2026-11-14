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
