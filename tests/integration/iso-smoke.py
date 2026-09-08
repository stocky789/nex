#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Disposable QEMU ISO smoke. Requires QEMU; install mode also needs extracted d-i kernel/initrd.

python3 tests/integration/iso-smoke.py ISO --output /tmp/nex-boot-UNIQUE
python3 tests/integration/iso-smoke.py ISO --output /tmp/nex-install-UNIQUE \
    --kernel VMLINUZ --initrd INITRD.GZ
Output must not already exist. Disks are newly created regular files, never devices.
Test automation and test-only root credentials are never written into the shipping ISO.
"""
import argparse
import gzip
import json
from pathlib import Path
import shutil
import socket
import subprocess
import time


def archive(files):
    """Minimal newc archive for appending test files to Debian's initramfs."""
    data = bytearray()
    for ino, (name, body) in enumerate([*files.items(), ('TRAILER!!!', b'')], 1):
        name = name.encode() + b'\0'
        values = [ino, 0o100644, 0, 0, 1, 0, len(body), 0, 0, 0, 0, len(name), 0]
        data.extend(b'070701' + ''.join(f'{n:08x}' for n in values).encode() + name)
        data.extend(b'\0' * (-len(data) % 4))
        data.extend(body)
        data.extend(b'\0' * (-len(data) % 4))
    return gzip.compress(data)


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


def run_vm(args, label, extra, seconds=15, completion=False, accel='kvm'):
    qmp_path = args.output / f'{label}.sock'
    serial = args.output / f'{label}.serial.log'
    command = ['qemu-system-x86_64', '-accel', accel, '-cpu', 'host' if accel == 'kvm' else 'max', '-m', '3072', '-smp', '2',
               '-display', 'none', '-monitor', 'none', '-serial', f'file:{serial}',
               '-qmp', f'unix:{qmp_path},server=on,wait=off', '-nic', 'none', '-no-reboot', *extra]
    (args.output / f'{label}.command.json').write_text(json.dumps(command, indent=2))
    with (args.output / f'{label}.qemu.log').open('w') as log:
        process = subprocess.Popen(command, stdout=log, stderr=log)
        try:
            deadline = time.monotonic() + seconds
            next_capture = time.monotonic() + 8
            while process.poll() is None and time.monotonic() < deadline:
                time.sleep(1)
                if time.monotonic() >= next_capture and qmp_path.exists():
                    qmp(qmp_path, 'screendump', {'filename': str(args.output / f'{label}.ppm')})
                    next_capture = time.monotonic() + 30
            if completion and process.poll() is None:
                raise RuntimeError(f'{label} timed out; inspect {serial}')
            if completion and process.returncode != 0:
                raise RuntimeError(f'{label} QEMU failed: {process.returncode}')
            if not completion and process.poll() is not None:
                raise RuntimeError(f'{label} exited before capture; inspect QEMU log')
        finally:
            if process.poll() is None:
                process.terminate()
                process.wait(timeout=15)
            qmp_path.unlink(missing_ok=True)


CHECK = r'''#!/bin/sh
exec >/dev/ttyS0 2>&1
set -eu
trap 'echo NEX_SMOKE_FAILED; systemctl poweroff' EXIT
for p in qemu-system-x86 qemu-utils libvirt-daemon-system libvirt-clients virtinst ovmf openssh-server nftables chrony; do
    test "$(dpkg-query -W -f='${Status}' "$p")" = 'install ok installed'
done
systemctl is-active libvirtd ssh chrony
virsh -c qemu:///system list --all
test -c /dev/kvm
check_identity() {
    . /etc/os-release
    test "$ID" = nex-core
    test "$ID_LIKE" = debian
    test "$NAME" = 'Nex Core'
    test -s /etc/debian_version
    ( . /usr/lib/os-release; test "$ID" = debian )
    grep -q Nex /etc/default/grub.d/nex-core.cfg
    grep -q "Nex Core" /boot/grub/grub.cfg
    cat /etc/os-release
    echo NEX_SMOKE_IDENTITY_PASS
}
check_identity
if test -f /var/lib/nex-smoke-marker; then
    test "$(cat /var/lib/nex-smoke-marker)" = persistent
    virsh -c qemu:///system dominfo nex-smoke
    echo NEX_SMOKE_REBOOT_PASS
else
    DEBIAN_FRONTEND=noninteractive dpkg -i /root/base-files_*.deb
    check_identity
    work=/var/lib/libvirt/images/nex-smoke
    mkdir -p "$work/root/bin"
    cp /bin/busybox "$work/root/bin/"
    ldd /bin/busybox | awk '/=> \// {print $3} $1 ~ /^\// {print $1}' | while read -r lib; do
        cp --parents "$lib" "$work/root/"
    done
    cat >"$work/root/init" <<'INIT'
#!/bin/busybox sh
echo NEX_NESTED_KVM_BOOT_PASS
/bin/busybox poweroff -f
INIT
    chmod +x "$work/root/init"
    (cd "$work/root" && find . -print0 | cpio --null -o -H newc | gzip) >"$work/initrd.gz"
    cp /boot/vmlinuz-* "$work/vmlinuz"
    cat >"$work/guest.xml" <<XML
<domain type='kvm'>
  <name>nex-smoke</name><memory unit='MiB'>256</memory><vcpu>1</vcpu>
  <os><type arch='x86_64'>hvm</type><kernel>$work/vmlinuz</kernel><initrd>$work/initrd.gz</initrd><cmdline>console=ttyS0 rdinit=/init panic=-1</cmdline></os>
  <devices><serial type='file'><source path='$work/guest.log'/><target port='0'/></serial></devices>
</domain>
XML
    virsh -c qemu:///system define "$work/guest.xml"
    virsh -c qemu:///system start nex-smoke
    tries=0
    until grep -q NEX_NESTED_KVM_BOOT_PASS "$work/guest.log"; do
        tries=$((tries + 1))
        test "$tries" -lt 60
        sleep 1
    done
    cat "$work/guest.log"
    virsh -c qemu:///system destroy nex-smoke || test "$(virsh -c qemu:///system domstate nex-smoke)" = "shut off"
    echo persistent >/var/lib/nex-smoke-marker
    echo NEX_SMOKE_FIRST_BOOT_PASS
fi
sync
trap - EXIT
systemctl poweroff
'''

