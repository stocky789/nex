#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Text frontend for Nex Core setup, used when the installer runs without the
# graphical frontend. It collects the same settings as the GTK wizard and writes
# the same /run/nex-installer/config.ini handoff, so nex-config remains the single
# validator and the installed host is identical either way. This never partitions
# a disk and never configures the running machine's network.
#
# set -e is deliberately not used: the debconf protocol reports "the operator went
# back" as exit status 30, which every db_* call can return.
set -u

# Every path this script reads or writes hangs off one root so the wizard can be
# exercised against a fixture tree. It is empty in the installer, and nex-config
# revalidates the handoff against the real machine before anything is written.
ROOT=${NEX_WIZARD_ROOT:-}
STATE=$ROOT/run/nex-installer
SYS=$ROOT/sys
MOUNTS=$ROOT/proc/mounts
SHARE=$ROOT/usr/share/nex-installer
TIMEZONES=$SHARE/timezones
PREPARE=$ROOT/usr/lib/nex-installer/prepare
VALIDATE=$ROOT/usr/lib/nex-installer/validate-config # nex-config, validate only
# Two slots and a nex-data that holds a staged update, the payloads and the VM store; nex-config enforces the same floor.
MIN_SECTORS=50331648 # 24 GiB in 512-byte sectors
OFFLINE='Offline (no management network)' # the graphical wizard's wording

umask 077
mkdir -p "$STATE"

# Hardware discovery is shared with the graphical wizard and is safe to repeat.
# cdrom-detect, anna and hw-detect drive debconf themselves, so this must run
# before a frontend exists: confmodule below re-executes this script under
# cdebconf, and only that first pass, with no question on screen, prepares.
if [ -z "${DEBIAN_HAS_FRONTEND-}" ] && [ ! -f "$STATE/prepared" ]; then
    printf '%s\n' 'Detecting installation media, disks and network adapters...'
    "$PREPARE" || true
fi

. "$ROOT/usr/share/debconf/confmodule"

db_x_loadtemplatefile "$SHARE/wizard-text.templates" nex-installer
db_capb backup
db_settitle nex-installer/title

# One failed apply from the previous pass, reported by the shared setup loop.
if [ -s "$STATE/error" ]; then
    db_subst nex-installer/failed detail "$(cat "$STATE/error")"
    db_fset nex-installer/failed seen false
    db_input critical nex-installer/failed || true
    db_go || true
fi

if [ ! -f "$STATE/prepared" ]; then
    db_subst nex-installer/failed detail "Hardware preparation did not finish. Details are in /var/log/nex-installer-prepare.log."
    db_fset nex-installer/failed seen false
    db_input critical nex-installer/failed || true
    db_go || true
    exit 1
fi

# ---------------------------------------------------------------- debconf helpers

# Step results: 0 forward, 1 the operator chose Go Back, 2 not applicable here.
SKIP=2

# ask TEMPLATE -> answer in $RET; returns 1 when the operator chose Go Back.
ask() {
    db_fset "$1" seen false
    db_input critical "$1" || true
    db_go || return 1
    db_get "$1"
}

fail_note() {
    db_subst "$1" detail "$2"
    db_fset "$1" seen false
    db_input critical "$1" || true
    db_go || true
}

# Shell variable names for a NIC: interface names may contain - which they cannot.
varkey() { printf '%s' "$1" | tr -c 'A-Za-z0-9' '_'; }
getv() { eval "printf '%s' \"\${$1-}\""; }
setv() { eval "$1=\$2"; }

# cdebconf separates Choices with commas, so a comma inside one would split it.
nocomma() { printf '%s' "$1" | tr ',' ' '; }

