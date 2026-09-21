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
if [ -d /cdrom/nex/third-party/ovmf-generic ]; then
    mkdir -p /target/usr/share/doc/nex-core/third-party
    cp -a /cdrom/nex/third-party/ovmf-generic /target/usr/share/doc/nex-core/third-party/
fi
if [ -d /cdrom/nex/third-party/virtio-win ]; then
    mkdir -p /target/usr/share/doc/nex-core/third-party
    cp -a /cdrom/nex/third-party/virtio-win /target/usr/share/doc/nex-core/third-party/
fi
[ -f /cdrom/nex/media/virtio-win.iso.gz ] && [ -f /cdrom/nex/media/virtio-win.iso.sha256 ] || {
    echo 'compressed virtio-win media or checksum missing from installation media' >&2
    exit 1
}
install -d -m755 /target/usr/share/nex/media
virtio_iso=/target/usr/share/nex/media/virtio-win.iso
trap 'rm -f "$virtio_iso"' 0 1 2 15
gunzip -c /cdrom/nex/media/virtio-win.iso.gz > "$virtio_iso"
(cd /target/usr/share/nex/media && sha256sum -c /cdrom/nex/media/virtio-win.iso.sha256)
trap - 0 1 2 15
mkdir -p /target/var/cache/nex-install
cp -a /cdrom/nex/repo /target/var/cache/nex-install/
cp /cdrom/nex/packages.txt /target/var/cache/nex-install/
cp /cdrom/nex/package-manifest.tsv /target/var/cache/nex-install/
# Packages were authenticated by APT during the ISO build. Use ONLY this local
# repository here: a missing dependency must fail rather than require internet.
cat > /target/var/cache/nex-install/offline.list <<'LIST'
deb [trusted=yes] file:/var/cache/nex-install/repo ./
LIST
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
