#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Runs in Debian Installer, after a successful base-system installation.
set -eu
[ -d /target/etc ] && [ -f /cdrom/nex/repo/Packages ]
mkdir -p /target/usr/share/doc/nex-core
cp /cdrom/nex/docs/* /target/usr/share/doc/nex-core/
cp /cdrom/nex/nex-source.tar.gz* /cdrom/nex/nex-gui-source.tar.gz* /target/usr/share/doc/nex-core/
if [ -d /cdrom/nex/third-party/dlm-pacemaker-abi ]; then
    mkdir -p /target/usr/share/doc/nex-core/third-party
    cp -a /cdrom/nex/third-party/dlm-pacemaker-abi /target/usr/share/doc/nex-core/third-party/
fi
if [ -d /cdrom/nex/third-party/virtio-win ]; then
    mkdir -p /target/usr/share/doc/nex-core/third-party
    cp -a /cdrom/nex/third-party/virtio-win /target/usr/share/doc/nex-core/third-party/
fi
mkdir -p /target/var/cache/nex-install
cp -a /cdrom/nex/repo /target/var/cache/nex-install/
cp /cdrom/nex/packages.txt /target/var/cache/nex-install/
cp /cdrom/nex/package-manifest.tsv /target/var/cache/nex-install/
# Offline bytes were authenticated during build; netinst verifies the same pinned
# bytes before installation. Neither variant permits APT to substitute live versions.
cat > /target/var/cache/nex-install/offline.list <<'LIST'
deb [trusted=yes] file:/var/cache/nex-install/repo ./
LIST
install -d -m755 /target/usr/share/nex/media
virtio_iso=/target/usr/share/nex/media/virtio-win.iso
if [ -f /cdrom/nex/netinst.tsv ]; then
    in-target apt-get -o Dir::Etc::sourcelist=/var/cache/nex-install/offline.list \
        -o Dir::Etc::sourceparts=- update
    # Only fetch APT's actual plan, rather than downloading the installed base twice.
    in-target --pass-stdout apt-get -o Dir::Etc::sourcelist=/var/cache/nex-install/offline.list \
        -o Dir::Etc::sourceparts=- --print-uris --yes --no-install-recommends \
        install $(cat /cdrom/nex/packages.txt) > /run/nex-netinst-plan
    sed -n "s/^'[^']*' \([^ ]*\) .*/\1/p" /run/nex-netinst-plan > /run/nex-netinst-packages
    if [ -f "$virtio_iso" ]; then
        mkdir -p /target/var/cache/nex-install/media
        rm -f /target/var/cache/nex-install/media/virtio-win.iso
        ln "$virtio_iso" /target/var/cache/nex-install/media/virtio-win.iso
    fi
    /bin/sh /cdrom/nex/netinst-fetch.sh /target/var/cache/nex-install /run/nex-netinst-packages
    rm -f "$virtio_iso"
    mv /target/var/cache/nex-install/media/virtio-win.iso "$virtio_iso"
    mkdir -p /target/usr/share/nex-core
    cp /cdrom/nex/netinst.tsv /target/usr/share/nex-core/netinst-available.tsv
    downloaded=/target/usr/share/nex-core/netinst-downloaded-packages.txt
    touch "$downloaded"
    sort -u "$downloaded" /run/nex-netinst-packages > "$downloaded.new"
    mv "$downloaded.new" "$downloaded"
else
    [ -f /cdrom/nex/media/virtio-win.iso.gz ] && [ -f /cdrom/nex/media/virtio-win.iso.sha256 ] || {
        echo 'compressed virtio-win media or checksum missing from installation media' >&2
        exit 1
    }
    trap 'rm -f "$virtio_iso"' 0 1 2 15
    gunzip -c /cdrom/nex/media/virtio-win.iso.gz > "$virtio_iso"
    (cd /target/usr/share/nex/media && sha256sum -c /cdrom/nex/media/virtio-win.iso.sha256)
    trap - 0 1 2 15
fi
cat > /target/var/cache/nex-install/configure.sh <<'TARGET'
#!/bin/sh
set -eu
export DEBIAN_FRONTEND=noninteractive
apt_local() {
    apt-get -o Dir::Etc::sourcelist=/var/cache/nex-install/offline.list \
        -o Dir::Etc::sourceparts=- -o APT::Get::List-Cleanup=0 "$@"
}
# Stamp the system as Nex Core before installing anything: nex-core-local's preinst
# refuses a fresh install without it, so the package cannot turn an arbitrary Debian
# system into something that resembles a Nex Core host.
mkdir -p /etc/nex
printf 'NEX_CORE=1\nINSTALLED_BY=nex-core-iso\n' > /etc/nex/core-release
# Debian Installer normally supplies policy-rc.d for in-target package work.
# Preserve it when present; otherwise suppress all maintainer-script starts while
# the native cluster packages and Core package are configured.
policy_rc_created=
if [ ! -e /usr/sbin/policy-rc.d ]; then
    install -d -m755 /usr/sbin
    printf '%s\n' '#!/bin/sh' 'exit 101' > /usr/sbin/policy-rc.d
    chmod 755 /usr/sbin/policy-rc.d
    policy_rc_created=/usr/sbin/policy-rc.d
    trap 'if [ -n "${policy_rc_created:-}" ]; then rm -f "$policy_rc_created"; fi' EXIT
