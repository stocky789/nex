#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Install and repair of Nex Core, run inside Debian Installer after the operator
# confirmed the selected disks. All disk work is nex-hostimage, the same implementation
# Core's slot writer imports; this script only finds its inputs and places payloads.
#
#   nex-image-install.sh preflight  check media, firmware and tools; change nothing
#   nex-image-install.sh install    lay out the disks, write the image, seed /etc, place payloads
#   nex-image-install.sh probe      print the layout and disks that carry a complete nex-data
#   nex-image-install.sh repair     re-lay the GPT, slots and ESPs of those disks, keeping nex-data
#
# The wizard handoff /run/nex-installer/image.env names the mode, layout and disks; install
# also reads the /etc seed staged by nex-config. Offline media carries the image and payloads
# under /cdrom/nex; a net installer downloads them by hash (payloads.tsv) instead.
set -eu

MEDIA=${NEX_MEDIA:-/cdrom/nex}
STATE=${NEX_STATE:-/run/nex-installer}
WORK=${NEX_IMAGE_WORK:-/tmp/nex-image}
UDEV=${NEX_UDEV_DATA:-/run/udev/data}
SYS=${NEX_SYS:-/sys}
DEV=${NEX_DEV:-/dev}
MDSTAT=${NEX_MDSTAT:-/proc/mdstat}
# The slot images of a net install are held in RAM until they are written.
MIN_RAM_KB=${NEX_MIN_RAM_KB:-1572864}

say() {
    printf 'nex-installer: %s\n' "$*"
    logger -t nex-installer -- "$*" 2>/dev/null || true
}
die() {
    printf '%s\n' "$*" >&2
    exit 1
}

# nex-hostimage execs only these; the udebs are part of the default installer set, and the
# module udebs are fetched here for a path that has no partman to load them.
load_tools() {
    missing=
    for tool in mkfs.fat mkfs.ext4 mdadm udevadm; do
        command -v "$tool" >/dev/null 2>&1 || missing=yes
    done
    if [ -n "$missing" ] && command -v anna-install >/dev/null 2>&1; then
        anna-install dosfstools-udeb e2fsprogs-udeb mdadm-udeb ext4-modules md-modules >/dev/null 2>&1 || true
        depmod -a 2>/dev/null || true
    fi
    for tool in mkfs.fat mkfs.ext4 mdadm udevadm; do
        command -v "$tool" >/dev/null 2>&1 || die "The installer environment lacks $tool; the installation media is incomplete."
    done
    for module in vfat ext4 raid1; do
        modprobe "$module" 2>/dev/null || true
    done
}

# The boot method follows the firmware that started this installer: UEFI starts shim, systemd-boot and the signed
# UKI from each disk's ESP; BIOS starts GRUB from each disk's MBR. The same image and layout serve both.
BOOT=
need_boot() {
    if [ -d "$SYS/firmware/efi" ]; then BOOT=uefi; else BOOT=bios; fi
    [ "$BOOT" != bios ] || grub_available || die 'The installation media carries no GRUB for BIOS firmware; the media is incomplete. No disk has been changed.'
}

# GRUB is written to each disk by grub-install, which the installer environment does not have. The media carries
# one under $MEDIA/grub: a root-tree fragment (grub2-common, grub-common and grub-pc-bin with the libraries they need). It is
# copied to RAM and run chrooted, with /dev, /proc and /sys bound in; nex-hostimage binds in the boot partition
# it mounts. A grub-install already in the environment is used as it is.
GRUB_ROOT=
grub_available() {
    command -v grub-install >/dev/null 2>&1 || [ -x "$MEDIA/grub/usr/sbin/grub-install" ]
}
prepare_grub() {
    [ "$BOOT" = bios ] || return 0
    ! command -v grub-install >/dev/null 2>&1 || return 0
    root=$WORK/grub-root
    mkdir -p "$root"
    cp -a "$MEDIA/grub/." "$root/" || die 'Could not copy GRUB from the installation media. No disk has been changed.'
    for dir in dev proc sys run; do
        mkdir -p "$root/$dir"
    done
    GRUB_ROOT=$root
    for dir in dev proc sys; do
        mount -o bind "/$dir" "$root/$dir" || { release_grub; die 'Could not prepare GRUB for BIOS firmware. No disk has been changed.'; }
    done
}
release_grub() {
    [ -n "$GRUB_ROOT" ] || return 0
    for dir in sys proc dev; do
        umount "$GRUB_ROOT/$dir" >/dev/null 2>&1 || true
    done
    GRUB_ROOT=
}

