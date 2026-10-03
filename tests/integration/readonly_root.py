#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Boot a disposable Debian 13 lab host with the read-only classic layout and check it.

The lab is a simulation of an installed host, not an installer run: a Debian trixie
root file system (built in a container) and a separate /var become two raw ext4 disks that
QEMU boots directly with -kernel/-initrd. The initramfs script, initramfs hook and
nex-root-ro.service under test are the repository's own files; the rest (kernel, systemd,
dpkg) is stock Debian. A lab-only deb exercises dpkg through the real rootfs.OpenWritable
code inside the same sandbox nex-core-local.service has. Bootloader, d-i partitioning and
the Nex packages are NOT involved.

Everything private stays under .cache; QEMU uses no network and only the lab's raw files.
Needs docker, qemu-system-x86_64 (KVM preferred), go.
"""
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
LAB = ROOT / '.cache/ro-classic-lab'
ROOT_UUID = '11111111-2222-4333-8444-555555555555'
VAR_UUID = '66666666-7777-4888-9999-aaaaaaaaaaaa'
# The mirror scenario: two md RAID1 arrays (/ and /var) over two disks, with fixed identities so the
# initramfs the image was built with can assemble them.
MIRROR_ROOT_UUID = '2222aaaa-3333-4444-8555-666666666666'
MIRROR_VAR_UUID = '7777bbbb-8888-4999-8aaa-bbbbbbbbbbbb'
MD0_UUID = '10000000:20000000:30000000:40000000'
MD1_UUID = '50000000:60000000:70000000:80000000'

DOCKERFILE = '''FROM debian:trixie-slim
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update -qq && apt-get install -y -qq --no-install-recommends \\
    systemd-sysv udev kmod util-linux e2fsprogs initramfs-tools iproute2 procps dpkg-dev dbus mdadm fdisk >/dev/null
RUN printf 'HOMEHOST <ignore>\\nARRAY /dev/md0 metadata=1.2 UUID=%(md0)s\\nARRAY /dev/md1 metadata=1.2 UUID=%(md1)s\\n' > /etc/mdadm/mdadm.conf
RUN sed -i 's/^MODULES=.*/MODULES=most/' /etc/initramfs-tools/initramfs.conf
COPY initramfs-local-bottom /usr/share/initramfs-tools/scripts/local-bottom/nex-root-state
COPY initramfs-hook /usr/share/initramfs-tools/hooks/nex-root-state
COPY nex-root-ro.service /usr/lib/systemd/system/nex-root-ro.service
RUN chmod 755 /usr/share/initramfs-tools/scripts/local-bottom/nex-root-state /usr/share/initramfs-tools/hooks/nex-root-state \\
 && systemctl enable nex-root-ro.service
# Kernel last, so the initramfs it builds contains the script and its hook.
RUN apt-get install -y -qq --no-install-recommends linux-image-amd64 >/dev/null
COPY lab-window /usr/local/bin/lab-window
COPY lab-probe.sh /usr/local/bin/lab-probe
COPY lab-probe.service /usr/lib/systemd/system/lab-probe.service
RUN chmod 755 /usr/local/bin/lab-window /usr/local/bin/lab-probe && systemctl enable lab-probe.service \\
 && mkdir -p /tmp/deb/DEBIAN /tmp/deb/usr/share/nex-lab \\
 && echo from-dpkg > /tmp/deb/usr/share/nex-lab/payload \\
 && printf 'Package: nex-lab\\nVersion: 1\\nArchitecture: all\\nMaintainer: lab\\nDescription: lab\\n' > /tmp/deb/DEBIAN/control \\
 && printf '#!/bin/sh\\necho postinst > /boot/nex-lab-postinst\\necho conf > /etc/nex-lab-from-dpkg\\n' > /tmp/deb/DEBIAN/postinst \\
 && chmod 755 /tmp/deb/DEBIAN/postinst && dpkg-deb --build /tmp/deb /usr/local/share/nex-lab_1_all.deb >/dev/null
RUN passwd -d root
'''

PROBE_UNIT = '''[Unit]
Description=Lab probe
After=multi-user.target nex-root-ro.service
[Service]
Type=oneshot
ExecStart=/usr/local/bin/lab-probe
[Install]
WantedBy=multi-user.target
'''

PROBE = r'''#!/bin/sh
out=/dev/ttyS0
say() { echo "NEXLAB: $*" > $out; }
check() { d=$1; shift; if "$@" >/dev/null 2>&1; then say "PASS $d"; else say "FAIL $d"; fi; }
refuse() { d=$1; shift; if "$@" >/dev/null 2>&1; then say "FAIL $d"; else say "PASS $d"; fi; }
mkdir -p /var/lab
phase=$(cat /var/lab/phase 2>/dev/null || echo 1)
mode=$(tr ' ' '\n' < /proc/cmdline | sed -n 's/^lab=//p')
say "boot phase=$phase mode=$mode kernel=$(uname -r)"
say "/ : $(findmnt -no FSTYPE,OPTIONS /)"
for m in /etc /root /var; do say "$m : $(findmnt -no SOURCE,OPTIONS $m | head -1)"; done
say "nex-root-ro.service: $(systemctl is-active nex-root-ro.service)"
finish() { sync; say "DONE"; poweroff; exit 0; }
if [ "$mode" = mkmirror ]; then
    # What the installer's partman recipe produces: GPT with / and /var members, two RAID1 arrays.
    trap 'say "FAIL the mirror could not be built"; poweroff -f' EXIT
    exec 2>$out; set -ex
    for d in /dev/vdb /dev/vdc; do printf 'label: gpt\n,2G\n,\n' | sfdisk -q $d; done
    udevadm settle
    mdadm --create /dev/md0 --run --level=1 --raid-devices=2 --metadata=1.2 --uuid=@MD0@ --name=nexroot /dev/vdb1 /dev/vdc1
    mdadm --create /dev/md1 --run --level=1 --raid-devices=2 --metadata=1.2 --uuid=@MD1@ --name=nexvar /dev/vdb2 /dev/vdc2
    mkfs.ext4 -q -U @MROOT@ /dev/md0
    mkfs.ext4 -q -U @MVAR@ /dev/md1
    mkdir /mnt/r && mount /dev/md0 /mnt/r && mkdir /mnt/r/var && mount /dev/md1 /mnt/r/var
    tar -C / --one-file-system --exclude=./var -cf - . | tar -C /mnt/r -xf -
    tar -C /var -cf - . | tar -C /mnt/r/var -xf -
    rm -rf /mnt/r/var/lab
    printf 'UUID=@MROOT@ / ext4 errors=remount-ro 0 1\nUUID=@MVAR@ /var ext4 defaults 0 2\n' > /mnt/r/etc/fstab
    sync; umount /mnt/r/var /mnt/r; mdadm --stop /dev/md0 /dev/md1
    trap - EXIT; set +x
    say "PASS built a two-array mirror (md0 /, md1 /var)"
    finish
fi
if [ "$mode" = mirror ] || [ "$mode" = degraded ]; then
    check "$mode: / is the md0 array" sh -c 'findmnt -no SOURCE / | grep -qx /dev/md0'
    check "$mode: /var is the md1 array" sh -c 'findmnt -no SOURCE /var | grep -qx /dev/md1'
    check "$mode: /etc comes from /var on md1" sh -c 'findmnt -no SOURCE /etc | grep -q "^/dev/md1\["'
fi
if [ "$mode" = mirror ]; then
    check "mirror: both arrays have both members" sh -c '[ "$(grep -c "\[UU\]" /proc/mdstat)" = 2 ]'
fi
if [ "$mode" = degraded ]; then
    check "degraded: both arrays run on one member" sh -c '[ "$(grep -c "\[U_\]\|\[_U\]" /proc/mdstat)" = 2 ]'
fi
if [ "$mode" = classic ]; then
    refuse "classic: /etc is not a mount" mountpoint -q /etc
    refuse "classic: /var is not a mount" mountpoint -q /var
    check "classic: / stays writable" touch /usr/lab-classic
    finish
fi
if [ "$phase" = 1 ]; then
    check "/ is mounted read-only" sh -c 'findmnt -no OPTIONS / | tr "," "\n" | grep -qx ro'
    refuse "/usr is read-only" touch /usr/lab-x
    refuse "/ is read-only" touch /lab-x
    refuse "/boot is read-only" touch /boot/lab-x
    check "/etc is a mount of /var/lib/nex/etc" sh -c 'findmnt -no SOURCE /etc | grep -q "/lib/nex/etc\]"'
    check "/root is a mount of /var/roothome" sh -c 'findmnt -no SOURCE /root | grep -q "/roothome\]"'
    check "/etc is writable" sh -c 'echo etc > /etc/nex-lab-marker'
    check "/root is writable" sh -c 'echo root > /root/lab-marker'
    check "/var is writable" sh -c 'echo var > /var/lab/marker'
    mkdir -p /tmp/rootview && mount --bind / /tmp/rootview
    refuse "the root file system's own /etc did not receive the edit" test -e /tmp/rootview/etc/nex-lab-marker
    check "the seeded /etc carries the image's fstab" test -e /var/lib/nex/etc/fstab
    umount /tmp/rootview
    refuse "plain dpkg -i cannot write the read-only root" dpkg -i /usr/local/share/nex-lab_1_all.deb
    check "a root shell can open and close the window by hand" sh -c 'mount -o remount,rw / && touch /usr/lab-w && rm /usr/lab-w; r=$?; mount -o remount,ro /; exit $r'
    refuse "/usr is read-only again after the manual window" touch /usr/lab-v
    # Same hardening as nex-core-local.service, running dpkg through rootfs.OpenWritable.
    check "dpkg -i succeeds inside the root window" systemd-run --wait --collect --pipe \
        -p PrivateTmp=yes -p ProtectKernelTunables=yes -p ProtectKernelModules=yes \
        -p ProtectControlGroups=yes -p NoNewPrivileges=yes -p RestrictSUIDSGID=yes -p LockPersonality=yes \
        -p ProtectHome=yes /usr/local/bin/lab-window dpkg -i /usr/local/share/nex-lab_1_all.deb
    check "package payload landed in /usr" test -e /usr/share/nex-lab/payload
    check "postinst wrote /boot" test -e /boot/nex-lab-postinst
    check "postinst wrote /etc" test -e /etc/nex-lab-from-dpkg
    refuse "/usr is read-only again after the window" touch /usr/lab-y
    check "/ is still mounted read-only after the window" sh -c 'findmnt -no OPTIONS / | tr "," "\n" | grep -qx ro'
    echo 2 > /var/lab/phase
else
    check "/ is mounted read-only after reboot" sh -c 'findmnt -no OPTIONS / | tr "," "\n" | grep -qx ro'
    check "/etc edit survived reboot" test "$(cat /etc/nex-lab-marker)" = etc
    check "/root file survived reboot" test "$(cat /root/lab-marker)" = root
    check "/var data survived reboot" test "$(cat /var/lab/marker)" = var
    check "package payload survived reboot" test -e /usr/share/nex-lab/payload
    check "dpkg database survived reboot" sh -c 'dpkg -s nex-lab | grep -q "Status: install ok installed"'
    check "dpkg's /etc change was not reseeded away" test -e /etc/nex-lab-from-dpkg
    refuse "/usr read-only after reboot" touch /usr/lab-z
fi
finish
'''

LAB_WINDOW = '''package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"nex.local/nex/internal/rootfs"
)

// Run a command inside rootfs.OpenWritable, the way nexupdate runs dpkg and apt.
func main() {
	restore, err := rootfs.OpenWritable(context.Background(), func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	})
	if err != nil {
		fmt.Println("window error:", err)
		os.Exit(3)
	}
	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	err = cmd.Run()
	restore()
	if err != nil {
		fmt.Println("command error:", err)
		os.Exit(4)
	}
}
'''

# Runs in a container as root: unpack the image into two ext4 disks, plus a classic control
# whose fstab has no separate /var.
BUILD_DISKS = '''set -eu
mkdir /r && tar -x -C /r
cp /r/boot/vmlinuz-* /lab/kernel; cp /r/boot/initrd.img-* /lab/initrd
printf "UUID=%(root)s / ext4 errors=remount-ro 0 1\\nUUID=%(var)s /var ext4 defaults 0 2\\n" > /r/etc/fstab
mkdir -p /v && cp -a /r/var/. /v/ && rm -rf /r/var/*
truncate -s 3G /lab/root.img && mkfs.ext4 -q -F -U %(root)s -d /r /lab/root.img
truncate -s 1G /lab/var.img && mkfs.ext4 -q -F -U %(var)s -d /v /lab/var.img
cp --sparse=always /lab/root.img /lab/classic.img
printf "UUID=%(root)s / ext4 errors=remount-ro 0 1\\n" > /tmp/fstab.classic
debugfs -w -R "rm /etc/fstab" /lab/classic.img >/dev/null && debugfs -w -R "write /tmp/fstab.classic /etc/fstab" /lab/classic.img >/dev/null
chown %(uid)d:%(gid)d /lab/*.img /lab/kernel /lab/initrd
'''


def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def build_lab():
    LAB.mkdir(parents=True, exist_ok=True)
    (LAB / 'lab-window-src').mkdir(exist_ok=True)
    (LAB / 'Dockerfile').write_text(DOCKERFILE % {'md0': MD0_UUID, 'md1': MD1_UUID})
    probe = PROBE
    for placeholder, value in (('@MD0@', MD0_UUID), ('@MD1@', MD1_UUID), ('@MROOT@', MIRROR_ROOT_UUID), ('@MVAR@', MIRROR_VAR_UUID)):
        probe = probe.replace(placeholder, value)
    (LAB / 'lab-probe.sh').write_text(probe)
    (LAB / 'lab-probe.service').write_text(PROBE_UNIT)
    for name, source in (('initramfs-local-bottom', 'initramfs-local-bottom'), ('initramfs-hook', 'initramfs-hook'),
                         ('nex-root-ro.service', 'nex-root-ro.service')):
        shutil.copyfile(ROOT / 'packaging/core-local' / source, LAB / name)
    # The helper must live inside the module to import internal/rootfs; the lab dir is the one place for it.
    (LAB / 'lab-window-src/main.go').write_text(LAB_WINDOW)
    env = dict(os.environ, CGO_ENABLED='0')
    run('go', 'build', '-o', str(LAB / 'lab-window'), './' + str((LAB / 'lab-window-src').relative_to(ROOT)), cwd=ROOT, env=env)
    run('docker', 'build', '-q', '-t', 'nex-ro-lab', str(LAB), stdout=subprocess.DEVNULL)
    subprocess.run(['docker', 'rm', '-f', 'nex-ro-lab-export'], capture_output=True)
    run('docker', 'create', '--name', 'nex-ro-lab-export', 'nex-ro-lab', stdout=subprocess.DEVNULL)
    try:
        export = subprocess.Popen(['docker', 'export', 'nex-ro-lab-export'], stdout=subprocess.PIPE)
        script = BUILD_DISKS % {'root': ROOT_UUID, 'var': VAR_UUID, 'uid': os.getuid(), 'gid': os.getgid()}
        run('docker', 'run', '-i', '--rm', '-v', f'{LAB}:/lab', 'nex-ro-lab', 'sh', '-ec', script, stdin=export.stdout)
        assert export.wait() == 0
    finally:
        subprocess.run(['docker', 'rm', 'nex-ro-lab-export'], capture_output=True)


def make_mirror_disks():
    for name in ('m1.img', 'm2.img'):
        (LAB / name).unlink(missing_ok=True)
        with (LAB / name).open('xb') as disk:
            disk.truncate(4 * 1024**3)
    shutil.copyfile(LAB / 'classic.img', LAB / 'builder.img')


def boot(root_image, mode, root_uuid=ROOT_UUID, disks=('var.img',)):
    """Boot once; return the probe's NEXLAB lines (QEMU stops itself when the probe powers off)."""
    log = LAB / f'serial-{root_image}-{mode}.log'
    log.unlink(missing_ok=True)
    cmd = ['timeout', '240', 'qemu-system-x86_64', '-machine', 'q35', '-accel', 'kvm', '-accel', 'tcg', '-m', '2048', '-smp', '2',
           '-display', 'none', '-serial', f'file:{log}', '-monitor', 'none', '-nic', 'none', '-no-reboot',
           '-kernel', str(LAB / 'kernel'), '-initrd', str(LAB / 'initrd'),
           '-append', f'root=UUID={root_uuid} ro console=ttyS0 lab={mode}']
    for image in (root_image, *disks):
        cmd += ['-drive', f'file={LAB / image},format=raw,if=virtio']
    run(*cmd)
    text = log.read_bytes().decode(errors='replace')
    # The initramfs is where a failure to prepare /etc and /root would be reported.
    assert 'Could not prepare /etc and /root' not in text or mode in ('classic', 'mkmirror'), 'initramfs script fell back'
    return [m.group(1).strip() for m in re.finditer(r'NEXLAB: (.*?)\r?\n', text)]