fi
apt_local update
apt_local install -y --no-install-recommends $(cat /var/cache/nex-install/packages.txt)
if [ -n "$policy_rc_created" ]; then
    rm -f "$policy_rc_created"
    policy_rc_created=
    trap - EXIT
fi
# Debian ships a sample Corosync conffile. Only its exact dpkg-recorded checksum
# is safe to mark as replaceable by the first explicit Nex clustered-storage setup.
# Any modified or active configuration remains untouched and unmarked.
if /usr/lib/nex/corosync-pristine; then :; fi
# These native clustered-storage components are present for an explicitly configured
# Core pool only.  Do not let package installation create a cluster or start a
# membership/recovery service on an ordinary host.
systemctl disable corosync.service dlm.service pacemaker.service pcsd.service pcsd-ruby.service || true
# A fresh host has no pool cache, so the scan service would import every ZFS pool left on
# disks the operator did not select. Pools Nex creates are cached and imported by
# zfs-import-cache; an old pool stays untouched until an operator imports it deliberately.
systemctl disable zfs-import-scan.service || true
# A mirrored system disk: partman created the array, and the kernel assembles it from
# the members' own superblocks. Record only the root array for this host's initramfs
# (never a leftover array on unselected disks), and keep every member's ESP bootable,
# because grub-efi-amd64 only ever writes the one mounted ESP.
root=$(findmnt -no SOURCE /)
case $root in /dev/md*)
    { /usr/share/mdadm/mkconf | grep -v '^ARRAY'; mdadm --detail --scan "$root"; } > /etc/mdadm/mdadm.conf
    update-initramfs -u
    cat > /usr/local/sbin/nex-sync-esp <<'ESP'
#!/bin/sh
# Copy the EFI boot files GRUB maintains in /boot/efi onto the other members of the
# root RAID1 mirror, so the host still boots when that disk fails. Only disks that
# carry the root mirror are touched.
set -eu
root=$(findmnt -no SOURCE /)
case $root in /dev/md*) ;; *) exit 1 ;; esac
# fstab names one member's ESP by UUID. When that disk is gone, /boot/efi is an empty
# directory on the root filesystem: remount any surviving member ESP and rewrite fstab
# so GRUB upgrades and the APT hook keep writing a real ESP instead of the empty dir.
if ! findmnt -no TARGET /boot/efi >/dev/null 2>&1 || ! [ -d /boot/efi/EFI ]; then
    findmnt -no TARGET /boot/efi >/dev/null 2>&1 && umount /boot/efi || true
    mounted=
    for member in $(mdadm --detail --export "$root" | sed -n 's/^MD_DEVICE_[^=]*_DEV=//p'); do
        disk=$(lsblk -no PKNAME "$member")
        esp=$(lsblk -nro PATH,PARTTYPE "/dev/$disk" | awk '$2 == "c12a7328-f81f-11d2-ba4b-00a0c93ec93b" { print $1 }')
        [ -n "$esp" ] || continue
        mount "$esp" /boot/efi || continue
        if [ -d /boot/efi/EFI ]; then
            uuid=$(blkid -s UUID -o value "$esp" 2>/dev/null || true)
            if [ -n "$uuid" ] && [ -f /etc/fstab ]; then
                sed -i '/[[:space:]]\/boot\/efi[[:space:]]/ s|^[^#[:space:]][^[:space:]]*|UUID='"$uuid"'|' /etc/fstab
            fi
            mounted=$esp
            break
        fi
        umount /boot/efi || true
    done
    [ -n "$mounted" ] || exit 1
fi
[ -d /boot/efi/EFI ] || exit 1
mounted=$(findmnt -no SOURCE /boot/efi)
copy=$(mktemp -d)
trap 'umount "$copy" 2>/dev/null || true; rmdir "$copy" 2>/dev/null || true' EXIT
written=0
for member in $(mdadm --detail --export "$root" | sed -n 's/^MD_DEVICE_[^=]*_DEV=//p'); do
    disk=$(lsblk -no PKNAME "$member")
    esp=$(lsblk -nro PATH,PARTTYPE "/dev/$disk" | awk '$2 == "c12a7328-f81f-11d2-ba4b-00a0c93ec93b" { print $1 }')
    { [ -n "$esp" ] && [ "$esp" != "$mounted" ]; } || continue
    mount "$esp" "$copy"
    mkdir -p "$copy/EFI"
    cp -a /boot/efi/EFI/. "$copy/EFI/"
    umount "$copy"
    written=$((written + 1))
