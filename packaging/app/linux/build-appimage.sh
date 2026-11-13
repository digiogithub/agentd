#!/bin/sh
# Build an AppImage from the Flutter Linux release bundle.
#   build-appimage.sh <bundle dir> <out.AppImage>
# Needs `appimagetool` on PATH (CI downloads it). Exits 3 when it is missing so
# callers can skip. Run from the repository root.
set -eu
bundle="$1"; out="$2"
command -v appimagetool >/dev/null 2>&1 || { echo "appimagetool not found" >&2; exit 3; }
meta=app/linux/packaging
id=dev.digio.agentd_app
appdir="$(mktemp -d)/agentd.AppDir"
mkdir -p "$appdir/usr/lib/agentd-app" "$appdir/usr/share/applications" "$appdir/usr/share/metainfo" \
  "$appdir/usr/share/icons/hicolor/512x512/apps"
cp -a "$bundle"/. "$appdir/usr/lib/agentd-app/"
cp "$meta/$id.desktop" "$appdir/$id.desktop"
cp "$meta/$id.desktop" "$appdir/usr/share/applications/"
cp "$meta/$id.metainfo.xml" "$appdir/usr/share/metainfo/"
cp "$meta/$id.png" "$appdir/$id.png"
cp "$meta/$id.png" "$appdir/usr/share/icons/hicolor/512x512/apps/"
cat > "$appdir/AppRun" <<'RUN'
#!/bin/sh
HERE="$(dirname "$(readlink -f "$0")")"
exec "$HERE/usr/lib/agentd-app/agentd_app" "$@"
RUN
chmod +x "$appdir/AppRun"
ARCH=x86_64 APPIMAGE_EXTRACT_AND_RUN=1 appimagetool "$appdir" "$out"