# sysfs pads serials and model names; nex-config compares stripped values, so a
# kept space would look like the disk or adapter had been swapped.
read_attr() { sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' "$@" 2>/dev/null || true; }

# NVMe reports device/serial and virtio-blk serial. SATA, SAS and SCSI disks only
# expose the unit-serial VPD page: a 4-byte binary header, then the serial; the
# same order as nex_install_disk_serial, which nex-config compares against.
disk_serial() {
    serial=$(read_attr "$SYS/class/block/$1/device/serial")
    [ -n "$serial" ] || serial=$(read_attr "$SYS/class/block/$1/serial")
    [ -n "$serial" ] || serial=$(tail -c +5 "$SYS/class/block/$1/device/vpd_pg80" 2>/dev/null | tr '\000' '\n' | head -n 1 | read_attr)
    printf '%s' "$serial"
}

# SSD, HDD or NVMe, and whether a disk already holds partitions: the same facts the
# graphical wizard shows in its columns, so the operator can tell the disks apart.
disk_kind() {
    case $1 in nvme*) printf 'NVMe'; return ;; esac
    case $(read_attr "$SYS/class/block/$1/queue/rotational") in
        0) printf 'SSD' ;;
        1) printf 'HDD' ;;
        *) printf 'disk' ;;
    esac
}
disk_contents() {
    for part in "$SYS/class/block/$1/$1"*; do
        if [ -f "$part/partition" ]; then printf 'has partitions'; return; fi
    done
    printf 'blank'
}

# --------------------------------------------------------------------- discovery

nics=
for path in "$SYS"/class/net/*; do
    [ -e "$path/device" ] || continue
    nics="$nics ${path##*/}"
done
# A host with no adapter installs offline, exactly as the graphical wizard does.
if [ -z "$nics" ] && [ -f "$ROOT/cdrom/nex/payloads.tsv" ]; then
    fail_note nex-installer/failed "The net installer requires a network adapter and internet access. Use the offline ISO if networking is unavailable."
    exit 1
fi
if [ -z "$nics" ]; then
    db_fset nex-installer/no-nic seen false
    db_input critical nex-installer/no-nic || true
    db_go || true
fi

# The disk holding the installation media is never an installation target.
media_names=
media_device=$(awk '$2 == "/cdrom" { print $1; exit }' "$MOUNTS" 2>/dev/null || true)
if [ -n "$media_device" ]; then
    media_real=$(readlink -f "$media_device" 2>/dev/null || true)
    if [ -n "$media_real" ]; then
        media_base=${media_real##*/}
        media_sys=$(readlink -f "$SYS/class/block/$media_base" 2>/dev/null || true)
        media_names="$media_base"
        [ -n "$media_sys" ] && media_names="$media_names $(basename "$(dirname "$media_sys")")"
    fi
fi

disks=
for path in "$SYS"/class/block/*; do
    name=${path##*/}
    case $name in
        sd*|vd*|xvd*|nvme*|mmcblk*) ;;
        *) continue ;;
    esac
    case $name in *[!A-Za-z0-9]*) continue ;; esac
    [ -e "$path/partition" ] && continue
    [ "$(read_attr "$path/ro")" = 0 ] || continue
    [ "$(read_attr "$path/removable")" = 0 ] || continue
    sectors=$(read_attr "$path/size")
    case ${sectors:-} in ''|*[!0-9]*) continue ;; esac
    [ "$sectors" -ge "$MIN_SECTORS" ] || continue
    skip=
    for media in $media_names; do
        [ "$media" = "$name" ] && skip=1
    done
    [ -n "$skip" ] && continue
    disks="$disks $name"
done
if [ -z "$disks" ]; then
    db_fset nex-installer/no-disk seen false
    db_input critical nex-installer/no-disk || true
    db_go || true
    exit 1
fi

# ------------------------------------------------------------------------- steps

step_action() {
    db_subst nex-installer/action found "$found_disks"
    ask nex-installer/action || exit 1
    case $RET in
        'Repair image') v_mode=repair ;;
        *) v_mode=install ;;
    esac
}

step_hostname() {
    db_set nex-installer/hostname "$(getv v_hostname)"
    ask nex-installer/hostname || return 1
    v_hostname=$RET
}

step_keymap() {
    while :; do
        db_set nex-installer/keymap "$(getv v_keymap)"
        ask nex-installer/keymap || return 1
        v_keymap=$RET
        # Apply it now so the root password is typed on the layout just chosen.
        # The console keymaps carry no bare fr map, only the latin9 variant, and
        # ckbcomp still exits 0 after printing its two header lines alone.
        case $v_keymap in
            fr) layout="fr latin9" ;;
            *) layout=$v_keymap ;;
        esac
        # ckbcomp emits a UTF-8 keymap, but loadkeys parses U+201c and friends
        # only in Unicode mode, which it otherwise takes from the console it
        # opens; -u makes that explicit, so gb, de and es load on any console.
        if map=$(ckbcomp $layout 2>/dev/null) &&
            printf '%s\n' "$map" | grep -q '^keycode' &&
            printf '%s\n' "$map" | loadkeys -u - >/dev/null 2>&1; then
            return 0
        fi
        # Never let the root password be typed on a layout this console rejected.
        fail_note nex-installer/keymap-error "The $v_keymap layout could not be applied to this console. Choose another layout."
    done
}

