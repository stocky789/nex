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


def lab_disks(lab):
    """This lab's target files: host.raw, then host2.raw, host3.raw, ..."""
    return [lab / 'host.raw'] + sorted(lab.glob('host[0-9].raw'))


def validate_target(path):
    lab = validate_lab(path)
    blocks = qmp(lab / 'qmp.sock', 'query-block')
    disks = [b.get('inserted', {}) for b in blocks if b.get('device', '').startswith('hostdisk')]
    expected = {str(d) for d in lab_disks(lab)}
    assert disks and all(d.get('drv') == 'raw' and d.get('file') in expected for d in disks), \
        'running QEMU must use only this new raw lab\'s targets'
    return lab


def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def prepare(evidence, memory_mib=12288, vcpus=4, disks=1):
    lab = Path(tempfile.mkdtemp(prefix='nex-fresh-install.', dir=ROOT / '.cache'))
    for index in range(disks):
        disk = lab / ('host.raw' if index == 0 else f'host{index + 1}.raw')
        with disk.open('xb') as f:
            f.truncate(128 * 1024**3)
        assert disk.stat().st_blocks == 0
        with disk.open('rb') as f:
            assert f.read(1024 * 1024) == bytes(1024 * 1024)
    disk = lab / 'host.raw'
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
        'guest_iso': str(ROOT / '.cache/nex-iso/debian-13.6.0-amd64-netinst.iso'),
        'memory_mib': memory_mib, 'vcpus': vcpus, 'disks': disks}
    (lab / 'config.json').write_text(json.dumps(config, indent=2) + '\n')
    evidence.mkdir(parents=True, exist_ok=True)
    safe_info = {k: v for k, v in info.items() if k != 'filename'}
    (evidence / 'blank-disk.json').write_text(json.dumps({'creation': 'Exclusive new regular-file creation (xb), sparse truncate; no old disk or backing image',
        'allocated_blocks_before_boot': disk.stat().st_blocks, 'image_info': safe_info,
        'memory_mib': memory_mib, 'vcpus': vcpus, 'network': 'isolated QEMU user networking; all forwards bind127.0.0.1; one virtio NIC; installed br0 bridges only inside guest'}, indent=2) + '\n')
    print(lab)
    return lab


OVMF_CODE_CANDIDATES = (
    Path('/usr/share/edk2/x64/OVMF_CODE.4m.fd'),
    Path('/usr/share/OVMF/OVMF_CODE_4M.ms.fd'),
    Path('/usr/share/OVMF/OVMF_CODE_4M.fd'),
)


def resolve_ovmf(code=None):
    """Return (CODE path, VARS path) for UEFI lab boots, or raise with a clear prerequisite."""
    resolved = Path(code) if code else next((p for p in OVMF_CODE_CANDIDATES if p.exists()), None)
    if not resolved:
        raise RuntimeError('OVMF 4M firmware not found; UEFI has not been tested')
    resolved = resolved.resolve(strict=True)
    variables = resolved.with_name(resolved.name.replace('CODE', 'VARS'))
    if not variables.is_file():
        raise RuntimeError(f'OVMF VARS file not found beside {resolved}; UEFI has not been tested')
    return resolved, variables


def start(lab, iso, evidence, installed=False, uefi=False, detach=(), uefi_code=None):
    lab = validate_lab(lab)
    config = json.loads((lab / 'config.json').read_text())
    p = config['ports']
    command = ['qemu-system-x86_64', '-accel', 'kvm', '-cpu', 'host', '-m', str(config.get('memory_mib', 12288)), '-smp', str(config.get('vcpus', 4)),
        '-display', 'none', '-monitor', 'none', '-serial', f'file:{lab}/serial.log',
        '-qmp', f'unix:{lab}/qmp.sock,server=on,wait=off', '-vga', 'none', '-device', 'VGA,xres=800,yres=600',
        '-device', 'qemu-xhci,id=xhci', '-device', 'usb-tablet,bus=xhci.0',
        '-netdev', f'user,id=n0,hostfwd=tcp:127.0.0.1:{p["ssh"]}-10.0.2.15:22,hostfwd=tcp:127.0.0.1:{p["core"]}-10.0.2.15:9443,hostfwd=tcp:127.0.0.1:{p["controller"]}-10.0.2.20:8443',
        '-device', 'virtio-net-pci,netdev=n0,mac=52:54:00:91:20:15', '-no-reboot']
    # Every prepared target, minus any the caller detached to prove the host still boots.
    for index, disk in enumerate(lab_disks(lab)):
        if disk.name in detach:
            continue
        command.extend(['-drive', f'file={disk},format=raw,if=none,id=hostdisk{index}',
            '-device', f'virtio-blk-pci,drive=hostdisk{index},serial=NEX-FRESH-QA-ONLY-{index},bootindex={index + 2}'])
    if uefi:
        ovmf_code, ovmf_vars = resolve_ovmf(uefi_code)
        firmware = lab / 'OVMF_VARS.fd'
        if not firmware.exists():
            firmware.write_bytes(ovmf_vars.read_bytes())
        command.extend(['-drive', f'if=pflash,format=raw,readonly=on,file={ovmf_code}',
            '-drive', f'if=pflash,format=raw,file={firmware}',
            '-machine', 'q35'])
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
    p.add_argument('--uefi', action='store_true', help='boot this lab through OVMF instead of the SeaBIOS default')
    p.add_argument('--uefi-code', type=Path, help='OVMF CODE file; matching VARS file must be beside it')
    p.add_argument('--detach', default='', help='comma-separated lab disk files to leave unattached, e.g. host.raw')
    p.add_argument('--disks', type=int, default=1, help='blank target disks to create at --prepare')
    p.add_argument('--memory-mib', type=int, default=12288, help='lab guest RAM, recorded in config.json at --prepare')
    p.add_argument('--vcpus', type=int, default=4, help='lab guest vCPUs, recorded in config.json at --prepare')
    p.add_argument('--evidence', type=Path, help='new evidence directory; required to prepare or boot the installer, never the committed docs/qa evidence')
    args = p.parse_args()
    if (args.prepare or not (args.poweroff or args.installed)) and not args.evidence:
        p.error('--evidence is required')
    if args.prepare:
        prepare(args.evidence, args.memory_mib, args.vcpus, args.disks)
    elif args.poweroff:
        lab = validate_target(args.lab_dir)
        qmp(lab / 'qmp.sock', 'system_powerdown')
        print('Requested graceful poweroff of ' + lab.name)
    else:
        assert args.lab_dir and (args.iso or args.installed)
        start(args.lab_dir, args.iso, args.evidence, args.installed, args.uefi,
              tuple(d for d in args.detach.split(',') if d), args.uefi_code)
