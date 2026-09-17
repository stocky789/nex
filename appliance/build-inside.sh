#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
set -euo pipefail
umask 077
trap 'chown -R "${OUTPUT_UID:?}:${OUTPUT_GID:?}" /output' EXIT
mkdir /build
cd /build
tar -xzf /input/nex-source.tar.gz --strip-components=1
export CGO_ENABLED=1
go build -mod=vendor -trimpath -ldflags "-X nex.local/nex/internal/version.Version=${NEX_VERSION:-0.1.0-dev}" -o /build/nex-controller ./cmd/nex-controller
go version /build/nex-controller > /output/go-version.txt
(cd web && npm ci && npm run typecheck && npm run build)
cp /input/base.qcow2 /output/controller.qcow2
qemu-img resize /output/controller.qcow2 32G
export LIBGUESTFS_MEMSIZE=2048
printf 'repo_url=https://nexvirtualization.com/apt\n' > /build/updates.conf
printf 'Types: deb\nURIs: https://nexvirtualization.com/apt\nSuites: nex\nComponents: main\nArchitectures: amd64\nSigned-By: /usr/share/keyrings/nex-archive-keyring.gpg\n' > /build/nex.sources
virt-customize -a /output/controller.qcow2 --install cloud-init,qemu-guest-agent,python3,ca-certificates \
 --mkdir /usr/local/lib/nex --mkdir /usr/local/share/nex --mkdir /usr/share/keyrings --mkdir /etc/nex-controller --mkdir /etc/nex \
 --upload /build/nex-controller:/usr/local/lib/nex/nex-controller \
 --upload /usr/local/go/LICENSE:/usr/local/share/nex/go-LICENSE \
 --copy-in /build/web/dist:/usr/local/share/nex \
 --copy-in /build/docs/api:/usr/local/share/nex \
 --upload /input/nex-source.tar.gz:/usr/local/share/nex/nex-source.tar.gz \
 --upload /build/ARTWORK-LICENSE.md:/usr/local/share/nex/ARTWORK-LICENSE.md \
 --upload /build/appliance/firstboot.py:/usr/local/lib/nex/appliance-firstboot \
 --upload /build/appliance/run-controller.sh:/usr/local/lib/nex/run-controller \
 --upload /build/packaging/controller/apply-update:/usr/local/lib/nex/apply-update \
 --upload /build/packaging/core-local/nex-archive-keyring.gpg:/usr/share/keyrings/nex-archive-keyring.gpg \
 --upload /build/updates.conf:/etc/nex/updates.conf \
 --upload /build/nex.sources:/etc/apt/sources.list.d/nex.sources \
 --upload /build/appliance/nex-controller.service:/etc/systemd/system/nex-controller.service \
 --upload /build/appliance/nex-appliance-init.service:/etc/systemd/system/nex-appliance-init.service \
 --upload /build/packaging/controller/nex-controller-update.path:/etc/systemd/system/nex-controller-update.path \
 --upload /build/packaging/controller/nex-controller-update.service:/etc/systemd/system/nex-controller-update.service \
 --upload /build/appliance/recovery.py:/usr/local/lib/nex/controller-recovery \
 --run-command 'chmod 755 /usr/local/lib/nex/*; chmod -R a+rX /usr/local/share/nex; chmod 700 /etc/nex-controller; chmod 644 /etc/nex/updates.conf; useradd --system --home /var/lib/nex-controller nex-controller; systemctl enable qemu-guest-agent nex-appliance-init nex-controller nex-controller-update.path; passwd -l root; rm -f /etc/ssh/ssh_host_*; truncate -s 0 /etc/machine-id; rm -f /var/lib/dbus/machine-id /var/lib/systemd/random-seed; cloud-init clean --logs' \
 --run-command 'dpkg-query -W -f="\${Package}\t\${Version}\t\${Architecture}\t\${source:Package}\t\${source:Version}\n" > /usr/local/share/nex/components.tsv' \
 --selinux-relabel
virt-cat -a /output/controller.qcow2 /usr/local/share/nex/components.tsv > /output/components.tsv
qemu-img check /output/controller.qcow2
cp /input/nex-source.tar.gz /output/
cp /build/LICENSE /build/NOTICE /build/ARTWORK-LICENSE.md /output/
