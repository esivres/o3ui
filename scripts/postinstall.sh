#!/bin/sh
# Postinstall hook for the o3ui .deb.
#
# The desklet is already on disk at /usr/share/cinnamon/desklets/
# o3ui@esivres/ — Cinnamon picks it up from there on its own. All we
# do here is surface a one-line hint when Cinnamon is actually
# installed, so non-Mint users aren't told to do something they
# can't. We deliberately do NOT write to the user's dconf to auto-
# enable the desklet on the desktop: postinst runs as root, the
# desktop config is per-user, and silently changing UI state on
# package install would be rude.

set -e

if command -v cinnamon >/dev/null 2>&1; then
    cat <<EOF
o3ui: Cinnamon detected.
  → Open System Settings → Desklets, find "OVPN3", click + to add it.
  → Right-click the desklet → Configure to point cli_path at /usr/bin/o3ui
    if it isn't found automatically.
EOF
fi

exit 0
