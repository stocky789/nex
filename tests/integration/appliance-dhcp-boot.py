#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Boot a disposable appliance overlay; verify DHCP and short-password initialization.

Requires QEMU/KVM, openssl, and genisoimage or the existing nex-iso-builder image.
Only QEMU user networking is used. The supplied image is a read-only backing file.
This does not exercise host-local deployment, enrollment, or a real LAN DHCP server.
"""
import argparse
import base64
import hashlib
import http.client
import json
import os
from pathlib import Path
import shutil
import socket
import ssl
import subprocess
import tempfile
import time


def qga(path, command, arguments=None):
    with socket.socket(socket.AF_UNIX) as sock:
        sock.settimeout(5)
        sock.connect(str(path))
        stream = sock.makefile('rwb')
        stream.write(json.dumps({'execute': command, 'arguments': arguments or {}}).encode() + b'\n')
        stream.flush()
        reply = json.loads(stream.readline())
        if 'error' in reply:
            raise RuntimeError(reply['error']['class'])
        return reply['return']


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', type=Path, required=True)
    parser.add_argument('--evidence', type=Path, required=True, help='new output directory')
    parser.add_argument('--docker', default='docker')
    args = parser.parse_args()
    image = args.image.resolve(strict=True)
    assert image.is_file() and image.suffix == '.qcow2', 'regular qcow2 backing image required'
    args.evidence.mkdir(mode=0o700, parents=True, exist_ok=False)
    os.umask(0o077)
    hostname, mac = 'controller.dhcp.test', '52:54:00:ab:cd:ef'
    results = []
    with tempfile.TemporaryDirectory(prefix='nex-appliance-dhcp.') as directory:
        work = Path(directory)
        seed = work / 'seed'
        seed.mkdir()
        cert, key = work / 'tls.crt', work / 'tls.key'
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
                        '-days', '1', '-subj', '/CN=' + hostname, '-addext',
                        'subjectAltName=DNS:' + hostname, '-keyout', str(key), '-out', str(cert)],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        bootstrap = {'admin_password': 'a', 'tls_certificate': cert.read_text(),
                     'tls_private_key': key.read_text(), 'hostname': hostname, 'network_mode': 'dhcp'}
        encoded = base64.b64encode(json.dumps(bootstrap).encode()).decode()
        (seed / 'user-data').write_text('#cloud-config\nhostname: ' + hostname +
            "\nmanage_etc_hosts: true\nssh_pwauth: true\ndisable_root: false\nwrite_files:\n"
            "  - path: /etc/nex-controller/bootstrap.json\n    owner: root:root\n"
            "    permissions: '0600'\n    encoding: b64\n    content: " + encoded + '\n')
        (seed / 'meta-data').write_text('instance-id: nex-dhcp-disposable\nlocal-hostname: ' + hostname + '\n')
        (seed / 'network-config').write_text("version: 2\nethernets:\n  management:\n    match:\n"
            "      macaddress: '" + mac + "'\n    set-name: nex0\n    dhcp4: true\n")
        if shutil.which('genisoimage'):
            command = ['genisoimage', '-quiet', '-output', str(work / 'seed.iso'),
                       '-volid', 'cidata', '-joliet', '-rock', str(seed)]
        else:
            command = [args.docker, 'run', '--rm', '--entrypoint', 'xorriso', '-v',
                       str(work) + ':/work', 'nex-iso-builder', '-as', 'mkisofs', '-quiet',
                       '-output', '/work/seed.iso', '-volid', 'cidata', '-joliet', '-rock', '/work/seed']
        subprocess.run(command, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        disk = work / 'guest.qcow2'
        command = ['qemu-img', 'create', '-f', 'qcow2', '-F', 'qcow2', '-b', str(image), str(disk), '32G']
        if not shutil.which('qemu-img'):
            command = [args.docker, 'run', '--rm', '--user', f'{os.getuid()}:{os.getgid()}',
                       '--entrypoint', 'qemu-img', '-v', str(image) + ':' + str(image) + ':ro',
                       '-v', str(work) + ':' + str(work), 'nex-appliance-builder', *command[1:]]
        subprocess.run(command, check=True, stdout=subprocess.DEVNULL)
        with socket.socket() as reservation:
            reservation.bind(('127.0.0.1', 0))
            port = reservation.getsockname()[1]
        agent = work / 'qga.sock'
        serial = work / 'serial.log'
        command = ['qemu-system-x86_64', '-enable-kvm', '-cpu', 'host', '-m', '4096', '-smp', '2',
                   '-display', 'none', '-monitor', 'none', '-serial', 'file:' + str(serial),
                   '-drive', 'file=' + str(disk) + ',format=qcow2,if=virtio',
                   '-drive', 'file=' + str(work / 'seed.iso') + ',format=raw,media=cdrom,readonly=on',
                   '-netdev', f'user,id=net0,hostfwd=tcp:127.0.0.1:{port}-:8443',
                   '-device', 'virtio-net-pci,netdev=net0,mac=' + mac,
                   '-device', 'virtio-serial-pci', '-chardev',
                   'socket,path=' + str(agent) + ',server=on,wait=off,id=qga0',
                   '-device', 'virtserialport,chardev=qga0,name=org.qemu.guest_agent.0']
        with (work / 'qemu.log').open('wb') as log:
            process = subprocess.Popen(command, stdout=log, stderr=log)
            try:
                context = ssl.create_default_context(cafile=str(cert))
                def request(path, body=None):
                    connection = http.client.HTTPConnection('127.0.0.1', port, timeout=5)
                    connection.sock = context.wrap_socket(socket.create_connection(('127.0.0.1', port), 5),
                                                          server_hostname=hostname)
                    try:
                        connection.request('POST' if body is not None else 'GET', path,
                            json.dumps(body) if body is not None else None,
                            {'Host': hostname, 'Origin': 'https://' + hostname, 'Content-Type': 'application/json'})
                        response = connection.getresponse()
                        return response.status, json.loads(response.read())
                    finally:
                        connection.close()
                deadline = time.monotonic() + 300
                while time.monotonic() < deadline:
                    assert process.poll() is None, 'QEMU exited before appliance readiness'
                    try:
                        interfaces = qga(agent, 'guest-network-get-interfaces')
                        matches = [item for item in interfaces if item.get('hardware-address', '').lower() == mac]
                        addresses = [address['ip-address'] for item in matches for address in item.get('ip-addresses', [])
                                     if address['ip-address-type'] == 'ipv4']
                        assert addresses == ['10.0.2.15'], 'DHCP management address not ready'
                        assert request('/appliance-ready.json') == (200, {'bootstrap_version': 1, 'status': 'ready'})
                        break
                    except (OSError, ValueError, RuntimeError, AssertionError, http.client.HTTPException):
                        time.sleep(2)
                else:
                    raise AssertionError('DHCP appliance not ready within 300 seconds')
                results.append('Native DHCP assigned 10.0.2.15 to exact management MAC; pinned-hostname HTTPS ready marker valid')
                assert request('/api/v1/session', {'username': 'root', 'password': 'wrong'})[0] == 401
                status, session = request('/api/v1/session', {'username': 'root', 'password': 'a'})
                assert status == 200 and session.get('csrf_token'), 'one-character admin login failed'
                results.append('Actual Controller rejects incorrect password and accepts one-character bootstrap password')
                command = ('systemctl is-active --quiet nex-controller qemu-guest-agent && '
                           'test -e /var/lib/nex-controller/appliance-initialized && '
                           'test ! -e /etc/nex-controller/bootstrap.json && '
                           'test ! -e /etc/nex-controller/admin-password && '
                           'test ! -e /var/lib/cloud/instance/user-data.txt && '
                           'test ! -e /run/cloud-init/instance-data-sensitive.json')
                pid = qga(agent, 'guest-exec', {'path': '/bin/sh', 'arg': ['-c', command]})['pid']
                for _ in range(30):
                    state = qga(agent, 'guest-exec-status', {'pid': pid})
                    if state.get('exited'):
                        assert state.get('exitcode') == 0, 'guest services or secret cleanup failed'
                        break
                    time.sleep(1)
                else:
                    raise AssertionError('guest verification timed out')
                results.append('Guest services active; firstboot credentials and sensitive cloud-init cache removed')
            finally:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
        # Preserve only public results; discard guest seed, key, overlay, and raw logs.
        digest = hashlib.file_digest(image.open('rb'), 'sha256').hexdigest()
        (args.evidence / 'results.json').write_text(json.dumps({'image': str(image), 'sha256': digest,
            'method': 'Disposable KVM overlay, 4 GiB/2 CPUs, QEMU user DHCP, no localcore enrollment',
            'results': results}, indent=2) + '\n')
        for result in results:
            print('PASS:', result)


if __name__ == '__main__':
    main()