step_tzarea() {
    choices=UTC
    for area in $(sed -n 's|/.*||p' "$TIMEZONES" | sort -u); do
        choices="$choices, $area"
    done
    db_subst nex-installer/timezone-area choices "$choices"
    db_set nex-installer/timezone-area "$(getv v_tzarea)"
    ask nex-installer/timezone-area || return 1
    v_tzarea=$RET
    [ "$v_tzarea" = UTC ] && v_timezone=UTC
    return 0
}

step_tzzone() {
    [ "$v_tzarea" = UTC ] && return "$SKIP"
    choices=
    for zone in $(grep "^$v_tzarea/" "$TIMEZONES"); do
        if [ -z "$choices" ]; then choices=$zone; else choices="$choices, $zone"; fi
    done
    db_subst nex-installer/timezone choices "$choices"
    db_set nex-installer/timezone "$(getv v_timezone)"
    ask nex-installer/timezone || return 1
    v_timezone=$RET
}

step_password() {
    while :; do
        db_set nex-installer/password ''
        ask nex-installer/password || return 1
        first=$RET
        db_set nex-installer/password ''
        db_set nex-installer/password-again ''
        ask nex-installer/password-again || return 1
        if [ "$first" != "$RET" ]; then
            fail_note nex-installer/password-error "The two entries did not match. Enter the root password again."
            continue
        fi
        # nex-config rejects the rest; report an empty password in place.
        if [ -z "$first" ]; then
            fail_note nex-installer/password-error "Enter a nonempty root password."
            continue
        fi
        v_password=$first
        db_set nex-installer/password-again ''
        return 0
    done
}

step_management() {
    [ -z "$nics" ] && return "$SKIP"
    choices=
    for nic in $nics; do
        mac=$(read_attr "$SYS/class/net/$nic/address")
        carrier=$(read_attr "$SYS/class/net/$nic/carrier")
        case $carrier in
            1) link="link detected" ;;
            0) link="no link" ;;
            *) link="link unknown" ;;
        esac
        entry=$(nocomma "$nic - $mac - $link")
        if [ -z "$choices" ]; then choices=$entry; else choices="$choices, $entry"; fi
    done
    # Offline stays last so the highlighted default is the first adapter.
    [ -f "$ROOT/cdrom/nex/payloads.tsv" ] || choices="$choices, $OFFLINE"
    db_subst nex-installer/management choices "$choices"
    ask nex-installer/management || return 1
    if [ "$RET" = "$OFFLINE" ]; then v_management=; else v_management=${RET%% *}; fi
}

# After the management choice, offer one Yes/No instead of walking every leftover
# adapter. No is the default: a normal install only needs the management NIC.
step_other() {
    extras=
    for nic in $nics; do
        [ -n "$v_management" ] && [ "$nic" = "$v_management" ] && continue
        extras="${extras:+$extras }$nic"
    done
    [ -z "$extras" ] && { v_other_adapters=false; return "$SKIP"; }

    count=$(set -- $extras; printf '%s' "$#")
    names=$(printf '%s' "$extras" | sed 's/ /, /g')
    if [ -n "$v_management" ]; then
        detail="Management adapter: $v_management. Remaining ($count): $names. A normal install only needs the management adapter."
    else
        detail="Offline install. Adapters ($count): $names. Answer no to leave every adapter unconfigured."
    fi
    db_subst nex-installer/other-adapters detail "$detail"
    db_set nex-installer/other-adapters "${v_other_adapters:-false}"
    ask nex-installer/other-adapters || return 1
    v_other_adapters=$RET

    if [ "$v_other_adapters" != true ]; then
        for nic in $extras; do
            key=$(varkey "$nic")
            setv "mode_$key" disabled
            setv "vlan_$key" 0
            setv "address_$key" ''
            setv "gateway_$key" ''
        done
    fi
}

