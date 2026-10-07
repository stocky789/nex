#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
# Install the website publish wrapper and passwordless sudo for the publishing user.
set -euo pipefail

PUBLISH_USER=${NEX_PUBLISH_USER:-}
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$SCRIPT_DIR/../.." && pwd)
RELEASE_LIB=/usr/local/lib/nex-website
PUBLISH=/usr/local/sbin/nex-website-publish
SUDOERS=/etc/sudoers.d/nex-website-publish

if [[ $(id -u) -ne 0 ]]; then
  echo "bootstrap-publish: run as root" >&2
  exit 1
fi

if [[ ! $PUBLISH_USER =~ ^[a-z_][a-z0-9_-]*$ ]]; then
  echo "bootstrap-publish: set NEX_PUBLISH_USER to the local publishing account" >&2
  exit 1
fi

if ! id -u "$PUBLISH_USER" >/dev/null 2>&1; then
  echo "bootstrap-publish: publishing user $PUBLISH_USER does not exist" >&2
  exit 1
fi

if [[ ! -f /etc/nex-website/health.env ]]; then
  echo "bootstrap-publish: missing /etc/nex-website/health.env; install the website package first" >&2
  exit 1
fi

# The release workflow publishes ISOs and image payloads as this user over SSH. It owns the download tree
# outright, so publishing them needs no sudo at all.
install -d -o "$PUBLISH_USER" -g "$PUBLISH_USER" -m 0755 /srv/nex-downloads
install -d -o "$PUBLISH_USER" -g "$PUBLISH_USER" -m 0755 /srv/nex-updates

install -d -o root -g root -m 0755 "$RELEASE_LIB"
install -D -m 0755 "$ROOT/scripts/website-release.py" "$RELEASE_LIB/website-release.py"
install -D -m 0755 "$ROOT/scripts/website-health.sh" "$RELEASE_LIB/website-health.sh"
install -D -m 0755 "$ROOT/scripts/nex-website-publish.py" "$PUBLISH"

cat >"$SUDOERS" <<EOF
# SPDX-License-Identifier: AGPL-3.0-only
$PUBLISH_USER ALL=(root) NOPASSWD: $PUBLISH
EOF
chmod 0440 "$SUDOERS"
if command -v visudo >/dev/null; then
  visudo -cf "$SUDOERS"
fi

echo "bootstrap-publish: $PUBLISH_USER may run sudo -n $PUBLISH"
