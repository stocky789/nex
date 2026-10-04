#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Regression tests for the pure safety logic of the image lab drivers (no Docker, KVM or QEMU): exit codes, pid
handling, destructive-path guards, the Secure Boot verdict helpers and the repro-check snapshot/digest gates."""
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import textwrap
import time
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
INTEGRATION = ROOT / 'tests/integration'
sys.path.insert(0, str(INTEGRATION))


def load(name):
    spec = importlib.util.spec_from_file_location(f'labdrv_{name}', INTEGRATION / f'{name}.py')
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class TempCase(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory(prefix='nex-lab-drivers-')
        self.addCleanup(tmp.cleanup)
        self.tmp = Path(tmp.name)

    def run_main(self, mod, *argv):
        with mock.patch.object(sys, 'argv', ['driver', *argv]):
            mod.main()


class PoolRolloutLabTests(TempCase):
    @classmethod
    def setUpClass(cls):
        cls.mod = load('pool_rollout_lab')

    def spawn(self, lab='lab-a'):
        args = [sys.executable, '-c', 'import time; time.sleep(60)', '--disk', str(self.tmp / lab / 'd0.img')]
        p = subprocess.Popen(args)
        self.addCleanup(lambda: (p.kill(), p.wait()))
        identity = self.mod.capture_process(p.pid, args)
        return p, identity

    def test_main_exits_non_zero_when_a_check_failed(self):
        with mock.patch.object(self.mod, 'cmd_down'), mock.patch.object(sys, 'argv', ['x', 'down']):
            with mock.patch.object(self.mod.upd.check, 'failed', 1):
                with self.assertRaises(SystemExit) as raised:
                    self.mod.main()
                self.assertEqual(raised.exception.code, 1)
            with mock.patch.object(self.mod.upd.check, 'failed', 0):
                self.mod.main()

    def test_owned_process_action_uses_the_captured_instance(self):
        p, identity = self.spawn()
        self.assertTrue(self.mod.is_ours(identity))
        self.assertTrue(self.mod.kill_pid(identity))
        self.assertEqual(p.wait(timeout=5), -signal.SIGKILL)
        self.assertFalse(self.mod.kill_pid(identity))
        self.assertFalse(self.mod.kill_pid(None))

    def test_stale_start_identity_never_signals_the_live_process(self):
        p, identity = self.spawn()
        stale = dict(identity, start_time=identity['start_time'] + 1)
        self.assertFalse(self.mod.is_ours(stale))
        self.assertFalse(self.mod.kill_pid(stale))
        self.assertIsNone(p.poll())

    def test_same_binary_in_another_lab_is_neither_trusted_nor_signalled(self):
        _, identity = self.spawn('lab-a')
        other, foreign = self.spawn('lab-b')
        stale = dict(identity, pid=foreign['pid'], start_time=foreign['start_time'])
        self.assertFalse(self.mod.kill_pid(stale))
        self.assertIsNone(other.poll())
        pidfile = self.tmp / 'qemu.pid'
        pidfile.write_text(str(other.pid))
        Path(str(pidfile) + '.identity.json').write_text(json.dumps(foreign))
        self.assertIsNone(self.mod.read_pidfile(str(pidfile), identity['argv']))
        self.assertFalse(self.mod.kill_qemu(str(pidfile), identity['argv']))
        self.assertIsNone(other.poll())

    def test_pidfile_is_removed_after_an_owned_cut(self):
        p, identity = self.spawn()
        pidfile = self.tmp / 'qemu.pid'
        pidfile.write_text(str(p.pid))
        Path(str(pidfile) + '.identity.json').write_text(json.dumps(identity))
        self.assertEqual(self.mod.read_pidfile(str(pidfile), identity['argv']), p.pid)
        self.assertTrue(self.mod.kill_qemu(str(pidfile), identity['argv']))
        self.assertFalse(pidfile.exists())
        p.wait(timeout=5)
        self.assertFalse(self.mod.kill_qemu(str(pidfile), identity['argv']))

    def test_pid_only_record_cannot_authorize_a_signal(self):
        p, identity = self.spawn()
        pidfile = self.tmp / 'qemu.pid'
        pidfile.write_text(str(p.pid))
        self.assertIsNone(self.mod.read_pidfile(str(pidfile), identity['argv']))
        self.assertFalse(self.mod.kill_pid(p.pid))
        self.assertFalse(self.mod.kill_qemu(str(pidfile), identity['argv']))
        self.assertIsNone(p.poll())
        self.assertTrue(pidfile.exists())

    def test_start_refuses_to_replace_an_unowned_live_pidfile(self):
        p, _ = self.spawn()
        hostdir = self.tmp / 'h1'
        hostdir.mkdir()
        (hostdir / 'qemu.pid').write_text(str(p.pid))
        with mock.patch.object(self.mod, 'WORK', str(self.tmp)), \
                mock.patch.object(self.mod.subprocess, 'Popen') as launch:
            host = self.mod.Host({'n': 1, 'loop': '127.0.0.11', 'ip': '10.77.0.11'}, {})
            with self.assertRaisesRegex(RuntimeError, 'unowned live process'):
                host.start()
        launch.assert_not_called()
        self.assertIsNone(p.poll())

    def test_wrapped_start_captures_and_signals_the_final_child_command(self):
        hostdir = self.tmp / 'h1'
        hostdir.mkdir()
        argv = [sys.executable, '-c',
                'import os,sys,time; from pathlib import Path; '
                'Path(sys.argv[1]).write_text(str(os.getpid())); time.sleep(60)',
                str(hostdir / 'qemu.pid'), str(hostdir / 'd0.img')]
        launched = []
        popen = subprocess.Popen

        def launch(*args, **kwargs):
            p = popen(*args, **kwargs)
            launched.append(p)
            self.addCleanup(lambda: (p.kill(), p.wait()))
            return p

        with mock.patch.object(self.mod, 'WORK', str(self.tmp)), \
                mock.patch.object(self.mod.subprocess, 'Popen', side_effect=launch):
            host = self.mod.Host({'n': 1}, {})
            with mock.patch.object(host, 'qemu_cmd', return_value=argv):
                host.start()
                p = launched[0]
                # Capturing exec can precede the child's pidfile write.
                for _ in range(200):
                    if host.pid() is not None:
                        break
                    time.sleep(0.01)
                self.assertEqual(host.pid(), p.pid)
                self.assertTrue(self.mod.kill_qemu(str(hostdir / 'qemu.pid'), argv))
                self.assertEqual(p.wait(timeout=5), -signal.SIGKILL)

    def test_missing_pidfd_support_fails_closed(self):
        p, identity = self.spawn()
        with mock.patch.object(self.mod.os, 'pidfd_open', side_effect=OSError('not supported')):
            self.assertFalse(self.mod.kill_pid(identity))
        self.assertIsNone(p.poll())

    def test_exit_between_identity_check_and_signal_cannot_hit_another_instance(self):
        p, identity = self.spawn('lab-a')
        other, foreign = self.spawn('lab-b')

        def exit_after_check(_):
            p.kill()
            p.wait()
            identity['pid'] = foreign['pid']
            return True

        with mock.patch.object(self.mod, 'is_ours', side_effect=exit_after_check):
            self.assertFalse(self.mod.kill_pid(identity))
        self.assertIsNone(other.poll())

    def resume(self, st):
        calls = []
        saved = []
        with mock.patch.object(self.mod, 'state', return_value=st), \
                mock.patch.object(self.mod, 'save', side_effect=saved.append), \
                mock.patch.object(self.mod, 'start_services', side_effect=lambda s: calls.append('services')), \
                mock.patch.object(self.mod, 'start_bmc', side_effect=lambda s: calls.append('bmc')), \
                mock.patch.object(self.mod, 'start_controller', side_effect=lambda s: calls.append('controller')), \
                mock.patch.object(self.mod, 'Host') as host:
            host.return_value.wait_shell.return_value = True
            with mock.patch('sys.stdout', new=io.StringIO()):
                self.mod.cmd_resume(mock.Mock(budget=1))
        return calls, saved

    def test_resume_restarts_the_bmc_only_when_it_was_running(self):
        hosts = [{'n': n} for n in (1, 2, 3)]
        with mock.patch.object(self.mod, 'WORK', str(self.tmp)):
            calls, saved = self.resume({'hosts': hosts, 'bmc_pid': 2147483647, 'pids': []})
            self.assertEqual(calls, ['services', 'bmc', 'controller'])
            calls, _ = self.resume({'hosts': hosts, 'pids': []})
            self.assertEqual(calls, ['services', 'controller'])

    def test_resume_refuses_to_duplicate_live_lab_processes(self):
        p, identity = self.spawn()
        with mock.patch.object(self.mod, 'state', return_value={'pids': [identity]}), \
                mock.patch.object(self.mod, 'start_services') as services:
            with self.assertRaisesRegex(SystemExit, 'still running'):
                self.mod.cmd_resume(mock.Mock(budget=1))
        services.assert_not_called()
        self.assertIsNone(p.poll())

    def test_resume_refuses_every_live_legacy_record_before_state_or_service_changes(self):
        p, _ = self.spawn()
        for field in ('pids', 'controller_pid', 'bmc_pid', 'qemu.pid'):
            st = {'hosts': [{'n': 1}], 'pids': []}
            hostdir = self.tmp / 'h1'
            hostdir.mkdir(exist_ok=True)
            pidfile = hostdir / 'qemu.pid'
            if field == 'qemu.pid':
                pidfile.write_text(str(p.pid))
            else:
                pidfile.unlink(missing_ok=True)
                st[field] = [p.pid] if field == 'pids' else p.pid
            before = json.loads(json.dumps(st))
            with self.subTest(field=field), mock.patch.object(self.mod, 'WORK', str(self.tmp)), \
                    mock.patch.object(self.mod, 'state', return_value=st), \
                    mock.patch.object(self.mod, 'save') as save, \
                    mock.patch.object(self.mod, 'start_services') as services, \
                    mock.patch.object(self.mod, 'start_controller') as controller, \
                    mock.patch.object(self.mod, 'start_bmc') as bmc:
                with self.assertRaisesRegex(SystemExit, 'no resume changes'):
                    self.mod.cmd_resume(mock.Mock(budget=1))
            self.assertEqual(st, before)
            save.assert_not_called()
            services.assert_not_called()
            controller.assert_not_called()
            bmc.assert_not_called()
            self.assertIsNone(p.poll())


class SignAndIsoGuardTests(TempCase):
    def release(self):
        rel = self.tmp / 'release'
        rel.mkdir()
        (rel / 'manifest.json').write_text('{"files": {}}')
        (rel / 'nex.efi').write_text('uki')
        return rel

    def test_sign_release_refuses_an_out_that_is_or_contains_or_lies_in_the_release(self):
        mod = load('image_sign_release')
        rel = self.release()
        for out in (rel, rel / 'sub', self.tmp):
            with self.subTest(out=out), self.assertRaisesRegex(SystemExit, 'inside|contains|is,'):
                self.run_main(mod, '--release', str(rel), '--out', str(out), '--key', 'k', '--cert', 'c')
        self.assertTrue((rel / 'nex.efi').exists())

    def test_sign_release_deletes_only_a_marker_bearing_out(self):
        mod = load('image_sign_release')
        rel = self.release()
        out = self.tmp / 'out'
        out.mkdir()
        (out / 'precious').write_text('x')
        with self.assertRaisesRegex(SystemExit, 'not created by this script'):
            self.run_main(mod, '--release', str(rel), '--out', str(out), '--key', 'k', '--cert', 'c')
        self.assertTrue((out / 'precious').exists())
        Path(str(out) + mod.SUFFIX).write_text('')
        with mock.patch.object(mod.subprocess, 'run', side_effect=RuntimeError('stop at docker')):
            with self.assertRaisesRegex(RuntimeError, 'stop at docker'):
                self.run_main(mod, '--release', str(rel), '--out', str(out), '--key', 'k', '--cert', 'c')
        self.assertFalse((out / 'precious').exists())
        self.assertTrue((out / 'nex.efi').exists())

    def sealed_release(self):
        import hashlib
        import json
        rel = self.tmp / 'sealed'
        rel.mkdir()
        roothash = 'ab' * 32
        files = {'root.erofs': b'erofs', 'root.verity': b'verity', 'roothash': (roothash + '\n').encode(),
                 'nex-core_2026.1_1.efi': b'uki'}
        for name, data in files.items():
            (rel / name).write_bytes(data)
        manifest = {'label': '2026.1', 'sequence': 1, 'roothash': roothash,
                    'files': {n: {'sha256': hashlib.sha256(d).hexdigest(), 'size': len(d)} for n, d in files.items()}}
        (rel / 'manifest.json').write_text(json.dumps(manifest))
        return rel

    def test_a_signed_output_dir_passes_the_publish_release_check(self):
        import json
        mod = load('image_sign_release')
        publish = importlib.util.spec_from_file_location('labdrv_update_publish', ROOT / 'scripts/update-publish.py')
        up = importlib.util.module_from_spec(publish)
        sys.modules[publish.name] = up
        publish.loader.exec_module(up)
        rel = self.sealed_release()

        def fake_sbsign(cmd, **kw):  # the docker sbsign step: write <uki>.signed beside the UKI
            src = Path(cmd[-1].replace('/o/', str(out) + '/'))
            Path(str(src) + '.signed').write_bytes(src.read_bytes() + b'SIGNATURE')
        for tamper in (False, True):
            out = self.tmp / ('tampered' if tamper else 'signed')
            argv = ['--release', str(rel), '--out', str(out), '--key', 'k', '--cert', 'c'] + (['--tamper'] if tamper else [])
            with mock.patch.object(mod.subprocess, 'run', side_effect=fake_sbsign), mock.patch('sys.stdout', new=io.StringIO()):
                self.run_main(mod, *argv)
            with self.subTest(tamper=tamper):
                up.check_release_dir(out, json.loads((out / 'manifest.json').read_text()))

    def test_iso_lab_refuses_out_or_work_that_overlaps_the_release(self):
        mod = load('image_iso_lab')
        rel = self.release()
        base = ['--release', str(rel), '--hostimage', 'h']
        for flag, other, value in (('--out', '--work', rel), ('--out', '--work', rel / 'sub'), ('--out', '--work', self.tmp),
                                   ('--work', '--out', rel), ('--work', '--out', rel / 'sub'), ('--work', '--out', self.tmp)):
            args = base + [flag, str(value), other, str(self.tmp / 'elsewhere')]
            with self.subTest(flag=flag, value=value), self.assertRaisesRegex(SystemExit, 'inside|contains|is,'):
                self.run_main(mod, *args)
        self.assertTrue((rel / 'nex.efi').exists())

    def test_iso_lab_deletes_only_a_marker_bearing_work_dir(self):
        mod = load('image_iso_lab')
        rel = self.release()
        work = self.tmp / 'work'
        work.mkdir()
        (work / 'precious').write_text('x')
        args = ['--release', str(rel), '--hostimage', 'h', '--out', str(self.tmp / 'iso'), '--work', str(work)]
        with self.assertRaisesRegex(SystemExit, 'not created by this script'):
            self.run_main(mod, *args)
        self.assertTrue((work / 'precious').exists())
        (work / '.image_iso_lab').write_text('')
        with mock.patch.object(mod, 'run', side_effect=RuntimeError('stop at verify')):
            with self.assertRaisesRegex(RuntimeError, 'stop at verify'):
                self.run_main(mod, *args)
        self.assertFalse((work / 'precious').exists())
        self.assertTrue((work / '.image_iso_lab').exists())


class SecureBootLabTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.mod = load('image_sb_lab')

    def test_empty_output_is_neither_enforcing_nor_a_valid_off(self):
        m = self.mod
        self.assertFalse(m.sb_enforcing(''))
        self.assertFalse(m.sb_off(''))  # a failed query proves nothing
        self.assertFalse(m.sb_off('  \n'))

    def test_enforcing_needs_bootctls_enabled_line(self):
        m = self.mod
        self.assertTrue(m.sb_enforcing('Secure Boot: enabled (user) | none integrity [confidentiality]'))
        self.assertFalse(m.sb_enforcing('Secure Boot: disabled | lockdown: [none] integrity'))
        self.assertFalse(m.sb_enforcing('dmesg: Lockdown: integrity; enabled something'))
        self.assertTrue(m.sb_off('Secure Boot: disabled'))
        self.assertFalse(m.sb_off('Secure Boot: enabled (user)'))

    def test_unknown_conflicting_or_failed_observation_never_passes(self):
        m = self.mod
        for text in ('bootctl: command not found', 'Permission denied', 'lockdown: [none]',
                     'Secure Boot: enabled | Secure Boot: disabled',
                     'Secure Boot: disabled | Secure Boot: unknown',
                     'Secure Boot: enabledish'):
            with self.subTest(text=text):
                self.assertFalse(m.sb_off(text))
                self.assertFalse(m.sb_enforcing(text))
        for text in ('Secure Boot: disabled', 'Secure Boot: enabled (user)'):
            ctl = mock.Mock()
            ctl.sh.return_value = (1, text)
            observed = m.secure_state(ctl)
            self.assertFalse(m.sb_off(observed))
            self.assertFalse(m.sb_enforcing(observed))

    def test_successful_query_preserves_explicit_firmware_state(self):
        for text, enabled in (('Secure Boot: disabled', False), ('Secure Boot: enabled (user)', True)):
            ctl = mock.Mock()
            ctl.sh.return_value = (0, text)
            observed = self.mod.secure_state(ctl)
            self.assertEqual(self.mod.sb_enforcing(observed), enabled)
            self.assertEqual(self.mod.sb_off(observed), not enabled)

    def test_a_missing_tampered_fixture_is_a_failed_check_and_boots_nothing(self):
        m = self.mod
        results = []
        with mock.patch.object(m, 'check', side_effect=lambda ok, what, detail='': results.append(ok)), \
                mock.patch.object(m, 'refused_boot') as boot, mock.patch.object(m, 'put_uki') as put:
            m.check_tampered(mock.Mock(), '/work', '/nonexistent/release', 'nex.efi')
        self.assertEqual(results, [False])
        boot.assert_not_called()
        put.assert_not_called()


class UpdateStateGuardLabTests(TempCase):
    @classmethod
    def setUpClass(cls):
        cls.mod = load('image_update')

    def test_format3_skips_only_the_incompatible_negative_proof_without_a_pass(self):
        vm, same_data = mock.Mock(), mock.Mock()
        with mock.patch.object(self.mod, 'info') as info, mock.patch.object(self.mod, 'check') as check:
            self.mod.prove_state_guard(vm, 1, same_data, 3)
        vm.ctl.sh.assert_not_called()
        vm.ctl.send.assert_not_called()
        check.assert_not_called()
        same_data.assert_not_called()
        self.assertIn('not covered', info.call_args.args[0])

    def read_metadata(self, current, retained):
        current_file, retained_file = self.tmp / 'current-release', self.tmp / 'retained-release'
        current_file.write_text(current)
        if retained is None:
            retained_file.unlink(missing_ok=True)
        else:
            retained_file.write_text(retained)
        return subprocess.run(['sh', '-c', self.mod.STATE_GUARD_METADATA, 'sh',
                               str(current_file), str(retained_file)], capture_output=True, text=True)

    def test_executable_metadata_reader_selects_only_actual_format2_older_image(self):
        for current_format, retained_format, expected in ((3, 2, True), (3, 3, False), (2, 2, False)):
            result = self.read_metadata(f'BUILD_ID="2026.10.2-2"\nNEX_STATE_FORMAT={current_format}\n',
                                        f'BUILD_ID="2026.10.1-1"\nNEX_STATE_FORMAT={retained_format}\n')
            with self.subTest(current=current_format, retained=retained_format):
                self.assertEqual(self.mod.state_guard_images_match(result.returncode, result.stdout), expected)
        # Exercise only admission to the synthetic-write boundary, not a simulated boot/refusal proof.
        result = self.read_metadata('BUILD_ID=2026.10.2-2\nNEX_STATE_FORMAT=3\n',
                                    'BUILD_ID=2026.10.1-1\nNEX_STATE_FORMAT=2\n')
        vm = mock.Mock()
        vm.ctl.sh.side_effect = [(result.returncode, result.stdout), RuntimeError('synthetic-write boundary')]
        with mock.patch.object(self.mod, 'status', return_value={'slots': [{'sequence': 1, 'slot': 'a'}]}), \
                mock.patch.object(self.mod, 'check') as check, \
                self.assertRaisesRegex(RuntimeError, 'synthetic-write boundary'):
            self.mod.prove_state_guard(vm, 1, mock.Mock(), 2)
        check.assert_not_called()
        vm.ctl.send.assert_not_called()

    def test_metadata_probe_cleans_up_without_changing_the_control_shell(self):
        current = self.tmp / 'current'
        current.write_text('BUILD_ID=2026.10.2-2\nNEX_STATE_FORMAT=3\n')
        root = self.tmp / 'retained'
        (root / 'usr/lib').mkdir(parents=True)
        (root / 'usr/lib/os-release').write_text('BUILD_ID=2026.10.1-1\nNEX_STATE_FORMAT=2\n')
        log = self.tmp / 'cleanup'
        prepare = """
