#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Read-only acceptance on the disposable nex-lab VM used by wizard QA.
set -eu
test "$(hostname)" = nex-lab
for unit in libvirtd.socket ssh.service chrony.service networking.service; do
    systemctl is-active "$unit"
done
test -d /sys/class/net/br0/bridge
test -e /sys/class/net/br0/brif/nex0
test -z "$(ip -4 -o addr show dev nex0)"
ip -c=never -4 -o addr show dev br0 | grep '10.0.2.15/24'
ip -c=never -4 -o addr show dev nex1.123 | grep '10.20.0.2/24'
ip -c=never -d link show nex1.123 | grep 'vlan protocol 802.1Q id 123'
test "$(ip -4 route show default | wc -l)" = 1
ip -4 route show default | grep 'dev br0'
grep -Fx 'nameserver 10.0.2.3' /etc/resolv.conf
runuser -u nobody -- cat /etc/resolv.conf
for file in /etc/hosts /etc/hostname /etc/resolv.conf /etc/network/interfaces; do
    test "$(stat -c %a "$file")" = 644
done
test "$(stat -c %a /etc)" = 755
test -s /var/log/installer/syslog
if grep -R 'TestNex2026' /var/log/installer >/dev/null; then
    echo 'Plaintext credential found in installer logs' >&2
    exit 1
else
    test "$?" = 1
fi
ping -c 2 -W 2 10.0.2.2
# QEMU's user network has no tagged peer: generate ARP for capture inspection.
ping -c 1 -W 1 -I nex1.123 10.20.0.1 || true
echo NEX_WIZARD_INSTALLED_PASS