step_nic() {
    nic=$1
    # Skipped leftovers stay disabled from step_other; do not ask about them.
    if [ "$nic" != "${v_management-}" ] && [ "${v_other_adapters:-false}" != true ]; then
        return "$SKIP"
    fi
    key=$(varkey "$nic")
    mac=$(read_attr "$SYS/class/net/$nic/address")
    if [ "$nic" = "$v_management" ]; then
        choices="dhcp, static"
        detail="$mac - management adapter: it becomes br0 and owns the default route and DNS."
        default=dhcp
    else
        # Same operator-facing word as the graphical wizard; the handoff still stores disabled.
        choices="Disabled, dhcp, static"
        detail="$mac - additional adapter: Disabled leaves it unconfigured. No default route or DNS."
        default=Disabled
    fi
    db_subst nex-installer/mode nic "$nic"
    db_subst nex-installer/mode detail "$detail"
    db_subst nex-installer/mode choices "$choices"
    previous=$(getv "mode_$key")
    case $previous in
        disabled) previous=Disabled ;;
    esac
    db_set nex-installer/mode "${previous:-$default}"
    ask nex-installer/mode || return 1
    mode=$RET
    [ "$mode" = Disabled ] && mode=disabled
    setv "mode_$key" "$mode"

    if [ "$mode" = disabled ]; then
        setv "vlan_$key" 0
        setv "address_$key" ''
        setv "gateway_$key" ''
        return 0
    fi

    db_subst nex-installer/vlan nic "$nic"
    # cdebconf falls back to the template default only while a question has no
    # value at all, so prefill the untagged default rather than an empty string.
    previous=$(getv "vlan_$key")
    db_set nex-installer/vlan "${previous:-0}"
    ask nex-installer/vlan || return 1
    setv "vlan_$key" "${RET:-0}"

    if [ "$mode" = static ]; then
        db_subst nex-installer/address nic "$nic"
        db_set nex-installer/address "$(getv "address_$key")"
        ask nex-installer/address || return 1
        setv "address_$key" "$RET"
        if [ "$nic" = "$v_management" ]; then
            db_subst nex-installer/gateway nic "$nic"
            db_set nex-installer/gateway "$(getv "gateway_$key")"
            ask nex-installer/gateway || return 1
            setv "gateway_$key" "$RET"
        else
            # Only the management adapter may carry a default route.
            setv "gateway_$key" ''
        fi
    else
        setv "address_$key" ''
        setv "gateway_$key" ''
    fi
}

step_dns() {
    db_set nex-installer/dns "$(getv v_dns)"
    ask nex-installer/dns || return 1
    v_dns=$RET
}

