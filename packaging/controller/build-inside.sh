#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
set -euo pipefail
mkdir /build
cd /build
tar -xzf /input/nex-source.tar.gz --strip-components=1
go build -mod=vendor -trimpath -ldflags "-X nex.local/nex/internal/version.Version=${NEX_VERSION}" -o /build/nex-controller ./cmd/nex-controller
(cd web && npm ci && npm run typecheck && npm run build)
pkg=/build/package
install -d "$pkg/DEBIAN" \
  "$pkg/usr/local/lib/nex" \
  "$pkg/usr/local/share/nex" \
  "$pkg/usr/lib/systemd/system" \
  "$pkg/usr/share/doc/nex-controller" \
  "$pkg/usr/share/keyrings" \
  "$pkg/etc/nex"
printf 'repo_url=https://nexvirtualization.com/apt\n' > "$pkg/etc/nex/updates.conf"
printf '/etc/nex/updates.conf\n' > "$pkg/DEBIAN/conffiles"
# The Nex APT archive public key; postinst writes the matching signed apt source so
# apply-update can upgrade through apt with dependency resolution and signature
# verification instead of installing a bare .deb.
install -m644 packaging/core-local/nex-archive-keyring.gpg "$pkg/usr/share/keyrings/nex-archive-keyring.gpg"
install -m755 /build/nex-controller "$pkg/usr/local/lib/nex/"
install -m755 packaging/controller/run-controller "$pkg/usr/local/lib/nex/"
install -m755 packaging/controller/apply-update "$pkg/usr/local/lib/nex/"
install -m755 packaging/core-local/storage-downgrade-guard.py "$pkg/usr/local/lib/nex/storage-downgrade-guard"
printf '2\n' > "$pkg/usr/local/share/nex/storage-format-v2"
install -m644 packaging/controller/nex-controller.service "$pkg/usr/lib/systemd/system/"
install -m644 packaging/controller/nex-controller-update.path "$pkg/usr/lib/systemd/system/"
install -m644 packaging/controller/nex-controller-update.service "$pkg/usr/lib/systemd/system/"
install -m755 packaging/controller/postinst packaging/controller/prerm "$pkg/DEBIAN/"
cp -a web/dist "$pkg/usr/local/share/nex/"
cp -a docs/api "$pkg/usr/local/share/nex/"
cp /input/nex-source.tar.gz "$pkg/usr/local/share/nex/"
cp LICENSE NOTICE docs/LICENSING.md "$pkg/usr/share/doc/nex-controller/"
install -m644 /usr/local/go/LICENSE "$pkg/usr/share/doc/nex-controller/go-LICENSE"
go version /build/nex-controller > "$pkg/usr/share/doc/nex-controller/go-version.txt"
cat > "$pkg/DEBIAN/control" <<CONTROL
Package: nex-controller
Version: ${NEX_VERSION}
Architecture: amd64
Maintainer: Nex contributors
Depends: init-system-helpers, libc6 (>= 2.41), ca-certificates, python3, curl
Section: admin
Priority: optional
Description: Nex Controller management service
 Centralized Nex management API and web UI.
CONTROL
chmod -R a+rX "$pkg/usr" "$pkg/usr/local"
dpkg-deb --root-owner-group --build "$pkg" "/output/nex-controller_${NEX_VERSION}_amd64.deb"
