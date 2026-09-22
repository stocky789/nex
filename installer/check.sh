#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Source-level installation safety check; actual offline installation is separate.
set -euo pipefail
cd "$(dirname "$0")/.."
bash -n installer/build.sh
bash -n installer/build-gui.sh
sh -n installer/late-install.sh
sh -n installer/gui/wizard-text.sh
sh -n installer/root-account.sh
python3 - <<'PY'
from pathlib import Path
import re

build = Path('installer/build.sh').read_text()
bios = build.split("<<'BIOS'\n")[2].split('\nBIOS')[0]
grub = build.split("<<'GRUB'\n")[1].split('\nGRUB')[0]


def boot_entry(label):
    """The BIOS and GRUB entries of one boot menu item."""
    return (next(entry for entry in re.split(r'(?m)^label ', bios)[1:]
                 if f'menu label {label}\n' in entry),
            grub.split(f"menuentry '{label}' {{\n")[1].split('}')[0])


# Both Nex modes boot the initrd carrying the Nex overlay, which supplies the wizard
# and its embedded safe seed; only the cdebconf frontend differs. A mode booting the
# untouched Debian initrd would silently install a different host.
for label, frontend in [('GUI install - Nex Core', ''),
                        ('Minimal install - Nex Core (text)', 'newt')]:
    b, g = boot_entry(label)
    assert 'kernel /install.amd/vmlinuz' in b
    assert 'initrd=/install.amd/gtk/initrd.gz' in b
    assert 'linux /install.amd/vmlinuz' in g
    assert 'initrd /install.amd/gtk/initrd.gz' in g
    assert 'preseed/file=' not in b + g, f'{label} uses the initrd seed, not the media file'
    if frontend:
        assert f'DEBIAN_FRONTEND={frontend}' in b and f'DEBIAN_FRONTEND={frontend}' in g
    else:
        assert 'DEBIAN_FRONTEND=' not in b + g, 'the graphical mode selects its own frontend'
    assert 'auto=true' not in b + g
# Serial and rescue both boot the untouched Debian initrd, but only the serial
# entries install: they carry the media seed. Rescue must reach a shell without
# seeding anything, so a preseed argument there would change what it does.
for entry in boot_entry('Install Nex Core (serial console ttyS0)'):
    assert '/install.amd/initrd.gz' in entry and '/install.amd/gtk/initrd.gz' not in entry
    assert 'preseed/file=/cdrom/preseed.cfg' in entry, 'the serial install needs the media seed'
    assert 'console=ttyS0,115200n8' in entry
for entry in boot_entry('Debian rescue mode'):
    assert '/install.amd/initrd.gz' in entry and '/install.amd/gtk/initrd.gz' not in entry
    assert 'preseed' not in entry, 'rescue must boot Debian unseeded'
    assert 'rescue/enable=true' in entry
assert 'timeout 0' in build and 'set timeout=-1' in grub
print('PASS: BIOS/UEFI GUI and minimal text entries, serial media seed, unseeded rescue, no timeout')

# Both wizards write the same handoff and nex-config remains the single validator.
start = Path('installer/gui/start.sh').read_text()
wizard = Path('installer/gui/wizard-text.sh').read_text()
templates = Path('installer/gui/wizard-text.templates').read_text()
gui = Path('installer/build-gui.sh').read_text()
assert '/usr/lib/nex-installer/wizard-text' in start and '/usr/bin/nex-wizard' in start
assert '"$nex_wizard"' in start, 'One setup loop must drive whichever frontend is running'
assert 'nex-config --apply /run/nex-installer/config.ini' in start
assert start.count('nex-config --apply') == 1, 'One apply site for both frontends'
assert "install -m 755 installer/gui/wizard-text.sh" in gui and 'wizard-text.templates' in gui
assert "assert (root / 'usr/lib/cdebconf/frontend/newt.so').is_file()" in gui
assert '/run/nex-installer/config.ini' in wizard and '/usr/bin/nex-config' not in wizard, \
    'The text wizard writes the handoff; the shared setup loop applies it'
assert '/usr/lib/nex-installer/validate-config' in wizard, \
    'The text wizard must reject settings through nex-config, not a second validator'
