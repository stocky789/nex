#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
# Bootstrap a private Nex APT repository on the website host.
set -euo pipefail

REPO_ROOT=/srv/nex-apt
DOWNLOADS_ROOT=/srv/nex-downloads
GPG_HOME=/etc/nex-apt/gnupg
PUBLISH=/usr/local/sbin/nex-apt-publish
NGINX_CONF=/etc/nex-website/nginx.conf
NGINX_INC=/etc/nex-website/nginx-apt.inc
NGINX_DOWNLOADS_INC=/etc/nex-website/nginx-downloads.inc
# The release workflow publishes ISOs as this user over SSH. It owns the download
# tree outright so ISO publishing needs no sudo at all; only nex-apt-publish does,
# and that keeps the archive signing key out of reach of the runner.
PUBLISH_USER=${NEX_PUBLISH_USER:-}
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$SCRIPT_DIR/../.." && pwd)

if [[ $(id -u) -ne 0 ]]; then
  echo "bootstrap: run as root" >&2
  exit 1
fi

if [[ ! $PUBLISH_USER =~ ^[a-z_][a-z0-9_-]*$ ]]; then
  echo "bootstrap: set NEX_PUBLISH_USER to the local publishing account" >&2
  exit 1
fi

need_pkg() {
  dpkg -s "$1" >/dev/null 2>&1
}

if ! need_pkg dpkg-dev || ! need_pkg gnupg; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install --yes --no-install-recommends dpkg-dev gnupg
fi

if ! id -u "$PUBLISH_USER" >/dev/null 2>&1; then
  echo "bootstrap: publishing user $PUBLISH_USER does not exist" >&2
  exit 1
fi

install -d -o root -g root -m 0755 "$REPO_ROOT"
install -d -o root -g root -m 0700 "$GPG_HOME"
install -d -o root -g root -m 0755 /etc/nex-apt
install -d -o "$PUBLISH_USER" -g "$PUBLISH_USER" -m 0755 "$DOWNLOADS_ROOT"
install -D -m 0755 "$ROOT/scripts/apt-publish.py" "$PUBLISH"
install -D -m 0644 "$ROOT/packaging/apt/nginx-apt.inc" "$NGINX_INC"
install -D -m 0644 "$ROOT/packaging/apt/nginx-downloads.inc" "$NGINX_DOWNLOADS_INC"

if ! gpg --homedir "$GPG_HOME" --batch --list-secret-keys --with-colons 2>/dev/null | grep -q '^sec:'; then
  "$PUBLISH" --gpg-home "$GPG_HOME" --init-key
fi

insert_nginx_include() {
  local conf=$1 inc=$2 marker=$3 label=$4
  if [[ ! -f $conf ]]; then
    echo "bootstrap: missing $conf; install website packaging first" >&2
    return 1
  fi
  if grep -q "$marker" "$conf"; then
    echo "bootstrap: $label location already present in $conf"
    return 0
  fi
  local tmp
  tmp=$(mktemp)
  awk -v inc="$inc" '
    BEGIN { done=0 }
    /^[[:space:]]*location[[:space:]]+\/[[:space:]]*\{/ && !done {
      print "        include " inc ";"
      print ""
      done=1
    }
    { print }
    END {
      if (!done) {
        print "bootstrap: could not find location / in nginx.conf" > "/dev/stderr"
        exit 1
      }
    }
  ' "$conf" >"$tmp"
  install -m 0644 "$tmp" "$conf"
  rm -f "$tmp"
  echo "bootstrap: inserted include $inc into $conf"
}

if [[ -f $NGINX_CONF ]]; then
  insert_nginx_include "$NGINX_CONF" "$NGINX_INC" 'nginx-apt.inc\|# APT repository' apt
  insert_nginx_include "$NGINX_CONF" "$NGINX_DOWNLOADS_INC" \
    'nginx-downloads.inc\|# Release downloads' downloads
  if command -v nginx >/dev/null && systemctl is-active --quiet nex-website.service 2>/dev/null; then
    sudo -u nex-website nginx -t -c "$NGINX_CONF"
    systemctl reload nex-website.service
  elif command -v nginx >/dev/null; then
    echo "bootstrap: nginx config updated; start or reload nex-website when ready"
  fi
else
  echo "bootstrap: $NGINX_CONF not found; merge packaging/apt/nginx-apt.inc before location /"
fi

SUDOERS=/etc/sudoers.d/nex-apt-publish
cat >"$SUDOERS" <<EOF
# SPDX-License-Identifier: AGPL-3.0-only
$PUBLISH_USER ALL=(root) NOPASSWD: /usr/local/sbin/nex-apt-publish
EOF
chmod 0440 "$SUDOERS"
if command -v visudo >/dev/null; then
  visudo -cf "$SUDOERS"
fi

"$PUBLISH" --repo-root "$REPO_ROOT" --gpg-home "$GPG_HOME"
echo "bootstrap: APT repository ready at $REPO_ROOT"
