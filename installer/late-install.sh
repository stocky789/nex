#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Runs in Debian Installer, after a successful base-system installation.
set -eu
[ -d /target/etc ] && [ -f /cdrom/nex/repo/Packages ]
mkdir -p /target/usr/share/doc/nex-core
cp /cdrom/nex/docs/* /target/usr/share/doc/nex-core/
cp /cdrom/nex/nex-source.tar.gz* /cdrom/nex/nex-gui-source.tar.gz* /target/usr/share/doc/nex-core/
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
apt_local update
apt_local install -y --no-install-recommends $(cat /var/cache/nex-install/packages.txt)
# nex-core-local's postinst diverts Debian's os-release symlink and writes the
# Nex Core identity from its own package version; the console banners reuse it.
. /etc/os-release
test "$ID" = nex-core
mkdir -p /etc/default/grub.d
printf '%s\n' 'GRUB_DISTRIBUTOR="Nex Core"' > /etc/default/grub.d/nex-core.cfg
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
# Minimal installation retains Debian networking; GUI settings are copied below.
# libvirt NAT is explicitly enabled by the operator.
cat > /etc/apt/sources.list.d/debian.sources <<'SOURCES'
Types: deb
URIs: https://deb.debian.org/debian
Suites: trixie trixie-updates
Components: main non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg

Types: deb
URIs: https://security.debian.org/debian-security
Suites: trixie-security
Components: main non-free-firmware
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
