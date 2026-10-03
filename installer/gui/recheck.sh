#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# partman aborts on init-hook failure; preseed early_command does not.
set -eu
SYS=/sys
DEV=/dev
MDSTAT=/proc/mdstat
PARTMAN=/var/lib/partman
# Also applies to the native serial installer, which configures networking via netcfg.
if [ -f /cdrom/nex/payloads.tsv ]; then
    # netcfg removes the live resolver after the Nex wizard's preflight.
    if [ -f /run/nex-installer/active ]; then
        install -m 644 /run/nex-installer/netinst-resolv.conf /etc/resolv.conf
    fi
    # The image edition downloads other rows than the classic edition.
    if [ -f /run/nex-installer/image.env ]; then NEX_NETINST_ROWS='image/ payload/'; export NEX_NETINST_ROWS; fi
    /bin/sh /cdrom/nex/netinst-fetch.sh --check || {
        echo 'Net installer sources are unavailable; check networking and retry before erasing disks.' >&2
        exit 1
    }
fi
if [ -f /run/nex-installer/active ]; then
    /usr/bin/nex-config --recheck /run/nex-installer/config.ini
    if [ -f /run/nex-installer/image.env ]; then
        /bin/sh /cdrom/nex/nex-image-install.sh preflight
    fi
    # The image edition has no partman, base-installer or GRUB step, and nothing may be cleared
    # here: the image installer verifies the image and payloads first, then stops only the arrays
    # on the selected disks and lays them out. Repair keeps nex-data and refuses unless the
    # selected disks are exactly the data disks. Neither returns (the host restarts).
    if [ -f /run/nex-installer/image.env ]; then
        if grep -qx 'mode=repair' /run/nex-installer/image.env; then
            exec /bin/sh /cdrom/nex/nex-image-install.sh repair
        fi
        exec /bin/sh /cdrom/nex/nex-image-install.sh install
    fi
    # The operator confirmed erasing these disks: one, or every member of the mirror.
    # A previous install's RAID membership or LVM makes partman-auto stop silently
    # ("No root file system", BUG-0413), and partman's own RAID removal zeroes every
    # member disk. Clear only the selected disks, once each, before partman assembles
    # arrays (25md-devices) and scans them.
    for disk in $(sed -n 's/^disk=//p' /run/nex-installer/config.ini | tr ';' ' '); do
        name=${disk#/dev/}
        marker=/run/nex-installer/wiped-$name
        if [ -e "$marker" ]; then continue; fi
        parts=
        for part in "$SYS/class/block/$name/$name"*; do
            [ -f "$part/partition" ] && parts="$parts ${part##*/}"
        done
        # Stop arrays holding this disk; nothing is written to their other members.
        for md in $(for p in $name $parts; do sed -n "s/^\\(md[0-9]*\\) : .*[[:space:]]$p\\[[0-9]*\\].*/\\1/p" "$MDSTAT" 2>/dev/null; done | sort -u); do
            mdadm --stop "$DEV/$md"
        done
        # Zero the first and last MiB of every partition, then of the disk: covers RAID
        # superblocks (0.90/1.x), LVM labels, filesystem signatures and both GPT copies.
        for p in $parts $name; do
            if [ "$p" = "$name" ]; then size=$(cat "$SYS/class/block/$name/size")
            else size=$(cat "$SYS/class/block/$name/$p/size"); fi
            n=2048; [ "$size" -lt 4096 ] && n=$size
            dd if=/dev/zero of="$DEV/$p" bs=512 count=$n conv=notrunc 2>/dev/null
            dd if=/dev/zero of="$DEV/$p" bs=512 count=$n seek=$((size - n)) conv=notrunc 2>/dev/null
        done
        sync
        blockdev --rereadpt "$DEV/$name" 2>/dev/null || true
        logger -t nex-installer "Cleared previous partitions and RAID/LVM signatures from $disk" 2>/dev/null || true
        : > "$marker"
    done
    # Mark partman's RAID/LVM scan done (25md-devices, 26lvm-devices) so it does not
    # reassemble leftover arrays on disks the operator did not select. partman-auto-raid
    # loads md-mod and creates this install's mirror itself, so the marker does not stop it.
    mkdir -p "$PARTMAN"
    : > "$PARTMAN/md"; : > "$PARTMAN/lvm"
fi
