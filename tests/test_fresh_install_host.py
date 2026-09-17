# SPDX-License-Identifier: AGPL-3.0-only
import hashlib
import importlib.util
import json
from pathlib import Path
import sys
import subprocess
import tempfile
import unittest
from unittest.mock import patch


sys.path.insert(0, str(Path(__file__).parent / 'integration'))
spec = importlib.util.spec_from_file_location(
    'fresh_install_host', Path(__file__).parent / 'integration/fresh_install_host.py')
host = importlib.util.module_from_spec(spec)
spec.loader.exec_module(host)


browser_spec = importlib.util.spec_from_file_location(
    'fresh_install_browser', Path(__file__).parent / 'integration/fresh_install_browser.py')
browser = importlib.util.module_from_spec(browser_spec)
browser_spec.loader.exec_module(browser)


class FreshInstallHostTests(unittest.TestCase):
    def test_provenance_uses_the_expected_version_package(self):
        self.assertEqual(host.core_local_package('/x/dist/nex-core-0.2.10~gate1-amd64.iso', '0.2.10~gate1'),
                         Path('/x/dist/core-local/nex-core-local_0.2.10~gate1_amd64.deb'))
        with tempfile.TemporaryDirectory() as directory:
            lab = Path(directory)
            (lab / 'config.json').write_text(json.dumps({'iso': str(lab / 'nex-core-0.2.10~gate1-amd64.iso')}))
            with self.assertRaisesRegex(AssertionError, r'nex-core-local_0\.2\.10~gate1_amd64\.deb'):
                host.provenance(lab, lab, lab / 'controller.json', '0.2.10~gate1')

    def test_console_typing_presses_shift_explicitly(self):
        events = []
        class Keyboard:
            def down(self, key): events.append(('down', key))
            def up(self, key): events.append(('up', key))
            def press(self, key): events.append(('press', key))
        browser.type_into_console(Keyboard(), 'Aa_:')
        self.assertEqual(events, [('down', 'Shift'), ('press', 'a'), ('up', 'Shift'), ('press', 'a'),
                                  ('down', 'Shift'), ('press', '-'), ('up', 'Shift'),
                                  ('down', 'Shift'), ('press', ';'), ('up', 'Shift')])

    def test_service_check_rejects_one_failed_unit_among_healthy_units(self):
        with tempfile.TemporaryDirectory() as directory:
            systemctl = Path(directory) / 'systemctl'
            systemctl.write_text('#!/bin/sh\nshift\nstatus=3\nfor unit do\n case "$unit" in nex-core-local) echo activating;; *) echo active; status=0;; esac\ndone\nexit "$status"\n')
            systemctl.chmod(0o755)
            import os
            env = {**os.environ, 'PATH': directory + ':' + os.environ['PATH']}
            aggregate = subprocess.run(['sh', '-c', 'systemctl is-active nex-core-local libvirtd'], env=env, capture_output=True)
            self.assertEqual(aggregate.returncode, 0, 'reproduce systemctl aggregate false-positive')
            result = subprocess.run(['sh', '-c', host.services_active_command(('nex-core-local', 'libvirtd'))],
                                    env=env, capture_output=True)
            self.assertNotEqual(result.returncode, 0)

    def test_baseline_and_reboot_refuse_failed_service_before_acceptance(self):
        class Result: stdout = 'nex-fresh-lab\n'
        def ssh(_lab, command, **_kwargs):
            if command == 'hostname':
                return Result()
            self.assertTrue(any(host.services_active_command(units) in command for units in
                                [('nex-core-local', 'libvirtd.socket', 'ssh'), ('nex-core-local', 'libvirtd.socket', 'ssh', 'networking', 'chrony')]))
            raise subprocess.CalledProcessError(1, command)
        with patch.object(host, 'ssh', side_effect=ssh):
            with self.assertRaises(subprocess.CalledProcessError):
                host.baseline(Path('/lab'), Path('/no-evidence'))
            with patch.object(host.time, 'monotonic', side_effect=[0, 0, 2]), patch.object(host.time, 'sleep'):
                with self.assertRaisesRegex(AssertionError, 'did not return'):
                    host.wait_for_host_reboot(Path('/lab'), '01234567-89ab-cdef-0123-456789abcdef', timeout=1)

    def test_fixture_source_requires_regular_exact_digest(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            image = root / 'guest.qcow2'
            image.write_bytes(b'fixture')
            digest = hashlib.sha256(b'fixture').hexdigest()
            self.assertEqual(host.fixture_source(image, digest), image.resolve())
            with self.assertRaises(AssertionError):
                host.fixture_source(image, '0' * 64)
            linked = root / 'linked.qcow2'
            linked.symlink_to(image)
            with self.assertRaises(AssertionError):
                host.fixture_source(linked, digest)

    def test_bios_domain_is_independent_disk_with_nat_not_uefi(self):
        xml = host.bios_domain_xml('nex-fresh-bios-' + 'a' * 24, '/var/lib/libvirt/images/nex-fresh-bios-' + 'a' * 24 + '/guest.qcow2', '52:54:00:fa:aa:aa')
        self.assertIn('<boot dev="hd"/>', xml)
        self.assertIn('type="qcow2"', xml)
        self.assertIn('bus="virtio"', xml)
        self.assertIn('<source network="default"/>', xml)
        self.assertNotIn('<loader', xml)
        self.assertNotIn('<nvram', xml)
        self.assertIn('backing-filename', host.independent_qcow_check())

    def test_qcow_check_rejects_a_digest_pinned_raw_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source, disk = root / 'source.json', root / 'disk.json'
            source.write_text(json.dumps({'format': 'raw', 'virtual-size': 8}))
            disk.write_text(json.dumps({'format': 'qcow2', 'virtual-size': 8}))
            rejected = subprocess.run([sys.executable, '-c', host.independent_qcow_check(), str(source), str(disk)], capture_output=True)
            self.assertNotEqual(rejected.returncode, 0)

    def test_release_refuses_any_target_outside_generated_fixture_dir(self):
        with self.assertRaises(AssertionError):
            host.release_bios_fixture(Path('/unsafe'), 'anything', '/var/lib/libvirt/images/unrelated')

    def test_boot_id_uses_only_the_exact_last_output_line(self):
        self.assertEqual(host.boot_id('hash OK\n01234567-89ab-cdef-0123-456789abcdef\n'), '01234567-89ab-cdef-0123-456789abcdef')
        with self.assertRaises(AssertionError):
            host.boot_id('hash OK\nnot-a-boot-id\n')
        with self.assertRaisesRegex(AssertionError, 'did not change'):
            host.changed_boot_id('hash OK\n01234567-89ab-cdef-0123-456789abcdef\n', '01234567-89ab-cdef-0123-456789abcdef')

    def test_guest_ssh_requires_the_verified_host_key(self):
        commands = []
        class Result:
            stdout = 'ok\n'
        with patch.object(host, 'bios_guest_ip', return_value='192.0.2.10'), patch.object(host, 'ssh', side_effect=lambda _lab, command, **_kwargs: commands.append(command) or Result()):
            host.bios_guest(Path('/lab'), 'nex-fresh-bios-' + 'a' * 24, '/fixture/client-key', '/fixture/known_hosts', 'true')
        self.assertIn('StrictHostKeyChecking=yes', commands[0])
        self.assertIn('HostKeyAlias=', commands[0])
        self.assertIn('UserKnownHostsFile=', commands[0])
        self.assertNotIn('StrictHostKeyChecking=no', commands[0])

    def test_persistence_uses_convert_never_formats_container_and_cleans_exact_target(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cache = root / '.cache'
            cache.mkdir()
            lab = cache / 'nex-fresh-install.test'
            lab.mkdir()
            (lab / 'host.raw').write_bytes(b'')
            (lab / 'config.json').write_text(json.dumps({'disposable': True, 'ports': {'ssh': 22}}))
            (lab / 'known_hosts').write_text('known')
            (lab / 'id_ed25519').write_text('key')
            image, key, host_key, evidence = root / 'guest.qcow2', root / 'guest_id_ed25519', root / 'guest_host_ed25519.pub', root / 'evidence'
            image.write_bytes(b'fixture')
            key.write_text('key'); key.chmod(0o600)
            host_key.write_text('ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFixtureHostKey fixture\n')
            evidence.mkdir()
            commands = []
            class Result:
                stdout = 'default\nOK\n'
            def ssh(_lab, command, **_kwargs):
                commands.append(command)
                return Result()
            with patch.object(host, 'ROOT', root), patch.object(host, 'validate_target', return_value=lab) as validate_target, \
                 patch.object(host, 'baseline') as baseline, patch.object(host, 'ssh', side_effect=ssh), \
                 patch.object(host, 'scp_to_host'), patch.object(host, 'wait_for_bios_guest', side_effect=['guest-before', 'guest-after', 'guest-after-host']) as wait_guest, \
                 patch.object(host, 'bios_guest', return_value='OK\n') as guest, \
                 patch.object(host, 'wait_for_host_reboot', return_value='host-after'), \
                 patch.object(host, 'fixture_names', return_value=('nex-fresh-bios-' + 'a' * 24, '52:54:00:fa:aa:aa')):
                host.bios_guest_persistence(lab, evidence, '0.1.8', image, key, host_key, hashlib.sha256(b'fixture').hexdigest())
            joined = '\n'.join(map(str, commands))
            validate_target.assert_called_once_with(lab)
            baseline.assert_called_once_with(lab, evidence, '0.1.8')
            self.assertEqual(wait_guest.call_args_list[1].args[4:], ('/root/nex-fresh-bios-persistence.sha256', 'guest-before'))
            self.assertEqual(wait_guest.call_args_list[2].args[4:6], ('/root/nex-fresh-bios-persistence.sha256', 'guest-after'))
            self.assertIn('conv=fsync', guest.call_args.args[4])
            self.assertIn('sha256sum -c', guest.call_args.args[4])
            self.assertIn('qemu-img convert -f qcow2 -O qcow2', joined)
            self.assertNotIn('mkfs.', joined)
            self.assertIn('virsh -c qemu:///system autostart', joined)
            self.assertIn('systemctl reboot', joined)
            self.assertIn('virsh -c qemu:///system shutdown', joined)
            self.assertIn('virsh -c qemu:///system undefine', joined)
            self.assertNotIn('rm -rf --', joined)
            result = json.loads((evidence / 'bios-guest-persistence.json').read_text())
            self.assertTrue(result['completed'])
            self.assertEqual(result['firmware'], 'bios')

    def test_release_failure_retains_disk_and_records_incomplete_result(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); cache = root / '.cache'; cache.mkdir(); lab = cache / 'nex-fresh-install.test'; lab.mkdir()
            (lab / 'host.raw').write_bytes(b''); (lab / 'config.json').write_text(json.dumps({'disposable': True, 'ports': {'ssh': 22}})); (lab / 'known_hosts').write_text('known'); (lab / 'id_ed25519').write_text('key')
            image, key, host_key, evidence = root / 'guest.qcow2', root / 'guest_id_ed25519', root / 'host.pub', root / 'evidence'
            image.write_bytes(b'fixture'); key.write_text('key'); key.chmod(0o600); host_key.write_text('ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFixtureHostKey\n'); evidence.mkdir()
            class Result: stdout = 'default\nOK\n'
            with patch.object(host, 'validate_target', return_value=lab), patch.object(host, 'baseline'), patch.object(host, 'ssh', return_value=Result()), patch.object(host, 'scp_to_host'), patch.object(host, 'wait_for_bios_guest', side_effect=['guest-before', 'guest-after', 'guest-after-host']), patch.object(host, 'bios_guest', return_value='OK\n'), patch.object(host, 'wait_for_host_reboot', return_value='host-after'), patch.object(host, 'fixture_names', return_value=('nex-fresh-bios-' + 'a' * 24, '52:54:00:fa:aa:aa')), patch.object(host, 'release_bios_fixture', side_effect=AssertionError('domain did not stop')):
                with self.assertRaisesRegex(AssertionError, 'domain did not stop'):
                    host.bios_guest_persistence(lab, evidence, '0.1.8', image, key, host_key, hashlib.sha256(b'fixture').hexdigest())
            record = json.loads((evidence / 'bios-guest-persistence.json').read_text())
            self.assertFalse(record['completed'])
            self.assertTrue(record['retained_fixture'])

    def test_wrong_image_digest_stops_before_baseline_or_host_commands(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); image, key, host_key = root / 'guest.qcow2', root / 'guest_id_ed25519', root / 'host.pub'
            image.write_bytes(b'fixture'); key.write_text('key'); key.chmod(0o600); host_key.write_text('ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFixtureHostKey\n')
            with patch.object(host, 'validate_target', return_value=root) as validate_target, patch.object(host, 'baseline') as baseline, patch.object(host, 'ssh') as ssh:
                with self.assertRaisesRegex(AssertionError, 'digest differs'):
                    host.bios_guest_persistence(Path('/not-used'), root, '0.1.8', image, key, host_key, '0' * 64)
            validate_target.assert_called_once()
            baseline.assert_not_called()
            ssh.assert_not_called()

    def test_missing_active_autostart_nat_stops_before_fixture_mutation(self):
        class Result: stdout = 'other-network\n'
        with patch.object(host, 'validate_target', return_value=Path('/lab')), \
             patch.object(host, 'fixture_source'), patch.object(host, 'fixture_key'), \
             patch.object(host, 'fixture_host_key'), patch.object(host, 'baseline'), \
             patch.object(host, 'ssh', return_value=Result()) as ssh, \
             patch.object(host, 'scp_to_host') as scp:
            with self.assertRaisesRegex(AssertionError, 'default NAT must already be active'):
                host.bios_guest_persistence(Path('/lab'), Path('/no-evidence'), '0.1.8', None, None, None, '0' * 64)
            ssh.assert_called_once_with(Path('/lab'), 'virsh -c qemu:///system net-list --name --autostart', text=True)
            scp.assert_not_called()


if __name__ == '__main__':
    unittest.main()