# These are printed on the raw Linux VT before bterm starts: the wizard and its
# questions, and the rejection messages it and the shared setup loop relay from
# nex-config. (nex-wizard.c is excluded: it only ever draws inside GTK.)
for name in ('wizard-text.sh', 'wizard-text.templates', 'nex-network.c', 'nex-config.c'):
    assert Path('installer/gui', name).read_text().isascii(), \
        f'{name} must stay ASCII for the installer console'
# BUG-0411: both frontends must enumerate adapters in the same order, or the
# same machine gets different nex0..nexN names depending on the installer mode.
assert 'g_ptr_array_sort(names, compare_names)' in Path('installer/gui/nex-wizard.c').read_text(), \
    'The graphical wizard must sort /sys/class/net, like the text wizard glob'
# The text frontend must not weaken the disk, account or network contract.
assert 'MIN_SECTORS=33554432' in wizard
assert 'sd*|vd*|xvd*|nvme*' in wizard
assert '[ -e "$path/partition" ] && continue' in wizard
assert '"$(read_attr "$path/removable")" = 0' in wizard
assert '/cdrom' in wizard, 'Installation media is never offered as a target'
assert 'db_set nex-installer/erase false' in wizard, 'Confirmation must not carry forward'
assert '${#first}" -lt 8' in wizard
for template in ('nex-installer/hostname', 'nex-installer/keymap', 'nex-installer/timezone',
                 'nex-installer/password', 'nex-installer/management', 'nex-installer/mode',
                 'nex-installer/vlan', 'nex-installer/address', 'nex-installer/gateway',
                 'nex-installer/dns', 'nex-installer/disk', 'nex-installer/erase'):
    assert f'Template: {template}\n' in templates, template
    assert template in wizard, template
assert 'Type: boolean' in templates.split('Template: nex-installer/erase\n')[1]
print('PASS: one setup handoff for both installer frontends')

entries = [line.split(None, 3) for line in Path('installer/preseed.cfg').read_text().splitlines()
           if line.strip() and not line.lstrip().startswith('#')]
account_policy = {'passwd/root-login': 'true', 'passwd/make-user': 'false'}
for key, value in account_policy.items():
    assert entries.count(['d-i', key, 'boolean', value]) == 1, f'Required account policy: {key}={value}'
for entry in entries:
    assert len(entry) >= 3, f'Malformed preseed entry: {entry}'
    key = entry[1]
    # Only the root-only policy is preseeded; passwords must remain interactive.
    if key.startswith('passwd/'):
        assert key in account_policy and entry == ['d-i', key, 'boolean', account_policy[key]], entry
    assert not key.startswith(('partman', 'netcfg/', 'grub-installer/')), key
    if key == 'preseed/early_command':
        assert entry == ['d-i', key, 'string', '/bin/sh /cdrom/nex/root-account.sh'], entry
    assert key not in ('preseed/run', 'auto-install/enable'), key
assert entries.count(['d-i', 'preseed/early_command', 'string', '/bin/sh /cdrom/nex/root-account.sh']) == 1
# BUG-0413: recheck.sh clears only the selected disk; partman's RAID removal zeroes every
# array member, so it must never be answered for the operator.
seed = Path('installer/gui/nex-config.c').read_text()
assert 'partman-md/device_remove_md' not in seed, 'RAID removal would touch disks the operator did not select'
# Two or more selected disks install one md RAID1 mirror of a single ext4 root, with an
# ESP per disk on the GPT label a UEFI install uses. One disk keeps the plain layout.
# Generated lines (including NVMe "p" suffixes and disk counts) are exercised by
# tests/integration/installer-hostname.py driving render(); keep only structural guards here.
assert 'd-i partman-auto/method string raid' in seed and 'd-i partman-auto/method string regular' in seed
assert 'd-i partman-auto/expert_recipe string nex-mirror :: ' in seed
assert 'd-i partman-partitioning/default_label string gpt' in seed
assert '1 1 1 free $iflabel{ gpt } method{ biosgrub } . ' in seed
assert '538 538 538 free $iflabel{ gpt } method{ efi } format{ } . ' in seed
assert '1000 20000 1000000000 raid $primary{ } method{ raid } .' in seed
assert 'd-i grub-installer/force-efi-extra-removable boolean true' in seed, 'every ESP needs the removable boot path'
assert 'd-i partman-basicfilesystems/no_swap boolean false' in seed, 'the mirror has no swap; partman must not ask'
# Every selected disk is validated, not just the first, and a mirror of mismatched
# disks is refused rather than silently shrunk to the smallest one.
assert 'validate_disk(disk[i],size[i],serial[i],dev[i])' in seed
assert "within 10% of the smallest selected disk's capacity" in seed
assert 'fail("Select each mirror disk only once.")' in seed
recheck = Path('installer/gui/recheck.sh').read_text()
assert "sed -n 's/^disk=//p' /run/nex-installer/config.ini | tr ';' ' '" in recheck, \
    'signatures must be cleared on every selected disk'