done
# Copying nothing is a failure, not success: a caller that makes a replacement disk
# bootable must be able to tell, and fall back to installing the ESP itself.
[ "$written" -gt 0 ]
ESP
    chmod 755 /usr/local/sbin/nex-sync-esp
    printf '%s\n' 'DPkg::Post-Invoke { "/usr/local/sbin/nex-sync-esp || true"; };' \
        > /etc/apt/apt.conf.d/99nex-sync-esp
    # It reports failure when there is no mounted ESP to copy, which is the normal case on a
    # legacy BIOS install; GRUB is on every disk there through grub-installer/bootdev.
    /usr/local/sbin/nex-sync-esp || true
    # /boot/efi is one member's own partition, not part of the mirror. A replaced or failed
    # disk must not drop the host into emergency mode while the mirror itself is healthy.
    # A short device timeout keeps local-fs.target from waiting the default 90s for a missing
    # ESP UUID before nofail lets boot continue (docs/qa/2026-09-22-system-disk-replace.md).
    sed -i '/[[:space:]]\/boot\/efi[[:space:]]/ s/\(vfat[[:space:]]\+\)\([^[:space:]]\+\)/\1\2,nofail,x-systemd.device-timeout=5s/' /etc/fstab
esac
# nex-core-local's postinst diverts Debian's os-release symlink and writes the
# Nex Core identity from its own package version; the console banners reuse it.
. /etc/os-release
test "$ID" = nex-core
mkdir -p /etc/default/grub.d
# Disable systemd's automatic AF_VSOCK SSH bind; without vsock it only spams the console.
printf '%s\n' \
    'GRUB_DISTRIBUTOR="Nex Core"' \
    'GRUB_CMDLINE_LINUX_DEFAULT="${GRUB_CMDLINE_LINUX_DEFAULT:+$GRUB_CMDLINE_LINUX_DEFAULT }systemd.ssh_auto=no"' \
    > /etc/default/grub.d/nex-core.cfg
update-grub
mkdir -p /usr/share/nex-core /etc/ssh/sshd_config.d
cp /var/cache/nex-install/package-manifest.tsv /usr/share/nex-core/
printf '%s\n' "$PRETTY_NAME — Debian 13 virtualization host" > /etc/motd
# Static identity only; nex-core-local publishes the live web UI URL under /run/issue.d.
# agetty expands \n (hostname) and \l (tty); keep single backslashes in this data string.
printf '%s\n' \
    "$PRETTY_NAME (Debian 13)" \
    'Host: \n  Console: \l' \
    '' > /etc/issue
printf '%s\n' 'PermitRootLogin no' > /etc/ssh/sshd_config.d/00-nex-core.conf
systemctl enable libvirtd.service ssh.service chrony.service nex-core-local.service
# Default local storage for Controller ISO library and guest disks (registered as "ISO").
install -d -m 0711 /var/lib/libvirt/images
# Both wizard modes stage their network settings below; serial/rescue keep Debian's.
# libvirt NAT is explicitly enabled by the operator.
cat > /etc/apt/sources.list.d/debian.sources <<'SOURCES'
Types: deb
URIs: https://deb.debian.org/debian
Suites: trixie trixie-updates
Components: main contrib non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg

Types: deb
URIs: https://security.debian.org/debian-security
Suites: trixie-security
Components: main contrib non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
SOURCES
# Remove installation-media entries; explicit trixie sources above own updates.
if [ -f /etc/apt/sources.list ]; then
    sed -i '/^[[:space:]]*deb[[:space:]].*cdrom:/s/^/# /' /etc/apt/sources.list
fi
TARGET
# Debian's wrapper clears inherited installer debconf descriptors and supplies
# target mounts/service-start suppression required by package maintainer scripts.
in-target /bin/sh /var/cache/nex-install/configure.sh
if [ -f /run/nex-installer/active ]; then
    # Only staged files are changed; these settings take effect on the first boot.
    test -f /run/nex-installer/target/etc/network/interfaces
    # Secondary DHCP route/DNS policy is implemented by the selected provider.
    in-target /bin/sh -c 'command -v dhcpcd >/dev/null && ! command -v dhclient >/dev/null && ! command -v udhcpc >/dev/null'
    cp -a /run/nex-installer/target/etc/. /target/etc/
    # Include MAC-bound interface names in the initial boot environment.
    in-target update-initramfs -u
fi
rm -rf /target/var/cache/nex-install
