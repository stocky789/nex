#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Capture both real ISO menu selections, with keyboard navigation, in diskless QEMU.

python3 tests/integration/installer-modes.py ISO --output NEW_DIRECTORY
Requires QEMU/KVM and OVMF. Inspect PPM captures; capture success is not UI acceptance.
"""
import argparse
import json
from pathlib import Path
import runpy
import shutil
import subprocess
import time

qmp = runpy.run_path(str(Path(__file__).with_name('iso-smoke.py')))['qmp']


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('iso', type=Path)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--uefi-code', type=Path, default=Path('/usr/share/OVMF/OVMF_CODE_4M.fd'))
    args = parser.parse_args()
    iso = args.iso.resolve(strict=True)
    assert iso.is_file(), 'ISO must be a regular file'
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    for firmware in ('bios', 'uefi'):
        for mode in ('gui', 'minimal'):
            prefix = output / f'{firmware}-{mode}'
            sock = prefix.with_suffix('.sock')
            command = ['qemu-system-x86_64', '-accel', 'tcg' if firmware == 'uefi' else 'kvm',
                       '-cpu', 'max' if firmware == 'uefi' else 'host', '-m', '4096',
                       '-smp', '2', '-display', 'none', '-monitor', 'none', '-serial', 'none',
                       '-qmp', f'unix:{sock},server=on,wait=off', '-nic', 'none',
                       '-cdrom', str(iso), '-boot', 'd']
            if firmware == 'uefi':
                variables = prefix.with_suffix('.vars.fd')
                shutil.copyfile(args.uefi_code.with_name('OVMF_VARS_4M.fd'), variables)
                command += ['-drive', f'if=pflash,format=raw,readonly=on,file={args.uefi_code}',
                            '-drive', f'if=pflash,format=raw,file={variables}']
            prefix.with_suffix('.command.json').write_text(json.dumps(command, indent=2))
            with prefix.with_suffix('.log').open('w') as log:
                process = subprocess.Popen(command, stdout=log, stderr=log)
                try:
                    def capture(stage):
                        assert process.poll() is None, f'QEMU exited: {prefix}'
                        qmp(sock, 'screendump', {'filename': f'{prefix}-{stage}.ppm'})

                    def key(code):
                        qmp(sock, 'send-key', {'keys': [{'type': 'qcode', 'data': code}]})
                        time.sleep(0.3)

                    time.sleep(20)
                    capture('menu')
                    if mode == 'minimal':
                        key('down')
                    key('ret')
                    time.sleep(35)
                    capture('language')
                    key('ret')
                    time.sleep(3)
                    capture('location')
                finally:
                    process.terminate()
                    process.wait(timeout=15)
                    sock.unlink(missing_ok=True)
    print(f'Captured BIOS/UEFI GUI/minimal selections in {output}; inspect all screens.')


if __name__ == '__main__':
    main()