set +e
m=parent-marker
trap 'printf "parent\\n" >> "$fixture_log"' EXIT
mktemp() { printf '%s\\n' "$fixture_root"; }
mount() { return "$fixture_status"; }
umount() { printf '%s\\n' "$1" >> "$fixture_log"; }
rmdir() { :; }
sh() { command sh -c "$2" sh "$fixture_current" "$fixture_root/usr/lib/os-release"; }
"""
        followup = """
probe_rc=$?
printf '\\n__PROBE_RC:%s\\n' "$probe_rc"
printf '__PARENT:%s\\n' "$m"
cat "$fixture_log"
unset probe_unset
printf '__UNSET:%s\\n' "$probe_unset"
false
printf '__ALIVE__\\n'
"""
        for mount_status in (0, 32):
            log.unlink(missing_ok=True)
            observed = []

            def execute(command, timeout=60):
                if observed:
                    raise RuntimeError('synthetic-write boundary')
                result = subprocess.run(
                    ['sh', '-c', prepare + command + ';' + followup],
                    env={**os.environ, 'fixture_root': str(root), 'fixture_current': str(current),
                         'fixture_log': str(log), 'fixture_status': str(mount_status)},
                    capture_output=True, text=True, timeout=5)
                observed.append(result)
                metadata, separator, tail = result.stdout.partition('\n__PROBE_RC:')
                self.assertTrue(separator, result.stderr)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('__PARENT:parent-marker\n', tail)
                self.assertIn(str(root) + '\n', tail)
                self.assertIn('__UNSET:\n__ALIVE__\n', tail)
                self.assertEqual(log.read_text().splitlines(), [str(root), 'parent'])
                return int(tail.splitlines()[0]), metadata

            vm = mock.Mock()
            vm.ctl.sh.side_effect = execute
            error = RuntimeError if mount_status == 0 else SystemExit
            message = 'synthetic-write boundary' if mount_status == 0 else 'no downgrade proof attempted'
            with self.subTest(mount_status=mount_status), \
                    mock.patch.object(self.mod, 'status', return_value={'slots': [{'sequence': 1, 'slot': 'a'}]}), \
                    self.assertRaisesRegex(error, message):
                self.mod.prove_state_guard(vm, 1, mock.Mock(), 2)

    def test_failed_malformed_or_incompatible_metadata_never_reaches_synthetic_write(self):
        for retained in (None, 'BUILD_ID=2026.10.1-1\nNEX_STATE_FORMAT=3\n',
                         'BUILD_ID=2026.10.1-1\n', 'NEX_STATE_FORMAT=2\n',
                         'BUILD_ID=2026.10.1-1\nNEX_STATE_FORMAT=2\nNEX_STATE_FORMAT=3\n',
                         'BUILD_ID=2026.10.1-1\nNEX_STATE_FORMAT="2\n'):
            result = self.read_metadata('BUILD_ID=2026.10.2-2\nNEX_STATE_FORMAT=3\n', retained)
            vm = mock.Mock()
            vm.ctl.sh.return_value = (result.returncode, result.stdout)
            with self.subTest(retained=retained), \
                    mock.patch.object(self.mod, 'status', return_value={'slots': [{'sequence': 1, 'slot': 'a'}]}), \
                    self.assertRaisesRegex(SystemExit, 'no downgrade proof attempted'):
                self.mod.prove_state_guard(vm, 1, mock.Mock(), 2)
            self.assertEqual(vm.ctl.sh.call_count, 1)
            vm.ctl.send.assert_not_called()


class CoreApiWorkdirTests(TempCase):
    def run_failing(self, *extra):
        mod = load('image_core_api')
        fresh = self.tmp / 'fresh'
        fresh.mkdir()
        rel = self.tmp / 'release'
        rel.mkdir()
        vm = mock.Mock()
        with mock.patch.object(mod.lab, 'require_host_tools'), \
                mock.patch.object(mod.lab, 'hostimage_for', return_value='h'), \
                mock.patch.object(mod.lab, 'scratch_dir', return_value=str(fresh)), \
                mock.patch.object(mod.lab, 'DISK_BYTES', 'not-an-int'), \
                mock.patch.object(mod.upd, 'VM', return_value=vm), \
                self.assertRaises(TypeError):
            self.run_main(mod, '--release', str(rel), *extra)
        vm.stop.assert_called_once()
        return fresh

    def test_a_work_dir_the_script_created_is_removed(self):
        self.assertFalse(self.run_failing().exists())

    def test_a_caller_supplied_work_dir_is_kept(self):
        given = self.tmp / 'given'
        given.mkdir()
        (given / 'mine').write_text('x')
        self.run_failing('--workdir', str(given))
        self.assertTrue((given / 'mine').exists())


class ReproSourceSnapshotTests(TempCase):
    def test_both_builds_use_one_frozen_git_snapshot_without_rsync(self):
        repo = self.tmp / 'work tree'
        (repo / 'scripts').mkdir(parents=True)
        (repo / 'image').mkdir()
        script = repo / 'scripts/image-repro-check.sh'
        shutil.copy2(ROOT / 'scripts/image-repro-check.sh', script)
        (repo / 'image/snapshot').write_text('20260921T000000Z\n')
        (repo / '.gitignore').write_text('ignored/\n')
        tracked = repo / 'tracked file'
        tracked.write_text('tracked bytes\n')
        tracked.chmod(0o751)
        missing = repo / 'deleted tracked file'
        missing.write_text('deleted\n')
        (repo / 'ignored').mkdir()
        (repo / 'ignored/tracked').write_text('tracked despite ignore\n')
        (repo / 'tracked link').symlink_to('tracked file')
        (repo / 'dangling link').symlink_to('missing target')
        subprocess.run(['git', 'init', '-q', str(repo)], check=True, capture_output=True)
        subprocess.run(['git', 'add', '.'], cwd=repo, check=True, capture_output=True)
        subprocess.run(['git', 'add', '-f', 'ignored/tracked'], cwd=repo, check=True, capture_output=True)
        missing.unlink()
        tracked.write_text('changed worktree bytes\n')
        (repo / 'untracked file').write_text('untracked bytes\n')
        (repo / '-leading option').write_text('literal filename\n')
        (repo / 'line\nbreak').write_text('newline filename\n')
        (repo / 'ignored/untracked').write_text('must not be copied\n')
        os.utime(tracked, ns=(1700000000000000001, 1700000000000000001))
        (repo / 'ignored').chmod(0o751)
        os.utime(repo / 'ignored', ns=(1700000001000000001, 1700000001000000001))

        builder = repo / 'image/build.sh'
        builder.write_text(textwrap.dedent('''\
            #!/bin/sh
            set -eu
            [ "$1" = base ] || exit 0
            cd "$(dirname "$0")/.."
            pwd > "$NEX_IMAGE_OUT/source-path"
            tar -cf "$NEX_IMAGE_OUT/root.erofs" -- "tracked file" "untracked file" \
                "tracked link" "dangling link" "ignored/tracked" "-leading option" "line
            break"
            printf 'edited during build\\n' > "$ORIGINAL_REPO/tracked file"
            chmod 600 "$ORIGINAL_REPO/tracked file"
            rm "$ORIGINAL_REPO/tracked link"
            ln -s "different target" "$ORIGINAL_REPO/tracked link"
            printf 'edited during build\\n' > "$ORIGINAL_REPO/untracked file"
            '''))
        builder.chmod(0o755)

        tools = self.tmp / 'tools'
        tools.mkdir()
        # Execute container shell commands locally; only the Docker boundary is simulated.
        docker = tools / 'docker'
        docker.write_text(f'#!{sys.executable}\n' + textwrap.dedent('''\
            import subprocess
            import sys
            args = sys.argv[1:]
            if args[:2] == ['image', 'inspect']:
                print('local-test-tools')
                sys.exit(0)
            mount = args[args.index('-v') + 1].removesuffix(':/r')
            command = args[-1].replace('/r/', mount + '/')
            sys.exit(subprocess.run(['sh', '-c', command]).returncode)
            '''))
        docker.chmod(0o755)
        for name, body in [('go', 'exit 0'), ('rsync', 'exit 97')]:
            tool = tools / name
            tool.write_text('#!/bin/sh\n' + body + '\n')
            tool.chmod(0o755)
        report = self.tmp / 'report'
        env = {key: value for key, value in os.environ.items() if not key.startswith('NEX_')}
        env.update(PATH=str(tools) + os.pathsep + env['PATH'], NEX_DOCKER=str(docker),
                   NEX_TOOLS_IMAGE='local-test-tools', ORIGINAL_REPO=str(repo))
        # A restrictive umask must not strip the source's executable permission bits.
        result = subprocess.run(['sh', '-c', 'umask 077; exec sh "$1" --report "$2"',
                                 'snapshot-test', str(script), str(report)],
                                env=env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        source = report / 'src'
        self.assertEqual((source / 'tracked file').read_text(), 'changed worktree bytes\n')
        self.assertEqual((source / 'tracked file').stat().st_mode & 0o777, 0o751)
        self.assertEqual((source / 'tracked file').stat().st_mtime_ns, 1700000000000000001)
        self.assertEqual((source / 'untracked file').read_text(), 'untracked bytes\n')
        self.assertEqual((source / '-leading option').read_text(), 'literal filename\n')
        self.assertEqual((source / 'line\nbreak').read_text(), 'newline filename\n')
        self.assertEqual((source / 'ignored/tracked').read_text(), 'tracked despite ignore\n')
        self.assertEqual((source / 'ignored').stat().st_mode & 0o777, 0o751)
        self.assertEqual((source / 'ignored').stat().st_mtime_ns, 1700000001000000001)
        self.assertEqual(os.readlink(source / 'tracked link'), 'tracked file')
        self.assertEqual(os.readlink(source / 'dangling link'), 'missing target')
        self.assertFalse((source / 'deleted tracked file').exists())
        self.assertFalse((source / 'ignored/untracked').exists())
        self.assertFalse((source / '.git').exists())
        self.assertEqual(tracked.read_text(), 'edited during build\n')
        self.assertEqual(tracked.stat().st_mode & 0o777, 0o600)
        self.assertEqual(os.readlink(repo / 'tracked link'), 'different target')
        for run in ('run1', 'run2'):
            self.assertEqual((report / run / 'source-path').read_text().strip(), str(source))
        self.assertEqual((report / 'run1/root.erofs').read_bytes(), (report / 'run2/root.erofs').read_bytes())
        self.assertTrue((report / 'summary.tsv').read_text().strip().endswith('\tyes'))


class ReproDigestGateTests(TempCase):
    def digest_of(self, docker_body, artifact='a.raw', setup=None):
        script = (ROOT / 'scripts/image-repro-check.sh').read_text()
        start = script.index("fn='manifest()")
        end = script.index('\nis_digest()')
        end = script.index('\n', end + 1)
        report = self.tmp / 'report'
        for run in ('run1', 'run2'):
            (report / run).mkdir(parents=True, exist_ok=True)
        if setup:
            setup(report)
        stub = self.tmp / 'docker'
        # the real command line is: run --rm <caps> -v <report>:/r <tools> sh -c <script>
        stub.write_text('#!/bin/sh\n' + docker_body)
        stub.chmod(0o755)
        driver = textwrap.dedent('''\
            docker=%s caps="" tools=t report=%s
            %s
            h=$(digest_of 1 %s)
            if is_digest "$h"; then echo "VALID:$h"; else echo "INVALID:$h"; fi
            ''') % (stub, report, script[start:end], artifact)
        out = subprocess.run(['sh', '-c', driver], capture_output=True, text=True, check=True)
        return out.stdout.strip()

    # runs the script handed to `sh -c` (last argument) on the host, with /r mapped to the report directory
    RUN_HERE = 'for last; do :; done\nexec sh -c "$(printf %s "$last" | sed "s#/r/#$REPORT/#g")"\n'

    def test_a_present_artifact_gets_its_plain_sha256(self):
        report = self.tmp / 'report'

        def setup(r):
            (r / 'run1' / 'a.raw').write_bytes(b'abc')
        body = f'REPORT={report}\n' + self.RUN_HERE
        got = self.digest_of(body, setup=setup)
        self.assertEqual(got, 'VALID:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad')

    def test_a_missing_artifact_is_not_a_digest(self):
        report = self.tmp / 'report'
        got = self.digest_of(f'REPORT={report}\n' + self.RUN_HERE)
        self.assertTrue(got.startswith('INVALID:'), got)

    def test_a_failing_container_is_not_a_digest(self):
        got = self.digest_of('echo partial; exit 1\n')
        self.assertEqual(got, 'INVALID:')

    def test_output_that_is_not_64_hex_digits_is_rejected(self):
        for junk in ('', 'e3b0c442', 'Z' * 64, 'A' * 64, 'a' * 65):
            with self.subTest(junk=junk):
                self.assertEqual(self.digest_of(f'echo {junk}\n'), 'INVALID:' + junk)


if __name__ == '__main__':
    unittest.main()