step_disk() {
    choices=
    # $disks comes from a sorted glob, so both frontends list the disks in the same order.
    for name in $disks; do
        sectors=$(read_attr "$SYS/class/block/$name/size")
        model=$(read_attr "$SYS/class/block/$name/device/model")
        serial=$(disk_serial "$name")
        entry=$(nocomma "$name - $((sectors / 2097152)) GiB - $(disk_kind "$name")${model:+ - $model}${serial:+ - $serial} - $(disk_contents "$name")")
        if [ -z "$choices" ]; then choices=$entry; else choices="$choices, $entry"; fi
    done
    # Match the graphical wizard: a lone eligible disk is already ticked.
    [ $(set -- $disks; echo $#) -eq 1 ] && db_set nex-installer/disk "$choices"
    while :; do
        db_subst nex-installer/disk choices "$choices"
        ask nex-installer/disk || return 1
        # cdebconf answers a multiselect as "entry, entry"; each entry starts with the name.
        v_disks=$(printf '%s\n' "$RET" | awk -F', ' '{ for (i = 1; i <= NF; i++) { split($i, f, " "); printf "%s ", f[1] } }')
        detail=
        if [ -z "$v_disks" ]; then
            detail="Select at least one disk: one to install on, or two or more to mirror."
        else
            # An md mirror is only as large as its smallest member.
            smallest=
            for name in $v_disks; do
                sectors=$(read_attr "$SYS/class/block/$name/size")
                if [ -z "$smallest" ] || [ "$sectors" -lt "$smallest" ]; then smallest=$sectors; fi
            done
            for name in $v_disks; do
                sectors=$(read_attr "$SYS/class/block/$name/size")
                if [ "$sectors" -gt $((smallest + smallest / 10)) ]; then
                    detail="Every mirror disk must be within 10% of the smallest selected disk's capacity, because a mirror is only as large as its smallest member. Select disks of similar size."
                fi
            done
        fi
        if [ -z "$detail" ]; then return 0; fi
        fail_note nex-installer/disk-error "$detail"
    done
}

step_erase() {
    # One review line per selected disk: name, capacity and serial.
    targets= ; capacities= ; serials=
    for name in $v_disks; do
        sectors=$(read_attr "$SYS/class/block/$name/size")
        serial=$(disk_serial "$name")
        targets="${targets:+$targets, }/dev/$name"
        capacities="${capacities:+$capacities, }$((sectors / 2097152)) GiB ($((sectors * 512)) bytes)"
        serials="${serials:+$serials, }${serial:-not reported}"
    done
    # A debconf substitution is one line, so every adapter is reviewed on one.
    adapters=
    for nic in $nics; do
        key=$(varkey "$nic")
        entry="$nic: $(getv "mode_$key")"
        address=$(getv "address_$key")
        gateway=$(getv "gateway_$key")
        tag=$(getv "vlan_$key")
        [ -n "$address" ] && entry="$entry $address"
        [ -n "$gateway" ] && entry="$entry, gateway $gateway"
        [ "${tag:-0}" = 0 ] || entry="$entry, VLAN $tag"
        if [ -z "$adapters" ]; then adapters=$entry; else adapters="$adapters; $entry"; fi
    done
    if [ -n "$v_management" ]; then
        management="$v_management ($(getv "mode_$(varkey "$v_management")"))"
    else
        management=Offline
    fi
    db_subst nex-installer/erase disk "$targets"
    db_subst nex-installer/erase disksize "$capacities"
    db_subst nex-installer/erase serial "$serials"
    db_subst nex-installer/erase layout "$(set -- $v_disks; if [ $# -gt 1 ]; then
        n=$#; f=$((n - 1))
        echo "one system disk as $n identical RAID1 copies (capacity not pooled); survives $f disk $([ "$f" -eq 1 ] && echo failure || echo failures)"
    else echo "one system disk, no redundancy"; fi)"
    db_subst nex-installer/erase hostname "$v_hostname"
    db_subst nex-installer/erase timezone "$v_timezone"
    db_subst nex-installer/erase keymap "$v_keymap"
    db_subst nex-installer/erase management "$management"
    db_subst nex-installer/erase adapters "${adapters:-none detected}"
    db_subst nex-installer/erase dns "${v_dns:-from DHCP or none}"
    # Never carry a previous confirmation forward into a changed selection.
    db_set nex-installer/erase false
    ask nex-installer/erase || return 1
    [ "$RET" = true ] || return 1
}

# The confirmed settings, validated by nex-config before any question is closed:
# a rejection reports nex-config's own reason and returns to the answers, which
# all stay filled in. Only a validated handoff leaves this wizard behind.
step_validate() {
    if ! write_config; then
        rm -f "$STATE/config.ini.new"
        fail_note nex-installer/failed "The installation settings could not be saved. No installation was started."
        return 1
    fi
    if detail=$("$VALIDATE" "$STATE/config.ini" 2>&1 >/dev/null); then
        return 0
    fi
    rm -f "$STATE/config.ini"
    # A substitution must stay on one line; nex-config reports a single reason.
    detail=$(printf '%s\n' "$detail" | grep -v '^[[:space:]]*$' | tail -n 1)
    fail_note nex-installer/failed "${detail:-The installation settings were rejected. Review them and try again.}"
    return 1
}

# Repair names exactly the disks that hold the data volume; the wizard never lets the operator
# widen or narrow that set, and nex-image-install.sh refuses any other.
step_repairdisk() {
    v_disks=$(printf '%s\n' "$found_disks" | tr ',' '\n' | sed 's|^/dev/||' | tr '\n' ' ')
    for name in $v_disks; do
        case " $disks " in *" $name "*) ;; *)
            fail_note nex-installer/disk-error "$name holds the Nex Core data but is below the $((MIN_SECTORS / 2097152)) GiB minimum or is not eligible. Use Erase and install, or attach the original disks."
            exit 1 ;;
        esac
    done
    return "$SKIP"
}

step_repairconfirm() {
    targets=
    for name in $v_disks; do targets="${targets:+$targets, }/dev/$name"; done
    db_subst nex-installer/repair-confirm disk "$targets"
    db_set nex-installer/repair-confirm false
    ask nex-installer/repair-confirm || return 1
    [ "$RET" = true ] || return 1
}

