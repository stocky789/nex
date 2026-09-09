#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Create a new empty disposable Core-install lab, then boot an unmodified local ISO.

Private files stay in .cache. All guest networking uses QEMU user mode with loopback
forwards; no workstation disks, bridges or libvirt services are touched.
"""
import argparse
import hashlib
import json
from pathlib import Path
import runpy
import secrets
import socket
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
qmp = runpy.run_path(str(ROOT / 'tests/integration/iso-smoke.py'))['qmp']


def validate_lab(path):
    path = Path(path)
    assert not path.is_symlink(), 'lab directory must not be a symlink'
    lab = path.resolve(strict=True)
    assert lab.parent == (ROOT / '.cache').resolve() and lab.name.startswith('nex-fresh-install.'), 'selected fresh-install lab must be directly under repository .cache'
    disk, config = lab / 'host.raw', lab / 'config.json'
    assert disk.is_file() and not disk.is_symlink(), 'lab disk must be a real regular file'
    assert config.is_file() and not config.is_symlink(), 'lab config must be a real file'
    assert json.loads(config.read_text()).get('disposable') is True, 'explicit disposable lab required'
    return lab


def validate_target(path):
    lab = validate_lab(path)
    blocks = qmp(lab / 'qmp.sock', 'query-block')
    disks = [b.get('inserted', {}) for b in blocks if b.get('device') == 'hostdisk']
    assert len(disks) == 1 and disks[0].get('drv') == 'raw' and disks[0].get('file') == str(lab / 'host.raw'), 'running QEMU must use this exact new raw lab target'
    return lab


def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def prepare(evidence):
    lab = Path(tempfile.mkdtemp(prefix='nex-fresh-install.', dir=ROOT / '.cache'))
    disk = lab / 'host.raw'
    with disk.open('xb') as f:
        f.truncate(128 * 1024**3)
    assert disk.stat().st_blocks == 0
    with disk.open('rb') as f:
        assert f.read(1024 * 1024) == bytes(1024 * 1024)
    info = {'format': 'raw', 'virtual-size': disk.stat().st_size, 'backing_file': None}
    ports = {key: port() for key in ('ssh', 'core', 'controller', 'vnc')}
    for name in ('root-password', 'controller-password'):
        path = lab / name
        path.write_text(secrets.token_hex(12))
        path.chmod(0o600)
    subprocess.run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-C', 'nex-fresh-qa', '-f', str(lab / 'id_ed25519')], check=True)
    config = {'disposable': True, 'lab_dir': str(lab), 'ports': ports, 'hostname': 'nex-fresh-lab',
        'local_url': f'https://127.0.0.1:{ports["core"]}', 'controller_url': f'https://127.0.0.1:{ports["controller"]}',
        'root_password_file': str(lab / 'root-password'), 'controller_password_file': str(lab / 'controller-password'),
        'controller_address': '10.0.2.20/24', 'gateway': '10.0.2.2', 'dns': '10.0.2.3',
        'guest_iso': str(ROOT / '.cache/nex-iso/debian-13.6.0-amd64-netinst.iso')}
    (lab / 'config.json').write_text(json.dumps(config, indent=2) + '\n')
    evidence.mkdir(parents=True, exist_ok=True)
    safe_info = {k: v for k, v in info.items() if k != 'filename'}
    (evidence / 'blank-disk.json').write_text(json.dumps({'creation': 'Exclusive new regular-file creation (xb), sparse truncate; no old disk or backing image',
        'allocated_blocks_before_boot': disk.stat().st_blocks, 'image_info': safe_info,
        'memory_mib': 12288, 'vcpus': 4, 'network': 'isolated QEMU user networking; all forwards bind127.0.0.1; one virtio NIC; installed br0 bridges only inside guest'}, indent=2) + '\n')
    print(lab)
    return lab


def start(lab, iso, evidence, installed=False):
    lab = validate_lab(lab)
    config = json.loads((lab / 'config.json').read_text())
    p = config['ports']
    command = ['qemu-system-x86_64', '-accel', 'kvm', '-cpu', 'host', '-m', '12288', '-smp', '4',
        '-display', 'none', '-monitor', 'none', '-serial', f'file:{lab}/serial.log',
        '-qmp', f'unix:{lab}/qmp.sock,server=on,wait=off', '-vga', 'none', '-device', 'VGA,xres=800,yres=600',
        '-device', 'qemu-xhci,id=xhci', '-device', 'usb-tablet,bus=xhci.0',
        '-drive', f'file={lab}/host.raw,format=raw,if=none,id=hostdisk',
        '-device', 'virtio-blk-pci,drive=hostdisk,serial=NEX-FRESH-QA-ONLY,bootindex=2',
        '-netdev', f'user,id=n0,hostfwd=tcp:127.0.0.1:{p["ssh"]}-10.0.2.15:22,hostfwd=tcp:127.0.0.1:{p["core"]}-10.0.2.15:9443,hostfwd=tcp:127.0.0.1:{p["controller"]}-10.0.2.20:8443',
        '-device', 'virtio-net-pci,netdev=n0,mac=52:54:00:91:20:15', '-no-reboot']
    if not installed:
        iso = iso.resolve(strict=True)
        command.extend(['-cdrom', str(iso), '-boot', 'd'])
        config['iso'] = str(iso)
        config['iso_sha256'] = hashlib.file_digest(iso.open('rb'), 'sha256').hexdigest()
        (lab / 'config.json').write_text(json.dumps(config, indent=2) + '\n')
        (evidence / 'iso.json').write_text(json.dumps({'file': iso.name, 'sha256': config['iso_sha256'],
            'method': 'Boot full unmodified local ISO; no initrd, preseed or installed disk injection'}, indent=2) + '\n')
    else:
        command.extend(['-boot', 'c'])
        command.remove('-no-reboot')
    (lab / ('installed-command.json' if installed else 'installer-command.json')).write_text(json.dumps(command, indent=2) + '\n')
    with (lab / 'qemu.log').open('a') as log:
        proc = subprocess.Popen(command, stdout=log, stderr=log, start_new_session=True)
    (lab / 'qemu.pid').write_text(str(proc.pid))
    print(f'QEMU PID {proc.pid}, private lab {lab.name}')


def key(lab, *codes):
    for down in (True, False):
        qmp(lab / 'qmp.sock', 'input-send-event', {'events': [
            {'type': 'key', 'data': {'down': down, 'key': {'type': 'qcode', 'data': code}}}
            for code in (codes if down else reversed(codes))]})
        time.sleep(.025)


def type_text(lab, text):
    punctuation = {' ': 'spc', '-': 'minus', '_': 'shift+minus', '.': 'dot', '/': 'slash',
        ':': 'shift+semicolon', ';': 'semicolon', '=': 'equal', '+': 'shift+equal',
        '>': 'shift+dot', '<': 'shift+comma', '|': 'shift+backslash', "'": 'apostrophe',
        '"': 'shift+apostrophe', '&': 'shift+7', '!': 'shift+1', '\n': 'ret'}
    for char in text:
        codes = punctuation.get(char, ('shift+' if char.isupper() else '') + char.lower()).split('+')
        key(lab, *codes)


def click(lab, x, y, width=800, height=600):
    qmp(lab / 'qmp.sock', 'input-send-event', {'events': [
        {'type': 'abs', 'data': {'axis': 'x', 'value': round(x * 32767 / (width - 1))}},
        {'type': 'abs', 'data': {'axis': 'y', 'value': round(y * 32767 / (height - 1))}}]})
    for down in (True, False):
        qmp(lab / 'qmp.sock', 'input-send-event', {'events': [{'type': 'btn', 'data': {'down': down, 'button': 'left'}}]})
        time.sleep(.1)


def capture(lab, path):
    from PIL import Image
    ppm = lab / 'screen.ppm'
    qmp(lab / 'qmp.sock', 'screendump', {'filename': str(ppm)})
    Image.open(ppm).save(path)


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--prepare', action='store_true')
    p.add_argument('--lab-dir', type=Path)
    p.add_argument('--iso', type=Path)
    p.add_argument('--installed', action='store_true')
    p.add_argument('--poweroff', action='store_true', help='request graceful ACPI poweroff of only the validated running fresh lab')
    p.add_argument('--evidence', type=Path, default=ROOT / 'docs/qa/fresh-install')
    args = p.parse_args()
    if args.prepare:
        prepare(args.evidence)
    elif args.poweroff:
        lab = validate_target(args.lab_dir)
        qmp(lab / 'qmp.sock', 'system_powerdown')
        print('Requested graceful poweroff of ' + lab.name)
    else:
        assert args.lab_dir and (args.iso or args.installed)
        start(args.lab_dir, args.iso, args.evidence, args.installed)
