#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Keep Debian's password validation, but reject its blank-password user fallback.
set -eu
ask=/usr/bin/user-setup-ask
if [ ! -f "$ask" ]; then
    # File preseeding runs before component loading; initialize anna's media
    # index as load-cdrom does, so anna-install installs rather than queues.
    anna cdrom-retriever default
    anna-install user-setup-udeb
fi
if ! grep -q 'NEX_ROOT_PASSWORD_REQUIRED' "$ask"; then
    # Fail on an upstream change instead of silently allowing ordinary accounts.
    test "$(grep -c '^[[:space:]]*db_set passwd/root-login false$' "$ask")" = 1
    sed -i '/^[[:space:]]*db_set passwd\/root-login false$/c\
                    # NEX_ROOT_PASSWORD_REQUIRED\
                    db_fset user-setup/password-empty seen false\
                    db_input critical user-setup/password-empty || true\
                    db_fset passwd/root-password seen false\
                    db_fset passwd/root-password-again seen false' "$ask"
fi
# Debian's stock prompt advertises the fallback that Nex intentionally disables.
cat > /tmp/nex-root-password.templates <<'TEMPLATE'
Template: passwd/root-password
Type: password
Description: Root password:
 Choose a nonempty password for the root account on this Nex Core host.
 You will enter it again to confirm. No ordinary user account will be created.
 Use root at the local console; root SSH login is disabled.
TEMPLATE
debconf-loadtemplate user-setup /tmp/nex-root-password.templates
