#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# partman aborts on init-hook failure; preseed early_command does not.
set -eu
SYS=/sys
DEV=/dev
MDSTAT=/proc/mdstat
PARTMAN=/var/lib/partman
if [ -f /run/nex-installer/active ]; then
    /usr/bin/nex-config --recheck /run/nex-installer/config.ini
    # The operator confirmed erasing this disk. A previous install's RAID membership or LVM
    # makes partman-auto stop silently ("No root file system", BUG-0413), and partman's own
    # RAID removal zeroes every member disk. Clear only the selected disk, once, before
    # partman assembles arrays (25md-devices) and scans it.
    disk=$(sed -n 's/^disk=//p' /run/nex-installer/config.ini)
    name=${disk#/dev/}
    marker=/run/nex-installer/wiped-$name
    if [ ! -e "$marker" ]; then
        parts=
        for part in "$SYS/class/block/$name/$name"*; do
            [ -f "$part/partition" ] && parts="$parts ${part##*/}"
        done
        # Stop arrays holding this disk; nothing is written to their other members.
        for md in $(for p in $name $parts; do sed -n "s/^\(md[0-9]*\) : .*[[:space:]]$p\[[0-9]*\].*/\1/p" "$MDSTAT" 2>/dev/null; done | sort -u); do
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
        # Mark partman's RAID/LVM scan done (25md-devices, 26lvm-devices) so it neither
        # reassembles the other disks' leftover array nor gives the installed host its mdadm.conf.
        mkdir -p "$PARTMAN"
        : > "$PARTMAN/md"; : > "$PARTMAN/lvm"
        : > "$marker"
    fi
fi
