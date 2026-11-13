#!/bin/sh
# Build a DMG from a signed .app: make-dmg.sh <App.app> <out.dmg> <volume name>
# Uses hdiutil only (no third-party tools). The DMG itself is signed/notarized by the workflow.
set -eu
app="$1"; out="$2"; vol="${3:-agentd}"
stage="$(mktemp -d)"
cp -R "$app" "$stage/"
ln -s /Applications "$stage/Applications"
hdiutil create -volname "$vol" -srcfolder "$stage" -ov -format UDZO "$out"
rm -rf "$stage"
