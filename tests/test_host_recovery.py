# SPDX-License-Identifier: AGPL-3.0-only
import base64
import importlib.util
import io
import os
import json
from pathlib import Path
import unittest
from unittest.mock import patch, MagicMock
from types import SimpleNamespace
from tempfile import TemporaryDirectory
import contextlib

spec = importlib.util.spec_from_file_location('host_recovery', Path(__file__).parents[1] / 'appliance/host-recovery.py')
recovery = importlib.util.module_from_spec(spec)
spec.loader.exec_module(recovery)


class RecoveryTransport(unittest.TestCase):
    def test_binary_archive_and_handle_cleanup(self):
        guest = recovery.Guest('qemu:///system', 'target')
        data = b'private\x00\xffarchive'
        with patch.object(guest, 'call', side_effect=[4, {'count': len(data), 'eof': True, 'buf-b64': base64.b64encode(data).decode()}, {}]) as call:
            output = io.BytesIO()
            guest.download('/archive', output)
            self.assertEqual(output.getvalue(), data)
            self.assertEqual(call.call_args.args, ('guest-file-close',))
        with patch.object(guest, 'call', side_effect=[4, {'count': 1}, {}]) as call:
            with self.assertRaisesRegex(RuntimeError, 'incomplete'):
                guest.upload('/archive', io.BytesIO(data))
            self.assertEqual(call.call_args.args, ('guest-file-close',))

    def test_guest_failure_cannot_report_completion(self):
        guest = recovery.Guest('qemu:///system', 'target')
        with patch.object(guest, 'call', side_effect=[{'pid': 9}, {'exited': True, 'exitcode': 1}]):
            with self.assertRaises(RuntimeError):
                guest.execute('/recovery', 'restore')

    def test_secrets_sent_through_stdin_only(self):
        with patch.object(recovery.subprocess, 'run') as run:
            run.return_value.returncode = 0
            run.return_value.stderr = ''
            run.return_value.stdout = 'virsh # \n<truncated-private-echo\n{"return":{}}\nvirsh # \n'
            recovery.virsh('qemu:///system', 'qemu-agent-command', 'uuid', '{"secret":"private"}')
            self.assertNotIn('private', str(run.call_args.args))
            self.assertIn('private', run.call_args.kwargs['input'])

    def test_short_libvirt_reply_strips_only_verified_prompt_frames(self):
        with patch.object(recovery.subprocess, 'run') as run:
            run.return_value.returncode = 0
            run.return_value.stderr = ''
            run.return_value.stdout = 'virsh # domstate uuid\nshut off\nvirsh # \n'
            self.assertEqual(recovery.virsh('qemu:///system', 'domstate', 'uuid'), 'shut off')
            run.return_value.stdout = 'unrecognized output'
            with self.assertRaises(RuntimeError):
                recovery.virsh('qemu:///system', 'domstate', 'uuid')

    def test_external_original_requires_explicit_exclusion(self):
        # main() sets os.umask(0o077); restore it so later tests keep the real umask.
        umask = os.umask(0)
        os.umask(umask)
        self.addCleanup(os.umask, umask)
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive = root / 'archive'
            archive.write_bytes(b'private-archive')
            args = SimpleNamespace(action='restore', vm='11111111-1111-4111-8111-111111111111', original_vm='22222222-2222-4222-8222-222222222222', original_external=True, original_stopped_and_excluded=True, replace_empty_controller=True, uri='qemu:///system', state_dir=root, storage_dir=root, archive=archive)
            with patch.object(recovery.argparse.ArgumentParser, 'parse_args', return_value=args), patch.object(recovery.os, 'geteuid', return_value=0), patch.object(recovery, 'owned') as owned, patch.object(recovery, 'Guest') as guest, patch.object(recovery, 'virsh') as virsh, contextlib.redirect_stdout(io.StringIO()):
                recovery.main()
                owned.assert_called_once_with(args.uri, args.vm, root, root)
                virsh.assert_not_called()
                self.assertTrue(guest.return_value.execute.called)
                guest.reset_mock()
                args.original_stopped_and_excluded = False
                with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                    recovery.main()
                guest.assert_not_called()

    def test_missing_ownership_rejected(self):
        from tempfile import TemporaryDirectory
        with TemporaryDirectory() as directory, patch.object(recovery, 'virsh') as virsh:
            with self.assertRaisesRegex(ValueError, 'ownership'):
                recovery.owned('qemu:///system', '11111111-1111-4111-8111-111111111111', Path(directory), Path('/storage'))
            virsh.assert_not_called()

    def profile(self, directory, system_readonly=True, data='controller-data.qcow2'):
        vm = '11111111-1111-4111-8111-111111111111'
        storage = Path('/storage')
        journal = {'vm_id': vm, 'kind': 'deploy', 'name': 'nex-controller', 'disk': str(storage / vm / 'controller-data.qcow2'),
                   'system': str(storage / vm / ('system-' + 'a' * 64 + '.img'))}
        (Path(directory) / 'deploy.json').write_text(json.dumps(journal))
        readonly = '<readonly/>' if system_readonly else ''
        xml = (f"<domain><name>nex-controller</name><uuid>{vm}</uuid><devices>"
               f"<disk type='file' device='disk'><source file='{journal['system']}'/>{readonly}</disk>"
               f"<disk type='file' device='disk'><source file='{storage / vm / data}'/></disk></devices></domain>")
        return vm, storage, xml

    def test_two_disk_profile_is_owned_when_system_is_read_only_and_data_is_the_journal_disk(self):
        with TemporaryDirectory() as directory:
            vm, storage, xml = self.profile(directory)
            with patch.object(recovery, 'virsh', return_value=xml):
                recovery.owned('qemu:///system', vm, Path(directory), storage)

    def test_two_disk_profile_rejects_a_writable_system_disk_or_a_foreign_data_disk(self):
        for kwargs in ({'system_readonly': False}, {'data': 'other.qcow2'}):
            with TemporaryDirectory() as directory:
                vm, storage, xml = self.profile(directory, **kwargs)
                with patch.object(recovery, 'virsh', return_value=xml), self.assertRaisesRegex(ValueError, 'differs'):
                    recovery.owned('qemu:///system', vm, Path(directory), storage)

    def failed_swap_profile(self, directory):
        vm, storage, xml = self.profile(directory)
        deployment = json.loads((Path(directory) / 'deploy.json').read_text())
        sha = 'b' * 64
        new = str(storage / vm / f'system-{sha}.img')
        journal = dict(deployment, id='swap-request-1', kind='swap', status='needs_attention', stage='swap_restore',
                       digest=f'swap:7:{sha}', swap={'sequence': 7, 'sha256': sha, 'old': deployment['system'],
                                                   'new': new, 'checkpoint': deployment['disk'] + '.pre-7',
                                                   'started': True, 'cause': 'new Controller was not ready'})
        (Path(directory) / 'swap-request-1.json').write_text(json.dumps(journal))
        return vm, storage, xml.replace(deployment['system'], new), journal

    def test_export_accepts_recorded_failed_swap_image_without_rewriting_deployment(self):
        umask = os.umask(0)
        os.umask(umask)
        self.addCleanup(os.umask, umask)
        with TemporaryDirectory() as directory:
            root = Path(directory)
            vm, storage, xml, _ = self.failed_swap_profile(root)
            deployment = (root / 'deploy.json').read_bytes()
            args = SimpleNamespace(action='export', vm=vm, archive=root / 'archive', state_dir=root, storage_dir=storage,
                                   uri='qemu:///system')
            with patch.object(recovery.argparse.ArgumentParser, 'parse_args', return_value=args), \
                    patch.object(recovery.os, 'geteuid', return_value=0), \
                    patch.object(recovery, 'virsh', return_value=xml), \
                    patch.object(recovery, 'Guest') as guest, contextlib.redirect_stdout(io.StringIO()):
                guest.return_value.download.side_effect = lambda remote, output: output.write(b'private-archive')
                recovery.main()
                self.assertEqual(args.archive.read_bytes(), b'private-archive')
                self.assertEqual((root / 'deploy.json').read_bytes(), deployment)
                self.assertTrue(any(call.args[:2] == ('/usr/local/lib/nex/controller-recovery', 'export')
                                    for call in guest.return_value.execute.call_args_list))

    def test_recovery_accepts_only_reachable_swap_stages_for_the_recorded_image(self):
        for status, stages in (
                ('running', ('swap_define', 'swap_start', 'swap_probe', 'swap_commit', 'swap_restore',
                             'swap_redefine', 'swap_restart', 'swap_reprobe')),
                ('needs_attention', ('swap_restore', 'swap_redefine', 'swap_restart', 'swap_reprobe'))):
            for stage in stages:
                with self.subTest(status=status, stage=stage), TemporaryDirectory() as directory:
                    vm, storage, xml, journal = self.failed_swap_profile(directory)
                    journal.update(status=status, stage=stage)
                    (Path(directory) / 'swap-request-1.json').write_text(json.dumps(journal))
                    with patch.object(recovery, 'virsh', return_value=xml):
                        recovery.owned('qemu:///system', vm, Path(directory), storage)

    def test_recovery_rejects_forged_or_foreign_swap_journal(self):
        mutations = {
            'foreign UUID': lambda j: j.update(vm_id='22222222-2222-4222-8222-222222222222'),
            'foreign name': lambda j: j.update(name='foreign-controller'),
            'foreign data': lambda j: j.update(disk=j['disk'] + '.foreign'),
            'foreign deployment system': lambda j: j.update(system=j['system'] + '.foreign'),
            'foreign bridge': lambda j: j.update(bridge='foreign-bridge'),
            'wrong digest': lambda j: j.update(digest='swap:8:' + 'b' * 64),
            'old not deployed': lambda j: j['swap'].update(old=j['system'] + '.foreign'),
            'outside storage': lambda j: j['swap'].update(new='/foreign/system-' + 'b' * 64 + '.img'),
            'arbitrary name': lambda j: j['swap'].update(new=str(Path(j['disk']).parent / 'unproven.img')),
            'wrong SHA': lambda j: j['swap'].update(sha256='c' * 64),
            'wrong checkpoint': lambda j: j['swap'].update(checkpoint=j['disk'] + '.foreign'),
            'invalid sequence': lambda j: j['swap'].update(sequence=True),
            'before defining': lambda j: j.update(status='running', stage='swap_checkpoint'),
            'failed without changing': lambda j: j.update(status='failed', stage='swap_shutdown'),
            'already rolled back': lambda j: j.update(status='rolled_back', stage='swap_rolled_back'),
            'already committed': lambda j: j.update(status='completed', stage='swap_committed'),
            'missing swap': lambda j: j.pop('swap'),
            'wrong operation ID': lambda j: j.update(id='forged-request'),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name), TemporaryDirectory() as directory:
                vm, storage, xml, journal = self.failed_swap_profile(directory)
                mutate(journal)
                (Path(directory) / 'swap-request-1.json').write_text(json.dumps(journal))
                with patch.object(recovery, 'virsh', return_value=xml), self.assertRaisesRegex(ValueError, 'differs'):
                    recovery.owned('qemu:///system', vm, Path(directory), storage)

    def test_recorded_swap_does_not_authorize_other_images_or_changed_disk_layout(self):
        for change in ('unrecorded image', 'outside storage', 'writable system', 'foreign data', 'foreign UUID'):
            with self.subTest(change=change), TemporaryDirectory() as directory:
                vm, storage, xml, journal = self.failed_swap_profile(directory)
                if change == 'unrecorded image':
                    xml = xml.replace(journal['swap']['new'], str(storage / vm / ('system-' + 'c' * 64 + '.img')))
                elif change == 'outside storage':
                    xml = xml.replace(journal['swap']['new'], '/foreign/system-' + 'b' * 64 + '.img')
                elif change == 'writable system':
                    xml = xml.replace('<readonly/>', '')
                elif change == 'foreign data':
                    xml = xml.replace(journal['disk'], journal['disk'] + '.foreign')
                else:
                    xml = xml.replace(f'<uuid>{vm}</uuid>', '<uuid>22222222-2222-4222-8222-222222222222</uuid>')
                with patch.object(recovery, 'virsh', return_value=xml), self.assertRaisesRegex(ValueError, 'differs'):
                    recovery.owned('qemu:///system', vm, Path(directory), storage)

    def test_both_live_and_persistent_definitions_must_use_proven_images(self):
        with TemporaryDirectory() as directory:
            vm, storage, xml, journal = self.failed_swap_profile(directory)
            old = xml.replace(journal['swap']['new'], journal['swap']['old'])
            foreign = xml.replace(journal['swap']['new'], journal['swap']['new'] + '.foreign')
            with patch.object(recovery, 'virsh', side_effect=[xml, old]):
                recovery.owned('qemu:///system', vm, Path(directory), storage)
            for definitions in ([xml, foreign], [foreign, xml]):
                with patch.object(recovery, 'virsh', side_effect=definitions), self.assertRaisesRegex(ValueError, 'differs'):
                    recovery.owned('qemu:///system', vm, Path(directory), storage)


if __name__ == '__main__':
    unittest.main()
