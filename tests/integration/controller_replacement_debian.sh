#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Controller replacement with real processes, real libvirt and (when the host offers it) KVM,
# inside a disposable Debian container with its own network namespace. NET_ADMIN is granted
# only to that container so it can own the test bridge; nothing on the host network changes.
set -euo pipefail
cd "$(dirname "$0")/../.."
repo=$PWD
evidence=${1:-.cache/controller-replacement-debian}
work=$(mktemp -d /tmp/nex-replacement-debian.XXXXXX)
name="nex-replacement-debian-${work##*.}"
engine=(docker)
if ! docker info >/dev/null 2>&1; then engine=(sudo -n docker); fi
cleanup() { "${engine[@]}" rm -f "$name" >/dev/null 2>&1 || true; "${engine[@]}" volume rm -f "$name-storage" >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT
export GO=${GO:-$(command -v go || printf '%s' "$repo/.cache/toolchains/go/bin/go")}
"$GO" build -o "$work/nex-controller" ./cmd/nex-controller
"$GO" build -o "$work/nex-core-agent" ./cmd/nex-core-agent
python3 scripts/build-source.py --output "$work/nex-source.tar.gz"
image=debian:13-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132
if ! "${engine[@]}" image inspect "$image" >/dev/null 2>&1; then "${engine[@]}" pull "$image"; fi
device=()
vm=()
if [[ -c /dev/kvm ]]; then device=(--device /dev/kvm); vm=(--vm); else echo "NOTE: /dev/kvm absent on this machine; VM definitions will not be covered by this run" >&2; fi
"${engine[@]}" run --detach --name "$name" --label nex.test=controller-replacement --cap-add NET_ADMIN "${device[@]}" \
  --mount "type=bind,src=$repo,dst=/repo,readonly" --mount "type=bind,src=$work,dst=/artifacts,readonly" \
  --mount "type=volume,src=$name-storage,dst=/var/lib/nexqa-replacement" "$image" sleep infinity >/dev/null
# The overlay root is not a supported storage filesystem; the named volume lives on the engine's local disk.
"${engine[@]}" exec "$name" sh -c 'apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends libvirt-daemon-system libvirt-clients qemu-system-x86 qemu-utils python3 openssl iproute2 >/tmp/packages.log 2>&1'
"${engine[@]}" exec "$name" sh -c 'libvirtd -d && for i in $(seq 1 50); do virsh -c qemu:///system list >/dev/null 2>&1 && break; sleep 0.2; done && ip link add nexqa type bridge && ip link set nexqa up && ip link set lo up'
if [[ ${#vm[@]} -gt 0 ]]; then
  # Warm libvirt's QEMU capability probe so the agent's bounded check does not race it.
  "${engine[@]}" exec "$name" sh -c 'virsh -c qemu:///system domcapabilities --virttype kvm --arch x86_64 --machine q35 | grep -q "<domain>kvm</domain>"' || { echo "KVM domain capabilities unavailable inside the container" >&2; exit 1; }
fi
status=0
"${engine[@]}" exec -e NEX_BIN=/artifacts -e NEX_LAB_DIR=/tmp "$name" python3 /repo/tests/integration/controller_replacement.py --bridge nexqa --storage-root /var/lib/nexqa-replacement --evidence /tmp/evidence "${vm[@]}" || status=$?
mkdir -p "$evidence"
"${engine[@]}" cp "$name:/tmp/evidence/controller-replacement.json" "$evidence/controller-replacement-debian.json" || true
if [[ $status -ne 0 ]]; then echo "replacement check failed ($status); diagnostics in $evidence/controller-replacement-debian.json" >&2; exit "$status"; fi
"${engine[@]}" exec "$name" sh -c 'cat /etc/os-release | head -2; dpkg-query -W libvirt-daemon qemu-system-x86' > "$evidence/controller-replacement-debian-environment.txt"
echo "Evidence: $evidence/controller-replacement-debian.json"