# ------------------------------------------------------------------------ handoff

# GKeyFile unescapes \\ and \s, so a value must be escaped before it is written.
ini() { printf '%s' "$1" | sed 's/\\/\\\\/g'; }
# List values also escape ; so a serial containing one cannot split the string list.
ini_list() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/;/\\;/g'; }
ini_secret() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/ /\\s/g'; }

write_config() {
    {
        [ "$v_mode" != repair ] || printf '[Install]\nmode=repair\n\n'
        printf '[Host]\n'
        printf 'hostname=%s\n' "$(ini "$v_hostname")"
        printf 'timezone=%s\n' "$(ini "$v_timezone")"
        printf 'keymap=%s\n' "$(ini "$v_keymap")"
        printf 'password=%s\n' "$(ini_secret "$v_password")"
        # One ;-separated list entry per selected disk, in the order nex-config checks them.
        for key in disk disk_size disk_serial disk_major_minor; do
            printf '%s=' "$key"
            for name in $v_disks; do
                case $key in
                    disk) printf '/dev/%s;' "$name" ;;
                    disk_size) printf '%s;' "$(($(read_attr "$SYS/class/block/$name/size") * 512))" ;;
                    disk_serial) printf '%s;' "$(ini_list "$(disk_serial "$name")")" ;;
                    disk_major_minor) printf '%s;' "$(ini_list "$(read_attr "$SYS/class/block/$name/dev")")" ;;
                esac
            done
            printf '\n'
        done
        printf '\n[Network]\n'
        printf 'management=%s\n' "$v_management"
        printf 'dns=%s\n' "$(ini "$v_dns")"
        # Group order decides the installed nex0..nexN names; keep it stable.
        for nic in $nics; do
            key=$(varkey "$nic")
            printf '\n[NIC:%s]\n' "$nic"
            printf 'mac=%s\n' "$(read_attr "$SYS/class/net/$nic/address")"
            printf 'mode=%s\n' "$(getv "mode_$key")"
            printf 'address=%s\n' "$(ini "$(getv "address_$key")")"
            printf 'gateway=%s\n' "$(ini "$(getv "gateway_$key")")"
            printf 'vlan=%s\n' "$(getv "vlan_$key")"
        done
    } > "$STATE/config.ini.new" || return 1
    mv "$STATE/config.ini.new" "$STATE/config.ini"
}

# --------------------------------------------------------------------- step loop

v_hostname=nexcore
v_keymap=us
v_tzarea=UTC
v_timezone=UTC
v_password=
v_management=
v_other_adapters=false
v_dns=
v_disks=
v_mode=install

# Repair image is offered only when a complete Nex Core data volume (a single nex-data or every member of a
# nex-data mirror) is on the disks.
found_disks=
if probe=$(sh "$ROOT/cdrom/nex/nex-image-install.sh" probe 2>/dev/null); then
    found_disks=$(printf '%s\n' "$probe" | sed -n 's/^disks=//p')
fi

build_steps() {
    steps=
    [ -z "$found_disks" ] || steps=action
    # Repair keeps the host's identity, account and data volume, so it asks only
    # what the download needs (net installer) and then for the confirmation.
    if [ "$v_mode" = repair ]; then
        if [ -f "$ROOT/cdrom/nex/payloads.tsv" ]; then
            steps="$steps management other"
            for nic in $nics; do steps="$steps nic:$nic"; done
            steps="$steps dns"
        fi
        steps="$steps repairdisk repairconfirm validate"
    else
        steps="$steps hostname keymap tzarea tzzone password management other"
        for nic in $nics; do steps="$steps nic:$nic"; done
        steps="$steps dns disk erase validate"
    fi
}

build_steps
set -- $steps
total=$#
index=1
direction=1
while [ "$index" -le "$total" ]; do
    eval "step=\${$index}"
    case $step in
        nic:*) step_nic "${step#nic:}" ;;
        *) "step_$step" ;;
    esac
    case $? in
        0)
            direction=1
            if [ "$step" = action ]; then
                build_steps
                set -- $steps
                total=$#
            fi ;;
        "$SKIP") ;;
        *) direction=-1 ;;
    esac
    index=$((index + direction))
    if [ "$index" -lt 1 ]; then
        index=1
        direction=1
    fi
done

exit 0