# Take the static nex-hostimage from the media; ISO file modes are not trusted to be executable.
install_binary() {
    mkdir -p "$WORK"
    [ -f "$MEDIA/bin/nex-hostimage" ] || die 'nex-hostimage is missing from the installation media.'
    cp "$MEDIA/bin/nex-hostimage" "$WORK/nex-hostimage"
    chmod 755 "$WORK/nex-hostimage"
}

# IMAGE names the release directory. Offline media is used in place; a net installer
# downloads the image set to RAM before any disk is touched, and the payloads after the data
# filesystem exists, because they are larger than installer memory.
IMAGE=
NETWORK=
select_sources() {
    if [ -f "$MEDIA/payloads.tsv" ]; then
        grep -q "$(printf '\timage/')" "$MEDIA/payloads.tsv" || die 'This net installer carries no system image.'
        NETWORK=yes
    else
        [ -f "$MEDIA/image/manifest.json" ] && [ -f "$MEDIA/image/root.erofs" ] || die 'The installation media carries no complete system image.'
        IMAGE=$MEDIA/image
    fi
}

download_image() {
    available=$(sed -n 's/^MemAvailable:[[:space:]]*\([0-9]*\) kB.*/\1/p' /proc/meminfo)
    [ "${available:-0}" -ge "$MIN_RAM_KB" ] || die 'Not enough memory to download the system image. Use the offline installer or a host with at least 4 GiB of RAM. No disk has been changed.'
    say 'Downloading the signed system image set'
    NEX_NETINST_ROWS='image/' sh "$MEDIA/netinst-fetch.sh" "$WORK/download" >/dev/null
    IMAGE=$WORK/download/image
}

# One line of progress per nex-hostimage output line, so the installer log follows the
# write. The exit status is nex-hostimage's, not the reader's. A pipe, not a named pipe: the
# installer's BusyBox has no mkfifo (found in the first QEMU run of the image ISO).
run_hostimage() {
    status_file=$WORK/hostimage.status
    rm -f "$status_file"
    { "$WORK/nex-hostimage" "$@" 2>&1; echo $? > "$status_file"; } | while IFS= read -r line; do say "$line"; done
    status=$(cat "$status_file" 2>/dev/null) || status=1
    return "${status:-1}"
}

# /dev/sda + 6 -> /dev/sda6, /dev/nvme0n1 + 6 -> /dev/nvme0n1p6
partition() {
    case $1 in
        *[0-9]) printf '%sp%s\n' "$1" "$2" ;;
        *) printf '%s%s\n' "$1" "$2" ;;
    esac
}

env_value() { sed -n "s/^$1=//p" "$STATE/image.env"; }

read_env() {
    mode=$(env_value mode)
    layout=$(env_value layout)
    disks=$(env_value disks)
    case ${mode:-install} in install|repair) ;; *) die 'Unsupported mode in the installation settings.' ;; esac
    case $layout in single|raid1) ;; *) die 'Unsupported layout in the installation settings.' ;; esac
    case $disks in ''|*[!a-zA-Z0-9/,_-]*) die 'Invalid disk list in the installation settings.' ;; esac
}

# Disk lists compare as sets: the wizard and sysfs order disks differently.
sorted_list() {
    list=$(printf '%s\n' "$1" | tr ',' '\n' | sort | tr '\n' ',')
    printf '%s\n' "${list%,}"
}

