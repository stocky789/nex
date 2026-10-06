#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Disposable QEMU boot-menu smoke: the media reaches its boot menu on BIOS and on UEFI firmware.

python3 tests/integration/iso-smoke.py ISO --output /tmp/nex-boot-UNIQUE
Output must not already exist. Needs QEMU. Installing from the media is exercised by
tests/integration/image_iso_lab.py and image_install_lab.py. Disks are never created or attached.
"""
import argparse
import json
from pathlib import Path
import shutil
import socket
import subprocess
import time


def drawn(ppm):
    """True when a binary PPM screendump shows anything but black."""
    data = ppm.read_bytes()
    header = data.split(b'\n', 3)
    return len(header) == 4 and header[0] == b'P6' and any(header[3])


def qmp(path, command, arguments=None):
    with socket.socket(socket.AF_UNIX) as sock:
        sock.settimeout(10)
        sock.connect(str(path))
        stream = sock.makefile('rwb')
        json.loads(stream.readline())
        for request in [{'execute': 'qmp_capabilities'}, {'execute': command, 'arguments': arguments or {}}]:
            stream.write(json.dumps(request).encode() + b'\n')
            stream.flush()
            while True:
                reply = json.loads(stream.readline())
                if 'error' in reply:
                    raise RuntimeError(reply)
                if 'return' in reply:
                    break
        return reply['return']


def run_vm(args, label, extra, seconds=15):
    accel = args.accel
    qmp_path = args.output / f'{label}.sock'
    serial = args.output / f'{label}.serial.log'
    command = ['qemu-system-x86_64', '-accel', accel, '-cpu', 'host' if accel == 'kvm' else 'max', '-m', '3072', '-smp', '2',
               '-display', 'none', '-monitor', 'none', '-serial', f'file:{serial}',
               '-qmp', f'unix:{qmp_path},server=on,wait=off',
               '-nic', 'none', '-no-reboot', *extra]
    (args.output / f'{label}.command.json').write_text(json.dumps(command, indent=2))
    with (args.output / f'{label}.qemu.log').open('w') as log:
        process = subprocess.Popen(command, stdout=log, stderr=log)
        try:
            deadline = time.monotonic() + seconds * (4 if accel == 'tcg' else 1)
            next_capture = time.monotonic() + 8
            while process.poll() is None and time.monotonic() < deadline:
                time.sleep(1)
                if time.monotonic() >= next_capture and qmp_path.exists():
                    qmp(qmp_path, 'screendump', {'filename': str(args.output / f'{label}.ppm')})
                    next_capture = time.monotonic() + 30
            if process.poll() is not None:
                raise RuntimeError(f'{label} exited before capture; inspect QEMU log')
            shot = args.output / f'{label}.ppm'
            qmp(qmp_path, 'screendump', {'filename': str(shot)})
            if not drawn(shot):
                raise RuntimeError(f'{label}: the screen is blank, so the ISO never reached a boot menu; inspect {shot}')
        finally:
            if process.poll() is None:
                process.terminate()
                process.wait(timeout=15)
            qmp_path.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('iso', type=Path)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--uefi-code', type=Path, help='OVMF CODE file; matching VARS file must be beside it')
    parser.add_argument('--accel', choices=['kvm', 'tcg'], default='kvm', help='QEMU accelerator for every guest (tcg when the host has no /dev/kvm)')
    args = parser.parse_args()
    args.iso = args.iso.resolve(strict=True)
    if not args.iso.is_file():
        parser.error('ISO must be a regular file')
    args.output = args.output.resolve()
    args.output.mkdir(parents=True, exist_ok=False)
    iso = ['-cdrom', str(args.iso), '-boot', 'd']
    run_vm(args, 'bios-menu', iso)
    code = args.uefi_code or next((p for p in [Path('/usr/share/edk2/x64/OVMF_CODE.4m.fd'), Path('/usr/share/OVMF/OVMF_CODE_4M.ms.fd'), Path('/usr/share/OVMF/OVMF_CODE_4M.fd')] if p.exists()), None)
    if not code:
        raise RuntimeError('OVMF 4M firmware not found; UEFI has not been tested')
    source_vars = code.with_name(code.name.replace('CODE', 'VARS'))
    nvram = args.output / 'uefi-vars.fd'
    shutil.copyfile(source_vars, nvram)
    code = code.resolve(strict=True)
    uefi = ['-machine', 'q35,smm=on', '-drive', f'if=pflash,format=raw,readonly=on,file={code}', '-drive', f'if=pflash,format=raw,file={nvram}']
    run_vm(args, 'uefi-menu', [*uefi, *iso], seconds=60)
    print(f'QEMU checks completed; inspect captures and logs at {args.output}. Secure Boot and physical hardware are not covered.')


if __name__ == '__main__':
    main()
