#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Request a lease for display only; never install an address, route or resolver.
set -eu

valid_ipv4() {
    case "$1" in ''|*[!0-9.]*|.*|*.|*..*) return 1 ;; esac
    old_ifs=$IFS
    IFS=.
    set -- $1
    IFS=$old_ifs
    [ "$#" -eq 4 ] || return 1
    for octet do
        [ "${#octet}" -le 3 ] && [ "$octet" -le 255 ] || return 1
    done
    [ "$1" -gt 0 ] && [ "$1" -lt 224 ]
}

# udhcpc's hook mode. Its ordinary configuration script must never run.
case "${1:-}" in
    bound|renew)
        valid_ipv4 "${ip:-}" || exit 1
        printf '%s\n' "$ip" > "${NEX_DHCP_RESULT:?}"
        exit 0 ;;
    deconfig|nak|leasefail) exit 0 ;;
esac

[ "$#" -eq 2 ] || exit 1
nic=$1
vlan=$2
case "$nic" in ''|*[!a-zA-Z0-9_.:-]*|.|..) exit 1 ;; esac
[ "${#nic}" -le 15 ] && [ -d "/sys/class/net/$nic" ] || exit 1
case "$vlan" in ''|*[!0-9]*) exit 1 ;; esac
[ "${#vlan}" -le 4 ] && [ "$vlan" -le 4094 ] || exit 1

umask 077
work=$(mktemp -d /run/nex-dhcp.XXXXXX)
client=
owned_vlan=
cleanup() {
    trap - EXIT HUP INT TERM
    if [ -n "$client" ]; then
        kill "$client" 2>/dev/null || :
        wait "$client" 2>/dev/null || :
    fi
    if [ -n "$owned_vlan" ]; then
        ip link delete dev "$owned_vlan" 2>/dev/null || :
    fi
    rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

interface=$nic
if [ "$vlan" -ne 0 ]; then
    interface=nxdp$$
    # A collision fails creation; cleanup owns only a link we created.
    ip link add link "$nic" name "$interface" type vlan id "$vlan" >/dev/null 2>&1 || exit 1
    owned_vlan=$interface
    if [ -f "/proc/sys/net/ipv6/conf/$interface/disable_ipv6" ]; then
        printf '1\n' > "/proc/sys/net/ipv6/conf/$interface/disable_ipv6"
    fi
    ip link set dev "$interface" up >/dev/null 2>&1 || exit 1
fi
export NEX_DHCP_RESULT="$work/address"
# Foreground and three three-second attempts: no daemon or background retries.
# The custom hook receives ACK data without changing the installer's network.
/usr/bin/busybox udhcpc -f -n -q -t 3 -T 3 -i "$interface" \
    -p "$work/pid" -s "$0" >"$work/client.log" 2>&1 &
client=$!
wait "$client" || exit 1
client=
[ -f "$NEX_DHCP_RESULT" ] || exit 1
address=$(cat "$NEX_DHCP_RESULT")
valid_ipv4 "$address" || exit 1
printf '%s\n' "$address"
