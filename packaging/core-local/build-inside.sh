#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
set -euo pipefail
# The package the read-only image installs at build time: no maintainer scripts (nothing runs on the host), no
# apt or initramfs hooks, no appliance and no cluster/ZFS/SMB dependencies (layers carry those).
mkdir /build
cd /build
tar -xzf /input/nex-source.tar.gz --strip-components=1
go build -mod=vendor -trimpath -buildvcs=false -ldflags "-buildid= -X nex.local/nex/internal/version.Version=${NEX_VERSION}" -o /build/nex-core-local ./cmd/nex-core-local
(cd web && npm ci && npm run typecheck && npm run build)
cc -Wall -Wextra -Werror -O2 packaging/core-local/nex-root-auth.c -lpam -o /build/nex-root-auth
# --export-dynamic keeps the helper's read/pread/pread64 in the dynamic symbol table, so libblkid's calls
# bind to them (symbol interposition) and the probe is served from the iSCSI LUN; the helper's selfcheck
# refuses to run if libblkid bypasses them. The test binary needs it for the same selfcheck.
cc -Wall -Wextra -Werror -O2 -Wl,--export-dynamic packaging/core-local/nex-iscsi-inspect.c $(pkg-config --cflags --libs libiscsi blkid) -o /build/nex-iscsi-inspect
cc -Wall -Wextra -Werror -O2 -Wl,--export-dynamic packaging/core-local/nex-iscsi-inspect-test.c $(pkg-config --cflags --libs libiscsi blkid) -o /build/nex-iscsi-inspect-test
/build/nex-iscsi-inspect-test
pkg=/build/package
install -d "$pkg/DEBIAN" "$pkg/usr/lib/nex" "$pkg/usr/lib/systemd/system" "$pkg/usr/share/nex/core-local/web" "$pkg/usr/share/doc/nex-core-local" "$pkg/etc/libvirt/hooks/qemu.d" "$pkg/usr/share/nex/core-local"
install -d "$pkg/etc/pam.d" "$pkg/etc/modules-load.d"
install -m644 packaging/core-local/nex-core-local.pam "$pkg/etc/pam.d/nex-core-local"
# SATA and SAS disks report their temperature only once drivetemp is loaded; CPU, NVMe and GPU
# sensors bind on their own. The agent never loads modules (ProtectKernelModules=yes).
install -m644 packaging/core-local/nex-sensors.conf "$pkg/etc/modules-load.d/nex-sensors.conf"
# /dev/ipmi0 exists only once the IPMI driver is loaded. It is what lets a host configure
# its own management controller for fencing without an operator typing an address.
install -m644 packaging/core-local/nex-ipmi.conf "$pkg/etc/modules-load.d/nex-ipmi.conf"
install -m644 packaging/core-local/nex-iscsi.conf "$pkg/etc/modules-load.d/nex-iscsi.conf"
printf '/etc/pam.d/nex-core-local\n/etc/modules-load.d/nex-sensors.conf\n/etc/modules-load.d/nex-ipmi.conf\n/etc/modules-load.d/nex-iscsi.conf\n' > "$pkg/DEBIAN/conffiles"
install -m755 /build/nex-root-auth "$pkg/usr/lib/nex/"
install -m755 /build/nex-iscsi-inspect "$pkg/usr/lib/nex/"
install -m755 /build/nex-core-local "$pkg/usr/lib/nex/"
install -m755 packaging/core-local/core-local-setup "$pkg/usr/lib/nex/"
install -d "$pkg/usr/lib/ocf/resource.d/nex"
install -m755 packaging/cluster-storage/cluster-iscsi-session "$pkg/usr/lib/ocf/resource.d/nex/cluster-iscsi-session"
printf '2\n' > "$pkg/usr/share/nex/core-local/storage-format-v2"
printf '3\n' > "$pkg/usr/share/nex/core-local/storage-format-v3"
install -m755 packaging/core-local/nex-storage-guard.sh "$pkg/etc/libvirt/hooks/qemu.d/nex-storage-guard"
# Retain the wrapper for existing domain definitions; new VMs use libvirt's emulator.
install -m755 packaging/core-local/qemu-vnc-lossy "$pkg/usr/lib/nex/"
install -m755 appliance/host-recovery.py "$pkg/usr/lib/nex/host-recovery"
install -m644 packaging/core-local/nex-core-local.service "$pkg/usr/lib/systemd/system/"
# Optional feature layers: verified and merged early in boot, only once Core has recorded an enabled layer
# (the unit's ConditionPathExists). Enablement ships in /usr (vendor wants link plus preset), so a verified
# image slot carries it and every slot update keeps it.
install -m644 packaging/core-local/nex-layers.service "$pkg/usr/lib/systemd/system/"
install -d "$pkg/usr/lib/systemd/system/sysinit.target.wants"
ln -s ../nex-layers.service "$pkg/usr/lib/systemd/system/sysinit.target.wants/nex-layers.service"
install -D -m644 packaging/core-local/30-nex-core-local.preset "$pkg/usr/lib/systemd/system-preset/30-nex-core-local.preset"
# No postinst enables the service: like nex-layers above, the vendor wants link ships in /usr, so every
# verified slot carries it and a first-boot preset pass cannot drop it.
install -d "$pkg/usr/lib/systemd/system/multi-user.target.wants"
ln -s ../nex-core-local.service "$pkg/usr/lib/systemd/system/multi-user.target.wants/nex-core-local.service"
cp -a web/dist/. "$pkg/usr/share/nex/core-local/web/"
cp web/dist/local.html "$pkg/usr/share/nex/core-local/web/index.html"
install -m644 /input/nex-source.tar.gz "$pkg/usr/share/nex/core-local/nex-source.tar.gz"
cp -a docs/api "$pkg/usr/share/nex/core-local/"
cp LICENSE NOTICE ARTWORK-LICENSE.md docs/LICENSING.md "$pkg/usr/share/doc/nex-core-local/"
install -m644 /usr/local/go/LICENSE "$pkg/usr/share/doc/nex-core-local/go-LICENSE"
install -m644 vendor/github.com/klauspost/compress/LICENSE "$pkg/usr/share/doc/nex-core-local/klauspost-compress-LICENSE"
install -m644 vendor/github.com/mattn/go-sqlite3/LICENSE "$pkg/usr/share/doc/nex-core-local/go-sqlite3-LICENSE"
install -m644 /usr/share/doc/libpam0g/copyright "$pkg/usr/share/doc/nex-core-local/pam-copyright"
install -m644 /usr/share/doc/libiscsi7/copyright "$pkg/usr/share/doc/nex-core-local/libiscsi-copyright"
install -m644 /usr/share/doc/libblkid1/copyright "$pkg/usr/share/doc/nex-core-local/libblkid-copyright"
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
# The read-only image gets cluster, ZFS and SMB software from layers, never from this package's dependencies.
#
# ifupdown, bridge-utils and vlan are what Core's networking actually runs on: it writes
# /etc/network/interfaces and interfaces.d stanzas and calls ifup/ifdown (ifupdown), the
# guest and management bridges are bridge_ports/bridge_stp/bridge_fd stanzas whose hooks
# ship in bridge-utils, and a tagged management NIC is a vlan-raw-device stanza whose hook
# ships in vlan. Without them a pool VLAN registration or a management network edit fails.
cat > "$pkg/DEBIAN/control" <<CONTROL
Package: nex-core-local
Version: ${NEX_VERSION}
Architecture: amd64
Maintainer: Nex contributors
Depends: init-system-helpers, libc6 (>= 2.41), python3, python3-virt-firmware, openssl, libpam0g, libpam-modules, iproute2, ifupdown, bridge-utils, vlan, dmidecode, pciutils, libvirt-clients, libvirt-daemon-system, qemu-system-x86, qemu-utils, genisoimage, nfs-common, libnfs-utils, open-iscsi, libiscsi7, libblkid1, ipmitool
Section: admin
Priority: optional
Description: Nex Core local management
 Authenticated host-local VM controls and Controller virtual appliance deployment.
 The appliance image is a separate payload.
 First boot selects the default-route management IP without changing networking.
CONTROL
chmod -R a+rX "$pkg/usr"
dpkg-deb --root-owner-group --build "$pkg" "/output/nex-core-local_${NEX_VERSION}_amd64.deb"
