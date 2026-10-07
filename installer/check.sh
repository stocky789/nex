#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Source-level installation safety check; actual offline installation is separate.
set -euo pipefail
cd "$(dirname "$0")/.."
bash -n installer/build.sh
bash -n installer/build-gui.sh
sh -n installer/netinst-fetch.sh
python3 -m py_compile installer/netinst-manifest.py
sh -n installer/gui/wizard-text.sh
sh -n installer/root-account.sh
sh -n installer/nex-image-install.sh
sh -n installer/gui/prepare.sh
grep -Fq ': > /var/lib/partman/loaded-extra' installer/gui/prepare.sh || { echo 'prepare.sh must stop partman loading LVM/crypto from the media' >&2; exit 1; }
python3 - <<'PY'
from pathlib import Path
import re

build = Path('installer/build.sh').read_text()
bios = build.split("<<'BIOS'\n")[2].split('\nBIOS')[0]
grub = build.split("<<'GRUB'\n")[1].split('\nGRUB')[0]


def boot_entry(label):
    """The BIOS (isolinux) and UEFI (GRUB) entries of one boot menu item."""
    return (next(entry for entry in re.split(r'(?m)^label ', bios)[1:]
                 if f'menu label {label}\n' in entry),
            grub.split(f"menuentry '{label}' {{\n")[1].split('}')[0])


# The media boots on BIOS and UEFI firmware: the base media's hybrid boot structure and signed
# EFI binaries are replayed, and every entry exists in both menus.
assert '-boot_image any replay' in build, 'the hybrid BIOS/UEFI boot structure must be replayed'
assert 'isolinux/isolinux.cfg' in build and 'boot/grub/grub.cfg' in build
assert len(re.findall(r'(?m)^label ', bios)) == len(re.findall(r'(?m)^menuentry ', grub)) == 4, \
    'the BIOS and UEFI menus must offer the same four entries'
# Every Nex mode boots the initrd carrying the Nex overlay, which supplies the wizard and its
# embedded account policy; only the cdebconf frontend and console differ. A mode booting the
# untouched Debian initrd would run Debian's own questions instead of the Nex installer.
for label, frontend, console in [('GUI install - Nex Core', '', False),
                                 ('Minimal install - Nex Core (text)', 'newt', False),
                                 ('Install Nex Core (serial console ttyS0)', 'newt', True)]:
    b, g = boot_entry(label)
    assert 'kernel /install.amd/vmlinuz' in b
    assert 'initrd=/install.amd/gtk/initrd.gz' in b
    assert 'linux /install.amd/vmlinuz' in g
    assert 'initrd /install.amd/gtk/initrd.gz' in g
    assert 'preseed/file=' not in b + g, f'{label} must not bypass the Nex wizard with a media seed'
    if frontend:
        assert f'DEBIAN_FRONTEND={frontend}' in b and f'DEBIAN_FRONTEND={frontend}' in g
    else:
        assert 'DEBIAN_FRONTEND=' not in b + g, 'the graphical mode selects its own frontend'
    if console:
        assert 'console=ttyS0,115200n8' in b and 'console=ttyS0,115200n8' in g
    else:
        assert 'console=' not in b + g
    assert 'auto=true' not in b + g
# Rescue reaches a shell without seeding anything.
for entry in boot_entry('Debian rescue mode'):
    assert '/install.amd/initrd.gz' in entry and '/install.amd/gtk/initrd.gz' not in entry
    assert 'preseed' not in entry, 'rescue must boot Debian unseeded'
    assert 'rescue/enable=true' in entry
assert 'timeout 0' in build and 'set timeout=-1' in grub
assert 'nex.edition' not in build and '/nex/edition' not in build
print('PASS: BIOS and UEFI menus offer GUI, text, serial and rescue; no timeout')

# Both wizards write the same handoff and nex-config remains the single validator.
start = Path('installer/gui/start.sh').read_text()
wizard = Path('installer/gui/wizard-text.sh').read_text()
templates = Path('installer/gui/wizard-text.templates').read_text()
gui = Path('installer/build-gui.sh').read_text()
assert '"debian-installer/main-menu"' in gui and 'cdebconf_gtk_nex_install_stopped(fe);' in gui, \
    'The graphical installer must stop on Debian main-menu instead of exposing component selection'
