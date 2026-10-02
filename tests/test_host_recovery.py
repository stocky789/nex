# SPDX-License-Identifier: AGPL-3.0-only
import base64
import importlib.util
import io
import os
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
        (Path(directory) / 'deploy.json').write_text(__import__('json').dumps(journal))
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


if __name__ == '__main__':
    unittest.main()
