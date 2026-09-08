#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
# DHCP fixtures run only in a disposable, network-isolated container.
set -euo pipefail
docker_command=docker
if [[ ${1:-} == --docker && $# == 2 ]]; then docker_command=$2; elif (($#)); then
    echo 'Usage: installer-dhcp.sh [--docker EXECUTABLE]' >&2; exit 2
fi
root=$(cd "$(dirname "$0")/../.." && pwd)
stage=$(mktemp -d -t nex-dhcp-check-XXXXXXXX)
trap 'rm -rf "$stage"' EXIT
cat > "$stage/Dockerfile" <<'DOCKER'
FROM debian:13
RUN apt-get update && apt-get install -y --no-install-recommends busybox iproute2 dnsmasq-base python3 && rm -rf /var/lib/apt/lists/*
DOCKER
cat > "$stage/inside.py" <<'PY'
import json
import os
from pathlib import Path
import signal
import subprocess
import time

HELPER = '/source/installer/gui/dhcp-preview.sh'
def command(*args):
    return subprocess.check_output(args, text=True).strip()
def ip(*args):
    return command('ip', *args)
def state():
    return (ip('-j', 'address'), ip('-j', '-4', 'route'), ip('-j', '-6', 'route'),
            Path('/etc/resolv.conf').read_bytes())
def interfaces():
    return set(os.listdir('/sys/class/net'))
def probe(nic='nex0', vlan='0', **kwargs):
    return subprocess.run([HELPER, nic, vlan], text=True, capture_output=True,
                          timeout=40, **kwargs)

# Keep IPv6 autoconfiguration out of the fixture, matching installer discovery.
# Docker sets the disposable network namespace's IPv6 policy below.
ip('link', 'add', 'nex0', 'type', 'veth', 'peer', 'name', 'peer0')
ip('link', 'add', 'nex1', 'type', 'veth', 'peer', 'name', 'peer1')
for nic in ('nex0', 'peer0', 'nex1', 'peer1'):
    ip('link', 'set', nic, 'up')
ip('address', 'add', '192.0.2.254/24', 'dev', 'peer0')
ip('link', 'add', 'link', 'peer0', 'name', 'peer0.42', 'type', 'vlan', 'id', '42')
ip('link', 'set', 'peer0.42', 'up')
ip('address', 'add', '198.51.100.254/24', 'dev', 'peer0.42')
servers = []
for nic, address in [('peer0', '192.0.2.10'), ('peer0.42', '198.51.100.10')]:
    output = open('/tmp/dnsmasq-' + nic + '.log', 'w')
    servers.append(subprocess.Popen(['dnsmasq', '--keep-in-foreground', '--log-facility=-',
        '--log-dhcp', '--port=0', '--bind-interfaces', '--interface=' + nic,
        '--except-interface=lo', '--dhcp-range=' + address + ',' + address + ',255.255.255.0,1h',
        '--dhcp-leasefile=/tmp/leases-' + nic, '--pid-file=/tmp/dnsmasq-' + nic + '.pid'],
        stdout=output, stderr=subprocess.STDOUT))
time.sleep(.3)
assert all(p.poll() is None for p in servers), 'fixture DHCP server failed'
before = state()
links = interfaces()
for vlan, expected in [('0', '192.0.2.10'), ('42', '198.51.100.10')]:
    result = probe(vlan=vlan)
    assert result.returncode == 0, (vlan, result.stdout, result.stderr)
    assert result.stdout.strip() == expected, result.stdout
    assert state() == before, 'preview modified addresses/routes/DNS'
    assert interfaces() == links, 'temporary VLAN leaked'
    assert not list(Path('/run').glob('nex-dhcp.*')), 'private lease files leaked'
    print('PASS actual DHCP ACK preview VLAN=' + vlan + ': ' + expected, flush=True)
for nic, vlan in [('missing0', '0'), ('../nex0', '0'), ('nex0', '-1'),
                  ('nex0', '4095'), ('nex0', 'foo'), ('nex0', '')]:
    result = probe(nic, vlan)
    assert result.returncode != 0 and not result.stdout, (nic, vlan, result)
assert state() == before and interfaces() == links
print('PASS invalid interface/VLAN rejected without network changes', flush=True)
# Exercise malformed lease callback data directly; server success above uses real ACKs.
for address in ('', '192.0.2', '192.0.2.256', '192..2.10', '224.0.0.1', '1.2.3.4;bad'):
    result = subprocess.run([HELPER, 'bound'], env={**os.environ, 'ip': address,
                            'NEX_DHCP_RESULT': '/tmp/malformed-address'}, capture_output=True)
    assert result.returncode != 0 and not Path('/tmp/malformed-address').exists(), address
print('PASS malformed ACK hook data rejected', flush=True)
start = time.monotonic()
result = probe('nex1')
assert result.returncode != 0 and not result.stdout, result
assert time.monotonic() - start < 20, 'DHCP timeout not bounded'
assert state() == before and interfaces() == links
print('PASS absent DHCP server fails within bounded timeout', flush=True)
for vlan in ('0', '43'):
    pending = subprocess.Popen([HELPER, 'nex1', vlan], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    time.sleep(.5)
    assert pending.poll() is None, 'probe did not remain pending for cancellation'
    pending.send_signal(signal.SIGTERM)
    stdout, stderr = pending.communicate(timeout=5)
    assert pending.returncode != 0 and not stdout, (stdout, stderr)
    assert state() == before and interfaces() == links, 'cancelled preview changed network/leaked VLAN'
    assert not list(Path('/run').glob('nex-dhcp.*')), 'cancelled preview leaked files'
    assert not any(b'udhcpc' in path.read_bytes().split(b'\0')
                   for path in Path('/proc').glob('[0-9]*/cmdline')), 'cancelled DHCP client survived'
print('PASS cancellation removes owned child/VLAN/files; existing interfaces preserved', flush=True)
PY
"$docker_command" build -t nex-installer-dhcp-check "$stage"
# Permit the helper to disable IPv6 on its VLAN in this isolated network namespace.
"$docker_command" run --rm --network none --cap-add NET_ADMIN --security-opt systempaths=unconfined --sysctl net.ipv6.conf.default.disable_ipv6=1 \
    -v "$root:/source:ro" -v "$stage:/check:ro" \
    nex-installer-dhcp-check python3 /check/inside.py
