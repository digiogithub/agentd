#!/bin/sh
# Refresh the desktop menu and icon caches after installing or removing
# agentd-desktop. Best effort: missing tools are not an error.
set -e
if command -v update-desktop-database >/dev/null 2>&1; then
	update-desktop-database -q /usr/share/applications || true
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
	gtk-update-icon-cache -q -t -f /usr/share/icons/hicolor || true
fi
exit 0