assert '/usr/lib/nex-installer/wizard-text' in start and '/usr/bin/nex-wizard' in start
assert '"$nex_wizard"' in start, 'One setup loop must drive whichever frontend is running'
assert 'nex-config --apply /run/nex-installer/config.ini' in start
assert start.count('nex-config --apply') == 1, 'One apply site for both frontends'
assert "install -m 755 installer/gui/wizard-text.sh" in gui and 'wizard-text.templates' in gui
assert "assert (root / 'usr/lib/cdebconf/frontend/newt.so').is_file()" in gui
assert 'install -m 644 installer/preseed.cfg "$overlay/preseed.cfg"' in gui
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
assert 'MIN_SECTORS=50331648' in wizard, 'text wizard needs the 24 GiB floor'
assert 'sd*|vd*|xvd*|nvme*' in wizard
assert '[ -e "$path/partition" ] && continue' in wizard
assert '"$(read_attr "$path/removable")" = 0' in wizard
assert '/cdrom' in wizard, 'Installation media is never offered as a target'
assert 'db_set nex-installer/erase false' in wizard, 'Confirmation must not carry forward'
assert '[ -z "$first" ]' in wizard
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
assert sorted(entry[1] for entry in entries) == sorted(account_policy), \
    'The seed carries only the root-only account policy'
for key, value in account_policy.items():
    assert entries.count(['d-i', key, 'boolean', value]) == 1, f'Required account policy: {key}={value}'
# Passwords, disks, networking and confirmations remain interactive; the image installer, not
# Debian's partitioning, base-system or boot loader steps, does the disk work.
seed = Path('installer/gui/nex-config.c').read_text()
assert 'partman-md/device_remove_md' not in seed, 'RAID removal would touch disks the operator did not select'
# Every selected disk is validated, not just the first, and a mirror of mismatched
# disks is refused rather than silently shrunk to the smallest one.
assert 'validate_disk(disk[i],size[i],serial[i],dev[i])' in seed
assert "within 10% of the smallest selected disk's capacity" in seed
assert 'fail("Select each mirror disk only once.")' in seed
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
assert 'What you get: one %.1f GiB system disk on all %u drives' in graphical
assert 'RAID1 copies — not pooled storage' in graphical
assert 'capacity not pooled); survives' in wizard
assert 'compare_names' in graphical.split('/sys/class/block', 1)[1], 'both frontends list disks in one order'
assert "within 10% of the smallest selected disk's capacity" in wizard
assert 'disk_serial "$name"' in wizard.split('step_disk()', 1)[1].split('step_erase()', 1)[0], \
    'the text Disk choices must name the serial like the graphical list'
# nex-config re-reads the serial through nex_install_disk_serial, so the text wizard must
# try the same sysfs attributes in the same order or a mirror would be refused as swapped.
helper = Path('installer/gui/nex-network.c').read_text().split('nex_install_disk_serial(', 1)[1]
shell = wizard.split('disk_serial() {', 1)[1].split('}', 1)[0]
assert [a for a in ('device/serial', '"serial"', 'device/vpd_pg80') if a in helper] == ['device/serial', '"serial"', 'device/vpd_pg80']
assert shell.index('device/serial') < shell.index('/serial"') < shell.index('device/vpd_pg80')
assert 'Disabled, dhcp, static' in wizard, 'both frontends use Disabled for an unconfigured adapter'
assert 'ini_list()' in wizard, ';-separated disk list values must escape semicolons'
assert 'bytes < disk_floor()) continue' in graphical, \
    'GTK discover must skip disks below the floor like the text frontend'
assert 'not pooled storage' in graphical, 'mirror summary must say capacity is not pooled'
assert 'Template: nex-installer/disk-error\n' in templates
assert 'nex-live-log' in Path('installer/gui/nex-shell.c').read_text(), 'Install progress must show a live activity log'
assert 'Still working' in Path('installer/gui/nex-shell.c').read_text(), 'Install progress must show activity while a step is quiet'
assert 'nex_progress_tick' in Path('installer/gui/nex-shell.c').read_text()

