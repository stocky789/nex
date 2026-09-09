#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
# Bootstrap a private Nex APT repository on the website host.
set -euo pipefail

REPO_ROOT=/srv/nex-apt
GPG_HOME=/etc/nex-apt/gnupg
PUBLISH=/usr/local/sbin/nex-apt-publish
NGINX_CONF=/etc/nex-website/nginx.conf
NGINX_INC=/etc/nex-website/nginx-apt.inc
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$SCRIPT_DIR/../.." && pwd)

if [[ $(id -u) -ne 0 ]]; then
  echo "bootstrap: run as root" >&2
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

install -d -o root -g root -m 0755 "$REPO_ROOT"
install -d -o root -g root -m 0700 "$GPG_HOME"
install -d -o root -g root -m 0755 /etc/nex-apt
install -D -m 0755 "$ROOT/scripts/apt-publish.py" "$PUBLISH"
install -D -m 0644 "$ROOT/packaging/apt/nginx-apt.inc" "$NGINX_INC"

if ! gpg --homedir "$GPG_HOME" --batch --list-secret-keys --with-colons 2>/dev/null | grep -q '^sec:'; then
  "$PUBLISH" --gpg-home "$GPG_HOME" --init-key
fi

insert_nginx_include() {
  local conf=$1
  if [[ ! -f $conf ]]; then
    echo "bootstrap: missing $conf; install website packaging first" >&2
    return 1
  fi
  if grep -q 'nginx-apt.inc\|# APT repository' "$conf"; then
    echo "bootstrap: apt location already present in $conf"
    return 0
  fi
  local tmp
  tmp=$(mktemp)
  awk -v inc="$NGINX_INC" '
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
  echo "bootstrap: inserted include $NGINX_INC into $conf"
}

if [[ -f $NGINX_CONF ]]; then
  insert_nginx_include "$NGINX_CONF"
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
cat >"$SUDOERS" <<'EOF'
# SPDX-License-Identifier: AGPL-3.0-only
matt ALL=(root) NOPASSWD: /usr/local/sbin/nex-apt-publish
EOF
chmod 0440 "$SUDOERS"
if command -v visudo >/dev/null; then
  visudo -cf "$SUDOERS"
fi

"$PUBLISH" --repo-root "$REPO_ROOT" --gpg-home "$GPG_HOME"
echo "bootstrap: APT repository ready at $REPO_ROOT"
