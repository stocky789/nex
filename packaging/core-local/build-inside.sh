#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
set -euo pipefail
mkdir /build
cd /build
tar -xzf /input/nex-source.tar.gz --strip-components=1
go build -mod=vendor -trimpath -ldflags "-X nex.local/nex/internal/version.Version=${NEX_VERSION}" -o /build/nex-core-local ./cmd/nex-core-local
(cd web && npm ci && npm run typecheck && npm run build)
cc -Wall -Wextra -Werror -O2 packaging/core-local/nex-root-auth.c -lpam -o /build/nex-root-auth
pkg=/build/package
install -d "$pkg/DEBIAN" "$pkg/usr/lib/nex" "$pkg/usr/lib/systemd/system" "$pkg/usr/share/nex/core-local/web" "$pkg/usr/share/nex/appliance" "$pkg/usr/share/doc/nex-core-local" "$pkg/etc/libvirt/hooks/qemu.d" "$pkg/usr/share/nex/core-local"
install -d "$pkg/usr/share/keyrings"
install -d "$pkg/etc/pam.d" "$pkg/etc/nex" "$pkg/etc/modules-load.d"
printf 'repo_url=https://nexvirtualization.com/apt\n' > "$pkg/etc/nex/updates.conf"
install -m644 packaging/core-local/nex.list.example "$pkg/usr/share/nex/core-local/"
# The Nex APT archive public key; postinst writes the matching signed apt source.
install -m644 packaging/core-local/nex-archive-keyring.gpg "$pkg/usr/share/keyrings/nex-archive-keyring.gpg"
install -m644 packaging/core-local/nex-core-local.pam "$pkg/etc/pam.d/nex-core-local"
# SATA and SAS disks report their temperature only once drivetemp is loaded; CPU, NVMe and GPU
# sensors bind on their own. The agent never loads modules (ProtectKernelModules=yes).
install -m644 packaging/core-local/nex-sensors.conf "$pkg/etc/modules-load.d/nex-sensors.conf"
# /dev/ipmi0 exists only once the IPMI driver is loaded. It is what lets a host configure
# its own management controller for fencing without an operator typing an address.
install -m644 packaging/core-local/nex-ipmi.conf "$pkg/etc/modules-load.d/nex-ipmi.conf"
printf '/etc/pam.d/nex-core-local\n/etc/nex/updates.conf\n/etc/modules-load.d/nex-sensors.conf\n/etc/modules-load.d/nex-ipmi.conf\n' > "$pkg/DEBIAN/conffiles"
install -m755 /build/nex-root-auth "$pkg/usr/lib/nex/"
install -m755 /build/nex-core-local "$pkg/usr/lib/nex/"
install -m755 packaging/core-local/core-local-setup "$pkg/usr/lib/nex/"
install -m755 packaging/core-local/corosync-pristine "$pkg/usr/lib/nex/"
install -m755 packaging/core-local/libvirtd-apparmor-policy.py "$pkg/usr/lib/nex/libvirtd-apparmor-policy"
install -m755 packaging/core-local/storage-downgrade-guard.py "$pkg/usr/lib/nex/storage-downgrade-guard"
install -d "$pkg/usr/lib/ocf/resource.d/nex"
install -m755 packaging/cluster-storage/cluster-iscsi-session "$pkg/usr/lib/ocf/resource.d/nex/cluster-iscsi-session"
printf '2\n' > "$pkg/usr/share/nex/core-local/storage-format-v2"
printf '3\n' > "$pkg/usr/share/nex/core-local/storage-format-v3"
install -m755 packaging/core-local/nex-storage-guard.sh "$pkg/etc/libvirt/hooks/qemu.d/nex-storage-guard"
# Retain the wrapper for existing domain definitions; new VMs use libvirt's emulator.
install -m755 packaging/core-local/qemu-vnc-lossy "$pkg/usr/lib/nex/"
install -m755 appliance/host-recovery.py "$pkg/usr/lib/nex/host-recovery"
install -m644 packaging/core-local/nex-core-local.service "$pkg/usr/lib/systemd/system/"
install -m755 packaging/core-local/preinst packaging/core-local/postinst packaging/core-local/prerm "$pkg/DEBIAN/"
cp -a web/dist/. "$pkg/usr/share/nex/core-local/web/"
cp web/dist/local.html "$pkg/usr/share/nex/core-local/web/index.html"
# The appliance source is verified equal to this build input before Docker starts.
# Keep one archive in the package while preserving the Core source-serving path.
ln -s ../appliance/nex-source.tar.gz "$pkg/usr/share/nex/core-local/nex-source.tar.gz"
cp -a docs/api "$pkg/usr/share/nex/core-local/"
cp LICENSE NOTICE ARTWORK-LICENSE.md docs/LICENSING.md "$pkg/usr/share/doc/nex-core-local/"
install -m644 /usr/local/go/LICENSE "$pkg/usr/share/doc/nex-core-local/go-LICENSE"
install -m644 /usr/share/doc/libpam0g/copyright "$pkg/usr/share/doc/nex-core-local/pam-copyright"
for file in \
    vendor/github.com/aws/aws-sdk-go-v2/LICENSE.txt \
    vendor/github.com/aws/aws-sdk-go-v2/NOTICE.txt \
    vendor/github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream/LICENSE.txt \
    vendor/github.com/aws/aws-sdk-go-v2/feature/s3/manager/LICENSE.txt \
    vendor/github.com/aws/aws-sdk-go-v2/internal/configsources/LICENSE.txt \
    vendor/github.com/aws/aws-sdk-go-v2/internal/endpoints/v2/LICENSE.txt \
    vendor/github.com/aws/aws-sdk-go-v2/internal/sync/singleflight/LICENSE \
    vendor/github.com/aws/aws-sdk-go-v2/internal/v4a/LICENSE.txt \
    vendor/github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding/LICENSE.txt \
    vendor/github.com/aws/aws-sdk-go-v2/service/internal/checksum/LICENSE.txt \
    vendor/github.com/aws/aws-sdk-go-v2/service/internal/presigned-url/LICENSE.txt \
    vendor/github.com/aws/aws-sdk-go-v2/service/internal/s3shared/LICENSE.txt \
    vendor/github.com/aws/aws-sdk-go-v2/service/s3/LICENSE.txt \
    vendor/github.com/aws/smithy-go/LICENSE \
    vendor/github.com/aws/smithy-go/NOTICE \
    vendor/github.com/aws/smithy-go/internal/sync/singleflight/LICENSE \
    vendor/github.com/kr/fs/LICENSE \
    vendor/github.com/pkg/sftp/LICENSE \
    vendor/golang.org/x/crypto/LICENSE; do
    install -D -m644 "$file" "$pkg/usr/share/doc/nex-core-local/go-dependencies/${file#vendor/}"
