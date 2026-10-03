#!/bin/sh
# Report installed-size footprint of a Nex Core host root, grouped by purpose.
# Run on a DISPOSABLE target (container, chroot, lab VM), never a workstation.
# Usage: measure-footprint.sh [ROOT]   (ROOT defaults to /; sizes are KiB from dpkg Installed-Size)
set -eu
root=${1:-/}
dq() { dpkg-query --admindir="$root/var/lib/dpkg" -W -f='${Installed-Size}\t${Package}\n' 2>/dev/null; }
group_of() {
    case $1 in
        zfs-dkms|zfsutils-linux|libzfs*|libzpool*|libnvpair*|libuutil*|spl*|zfs-*) echo zfs ;;
        linux-headers-*|linux-kbuild-*|linux-compiler-*|dkms|gcc*|cpp*|g++*|make|dpkg-dev|build-essential|libc6-dev|binutils*|libgcc-*-dev|libstdc++-*-dev|perl|perl-*|patch|fakeroot|kmod-dev|libc-dev-bin|libc-devtools|linux-libc-dev|rpcsvc-proto|libcrypt-dev|libtirpc-dev|libnsl-dev) echo dev-tools ;;
        corosync*|pacemaker*|pcs|sbd|sanlock|python3-sanlock|libvirt-daemon-plugin-sanlock|fence-agents*|dlm-controld|gfs2-utils|resource-agents|libqb*|libcorosync*|libcfg*|libcmap*|libcpg*|libquorum*|libvotequorum*|libsam*|libknet*|libdlm*|ipmitool|libcrmcommon*|libpe-*|libcib*|libcrmservice*|liblrmd*|libstonithd*|libpacemaker*|libsbd*|libsanlock*|libwdmd*) echo cluster ;;
        cifs-utils|smbclient|libsmbclient*|samba-*|libwbclient*|libsmb*|libldb*|libtalloc*|libtdb*|libtevent*|nfs-common|libnfs*|open-iscsi|libiscsi*) echo smb ;;
        *) echo base ;;
    esac
}
tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
dq | while IFS="$(printf '\t')" read -r size pkg; do
    printf '%s\t%s\t%s\n' "$(group_of "$pkg")" "${size:-0}" "$pkg"
done > "$tmp"
echo "== Installed-Size per group (KiB, MiB) =="
awk -F'\t' '{s[$1]+=$2; n[$1]++; t+=$2} END {for (g in s) printf "%-10s %4d pkgs %10d KiB %8.1f MiB\n", g, n[g], s[g], s[g]/1024; printf "%-10s %4s      %10d KiB %8.1f MiB\n","total","",t,t/1024}' "$tmp" | sort
echo "== Largest packages =="
sort -t"$(printf '\t')" -k2 -nr "$tmp" | head -15 | awk -F'\t' '{printf "%8d KiB  %-10s %s\n",$2,$1,$3}'
echo "== du (MiB) =="
for d in usr/lib/modules usr/src var/lib/dkms usr/share/nex; do
    if [ -e "$root/$d" ]; then printf '%8s MiB  /%s\n' "$(du -sm "$root/$d" | cut -f1)" "$d"; else printf '%8s      /%s (absent)\n' - "$d"; fi
done
printf '%8s MiB  / (whole root, excluding proc/sys/dev/run/tmp)\n' "$(du -smx "$root" --exclude=proc --exclude=sys --exclude=dev --exclude=run --exclude=tmp 2>/dev/null | cut -f1)"
