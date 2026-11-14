#!/bin/sh
# Installs agentd from its GitHub releases (Linux amd64/arm64, macOS universal):
#
#   curl -fsSL https://github.com/digiogithub/agentd/releases/latest/download/install.sh | sh
#
# Environment:
#   AGENTD_VERSION      version to install (0.4.0 or v0.4.0; default: the latest release)
#   AGENTD_INSTALL_DIR  where the binary goes (default: ~/.local/bin; /usr/local/bin as root)
#   AGENTD_REPO         GitHub repository (default: digiogithub/agentd)
#
# The download is verified against the release's SHA256SUMS before anything is
# installed. No sudo is needed for the default location. On Debian/Ubuntu the
# .deb from the release page also installs the systemd user unit.
set -eu

repo=${AGENTD_REPO:-digiogithub/agentd}
base="https://github.com/$repo/releases"

say() { printf '%s\n' "$*"; }
fail() { say "agentd install: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "needs $1"; }
need curl
need uname

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "no build for $(uname -s) (agentd supports Linux and macOS; Windows is not supported yet)" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "no build for $(uname -m)" ;;
esac

if [ -n "${AGENTD_INSTALL_DIR:-}" ]; then
  dir=$AGENTD_INSTALL_DIR
elif [ "$(id -u)" = 0 ]; then
  dir=/usr/local/bin
else
  dir="$HOME/.local/bin"
fi

# The version: given, or the tag the "latest" redirect points to (no API call,
# so no rate limit).
version=${AGENTD_VERSION:-}
if [ -z "$version" ]; then
  url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$base/latest") || fail "cannot reach $base"
  version=${url##*/tag/}
  [ "$version" != "$url" ] || fail "cannot find the latest release at $base"
fi
version=${version#v}
case $version in
  *[!0-9A-Za-z.+-]* | "") fail "invalid version: $version" ;;
esac

if [ "$os" = linux ]; then
  asset="agentd_${version}_linux_${arch}.tar.gz"
else
  asset="agentd_${version}_darwin_universal.zip"
fi

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | cut -d' ' -f1
  else fail "needs sha256sum or shasum"; fi
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Installing agentd $version for $os/$arch into $dir..."
curl -fsSL "$base/download/v$version/$asset" -o "$tmp/$asset" || fail "download failed: $base/download/v$version/$asset"
curl -fsSL "$base/download/v$version/SHA256SUMS" -o "$tmp/SHA256SUMS" || fail "download failed: SHA256SUMS"
want=$(awk -v f="$asset" '$2 == f || $2 == "*"f { print $1 }' "$tmp/SHA256SUMS")
[ -n "$want" ] || fail "$asset is not listed in SHA256SUMS"
[ "$(sha256 "$tmp/$asset")" = "$want" ] || fail "checksum mismatch for $asset; try again"

mkdir -p "$tmp/x"
if [ "$os" = linux ]; then
  need tar
  tar -xzf "$tmp/$asset" -C "$tmp/x"
elif command -v ditto >/dev/null 2>&1; then
  ditto -x -k "$tmp/$asset" "$tmp/x"
else
  need unzip
  unzip -q "$tmp/$asset" -d "$tmp/x"
fi
[ -f "$tmp/x/agentd" ] || fail "$asset has no agentd binary"

mkdir -p "$dir"
# Replace through a temporary name: a running daemon keeps its old inode.
cp "$tmp/x/agentd" "$dir/.agentd.new.$$"
chmod 755 "$dir/.agentd.new.$$"
mv -f "$dir/.agentd.new.$$" "$dir/agentd"
"$dir/agentd" version >/dev/null 2>&1 || fail "installed $dir/agentd does not run"

if [ "$os" = linux ] && [ -f "$tmp/x/agentd.service" ] && [ "$(id -u)" != 0 ]; then
  units="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
  if [ -e "$units/agentd.service" ]; then
    say "kept the existing systemd user unit $units/agentd.service"
  else
    mkdir -p "$units"
    sed "s|/usr/bin/agentd|$dir/agentd|g" "$tmp/x/agentd.service" > "$units/agentd.service"
    say "systemd user unit: $units/agentd.service (enable with: systemctl --user enable --now agentd)"
  fi
fi

say "agentd $version installed: $dir/agentd"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) say "Add $dir to your PATH to use agentd from a terminal." ;;
esac
say "A running agentd daemon keeps the old version until restarted: agentd shutdown (it ends its sessions)."
