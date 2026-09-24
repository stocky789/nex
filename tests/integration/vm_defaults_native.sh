#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Run Core VM defaults against real libvirt in a disposable Debian 13 container.
set -euo pipefail
cd "$(dirname "$0")/../.."
repo=$PWD
guest_image=${1:-}
work=$(mktemp -d /tmp/nex-vm-defaults.XXXXXX)
name="nex-vm-defaults-${work##*.}"
engine=(docker)
if ! docker info >/dev/null 2>&1; then engine=(sudo -n docker); fi
cleanup() { "${engine[@]}" rm -f "$name" >/dev/null 2>&1 || true; "${engine[@]}" volume rm -f "$name-storage" >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT
test -c /dev/kvm || { echo '/dev/kvm is required' >&2; exit 1; }
test -c /dev/net/tun || { echo '/dev/net/tun is required' >&2; exit 1; }
python3 -c 'import os; fd=os.open("/dev/net/tun", os.O_RDWR); os.close(fd)' >/dev/null 2>&1 || { echo 'a working TUN device is required' >&2; exit 1; }
if [[ -n $guest_image ]]; then
  test -f "$guest_image" && test ! -L "$guest_image" || { echo 'guest image must be an existing regular, non-symlink qcow2' >&2; exit 1; }
  guest_image=$(realpath "$guest_image")
  printf '%s  %s\n' '85a969b7e99d7c817414136033df18c58d5c45ac8d27bb36e8ccb67173d2d4e3' "$guest_image" | sha256sum --check --status || { echo 'guest image differs from the verified Debian 13 genericcloud base' >&2; exit 1; }
fi
go_bin=${GO:-$(command -v go || printf '%s' "$repo/.cache/toolchains/go/bin/go")}
"$go_bin" build -o "$work/nex-controller" ./cmd/nex-controller
"$go_bin" build -o "$work/nex-core-agent" ./cmd/nex-core-agent
GO="$go_bin" python3 scripts/build-source.py --output "$work/nex-source.tar.gz"
image=debian:13-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132
if ! "${engine[@]}" image inspect "$image" >/dev/null 2>&1; then "${engine[@]}" pull "$image"; fi
mounts=(--mount "type=bind,src=$repo,dst=/repo,readonly" --mount "type=bind,src=$work,dst=/artifacts,readonly" --mount "type=volume,src=$name-storage,dst=/var/lib/nexqa-defaults")
if [[ -n $guest_image ]]; then mounts+=(--mount "type=bind,src=$guest_image,dst=/base/guest.qcow2,readonly"); fi
devices=(--device /dev/kvm --device /dev/net/tun)
if [[ -c /dev/vhost-net ]]; then devices+=(--device /dev/vhost-net); fi
"${engine[@]}" run --detach --init --name "$name" --label nex.test=vm-defaults --cap-add NET_ADMIN --sysctl net.ipv4.ip_forward=1 "${devices[@]}" "${mounts[@]}" "$image" sleep infinity >/dev/null
"${engine[@]}" exec "$name" sh -c 'apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends libvirt-daemon-system libvirt-clients qemu-system-x86 qemu-utils python3 openssl iproute2 genisoimage openssh-client iptables iperf3 >/tmp/packages.log 2>&1'
"${engine[@]}" exec "$name" sh -c 'printf "%s\n" "remember_owner = 0" >> /etc/libvirt/qemu.conf && virtlogd -d && libvirtd -d && virsh -c qemu:///system domcapabilities --virttype kvm --arch x86_64 --machine q35 | grep -q "<domain>kvm</domain>" && ip link add nexqa type bridge && ip addr add 198.18.99.1/24 dev nexqa && ip link set nexqa up && ip link set lo up && iptables -t nat -A POSTROUTING -s 198.18.99.0/24 ! -d 198.18.99.0/24 -j MASQUERADE'
"${engine[@]}" exec -e NEX_BIN=/artifacts -e NEX_LAB_DIR=/tmp "$name" python3 /repo/tests/integration/vm_defaults_native.py