# Partition names that carry the Nex data volume, from udev's probe of each GPT entry:
# a plain ext4 labelled nex-data, or a RAID member of an array named nex-data. Sets
# FOUND_DISKS (comma list) and FOUND_LAYOUT; returns 1 unless that data volume is complete:
# every member found has the same kind, and a mirror shows at least two members.
probe_data() {
    FOUND_DISKS=
    FOUND_LAYOUT=
    members=0
    kinds=
    for part in "$SYS"/class/block/*; do
        [ -f "$part/partition" ] || continue
        entry=$UDEV/b$(cat "$part/dev")
        [ -f "$entry" ] || continue
        grep -qx 'E:ID_PART_ENTRY_NAME=nex-data' "$entry" || continue
        if grep -qx 'E:ID_FS_TYPE=ext4' "$entry" && grep -qx 'E:ID_FS_LABEL=nex-data' "$entry"; then
            kind=single
        elif grep -qx 'E:ID_FS_TYPE=linux_raid_member' "$entry" && grep -Eq '^E:ID_FS_LABEL=(.*:)?nex-data$' "$entry"; then
            kind=raid1
        else
            continue
        fi
        disk=$(basename "$(readlink -f "$part/..")")
        case ",$FOUND_DISKS," in *",/dev/$disk,"*) continue ;; esac
        FOUND_DISKS=${FOUND_DISKS:+$FOUND_DISKS,}/dev/$disk
        members=$((members + 1))
        case " $kinds " in *" $kind "*) ;; *) kinds="$kinds $kind" ;; esac
    done
    kinds=${kinds# }
    FOUND_DISKS=$(sorted_list "$FOUND_DISKS")
    case $kinds in
        single) [ "$members" -eq 1 ] || return 1 ;;
        raid1) [ "$members" -ge 2 ] || return 1 ;;
        *) return 1 ;;
    esac
    FOUND_LAYOUT=$kinds
}

# Stop the arrays that hold a selected disk, so its partitions can be re-laid. Nothing else is
# assembled, stopped or written here: another array stays untouched even when it shares a name.
release_disks() {
    names=
    for disk in $(printf '%s' "$disks" | tr ',' ' '); do
        name=${disk#/dev/}
        names="$names $name"
        for part in "$SYS/class/block/$name/$name"*; do
            [ -f "$part/partition" ] && names="$names ${part##*/}"
        done
    done
    for md in $(for n in $names; do sed -n "s/^\\(md[0-9]*\\) : .*[[:space:]]$n\\[[0-9]*\\].*/\\1/p" "$MDSTAT" 2>/dev/null; done | sort -u); do
        say "Stopping $md, which holds a selected disk"
        mdadm --stop "$DEV/$md" || die "Could not stop $md, which holds a selected disk. No disk has been changed."
    done
}

# Each payload directory is named by the SHA-256 of its main file. Offline media carries the
# VirtIO ISO gzip-compressed, so the hash is taken over the expanded bytes. Net installs
# verify their payloads after download, once the data volume exists.
verify_media_payloads() {
    [ -z "$NETWORK" ] || return 0
    for dir in "$MEDIA"/payload/*/; do
        [ -d "$dir" ] || continue
        for main in virtio-win.iso.gz controller-system.img; do
            [ -f "$dir$main" ] || continue
            case $main in
                *.gz)
                    # A pipeline reports sha256sum's status even when gunzip rejects the trailer.
                    gunzip -t "$dir$main" || die "Payload $main on the installation media has a damaged gzip stream. No disk has been changed."
                    sum=$(gunzip -c "$dir$main" | sha256sum)
                    ;;
                *) sum=$(sha256sum < "$dir$main") ;;
            esac
            [ "${sum%% *}" = "$(basename "$dir")" ] || die "Payload $main on the installation media does not match its recorded SHA-256; the media is damaged. No disk has been changed."
        done
    done
}

# The net installer places the payloads on the new nex-data, which is mounted as /var on
# the host: /var/lib/nex/payload is <data>/lib/nex/payload.
mount_data() {
    if [ "$layout" = raid1 ]; then
        # Only this install's members: --scan would assemble every array on every attached disk.
        parts=
        for disk in $(printf '%s' "$disks" | tr ',' ' '); do parts="$parts $(partition "$disk" 6)"; done
        [ -e "$DEV/md/nex-data" ] || mdadm --assemble "$DEV/md/nex-data" --run $parts >/dev/null 2>&1 || true
        data=$DEV/md/nex-data
    else
        data=$(partition "${disks%%,*}" 6)
    fi
    # The kernel re-reads the partition table when nex-hostimage closes the disk and the node can be
    # absent for a moment (found in the first QEMU run of the image ISO).
    udevadm settle >/dev/null 2>&1 || true
    waited=0
    while [ ! -e "$data" ] && [ "$waited" -lt "${NEX_NODE_WAIT:-30}" ]; do
        sleep 1
        waited=$((waited + 1))
    done
    [ -e "$data" ] || die "The new data volume $data is missing."
    mkdir -p "$WORK/data"
    mount -t ext4 "$data" "$WORK/data"
    PAYLOAD_DIR=$WORK/data/lib/nex/payload
    mkdir -p "$PAYLOAD_DIR"
}

# Each payload directory is named by the SHA-256 of its main file. Offline media may carry
# that file gzip-compressed as transport; it is expanded and re-hashed here.
copy_payloads() {
    for source in "$MEDIA"/payload/*/; do
        [ -d "$source" ] || continue
        name=$(basename "$source")
        mkdir -p "$PAYLOAD_DIR/$name"
        for file in "$source"*; do
            case $file in
                *.gz) gunzip -c "$file" > "$PAYLOAD_DIR/$name/$(basename "${file%.gz}")" ;;
                *) cp "$file" "$PAYLOAD_DIR/$name/" ;;
            esac
        done
    done
}

