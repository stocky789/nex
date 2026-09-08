#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Runs in Debian Installer, after a successful base-system installation.
set -eu
[ -d /target/etc ] && [ -f /cdrom/nex/repo/Packages ]
mkdir -p /target/usr/share/doc/nex-core
cp /cdrom/nex/docs/* /target/usr/share/doc/nex-core/
cp /cdrom/nex/nex-source.tar.gz* /target/usr/share/doc/nex-core/
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
apt_local update
apt_local install -y --no-install-recommends $(cat /var/cache/nex-install/packages.txt)
# Preserve Debian's package-owned symlink and its upgrade path. The local
# /etc override wins without modifying upstream /usr/lib/os-release.
dpkg-divert --local --add --rename --divert /etc/os-release.debian /etc/os-release
cat > /etc/os-release <<'RELEASE'
NAME="Nex Core"
PRETTY_NAME="Nex Core 0.1.0-alpha1"
ID=nex-core
ID_LIKE=debian
VERSION="0.1.0-alpha1"
VERSION_ID=0.1.0-alpha1
DEBIAN_CODENAME=trixie
RELEASE
mkdir -p /etc/default/grub.d
printf '%s\n' 'GRUB_DISTRIBUTOR="Nex Core"' > /etc/default/grub.d/nex-core.cfg
update-grub
mkdir -p /usr/share/nex-core /etc/ssh/sshd_config.d
cp /var/cache/nex-install/package-manifest.tsv /usr/share/nex-core/
printf '%s\n' 'Nex Core 0.1.0-alpha1 — Debian 13 virtualization host' \
    'Local alpha: no Nex Controller or enrollment service is installed.' > /etc/motd
printf 'Nex Core 0.1.0-alpha1 (Debian 13) \\n \\l\n\n' > /etc/issue
printf '%s\n' 'PermitRootLogin no' > /etc/ssh/sshd_config.d/00-nex-core.conf
systemctl enable libvirtd.service ssh.service chrony.service
# Keep management networking as configured by Debian Installer. Do not invent
# bridges or format VM data disks. libvirt NAT is explicitly enabled by operator.
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
rm -rf /target/var/cache/nex-install