PRESEED = '''d-i debian-installer/locale string en_US.UTF-8
d-i keyboard-configuration/xkb-keymap select us
d-i netcfg/enable boolean false
d-i netcfg/get_hostname string nex-smoke
d-i netcfg/get_domain string test.invalid
d-i hw-detect/load_firmware boolean false
d-i passwd/root-password password nex-disposable-test-only
d-i passwd/root-password-again password nex-disposable-test-only
d-i passwd/make-user boolean false
d-i clock-setup/utc boolean true
d-i clock-setup/ntp boolean false
d-i time/zone string UTC
d-i partman-auto/disk string /dev/vda
d-i partman-auto/method string regular
d-i partman-auto/choose_recipe select atomic
d-i partman/confirm_write_new_label boolean true
d-i partman/choose_partition select finish
d-i partman/confirm boolean true
d-i partman/confirm_nooverwrite boolean true
d-i grub-installer/only_debian boolean true
d-i grub-installer/bootdev string /dev/vda
d-i finish-install/reboot_in_progress note
d-i debian-installer/exit/poweroff boolean true
d-i preseed/late_command string /bin/sh /cdrom/nex/late-install.sh && cp /cdrom/nex/repo/base-files_*.deb /target/root/ && cp /nex-smoke.sh /target/root/nex-smoke.sh && cp /nex-smoke.service /target/etc/systemd/system/nex-smoke.service && in-target systemctl enable nex-smoke.service && echo NEX_SMOKE_INSTALL_PASS > /dev/ttyS0
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('iso', type=Path)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--kernel', type=Path)
    parser.add_argument('--initrd', type=Path)
    parser.add_argument('--uefi-code', type=Path, help='OVMF CODE file; matching VARS file must be beside it')
    parser.add_argument('--uefi-accel', choices=['kvm', 'tcg'], default='kvm')
    args = parser.parse_args()
    args.iso = args.iso.resolve(strict=True)
    if not args.iso.is_file():
        parser.error('ISO must be a regular file')
    if bool(args.kernel) != bool(args.initrd):
        parser.error('--kernel and --initrd must be used together')
    args.output = args.output.resolve()
    args.output.mkdir(parents=True, exist_ok=False)
    iso = ['-cdrom', str(args.iso), '-boot', 'd']
    run_vm(args, 'bios-menu', iso)
    code = args.uefi_code or next((p for p in [Path('/usr/share/edk2/x64/OVMF_CODE.4m.fd'), Path('/usr/share/OVMF/OVMF_CODE_4M.fd')] if p.exists()), None)
    if not code:
        raise RuntimeError('OVMF 4M firmware not found; UEFI has not been tested')
    source_vars = code.with_name(code.name.replace('CODE', 'VARS'))
    nvram = args.output / 'uefi-vars.fd'
    shutil.copyfile(source_vars, nvram)
    code = code.resolve(strict=True)
    uefi = ['-machine', 'q35,smm=on', '-drive', f'if=pflash,format=raw,readonly=on,file={code}', '-drive', f'if=pflash,format=raw,file={nvram}']
    run_vm(args, 'uefi-menu', [*uefi, *iso], seconds=60, accel=args.uefi_accel)
    if args.kernel:
        disk = args.output / 'disposable.raw'
        with disk.open('xb') as handle:
            handle.truncate(24 * 1024**3)
        drives = ['-drive', f'file={disk},format=raw,if=virtio']
        shipping = Path('installer/preseed.cfg').read_text()
        shipping = '\n'.join(line for line in shipping.splitlines() if not line.startswith('d-i preseed/late_command'))
        initrd = args.output / 'test-only-initrd.gz'
        service = '[Unit]\nAfter=libvirtd.service ssh.service chrony.service\nWants=libvirtd.service ssh.service chrony.service\n[Service]\nType=oneshot\nExecStart=/bin/sh /root/nex-smoke.sh\n[Install]\nWantedBy=multi-user.target\n'
        initrd.write_bytes(args.initrd.read_bytes() + archive({'preseed.cfg': (shipping + '\n' + PRESEED).encode(), 'nex-smoke.sh': CHECK.encode(), 'nex-smoke.service': service.encode()}))
        run_vm(args, 'offline-install', [*drives, *iso, '-kernel', str(args.kernel.resolve()), '-initrd', str(initrd), '-append', 'auto=true priority=critical console=ttyS0,115200n8'], seconds=1800, completion=True)
        if 'NEX_SMOKE_INSTALL_PASS' not in (args.output / 'offline-install.serial.log').read_text(errors='replace'):
            raise RuntimeError('Installer did not report success')
        for label, marker in [('installed-first-boot', 'NEX_SMOKE_FIRST_BOOT_PASS'), ('installed-reboot', 'NEX_SMOKE_REBOOT_PASS')]:
            run_vm(args, label, drives, seconds=180, completion=True)
            if marker not in (args.output / f'{label}.serial.log').read_text(errors='replace'):
                raise RuntimeError(f'Missing {marker}; inspect serial log')
    print(f'QEMU checks completed; inspect captures and logs at {args.output}. Secure Boot and physical hardware are not covered.')


if __name__ == '__main__':
    main()
