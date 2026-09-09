#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
set -eu
set -- /usr/local/lib/nex/nex-controller -data-dir /var/lib/nex-controller -listen :8443 -tls-cert /etc/nex-controller/tls.crt -tls-key /etc/nex-controller/tls.key -ui-dir /usr/local/share/nex/dist -docs-dir /usr/local/share/nex/api -source-archive /usr/local/share/nex/nex-source.tar.gz
if [ -f /etc/nex-controller/admin-password ]; then
 set -- "$@" -admin-password-file /etc/nex-controller/admin-password
fi
exec "$@"
