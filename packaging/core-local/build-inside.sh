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
install -d "$pkg/DEBIAN" "$pkg/usr/lib/nex" "$pkg/usr/lib/systemd/system" "$pkg/usr/share/nex/core-local/web" "$pkg/usr/share/nex/appliance" "$pkg/usr/share/doc/nex-core-local"
install -d "$pkg/etc/pam.d" "$pkg/etc/nex"
printf 'repo_url=https://nexvirtualization.com/apt\n' > "$pkg/etc/nex/updates.conf"
install -m644 packaging/core-local/nex.list.example "$pkg/usr/share/nex/core-local/"
install -m644 packaging/core-local/nex-core-local.pam "$pkg/etc/pam.d/nex-core-local"
printf '/etc/pam.d/nex-core-local\n/etc/nex/updates.conf\n' > "$pkg/DEBIAN/conffiles"
install -m755 /build/nex-root-auth "$pkg/usr/lib/nex/"
install -m755 /build/nex-core-local "$pkg/usr/lib/nex/"
install -m755 packaging/core-local/core-local-setup "$pkg/usr/lib/nex/"
install -m755 appliance/host-recovery.py "$pkg/usr/lib/nex/host-recovery"
install -m644 packaging/core-local/nex-core-local.service "$pkg/usr/lib/systemd/system/"
install -m755 packaging/core-local/postinst packaging/core-local/prerm "$pkg/DEBIAN/"
cp -a web/dist/. "$pkg/usr/share/nex/core-local/web/"
cp web/dist/local.html "$pkg/usr/share/nex/core-local/web/index.html"
cp /input/nex-source.tar.gz "$pkg/usr/share/nex/core-local/"
cp -a docs/api "$pkg/usr/share/nex/core-local/"
cp LICENSE NOTICE docs/LICENSING.md "$pkg/usr/share/doc/nex-core-local/"
install -m644 /usr/local/go/LICENSE "$pkg/usr/share/doc/nex-core-local/go-LICENSE"
install -m644 /usr/share/doc/libpam0g/copyright "$pkg/usr/share/doc/nex-core-local/pam-copyright"
go version /build/nex-core-local > "$pkg/usr/share/doc/nex-core-local/go-version.txt"
for artifact in controller.qcow2 manifest.json manifest.sig nex-source.tar.gz components.tsv LICENSE NOTICE build-inputs.json; do
    cp "/input/bundle/$artifact" "$pkg/usr/share/nex/appliance/"
done
install -d "$pkg/usr/share/nex/appliance/update"
install -m755 packaging/controller/apply-update "$pkg/usr/share/nex/appliance/update/"
install -m644 packaging/controller/nex-controller-update.path "$pkg/usr/share/nex/appliance/update/"
install -m644 packaging/controller/nex-controller-update.service "$pkg/usr/share/nex/appliance/update/"
printf 'repo_url=https://nexvirtualization.com/apt\n' > "$pkg/usr/share/nex/appliance/update/updates.conf"
cp /input/trusted-key.raw "$pkg/usr/share/nex/appliance/release.pub"
cat > "$pkg/DEBIAN/control" <<CONTROL
Package: nex-core-local
Version: ${NEX_VERSION}
Architecture: amd64
Maintainer: Nex contributors
Depends: init-system-helpers, libc6 (>= 2.41), python3, openssl, libpam0g, libpam-modules, iproute2, libvirt-clients, libvirt-daemon-system, qemu-system-x86, qemu-utils, genisoimage
Section: admin
Priority: optional
Description: Nex Core local management and Controller virtual appliance
 Authenticated host-local VM controls and signed Controller appliance deployment.
 First boot selects the default-route management IP without changing networking.
CONTROL
chmod -R a+rX "$pkg/usr"
dpkg-deb --root-owner-group --build "$pkg" "/output/nex-core-local_${NEX_VERSION}_amd64.deb"
