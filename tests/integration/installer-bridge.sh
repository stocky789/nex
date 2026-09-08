#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
# Native network changes occur only in disposable, network-isolated containers.
set -euo pipefail
docker_command=docker
if [[ ${1:-} == --docker && $# == 2 ]]; then docker_command=$2; elif (($#)); then
    echo 'Usage: installer-bridge.sh [--docker EXECUTABLE]' >&2; exit 2
fi
root=$(cd "$(dirname "$0")/../.." && pwd)
stage=$(mktemp -d -t nex-bridge-check-XXXXXXXX)
trap 'rm -rf "$stage"' EXIT
cat > "$stage/Dockerfile" <<'DOCKER'
FROM debian:13
RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev libglib2.0-dev pkg-config python3 iproute2 ifupdown bridge-utils dhcpcd-base dnsmasq-base && rm -rf /var/lib/apt/lists/*
DOCKER
cat > "$stage/fixture.c" <<'C'
#include "nex-network.h"
int main(int argc, char **argv) {
    g_assert_cmpint(argc, ==, 4);
    GKeyFile *c = g_key_file_new(); GError *e = NULL;
    gboolean dhcp = g_str_equal(argv[1], "dhcp");
    char *data = g_strdup_printf("[Network]\nmanagement=enp1s0\ndns=192.0.2.1\n[NIC:enp1s0]\nmac=02:00:00:00:00:10\nmode=%s\naddress=%s\ngateway=%s\nvlan=%s\n[NIC:enp2s0]\nmac=02:00:00:00:00:20\nmode=dhcp\naddress=\ngateway=\nvlan=43\n", argv[1], dhcp ? "" : "192.0.2.10/24", dhcp ? "" : "192.0.2.1", argv[2]);
    g_assert_true(g_key_file_load_from_data(c, data, -1, 0, &e));
    g_assert_true(nex_network_write(c, argv[3], &e));
    g_assert_no_error(e); g_free(data); g_key_file_free(c);
}
C
cat > "$stage/check.py" <<'PY'
import json, runpy, subprocess
from pathlib import Path
def ip(*args):
    return json.loads(subprocess.check_output(['ip','-j',*args]))
port='nex0.42' if Path('/sys/class/net/nex0.42').exists() else 'nex0'
assert Path('/sys/class/net/br0/bridge').is_dir()
assert Path('/sys/class/net',port,'master').resolve().name=='br0'
assert Path('/sys/class/net/br0/address').read_text().strip()=='02:00:00:00:00:10'
assert [(r['dev'],r.get('gateway')) for r in ip('-4','route','show','default')]==[('br0','192.0.2.1')]
for name in {'nex0',port}:
    assert not any(i['addr_info'] for i in ip('-4','address','show','dev',name)), name
addresses=ip('-4','address','show','dev','br0')[0]['addr_info']
assert [(a['local'],a['prefixlen']) for a in addresses]==[('192.0.2.10',24)], addresses
secondary=ip('-4','address','show','dev','nex1.43')[0]['addr_info']
assert [(a['local'],a['prefixlen']) for a in secondary]==[('198.51.100.10',24)], secondary
assert not Path('/sys/class/net/nex1.43/master').exists()
assert not any(i['addr_info'] for i in ip('-4','address','show','dev','nex1'))
assert runpy.run_path('/source/packaging/core-local/core-local-setup')['discover']()==('192.0.2.10','br0')
assert 'nameserver 192.0.2.1' in Path('/etc/resolv.conf').read_text()
print('PASS native bridge, selected MAC/port, bridge-only management IP/default route, DNS, secondary VLAN DHCP isolation, local GUI discovery')
PY
cat > "$stage/inside.sh" <<'INSIDE'
#!/bin/bash
set -euo pipefail
mode=$1
vlan=$2
trap 'ip -d address; ip route; cat /etc/network/interfaces; cat /tmp/dnsmasq.log /tmp/secondary-dnsmasq.log 2>/dev/null || true' ERR
cc -Wall -Wextra -Werror -I/source/installer/gui /check/fixture.c /source/installer/gui/nex-network.c $(pkg-config --cflags --libs glib-2.0) -o /tmp/fixture
mkdir /tmp/target
/tmp/fixture "$mode" "$vlan" /tmp/target
cp /tmp/target/etc/network/interfaces /etc/network/interfaces
cp /tmp/target/etc/dhcpcd.conf /etc/dhcpcd.conf
cat /tmp/target/etc/resolv.conf > /etc/resolv.conf
ip link add nex0 type veth peer name peer0
ip link set nex0 address 02:00:00:00:00:10
ip link set peer0 up
server=peer0
if [[ $vlan != 0 ]]; then
    ip link add link peer0 name peer0.42 type vlan id 42
    ip link set peer0.42 up
    server=peer0.42
fi
# The fixture server is not the gateway, and uses /32 so br0 owns the subnet route.
ip address add 192.0.2.254/32 dev "$server"
ip link add nex1 type veth peer name peer1
ip link set nex1 address 02:00:00:00:00:20
ip link set peer1 up
ip link add link peer1 name peer1.43 type vlan id 43
ip link set peer1.43 up
ip address add 198.51.100.254/32 dev peer1.43
dnsmasq --keep-in-foreground --log-facility=- --log-dhcp --port=0 --bind-interfaces --interface=peer1.43 --except-interface=lo --dhcp-range=198.51.100.10,198.51.100.10,255.255.255.0,1h --dhcp-option=3,198.51.100.1 --dhcp-option=6,198.51.100.1 --dhcp-leasefile=/tmp/secondary-leases --pid-file=/tmp/secondary-dnsmasq.pid >/tmp/secondary-dnsmasq.log 2>&1 &
if [[ $mode == dhcp ]]; then
    dnsmasq --keep-in-foreground --log-facility=- --log-dhcp --port=0 --bind-interfaces --interface="$server" --except-interface=lo --dhcp-range=192.0.2.10,192.0.2.10,255.255.255.0,1h --dhcp-option=3,192.0.2.1 --dhcp-option=6,192.0.2.1 --dhcp-leasefile=/tmp/leases --pid-file=/tmp/dnsmasq.pid >/tmp/dnsmasq.log 2>&1 &
    sleep .3
fi
for cycle in first-boot recovery; do
    ifup br0
    ifup nex1.43
    python3 /check/check.py
    echo "PASS $mode VLAN=$vlan $cycle"
    ifdown nex1.43
    ifdown br0
    test ! -e /sys/class/net/br0
    test -z "$(ip -4 route show default)"
done
INSIDE
"$docker_command" build -t nex-installer-bridge-check "$stage"
for mode in static dhcp; do
    for vlan in 0 42; do
        "$docker_command" run --rm --network none --cap-add NET_ADMIN \
            -v "$root:/source:ro" -v "$stage:/check:ro" \
            nex-installer-bridge-check bash /check/inside.sh "$mode" "$vlan"
    done
done
