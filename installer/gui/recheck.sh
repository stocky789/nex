#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Debian Installer runs this as the first step of its partitioning stage and aborts on a failure here;
# preseed early_command does not abort. It never returns once Nex setup is active: the host restarts.
set -eu
# Also applies to the native serial installer, which configures networking via netcfg.
if [ -f /cdrom/nex/payloads.tsv ]; then
    # netcfg removes the live resolver after the Nex wizard's preflight.
    if [ -f /run/nex-installer/active ]; then
        install -m 644 /run/nex-installer/netinst-resolv.conf /etc/resolv.conf
    fi
    /bin/sh /cdrom/nex/netinst-fetch.sh --check || {
        echo 'Net installer sources are unavailable; check networking and retry before erasing disks.' >&2
        exit 1
    }
fi
if [ -f /run/nex-installer/active ]; then
    /usr/bin/nex-config --recheck /run/nex-installer/config.ini
    /bin/sh /cdrom/nex/nex-image-install.sh preflight
    # Nothing may be cleared here: the image installer verifies the image and payloads first, then stops only
    # the arrays on the selected disks and lays them out. Repair keeps nex-data and refuses unless the
    # selected disks are exactly the data disks. Neither returns (the host restarts).
    if grep -qx 'mode=repair' /run/nex-installer/image.env; then
        exec /bin/sh /cdrom/nex/nex-image-install.sh repair
    fi
    exec /bin/sh /cdrom/nex/nex-image-install.sh install
fi
