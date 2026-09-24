#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# nex-config validates these arguments and the selected adapter's current MAC.
set -eu

# Keep Debian's DHCP handling and lease renewal, including an explicit DNS override.
case "${1:-}" in
    bound|renew)
        [ -z "${NEX_NETINST_DNS:-}" ] || export dns="$NEX_NETINST_DNS"
        /etc/udhcpc/default.script "$1"
        cp /etc/resolv.conf /run/nex-installer/netinst-resolv.conf
        exit ;;
    deconfig|nak|leasefail)
        exec /etc/udhcpc/default.script "$1" ;;
esac

[ "$#" -eq 6 ] || exit 1
nic=$1 mode=$2 vlan=$3 address=$4 gateway=$5 dns=$6
state=/run/nex-installer
mkdir -p "$state"
# A retry replaces only the network configuration owned by this installer helper.
if [ -s "$state/netinst-dhcp.pid" ]; then
    kill "$(cat "$state/netinst-dhcp.pid")" 2>/dev/null || true
    rm -f "$state/netinst-dhcp.pid"
fi
if [ -s "$state/netinst-interface" ]; then
    previous=$(cat "$state/netinst-interface")
    if ip link show dev "$previous" >/dev/null 2>&1; then
        ip -4 addr flush dev "$previous"
        ip -4 route flush dev "$previous"
        if [ "$previous" = nxinstall ]; then ip link delete dev nxinstall; fi
    fi
fi
interface=$nic
ip link set dev "$nic" up
if [ "$vlan" -ne 0 ]; then
    interface=nxinstall
    ip link add link "$nic" name "$interface" type vlan id "$vlan"
    ip link set dev "$interface" up
fi
printf '%s\n' "$interface" > "$state/netinst-interface"
ip -4 addr flush dev "$interface"
ip -4 route flush dev "$interface"
case "$mode" in
    dhcp)
        export NEX_NETINST_DNS="$dns"
        # Remains alive to renew leases during long downloads; failure is bounded.
        /usr/bin/busybox udhcpc -n -t 5 -T 3 -i "$interface" \
            -p "$state/netinst-dhcp.pid" -s "$0" >"$state/netinst-dhcp.log" 2>&1 ;;
    static)
        ip -4 addr replace "$address" dev "$interface"
        if [ -n "$gateway" ]; then ip -4 route replace default via "$gateway" dev "$interface"; fi ;;
    *) exit 1 ;;
esac
if [ -n "$dns" ] || [ "$mode" = static ]; then
    : > /etc/resolv.conf
    for server in $dns; do printf 'nameserver %s\n' "$server" >> /etc/resolv.conf; done
fi
cp /etc/resolv.conf "$state/netinst-resolv.conf"