graphical = Path('installer/gui/nex-wizard.c').read_text()
# Both frontends offer the mirror the same way: a tick list of disks, keyboard included.
assert 'gtk_cell_renderer_toggle_new()' in graphical and 'disk_toggled' in graphical, \
    'the graphical Disk step must offer a mirror as a tick list'
assert 'GDK_space' in graphical, 'Space must tick the focused disk'
assert 'Type: multiselect' in templates.split('Template: nex-installer/disk\n')[1], \
    'the text Disk step must offer a mirror'
assert '[ $(set -- $disks; echo $#) -eq 1 ] && db_set nex-installer/disk "$choices"' in wizard, \
    'the text Disk step must preselect a lone eligible disk, like the GUI'
# The same facts about each disk in both frontends, so neither is the informed one.
for fact in ('"Capacity"', '"Type"', '"Model"', '"Serial"', '"Contents"'):
    assert fact in graphical, fact
assert 'queue/rotational' in graphical and 'queue/rotational' in wizard
assert 'has_partitions' in graphical and 'has partitions' in wizard
assert 'keeps running if one disk fails.' in graphical
assert 'one disk can fail without stopping the host' in wizard
assert 'compare_names' in graphical.split('/sys/class/block', 1)[1], 'both frontends list disks in one order'
assert "within 10% of the smallest selected disk's capacity" in wizard
assert 'device/serial' in wizard.split('step_disk()', 1)[1].split('step_erase()', 1)[0], \
    'the text Disk choices must name the serial like the graphical list'
assert 'Disabled, dhcp, static' in wizard, 'both frontends use Disabled for an unconfigured adapter'
assert 'ini_list()' in wizard, ';-separated disk list values must escape semicolons'
assert 'bytes < 16ULL * 1024 * 1024 * 1024) continue' in graphical, \
    'GTK discover must skip disks below 16 GiB like the text frontend'
assert 'smallest disk %.1f GiB' in graphical, 'mirror summary must not call raw capacity usable'
assert 'Template: nex-installer/disk-error\n' in templates
assert '"/var/log/partman"' in Path('installer/gui/nex-shell.c').read_text(), 'View log must include the partman log'

packages = Path('installer/packages.txt').read_text().split()
assert len(packages) == len(set(packages)), 'Duplicate package'
assert {'qemu-system-x86', 'libvirt-daemon-system', 'libvirt-clients', 'openssh-server', 'genisoimage', 'ovmf-generic', 'python3-virt-firmware'} <= set(packages)
assert 'ovmf' not in packages, 'Use only the pinned ovmf-generic package'
assert 'nex-core-local' not in packages, 'Ship nex-core-local via the ISO repo copy, not Debian APT download'
assert all(p.replace('-', '').isalnum() for p in packages), 'Unexpected package argument'
late = Path('installer/late-install.sh').read_text()
assert 'Dir::Etc::sourceparts=-' in late and 'file:/var/cache/nex-install/repo' in late
assert 'PermitRootLogin no' in late
assert 'systemctl disable zfs-import-scan.service' in late, 'Leftover ZFS pools on unselected disks must not be imported at boot'
# Embedded configure.sh and nex-sync-esp are data to sh -n on late-install.sh; syntax-check them.
import subprocess as _sp
for marker, label in (("<<'TARGET'", 'TARGET'), ("<<'ESP'", 'ESP')):
    block = late.split(f"{marker}\n", 1)[1].split(f'\n{label}\n', 1)[0]
    _sp.run(['sh', '-n'], input=block, text=True, check=True)
    print(f'PASS: late-install {label} heredoc is valid shell')
