# SPDX-License-Identifier: AGPL-3.0-only
"""Target guards of the interrupted-delete lab stage, run without a lab."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock
import shlex
import xml.etree.ElementTree as ET


sys.path.insert(0, str(Path(__file__).parent / 'integration'))
spec = importlib.util.spec_from_file_location(
    'fresh_install_recovery', Path(__file__).parent / 'integration/fresh_install_recovery.py')
recovery = importlib.util.module_from_spec(spec)
spec.loader.exec_module(recovery)

GUEST = {'id': '11111111-1111-4111-8111-111111111111', 'name': 'gate-vm-c'}
APPLIANCE = {'id': '22222222-2222-4222-8222-222222222222', 'name': 'nex-controller', 'role': 'controller', 'protected': True}
RECORDED = {v['id']: v for v in (GUEST, APPLIANCE)}


class InterruptTargetTests(unittest.TestCase):
    def test_accepts_only_the_named_recorded_guest(self):
        self.assertIs(recovery.interrupt_target(RECORDED, 'gate-vm-c', {APPLIANCE['id']}), GUEST)

    def test_refuses_missing_unknown_or_duplicate_names(self):
        for name in (None, '', 'gate-vm-a'):
            with self.assertRaises(AssertionError):
                recovery.interrupt_target(RECORDED, name, set())
        twin = {**GUEST, 'id': '33333333-3333-4333-8333-333333333333'}
        with self.assertRaisesRegex(AssertionError, 'not exactly one'):
            recovery.interrupt_target({**RECORDED, twin['id']: twin}, 'gate-vm-c', set())

    def test_refuses_the_appliance_however_it_is_marked(self):
        with self.assertRaisesRegex(AssertionError, 'Controller appliance'):
            recovery.interrupt_target(RECORDED, 'nex-controller', set())
        with self.assertRaisesRegex(AssertionError, 'Controller appliance'):
            recovery.interrupt_target({GUEST['id']: GUEST}, 'gate-vm-c', {GUEST['id']})
        unmarked = {**APPLIANCE, 'role': None, 'protected': False}
        with self.assertRaisesRegex(AssertionError, 'Controller appliance'):
            recovery.interrupt_target({unmarked['id']: unmarked}, 'nex-controller', {APPLIANCE['id']})

    def test_hold_path_is_only_this_vms_disk_or_nvram(self):
        disk = f"<domain><devices><disk device='disk'><source file='/var/lib/libvirt/images/nex-vms/{GUEST['id']}/disk-0.qcow2'/></disk></devices></domain>"
        self.assertTrue(recovery.hold_path(ET.fromstring(disk), GUEST, 'wiping').endswith('/disk-0.qcow2'))
        other = disk.replace(GUEST['id'], APPLIANCE['id'])
        with self.assertRaises(AssertionError):
            recovery.hold_path(ET.fromstring(other), GUEST, 'wiping')
        # A held cleaning-nvram fails as `failed`, which the watcher rejects, so the hold is refused.
        nvram = "<domain><os><nvram>/var/lib/libvirt/qemu/nvram/gate-vm-c_VARS.fd</nvram></os></domain>"
        with self.assertRaisesRegex(AssertionError, 'only to wiping'):
            recovery.hold_path(ET.fromstring(nvram), GUEST, 'cleaning-nvram')

    def test_only_a_pending_kill_counts_as_an_interrupted_operation(self):
        kill = lambda status: {'entry': {'stage': 'wiping', 'result': {'status': status}}}
        self.assertEqual(recovery.interrupt_case(kill('pending')), 'interrupted-pending')
        self.assertEqual(recovery.interrupt_case(kill('needs_attention')), 'held-needs-attention')

    def test_watcher_kills_only_for_a_new_entry_of_the_named_vm_at_the_stage(self):
        with tempfile.TemporaryDirectory() as directory:
            root, bin_dir = Path(directory) / 'journal', Path(directory) / 'bin'
            root.mkdir(); bin_dir.mkdir()
            systemctl = bin_dir / 'systemctl'
            systemctl.write_text('#!/bin/sh\necho "$@" >> "$(dirname "$0")/killed"\n')
            systemctl.chmod(0o755)
            def entry(op, vm_id, stage, status='pending'):
                (root / (op + '.json')).write_text(json.dumps({'command': {'operation_id': op, 'kind': 'vm-delete', 'delete': {'vm_id': vm_id}},
                                                               'stage': stage, 'result': {'status': status}}))
            entry('old', GUEST['id'], 'wiping')
            env = {**os.environ, 'PATH': f"{bin_dir}:{os.environ['PATH']}"}
            watch = subprocess.Popen([sys.executable, '-c', recovery.WATCH, str(root), 'vm-delete', GUEST['id'], 'wiping', '10'],
                                     env=env, stdout=subprocess.PIPE, text=True)
            self.assertEqual(watch.stdout.readline().strip(), 'waiting')
            entry('other-vm', APPLIANCE['id'], 'wiping')
            time.sleep(0.2)
            self.assertFalse((bin_dir / 'killed').exists(), 'killed for a pre-existing entry or another VM')
            entry('new', GUEST['id'], 'wiping')
            out, _ = watch.communicate(timeout=10)
            self.assertEqual(watch.returncode, 0)
            self.assertEqual(json.loads(out)['operation_id'], 'new')
            self.assertIn('--signal=SIGKILL nex-core-local', (bin_dir / 'killed').read_text())

    def test_watcher_refuses_an_entry_that_settled_before_the_stage(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            watch = subprocess.Popen([sys.executable, '-c', recovery.WATCH, str(root), 'vm-delete', GUEST['id'], 'wiping', '10'],
                                     stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            watch.stdout.readline()
            (root / 'done.json').write_text(json.dumps({'command': {'operation_id': 'done', 'kind': 'vm-delete', 'delete': {'vm_id': GUEST['id']}},
                                                        'stage': 'undefining', 'result': {'status': 'succeeded'}}))
            _, err = watch.communicate(timeout=10)
            self.assertNotEqual(watch.returncode, 0)
            self.assertIn('not evidence', err)


class FakeHost:
    """Answers the ssh commands of the reboot and verify-reboot stages from an in-memory libvirt."""
    def __init__(self, domains):
        self.domains, self.boot, self.rebooted = domains, 'boot-1', False

    def __call__(self, lab, command, text=False, **kwargs):
        words = shlex.split(command)
        if command == 'hostname':
            out = 'nex-fresh-lab\n'
        elif words[:3] == ['virsh', '-c', 'qemu:///system']:
            verb, uuid = words[3], words[-1]
            d = self.domains[uuid]
            out = {'domstate': d['state'] + '\n', 'dominfo': 'Autostart:      ' + ('enable' if d['autostart'] else 'disable') + '\n',
                   'dumpxml': f"<domain><uuid>{uuid}</uuid><devices/></domain>", 'destroy': ''}[verb]
        elif command == 'cat /var/lib/nex-core-agent/state.json':
            out = json.dumps({'host_id': 'host-1', 'certificate_pem': 'pem'})
        elif command == 'cat /proc/sys/kernel/random/boot_id':
            out = self.boot + '\n'
        elif command == 'systemctl reboot':
            self.rebooted, out = True, ''
        elif command.startswith('systemctl is-active'):
            out = 'active\n'
        else:
            raise AssertionError('unexpected host command ' + command)
        return subprocess.CompletedProcess(words, 0, out)


class RebootStageTests(unittest.TestCase):
    def run_stage(self, host, stage, evidence):
        argv = ['fresh_install_recovery.py', '--lab-dir', 'lab', '--stage', stage, '--evidence', str(evidence),
                '--vm-name', GUEST['name'], '--guest', 'installed']
        with mock.patch.object(recovery, 'ssh', host), mock.patch.object(recovery, 'validate_lab', lambda path: path), \
                mock.patch.object(sys, 'argv', argv), mock.patch('builtins.print'):
            recovery.main()

    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.evidence = Path(directory.name)
        (self.evidence / 'browser-managed-results.json').write_text(json.dumps({'vms': [GUEST, APPLIANCE]}))

    def host(self, appliance_state, appliance_autostart):
        return FakeHost({GUEST['id']: {'state': 'running', 'autostart': True},
                         APPLIANCE['id']: {'state': appliance_state, 'autostart': appliance_autostart}})

    def test_reboot_accepts_an_excluded_appliance_shut_off_without_autostart(self):
        host = self.host('shut off', False)
        self.run_stage(host, 'reboot', self.evidence)
        self.assertTrue(host.rebooted)
        before = json.loads((self.evidence / 'recovery-before.json').read_text())
        self.assertEqual(before['vms'][APPLIANCE['id']]['expect_running'], False)
        self.assertEqual(before['vms'][GUEST['id']]['expect_running'], True)

    def test_reboot_refuses_an_appliance_that_is_neither_autostarting_nor_excluded(self):
        for state, autostart in (('shut off', True), ('running', False)):
            host = self.host(state, autostart)
            with self.assertRaisesRegex(AssertionError, 'must autostart or be excluded'):
                self.run_stage(host, 'reboot', self.evidence)
            self.assertFalse(host.rebooted)

    def rebooted(self, flip=None):
        host = self.host('shut off', False)
        self.run_stage(host, 'reboot', self.evidence)
        host.boot = 'boot-2'
        if flip:
            host.domains[flip]['autostart'] = not host.domains[flip]['autostart']
        return host

    def test_verify_reboot_passes_with_unchanged_autostart_flags(self):
        self.run_stage(self.rebooted(), 'verify-reboot', self.evidence)
        after = json.loads((self.evidence / 'recovery-after.json').read_text())
        self.assertEqual({k: v['autostart'] for k, v in after['vms'].items()}, {GUEST['id']: True, APPLIANCE['id']: False})

    def test_verify_reboot_fails_when_an_autostart_flag_changed(self):
        for vm in (APPLIANCE, GUEST):
            with self.subTest(vm=vm['name']):
                for f in self.evidence.glob('recovery-*.json'):
                    f.unlink()
                host = self.rebooted(flip=vm['id'])
                with self.assertRaisesRegex(AssertionError, "'autostart'"):
                    self.run_stage(host, 'verify-reboot', self.evidence)
                self.assertFalse((self.evidence / 'recovery-after.json').exists())


if __name__ == '__main__':
    unittest.main()
