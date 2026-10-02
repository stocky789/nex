# SPDX-License-Identifier: AGPL-3.0-only
"""Behaviour of installer/nex-image-install.sh that must hold before any disk is written."""
import os
import re
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent.parent / 'installer' / 'nex-image-install.sh'


class ImageInstallerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.sys, self.udev, self.media, self.state = (root / n for n in ('sys', 'udev', 'media', 'state'))
        for path in (self.sys / 'class/block', self.udev, self.media / 'bin', self.media / 'image', self.state):
            path.mkdir(parents=True)
        (self.media / 'bin/nex-hostimage').write_text('#!/bin/sh\nexit 1\n')
        (self.media / 'image/manifest.json').write_text('{}')
        (self.media / 'image/root.erofs').write_text('')
        tools = root / 'tools'
        tools.mkdir()
        self.log, self.mdstat, self.dev = root / 'calls.log', root / 'mdstat', root / 'dev'
        for tool in ('mkfs.fat', 'mkfs.ext4', 'mdadm', 'udevadm', 'logger', 'modprobe', 'mount', 'umount', 'sync',
                     'sleep', 'reboot'):
            (tools / tool).write_text('#!/bin/sh\ncase $(basename "$0") in logger|modprobe) ;; *) echo "$(basename "$0") $*" >> "$NEX_TEST_LOG" ;; esac\n'
                                      '[ "$(basename "$0")-$1" != mdadm---assemble ] || { mkdir -p "$NEX_DEV/md"; : > "$NEX_DEV/md/nex-data"; }\n'
                                      'exit 0\n')
            (tools / tool).chmod(0o755)
        self.env = dict(os.environ, PATH=f'{tools}:{os.environ["PATH"]}', NEX_SYS=str(self.sys),
                        NEX_UDEV_DATA=str(self.udev), NEX_MEDIA=str(self.media), NEX_STATE=str(self.state),
                        NEX_IMAGE_WORK=str(root / 'work'), NEX_TEST_LOG=str(self.log), NEX_DEV=str(self.dev),
                        NEX_MDSTAT=str(self.mdstat), NEX_RESTART_DELAY='0')

    def tearDown(self):
        self.tmp.cleanup()

    def run_script(self, *args):
        return subprocess.run(['sh', str(SCRIPT), *args], env=self.env, capture_output=True, text=True, timeout=30)

    def add_data_partition(self, disk, number, kind, minor):
        """A GPT entry named nex-data on disk, with the udev probe result of the given kind."""
        parent = self.sys / 'devices' / disk
        part = parent / f'{disk}{number}'
        part.mkdir(parents=True, exist_ok=True)
        (part / 'partition').write_text(str(number))
        (part / 'dev').write_text(f'8:{minor}')
        (self.sys / 'class/block' / f'{disk}{number}').symlink_to(part)
        probe = ['E:ID_PART_ENTRY_NAME=nex-data']
        if kind == 'single':
            probe += ['E:ID_FS_TYPE=ext4', 'E:ID_FS_LABEL=nex-data']
        else:
            probe += ['E:ID_FS_TYPE=linux_raid_member', 'E:ID_FS_LABEL=host:nex-data']
        (self.udev / f'b8:{minor}').write_text('\n'.join(probe) + '\n')

    def test_bios_boot_is_refused_before_anything_is_touched(self):
        (self.state / 'image.env').write_text('mode=install\nlayout=single\ndisks=/dev/sda\n')
        result = self.run_script('install')
        self.assertEqual(result.returncode, 1)
        self.assertIn('UEFI firmware only', result.stderr)
        self.assertIn('No disk has been changed', result.stderr)
        self.assertFalse((Path(self.tmp.name) / 'work').exists(), 'nothing may be staged before the firmware check')

    def test_probe_accepts_a_complete_mirror_and_rejects_a_single_member(self):
        self.add_data_partition('sdb', 6, 'raid1', 22)
        self.assertNotEqual(self.run_script('probe').returncode, 0, 'one member of a mirror is not a complete data volume')
        self.add_data_partition('sda', 6, 'raid1', 6)
        result = self.run_script('probe')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, 'layout=raid1\ndisks=/dev/sda,/dev/sdb\n')

    def test_probe_rejects_mixed_data_volumes(self):
        self.add_data_partition('sda', 6, 'single', 6)
        self.add_data_partition('sdb', 6, 'raid1', 22)
        self.assertNotEqual(self.run_script('probe').returncode, 0)

    def test_probe_finds_a_single_data_volume(self):
        self.add_data_partition('nvme0n1', 6, 'single', 6)
        result = self.run_script('probe')
        self.assertEqual(result.stdout, 'layout=single\ndisks=/dev/nvme0n1\n')

    def test_repair_must_name_exactly_the_disks_that_hold_the_data(self):
        (self.sys / 'firmware/efi').mkdir(parents=True)
        self.add_data_partition('sda', 6, 'raid1', 6)
        self.add_data_partition('sdb', 6, 'raid1', 22)
        (self.state / 'image.env').write_text('mode=repair\nlayout=raid1\ndisks=/dev/sda\n')
        result = self.run_script('repair')
        self.assertEqual(result.returncode, 1)
        self.assertIn('exactly the disks that hold the Nex Core data', result.stderr)
        self.assertIn('No disk has been changed', result.stderr)

    def test_repair_without_a_complete_data_volume_has_nothing_to_keep(self):
        (self.sys / 'firmware/efi').mkdir(parents=True)
        self.add_data_partition('sda', 6, 'raid1', 6)
        (self.state / 'image.env').write_text('mode=repair\nlayout=raid1\ndisks=/dev/sda,/dev/sdb\n')
        result = self.run_script('repair')
        self.assertEqual(result.returncode, 1)
        self.assertIn('Use Erase and install instead', result.stderr)

    def test_settings_are_validated(self):
        (self.sys / 'firmware/efi').mkdir(parents=True)
        (self.state / 'image.env').write_text('mode=install\nlayout=raid5\ndisks=/dev/sda\n')
        self.assertIn('Unsupported layout', self.run_script('install').stderr)
        (self.state / 'image.env').write_text('mode=install\nlayout=single\ndisks=/dev/sda;rm\n')
        self.assertIn('Invalid disk list', self.run_script('install').stderr)

    def stage_install(self, layout='single', disks='/dev/sda'):
        """A media tree and settings that reach nex-hostimage; every tool call is logged in order."""
        (self.sys / 'firmware/efi').mkdir(parents=True)
        (self.state / 'image.env').write_text(f'mode=install\nlayout={layout}\ndisks={disks}\n')
        (self.media / 'bin/nex-hostimage').write_text(
            '#!/bin/sh\necho "nex-hostimage $*" >> "$NEX_TEST_LOG"\n'
            '[ "$1" != verify ] || [ -z "$NEX_VERIFY_FAIL" ]\n')
        self.mdstat.write_text('Personalities : [raid1]\n'
                               'md0 : active raid1 sdb1[1] sda1[0]\n      1000 blocks\n'
                               'md1 : active raid1 sdd1[1] sdc1[0]\n      1000 blocks\n')
        for disk in ('sda', 'sdb', 'sdc'):
            (self.sys / 'devices' / disk).mkdir(parents=True, exist_ok=True)
            (self.sys / 'class/block' / disk).symlink_to(self.sys / 'devices' / disk)
            part = self.sys / 'devices' / disk / f'{disk}1'
            part.mkdir()
            (part / 'partition').write_text('1')

    def calls(self):
        return self.log.read_text().splitlines() if self.log.exists() else []

    def test_nothing_is_stopped_or_written_before_the_image_verifies(self):
        self.stage_install()
        self.env['NEX_VERIFY_FAIL'] = '1'
        result = self.run_script('install')
        self.assertEqual(result.returncode, 1)
        self.assertIn('No disk has been changed', result.stderr)
        self.assertEqual(self.calls(), [f'nex-hostimage verify {self.media}/image'])

    def test_damaged_media_payload_stops_the_install_before_any_disk_changes(self):
        self.stage_install()
        payload = self.media / 'payload' / ('0' * 64)
        payload.mkdir(parents=True)
        (payload / 'controller.qcow2').write_text('not the recorded bytes')
        result = self.run_script('install')
        self.assertEqual(result.returncode, 1)
        self.assertIn('does not match its recorded SHA-256', result.stderr)
        self.assertNotIn('nex-hostimage install', ' '.join(self.calls()))
        self.assertFalse([c for c in self.calls() if c.startswith('mdadm')])

    def test_install_stops_only_the_arrays_on_the_selected_disks_and_assembles_only_its_own(self):
        self.stage_install('raid1', '/dev/sda,/dev/sdb')
        result = self.run_script('install')
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.calls()
        self.assertEqual(calls[0], f'nex-hostimage verify {self.media}/image')
        self.assertEqual(calls[1], f'mdadm --stop {self.dev}/md0')
        self.assertEqual(calls[2].split()[:2], ['nex-hostimage', 'install'])
        self.assertEqual([c for c in calls if c.startswith('mdadm')][1:],
                         [f'mdadm --assemble {self.dev}/md/nex-data --run /dev/sda6 /dev/sdb6',
                          f'mdadm --stop {self.dev}/md/nex-data'])
        self.assertFalse([c for c in calls if '--scan' in c or 'md1' in c], 'a foreign array was touched')

    def test_install_needs_no_mkfifo_and_reports_the_tool_output_and_status(self):
        """The installer's BusyBox has no mkfifo: progress must flow through a plain pipe and a failing tool must still fail the install."""
        self.stage_install('raid1', '/dev/sda,/dev/sdb')
        tools = Path(self.env['PATH'].split(':')[0])
        (tools / 'mkfifo').write_text('#!/bin/sh\necho "mkfifo $*" >> "$NEX_TEST_LOG"\nexit 127\n')
        (tools / 'mkfifo').chmod(0o755)
        (self.media / 'bin/nex-hostimage').write_text(
            '#!/bin/sh\necho "nex-hostimage $*" >> "$NEX_TEST_LOG"\n'
            '[ "$1" != install ] || echo "slot a written"\n'
            '[ "$1" != install ] || [ -z "$NEX_INSTALL_FAIL" ] || exit 7\n'
            '[ "$1" != verify ] || [ -z "$NEX_VERIFY_FAIL" ]\n')
        result = self.run_script('install')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('nex-installer: slot a written', result.stdout)
        self.assertFalse([c for c in self.calls() if c.startswith('mkfifo')])
        self.env['NEX_INSTALL_FAIL'] = '1'
        failed = self.run_script('install')
        self.assertEqual(failed.returncode, 1)
        self.assertIn('could not install the image', failed.stderr)

    def test_the_data_volume_node_may_appear_a_moment_after_the_image_is_written(self):
        self.stage_install('raid1', '/dev/sda,/dev/sdb')
        tools = Path(self.env['PATH'].split(':')[0])
        # The array node is created by the first sleep, as the kernel's partition re-read does a moment after the tool exits.
        (tools / 'mdadm').write_text('#!/bin/sh\necho "mdadm $*" >> "$NEX_TEST_LOG"\nexit 0\n')
        (tools / 'sleep').write_text('#!/bin/sh\necho "sleep $*" >> "$NEX_TEST_LOG"\nmkdir -p "$NEX_DEV/md"; : > "$NEX_DEV/md/nex-data"\nexit 0\n')
        self.env['NEX_NODE_WAIT'] = '5'
        result = self.run_script('install')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('sleep 1', self.calls())
        (self.dev / 'md/nex-data').unlink()
        (tools / 'sleep').write_text('#!/bin/sh\necho "sleep $*" >> "$NEX_TEST_LOG"\nexit 0\n')
        self.env['NEX_NODE_WAIT'] = '2'
        missing = self.run_script('install')
        self.assertEqual(missing.returncode, 1)
        self.assertIn('data volume', missing.stderr)

    def test_a_failed_payload_copy_leaves_nothing_mounted_or_assembled(self):
        self.stage_install('raid1', '/dev/sda,/dev/sdb')
        payload = self.media / 'payload' / ('0' * 64)
        payload.mkdir(parents=True)
        (payload / 'extra.bin').write_text('x')
        tools = Path(self.env['PATH'].split(':')[0])
        (tools / 'cp').write_text('#!/bin/sh\n[ "${2##*/}" = nex-hostimage ] || [ "${1##*/}" = nex-hostimage ] && exec /bin/cp "$@"\nexit 1\n')
        (tools / 'cp').chmod(0o755)
        result = self.run_script('install')
        self.assertEqual(result.returncode, 1)
        calls = self.calls()
        self.assertTrue(any(c.startswith('umount') for c in calls))
        self.assertEqual(calls[-1], f'mdadm --stop {self.dev}/md/nex-data')

    def stage_repair(self):
        """A complete two-disk mirror whose nex-data array md0 is assembled, as after a previous session."""
        (self.sys / 'firmware/efi').mkdir(parents=True)
        for disk, minor in (('sda', 6), ('sdb', 22)):
            self.add_data_partition(disk, 6, 'raid1', minor)
            (self.sys / 'class/block' / disk).symlink_to(self.sys / 'devices' / disk)
        (self.state / 'image.env').write_text('mode=repair\nlayout=raid1\ndisks=/dev/sda,/dev/sdb\n')
        (self.media / 'bin/nex-hostimage').write_text('#!/bin/sh\necho "nex-hostimage $*" >> "$NEX_TEST_LOG"\n')
        self.mdstat.write_text('Personalities : [raid1]\n'
                               'md0 : active raid1 sdb6[1] sda6[0]\n      1000 blocks\n'
                               'md1 : active raid1 sdd1[1] sdc1[0]\n      1000 blocks\n')

    def test_repair_stops_the_arrays_on_the_selected_disks_before_it_writes(self):
        self.stage_repair()
        result = self.run_script('repair')
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.calls()
        self.assertEqual(calls[0], f'mdadm --stop {self.dev}/md0')
        self.assertEqual(calls[1].split()[:2], ['nex-hostimage', 'repair'])
        self.assertFalse([c for c in calls if 'md1' in c], 'a foreign array was touched')

    def test_repair_refuses_before_any_write_when_an_array_cannot_be_stopped(self):
        self.stage_repair()
        tools = Path(self.env['PATH'].split(':')[0])
        (tools / 'mdadm').write_text('#!/bin/sh\necho "mdadm $*" >> "$NEX_TEST_LOG"\nexit 1\n')
        result = self.run_script('repair')
        self.assertEqual(result.returncode, 1)
        self.assertIn('Could not stop md0', result.stderr)
        self.assertIn('No disk has been changed', result.stderr)
        self.assertFalse([c for c in self.calls() if c.startswith('nex-hostimage')])