# A mirrored system disk is assembled by the kernel from the members' own superblocks;
# the installed host needs mdadm, its config in the initramfs and GRUB on every ESP.
# Gate on the installed root being an md device, not on any leftover array in mdstat.
assert 'mdadm' in packages
assert "case $root in /dev/md*)" in late
assert "mdadm --detail --scan \"$root\"" in late and 'update-initramfs -u' in late
assert '/usr/local/sbin/nex-sync-esp' in late and 'DPkg::Post-Invoke' in late
assert 'mkdir -p "$copy/EFI"' in late, 'a freshly formatted ESP has no EFI directory to copy into'
assert 'sed -i' in late and 'UUID=' in late.split('nex-sync-esp', 1)[1], \
    'nex-sync-esp must rewrite fstab when it remounts a surviving ESP'
# The mounted ESP is one member's own partition: a replaced or failed disk must not drop the
# host into emergency mode while the mirror itself is healthy. Apply the real sed to a fixture.
fstab_fixture = (
    '# / was on /dev/md0 during installation\n'
    'UUID=root-uuid / ext4 errors=remount-ro 0 1\n'
    '# /boot/efi was on /dev/sda1 during installation\n'
    'UUID=esp-uuid /boot/efi vfat umask=0077 0 1\n'
    '/dev/sr0 /media/cdrom0 udf,iso9660 user,noauto 0 0\n'
)
sed_expr = None
for line in late.splitlines():
    if 'sed -i' in line and 'boot' in line and 'nofail' in line and 'x-systemd.device-timeout' in line:
        # late-install runs: sed -i 'EXPR' /etc/fstab — extract EXPR
        start = line.index("'") + 1
        end = line.rindex("'")
        sed_expr = line[start:end]
        break
assert sed_expr, 'late-install must rewrite the /boot/efi fstab options'
rewritten = _sp.run(['sed', sed_expr], input=fstab_fixture, text=True, capture_output=True, check=True).stdout
assert rewritten.splitlines()[0] == fstab_fixture.splitlines()[0]
assert rewritten.splitlines()[1] == fstab_fixture.splitlines()[1]
assert rewritten.splitlines()[2] == fstab_fixture.splitlines()[2]
assert 'UUID=esp-uuid /boot/efi vfat umask=0077,nofail,x-systemd.device-timeout=5s 0 1' in rewritten
assert rewritten.splitlines()[4] == fstab_fixture.splitlines()[4]
print('PASS: /boot/efi fstab rewrite adds only nofail and a short device timeout')
# Copying to no ESP is a failure, so the replace operation can tell and install the ESP
# itself; the installer and the APT hook both tolerate that status.
assert '[ "$written" -gt 0 ]' in late, 'nex-sync-esp must report copying nothing'
assert "trap 'umount \"$copy\" 2>/dev/null || true; rmdir \"$copy\" 2>/dev/null || true' EXIT" in late, \
    'nex-sync-esp must unmount a failed member ESP copy on EXIT'
