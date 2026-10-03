#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
# Entry point of the nex-appliance-builder container for the Controller guest image: compile the Controller and its UI
# and lay out the files the image installs, as a tree that mkosi copies over the image root. No guest is customised here;
# image/build.sh controller builds the read-only system image from the Debian packages plus this tree.
set -euo pipefail
umask 022
trap 'chown -R "${OUTPUT_UID:?}:${OUTPUT_GID:?}" /output' EXIT
mkdir /build
cd /build
tar -xzf /input/nex-source.tar.gz --strip-components=1
export CGO_ENABLED=1
go build -mod=vendor -trimpath -ldflags "-X nex.local/nex/internal/version.Version=${NEX_VERSION:-0.1.0-dev}" -o /build/nex-controller ./cmd/nex-controller
go version /build/nex-controller > /output/go-version.txt
(cd web && npm ci && npm run typecheck && npm run build)
t=/output/payload
install -d "$t/usr/local/lib/nex" "$t/usr/local/share/nex" "$t/etc/systemd/system/corosync-qnetd.service.d"
install -m 755 /build/nex-controller "$t/usr/local/lib/nex/nex-controller"
install -m 755 /build/appliance/firstboot.py "$t/usr/local/lib/nex/appliance-firstboot"
install -m 755 /build/appliance/run-controller.sh "$t/usr/local/lib/nex/run-controller"
install -m 755 /build/appliance/recovery.py "$t/usr/local/lib/nex/controller-recovery"
install -m 755 /build/appliance/qnetd-runtime-init "$t/usr/local/lib/nex/qnetd-runtime-init"
# Used once by the image's post-install step (qnetd keeps its NSS database in /run) and removed there.
install -m 755 /build/appliance/package-service-policy.sh "$t/usr/local/lib/nex/package-service-policy"
install -m 644 /build/appliance/corosync-qnetd-runtime.conf "$t/etc/systemd/system/corosync-qnetd.service.d/nex-runtime.conf"
install -m 644 /build/appliance/nex-controller.service /build/appliance/nex-appliance-init.service "$t/etc/systemd/system/"
install -m 644 /usr/local/go/LICENSE "$t/usr/local/share/nex/go-LICENSE"
install -m 644 /build/ARTWORK-LICENSE.md "$t/usr/local/share/nex/ARTWORK-LICENSE.md"
install -m 644 /input/nex-source.tar.gz "$t/usr/local/share/nex/nex-source.tar.gz"
cp -r /build/web/dist "$t/usr/local/share/nex/dist"
cp -r /build/docs/api "$t/usr/local/share/nex/api"
cp /input/nex-source.tar.gz /build/LICENSE /build/NOTICE /build/ARTWORK-LICENSE.md /output/