verify_payloads() {
    for dir in "$PAYLOAD_DIR"/*/; do
        [ -d "$dir" ] || continue
        name=$(basename "$dir")
        for main in virtio-win.iso controller-system.img; do
            [ -f "$dir$main" ] || continue
            printf '%s  %s\n' "$name" "$dir$main" | sha256sum -c - >/dev/null 2>&1 || {
                die "Payload $main does not match its recorded SHA-256; the media or download is damaged."
            }
        done
    done
}

# Leave nothing mounted or assembled when placing payloads fails, so a rerun in the same
# session is not refused by release_disks.
cleanup_data() {
    umount "$WORK/data" >/dev/null 2>&1 || true
    [ "$layout" != raid1 ] || mdadm --stop "$DEV/md/nex-data" >/dev/null 2>&1 || true
}

place_payloads() {
    trap cleanup_data EXIT
    mount_data
    if [ -n "$NETWORK" ]; then
        say 'Downloading the Controller appliance and VirtIO driver payloads'
        NEX_NETINST_ROWS='payload/' sh "$MEDIA/netinst-fetch.sh" "$WORK/data/lib/nex" >/dev/null \
            || die 'Payload download failed verification. The system is installed but carries no Controller appliance or VirtIO payloads (Repair does not place them). Fetch them from Nex Core, or run Erase and install again once the network is fixed.'
    else
        say 'Copying the Controller appliance and VirtIO driver payloads'
        copy_payloads
    fi
    verify_payloads
    sync
    umount "$WORK/data"
    trap - EXIT
    [ "$layout" != raid1 ] || mdadm --stop "$DEV/md/nex-data" >/dev/null 2>&1 || true
}

finish() {
    sync
    say 'Installation complete. Remove the installation media; the host restarts now.'
    sleep "${NEX_RESTART_DELAY:-10}"
    reboot
}

case ${1:-} in
preflight)
    need_boot
    select_sources
    [ -f "$STATE/image.env" ] && read_env
    [ -f "$MEDIA/bin/nex-hostimage" ] || die 'nex-hostimage is missing from the installation media.'
    ;;
install)
    need_boot
    select_sources
    read_env
    [ "${mode:-install}" = install ] || die 'The settings select Repair, not Install.'
    load_tools
    install_binary
    [ -z "$NETWORK" ] || download_image
    # Nothing on any disk changes until the image and the media payloads have verified.
    "$WORK/nex-hostimage" verify "$IMAGE" >/dev/null || die 'The system image failed verification; the media or download is damaged. No disk has been changed.'
    verify_media_payloads
    prepare_grub
    release_disks
    say "Installing the system image on $disks ($layout, $BOOT boot)"
    status=0
    run_hostimage install --boot "$BOOT" ${GRUB_ROOT:+--grub-chroot "$GRUB_ROOT"} --layout "$layout" --disks "$disks" --image-dir "$IMAGE" --etc-seed "$STATE/target/etc" || status=$?
    release_grub
    [ "$status" -eq 0 ] || die 'nex-hostimage could not install the image; see the installer log.'
    place_payloads
    finish
    ;;
probe)
    load_tools
    probe_data || die 'No complete Nex Core data volume was found.'
    printf 'layout=%s\ndisks=%s\n' "$FOUND_LAYOUT" "$FOUND_DISKS"
    ;;
repair)
    need_boot
    select_sources
    read_env
    [ "$mode" = repair ] || die 'The settings select Install, not Repair.'
    load_tools
    probe_data || die 'No complete Nex Core data volume was found. A mirror needs every member disk; Repair has nothing to keep. Use Erase and install instead. No disk has been changed.'
    [ "$FOUND_LAYOUT" = "$layout" ] && [ "$FOUND_DISKS" = "$(sorted_list "$disks")" ] \
        || die "Repair must name exactly the disks that hold the Nex Core data ($FOUND_DISKS). No disk has been changed."
    install_binary
    [ -z "$NETWORK" ] || download_image
    prepare_grub
    # Repair re-lays the table, so an array holding a selected disk must stop first; refuse before any write.
    release_disks
    say "Repairing the system image on $disks ($layout, $BOOT boot); nex-data is kept"
    status=0
    run_hostimage repair --boot "$BOOT" ${GRUB_ROOT:+--grub-chroot "$GRUB_ROOT"} --layout "$layout" --disks "$disks" --image-dir "$IMAGE" || status=$?
    release_grub
    [ "$status" -eq 0 ] || die 'nex-hostimage could not repair the image; see the installer log.'
    finish
    ;;
*)
    die 'Usage: nex-image-install.sh preflight|install|probe|repair'
    ;;
esac