class MediaMenuTests(unittest.TestCase):
    """The boot menus installer/build.sh writes: the serial entries run Debian's own questions, never the Nex wizard."""
    BUILD = SCRIPT.parent / 'build.sh'

    def menus(self, image):
        lines = self.BUILD.read_text().splitlines()
        start = next(i for i, l in enumerate(lines) if l.startswith('cat > "$stage/isolinux/menu.cfg"'))
        image_if = next(i for i in range(start, len(lines)) if lines[i].startswith('if [[ -n ${NEX_IMAGE_DIR:-} ]]'))
        end = next(i for i in range(image_if, len(lines)) if lines[i] == 'fi')
        with tempfile.TemporaryDirectory() as stage:
            Path(stage, 'isolinux').mkdir()
            Path(stage, 'boot/grub').mkdir(parents=True)
            env = dict(os.environ, stage=stage, NEX_IMAGE_DIR='image/out/release' if image else '')
            subprocess.run(['bash', '-ec', '\n'.join(lines[start:end + 1])], env=env, check=True)
            return Path(stage, 'isolinux/menu.cfg').read_text(), Path(stage, 'boot/grub/grub.cfg').read_text()

    def serial_titles(self, text):
        return re.findall(r"(?:menu label|menuentry) '?([^'\n{]*serial console[^'\n{]*?)'? *\{?$", text, re.M)

    def test_serial_entries_on_image_media_say_they_install_the_classic_edition(self):
        for menu in self.menus(image=True):
            self.assertEqual(self.serial_titles(menu), ['Classic edition install (serial console ttyS0)'], menu)

    def test_serial_entry_of_classic_only_media_keeps_its_name(self):
        for menu in self.menus(image=False):
            self.assertEqual(self.serial_titles(menu), ['Install Nex Core (serial console ttyS0)'], menu)
            self.assertNotIn('Classic', menu)


if __name__ == '__main__':
    unittest.main()