def main():
    for tool in ('docker', 'qemu-system-x86_64', 'go'):
        if not shutil.which(tool):
            print(f'SKIP: {tool} is not installed; the read-only root lab was not run')
            return 0
    build_lab()
    make_mirror_disks()
    failures = 0
    scenarios = (('root.img', 'ro', ROOT_UUID, ('var.img',)), ('root.img', 'ro', ROOT_UUID, ('var.img',)),
                 ('classic.img', 'classic', ROOT_UUID, ()),
                 ('builder.img', 'mkmirror', ROOT_UUID, ('m1.img', 'm2.img')),
                 ('m1.img', 'mirror', MIRROR_ROOT_UUID, ('m2.img',)), ('m1.img', 'mirror', MIRROR_ROOT_UUID, ('m2.img',)),
                 # One disk gone: both arrays start degraded, /var is still mounted and /etc still bound.
                 ('m1.img', 'degraded', MIRROR_ROOT_UUID, ()))
    for image, mode, root_uuid, disks in scenarios:
        lines = boot(image, mode, root_uuid, disks)
        assert lines and lines[-1] == 'DONE', f'{image}/{mode}: the probe did not finish: {lines}'
        for line in lines:
            print(line)
            failures += line.startswith('FAIL')
        # A lab that checked nothing must not pass.
        assert sum(line.startswith('PASS') for line in lines) >= (1 if mode == 'mkmirror' else 3), lines
    if failures:
        print(f'FAIL: {failures} read-only root check(s) failed')
        return 1
    print('PASS: read-only root, persistent /etc and /root, dpkg window and reboot persistence (simulated lab host, not a d-i install)')
    return 0


if __name__ == '__main__':
    sys.exit(main())