# The media is the image and the static installer; the builder needs both.
assert 'NEX_IMAGE_DIR is required' in build and 'NEX_HOSTIMAGE_BIN is required' in build, \
    'the media cannot be built without the image and the static nex-hostimage'
assert '"$hostimage" verify "$image"' in build, 'the staged image must verify before it is put on the media'
assert 'install -m755 installer/nex-image-install.sh "$stage/nex/nex-image-install.sh"' in build
assert 'stage_grub_bios\n' in build and 'local tree=$cache/grub-image-tree grub=$stage/nex/grub' in build, \
    'BIOS firmware needs a GRUB the installer environment lacks: it is staged at nex/grub (grub-install)'
assert 'usr/sbin/grub-install' in build and 'usr/lib/grub/i386-pc/grub-bios-setup' in build
assert all(p in Path('image/base.packages').read_text().split() for p in ('grub2-common', 'grub-common', 'grub-pc-bin')), \
    'grub-install (grub2-common) and its i386-pc parts must be in the system image'
assert '"$grub/usr/lib64"' in build and 'fsck.erofs --extract="$tree"' in build, \
    'the staged GRUB needs usr/lib64 for the loader (/lib64 is a link into usr) and is read from the erofs'
assert '--parents' not in build.split('stage_grub_bios() {')[1].split('\n}\n')[0], \
    'library copies go through the same symlinked layout; no silent cp fallback'
assert 'stage_efibootmgr "$tree"' in build and 'efi=$stage/nex/efi' in build and 'prepare_efibootmgr' in Path('installer/nex-image-install.sh').read_text(), \
    'UEFI firmware needs an efibootmgr the installer environment lacks: it is staged at nex/efi and wrapped before the install'
image_install = Path('installer/nex-image-install.sh').read_text()
finish = image_install.split('finish() {')[1].split('\n}\n')[0]
assert 'db_input critical finish-install/reboot_in_progress' in finish and finish.index('db_go') < finish.rindex('reboot'), \
    'finish-install never runs after the image install: it must ask the completion question (the GUI completion page) before it restarts'
assert 'install -m755 installer/root-account.sh "$stage/nex/root-account.sh"' in build, \
    'the preparation step reads the account hook from the media'
assert 'dist/nex-source.tar.gz' in build and 'appliance/build.py --version' in build, 'ISO builds must regenerate corresponding source'
assert 'controller-system.img' in build, 'ISO builds must carry the Controller system image'
assert 'stage_virtio_win' in build and 'virtio-win-0.1.302-1.src.rpm' in build
assert 'ensure_perl_exact_source' in build and 'perl-exact-source' in build
assert '303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d' in build
assert '25561bba120e67bfae842429d5e25764c38e74b92f78d9ef90c20ec41d2ad75d' in build
assert 'third-party/virtio-win' in build and 'source_retained' in build
assert 'BusyBox\'s gunzip applet' in build
assert 'grep -Fx gunzip' in build
assert 'virtio-win corresponding source RPM missing' in build
assert 'deb-src $source_snapshot trixie main' in build and 'Dir::Etc::sourceparts=-' in gui
assert 'dist/nex-source.tar.gz required' in build
assert 'ARTWORK-LICENSE.md is required for branded media.' in build
assert 'cp LICENSE NOTICE ARTWORK-LICENSE.md docs/LICENSING.md "$stage/nex/docs/"' in build
assert 'cp -f LICENSE NOTICE ARTWORK-LICENSE.md docs/LICENSING.md "$source_stage/nex-source/"' in build
assert 'cp -a installer "$source_stage/nex-source/"' in build
assert re.search(r'tar .* -czf "\$stage/nex/nex-source\.tar\.gz" \\\s*LICENSE NOTICE docs/LICENSING\.md installer', build) is None, \
    'Do not ship installer-only source as the sole ISO archive'
# The net installer carries the same boot files and downloads only the image set and payloads.
fetch = Path('installer/netinst-fetch.sh').read_text()
assert 'image/*|payload/*/*' in fetch and 'repo/' not in fetch and 'media/' not in fetch
assert '--image "$stage/nex/image" --appliance /work/dist/appliance' in build
assert 'rm -rf "$netstage/nex/image" "$netstage/nex/payload"' in build
print('PASS: shell syntax, interactive disk/account/network contract, image-only media inputs')
PY
