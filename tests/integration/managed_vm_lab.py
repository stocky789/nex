#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Prepare a private copy of an existing disposable appliance lab for managed-VM QA.

Only regular-file lab images are copied. QEMU uses loopback forwards/user networking;
no workstation disks, bridges or libvirt services are changed. The copied host may
contain old test VMs; stop those copies before testing. Keep private keys out of QA evidence.
"""
import argparse
import json
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def ssh(lab, command, **kwargs):
    ports = json.loads((lab / 'ports.json').read_text())
    return subprocess.run(['ssh', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=3',
        '-o', f'UserKnownHostsFile={lab}/known_hosts', '-i', str(lab / 'id_ed25519'),
        '-p', str(ports['ssh']), 'root@127.0.0.1', command], check=True,
        capture_output=True, **kwargs)


def prepare(source):
    source = source.resolve(strict=True)
    assert source.name.startswith('nex-appliance-lab.'), 'source must be an explicitly disposable appliance lab'
    for name in ('host.raw', 'appliance-storage.raw', 'id_ed25519', 'known_hosts'):
        assert (source / name).is_file() and not (source / name).is_symlink(), name
    lab = Path(tempfile.mkdtemp(prefix='nex-managed-vm-lab.', dir=ROOT / '.cache'))
    for name in ('host.raw', 'appliance-storage.raw'):
        subprocess.run(['cp', '--reflink=auto', '--sparse=always', str(source / name), str(lab / name)], check=True)
    shutil.copy2(source / 'id_ed25519', lab / 'id_ed25519')
    ports = {'ssh': free_port()}
    (lab / 'ports.json').write_text(json.dumps(ports))
    # Reuse the known public host key, replacing only the loopback port identity.
    keys = (source / 'known_hosts').read_text().splitlines()
    (lab / 'known_hosts').write_text('\n'.join(f'[127.0.0.1]:{ports["ssh"]} ' + ' '.join(line.split()[1:]) for line in keys if line and not line.startswith('#')) + '\n')
    command = ['qemu-system-x86_64', '-accel', 'kvm', '-cpu', 'host', '-m', '8192', '-smp', '4',
        '-display', 'none', '-monitor', 'none', '-serial', f'file:{lab}/serial.log',
        '-qmp', f'unix:{lab}/qmp.sock,server=on,wait=off',
        '-drive', f'file={lab}/host.raw,format=raw,if=none,id=host-disk',
        '-device', 'virtio-blk-pci,drive=host-disk,bootindex=1',
        '-drive', f'file={lab}/appliance-storage.raw,format=raw,if=none,id=appliance-store',
        '-device', 'virtio-blk-pci,drive=appliance-store,serial=NEX-APPLIANCE-QA-ONLY',
        '-netdev', f'user,id=n0,hostfwd=tcp:127.0.0.1:{ports["ssh"]}-:22',
        '-device', 'virtio-net-pci,netdev=n0,mac=52:54:00:12:34:10',
        '-netdev', 'user,id=n1,net=10.0.3.0/24', '-device', 'virtio-net-pci,netdev=n1,mac=52:54:00:12:34:11']
    (lab / 'command.json').write_text(json.dumps(command, indent=2) + '\n')
    with (lab / 'qemu.log').open('w') as log:
        proc = subprocess.Popen(command, stdout=log, stderr=log, start_new_session=True)
    (lab / 'qemu.pid').write_text(str(proc.pid))
    print(lab, flush=True)
    for _ in range(90):
        try:
            assert ssh(lab, 'hostname', text=True).stdout.strip() == 'nex-lab'
            break
        except subprocess.CalledProcessError:
            if proc.poll() is not None:
                raise RuntimeError((lab / 'qemu.log').read_text())
            time.sleep(1)
    else:
        proc.terminate()
        raise RuntimeError('copied lab SSH did not start')
    ssh(lab, 'systemctl stop nex-core-local nex-core-agent 2>/dev/null || true', text=True)
    # These are only domains in this new copy, never original lab or workstation VMs.
    ssh(lab, "for id in $(virsh -c qemu:///system list --uuid); do virsh -c qemu:///system destroy \"$id\"; done", text=True)
    baseline = ssh(lab, 'uname -a; cat /etc/os-release; test -c /dev/kvm; virsh -c qemu:///system list --all; virsh -c qemu:///system net-list --all; mkdir -p /var/lib/nex/managed-qa; df -h /var/lib/nex/managed-qa', text=True).stdout
    (lab / 'baseline.txt').write_text(baseline)
    print(baseline)
    return lab


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--copy-from', type=Path, required=True)
    args = parser.parse_args()
    prepare(args.copy_from)
