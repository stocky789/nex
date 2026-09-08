#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# partman aborts on init-hook failure; preseed early_command does not.
set -eu
if [ -f /run/nex-installer/active ]; then
    /usr/bin/nex-config --recheck /run/nex-installer/config.ini
fi