done
go version /build/nex-core-local > "$pkg/usr/share/doc/nex-core-local/go-version.txt"
for artifact in controller.qcow2 manifest.json manifest.sig nex-source.tar.gz components.tsv LICENSE NOTICE ARTWORK-LICENSE.md build-inputs.json; do
    cp "/input/bundle/$artifact" "$pkg/usr/share/nex/appliance/"
done
install -d "$pkg/usr/share/nex/appliance/update"
install -m755 packaging/controller/apply-update "$pkg/usr/share/nex/appliance/update/"
install -m755 appliance/firstboot.py "$pkg/usr/share/nex/appliance/update/appliance-firstboot"
install -m644 packaging/controller/nex-controller-update.path "$pkg/usr/share/nex/appliance/update/"
install -m644 packaging/controller/nex-controller-update.service "$pkg/usr/share/nex/appliance/update/"
printf 'repo_url=https://nexvirtualization.com/apt\n' > "$pkg/usr/share/nex/appliance/update/updates.conf"
cp /input/trusted-key.raw "$pkg/usr/share/nex/appliance/release.pub"
# OpenZFS lives in Debian contrib. A host installed before Nex enabled contrib has
# no candidate for it, and a hard dependency there makes the in-product update fail
# permanently on exactly those hosts. Recommends keeps it installed by default on
# every host that can resolve it (apt skips an unsatisfiable recommendation), the
# ISO installs it explicitly from installer/packages.txt, postinst enables contrib,
# and Core reports ZFS discovery as unavailable until the packages are present.
#
# ifupdown, bridge-utils and vlan are what Core's networking actually runs on: it writes
# /etc/network/interfaces and interfaces.d stanzas and calls ifup/ifdown (ifupdown), the
# guest and management bridges are bridge_ports/bridge_stp/bridge_fd stanzas whose hooks
# ship in bridge-utils, and a tagged management NIC is a vlan-raw-device stanza whose hook
# ships in vlan. Without them a pool VLAN registration or a management network edit fails
# on a host that installed this package on its own. All three are main, so they are hard
# dependencies rather than Recommends.
cat > "$pkg/DEBIAN/control" <<CONTROL
Package: nex-core-local
Version: ${NEX_VERSION}
Architecture: amd64
Maintainer: Nex contributors
Depends: init-system-helpers, libc6 (>= 2.41), python3, python3-virt-firmware, openssl, libpam0g, libpam-modules, iproute2, ifupdown, bridge-utils, vlan, dmidecode, pciutils, libvirt-clients, libvirt-daemon-system, qemu-system-x86, qemu-utils, genisoimage, nfs-common, libnfs-utils, cifs-utils, smbclient, open-iscsi, corosync, dlm-controld, gfs2-utils, pacemaker, pacemaker-cli-utils, pcs, fence-agents-redfish, fence-agents-ipmilan, fence-agents-virsh, ipmitool, sbd
Recommends: linux-headers-amd64, zfs-dkms, zfsutils-linux
Section: admin
Priority: optional
Description: Nex Core local management and Controller virtual appliance
 Authenticated host-local VM controls and signed Controller appliance deployment.
 First boot selects the default-route management IP without changing networking.
CONTROL
chmod -R a+rX "$pkg/usr"
dpkg-deb --root-owner-group --build "$pkg" "/output/nex-core-local_${NEX_VERSION}_amd64.deb"