assert '/usr/local/sbin/nex-sync-esp || true' in late
assert 'DPkg::Post-Invoke { "/usr/local/sbin/nex-sync-esp || true"; };' in late
assert 'systemd.ssh_auto=no' in late
assert 'nex-core-local.service' in late
assert 'Local alpha: no Nex Controller' not in late
assert r"Host: \n  Console: \l" in late
assert 'CORE_LOCAL_DEB' in build and 'nex-core-local_' in build
assert "printf 'nex-core-local\\n' >>" in build
assert 'scripts/build-source.py' in build, 'ISO builds must regenerate corresponding source'
assert 'appliance/build.py' in build, 'ISO builds must rebuild the Controller appliance'
assert 'assert_iso_artifacts_current' in build, 'ISO builds must verify Core/Controller source freshness'
assert 'stage_ovmf_generic' in build and 'local version=2026.05-2' in build and 'ovmf-generic=$version' in build
assert '0f2d4de080ebeaf0972c5e212253c28dd2d90aa641542f42f37c6d6ecd4c67eb' in build
assert 'stage_virtio_win' in build and 'virtio-win-0.1.302-1.src.rpm' in build
assert 'ensure_perl_exact_source' in build and 'perl-exact-source' in build
assert '303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d' in build
assert '25561bba120e67bfae842429d5e25764c38e74b92f78d9ef90c20ec41d2ad75d' in build
assert 'third-party/virtio-win' in build and 'source_retained' in build
assert 'BusyBox\'s gunzip applet' in build
assert 'grep -Fx gunzip' in build
assert 'refusing to publish virtio-win.iso without complete corresponding-source and legal accounting' not in build
assert 'virtio-win corresponding source RPM missing' in build
assert 'Dir::Etc::sourceparts=-' in build and 'sid main' in build
assert "grep -aFq 'Microsoft UEFI CA 2023'" in build
assert "grep -aFq 'Windows UEFI CA 2023'" in build
assert 'OVMF_CODE_4M.ms.fd' in build and 'OVMF_VARS_4M.ms.fd' in build
assert '40-edk2-x86_64-secure-enrolled.json' in build
assert 'third-party/ovmf-generic' in build and 'checksums.sha256' in build
assert 'Unset CORE_LOCAL_DEB and NEX_SKIP_APPLIANCE_REBUILD' in build, 'ISO builds must reject reused packages'
assert 'dist/nex-source.tar.gz required' in build
assert 'ARTWORK-LICENSE.md is required for branded media.' in build
assert 'cp LICENSE NOTICE ARTWORK-LICENSE.md docs/LICENSING.md "$stage/nex/docs/"' in build
assert 'cp -f LICENSE NOTICE ARTWORK-LICENSE.md docs/LICENSING.md "$source_stage/nex-source/"' in build
assert 'cp -a installer "$source_stage/nex-source/"' in build
assert re.search(r'tar .* -czf "\$stage/nex/nex-source\.tar\.gz" \\\s*LICENSE NOTICE docs/LICENSING\.md installer', build) is None, \
    'Do not ship installer-only source as the sole ISO archive'
# BUG-0004: raw chroot inherits the installer's stale debconf descriptors.
assert '\nin-target /bin/sh /var/cache/nex-install/configure.sh\n' in late, 'Use Debian in-target for package configuration'
assert '\nchroot /target ' not in late, 'Raw chroot bypasses installer setup'
post = Path('packaging/core-local/postinst').read_text()
core_package = Path('packaging/core-local/build-inside.sh').read_text()
# BUG-0406: the installed system resolves nex-core-local from the ISO's offline repo,
# whose closure is downloaded from packages.txt alone. A dependency that nothing in that
# list pulls in is simply absent from the media and the in-target install fails.
core_depends_field = re.findall(r'(?m)^Depends: (.+)$', core_package)
assert len(core_depends_field) == 1, 'One nex-core-local control stanza expected'
core_depends = {d.split()[0] for d in core_depends_field[0].split(',')}
# Supplied by the Debian base system or already pulled in by a listed package's own
# dependencies; everything else nex-core-local needs must be downloaded explicitly.
base_system = {'init-system-helpers', 'libc6', 'python3', 'openssl', 'libpam0g',
               'libpam-modules', 'iproute2'}
undownloaded = sorted(core_depends - base_system - set(packages))
assert not undownloaded, f'nex-core-local depends on packages the ISO repo never downloads: {undownloaded}'
assert 'python3-virt-firmware' in core_package, 'nex-core-local must depend on the Secure Boot variable tool package'
assert 'dpkg-divert --local --add --rename --divert /etc/os-release.debian /etc/os-release' in post
assert 'ID=nex-core\nID_LIKE=debian\n' in post and "dpkg-query -W -f='${Version}' nex-core-local" in post
assert 'systemd.ssh_auto=no' in post
assert '0.1.0-alpha1' not in build + late and 'test "$ID" = nex-core' in late, 'One Nex Core version: the package'
assert 'if [ -d /cdrom/nex/third-party/virtio-win ]' in late
assert '/cdrom/nex/media/virtio-win.iso.gz' in late
assert '/cdrom/nex/media/virtio-win.iso.sha256' in late
assert 'gunzip -c /cdrom/nex/media/virtio-win.iso.gz > "$virtio_iso"' in late
assert "trap 'rm -f \"$virtio_iso\"' 0 1 2 15" in late
assert '> /usr/lib/os-release' not in late, 'Preserve upstream release metadata'
assert 'third-party/ovmf-generic' in late
print('PASS: shell syntax, interactive disk/account/network contract, package inputs, offline APT scope')
PY
