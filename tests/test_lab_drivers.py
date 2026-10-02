#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Regression tests for the pure safety logic of the image lab drivers (no Docker, KVM or QEMU): exit codes, pid
handling, destructive-path guards, the Secure Boot verdict helpers and the repro-check digest gate."""
import importlib.util
import io
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

    def spawn(self, argv0='sleep'):
        p = subprocess.Popen([argv0, '60'])
        for _ in range(200):  # until the child has exec'd (its cmdline is empty before that)
            if Path(f'/proc/{p.pid}/cmdline').read_bytes():
                break
            time.sleep(0.01)
        self.addCleanup(lambda: (p.kill(), p.wait()))
        return p

    def test_main_exits_non_zero_when_a_check_failed(self):
        with mock.patch.object(self.mod, 'cmd_down'), mock.patch.object(sys, 'argv', ['x', 'down']):
            with mock.patch.object(self.mod.upd.check, 'failed', 1):
                with self.assertRaises(SystemExit) as raised:
                    self.mod.main()
                self.assertEqual(raised.exception.code, 1)
            with mock.patch.object(self.mod.upd.check, 'failed', 0):
                self.mod.main()

    def test_kill_pid_signals_only_the_expected_binary(self):
        p = self.spawn()
        self.assertFalse(self.mod.kill_pid(p.pid, 'qemu-system'))  # a reused pid: some other program
        time.sleep(0.05)
        self.assertIsNone(p.poll())
        self.assertTrue(self.mod.kill_pid(p.pid, 'sleep'))
        self.assertEqual(p.wait(timeout=5), -signal.SIGKILL)
        self.assertFalse(self.mod.kill_pid(p.pid, 'sleep'))  # reaped: gone
        self.assertFalse(self.mod.kill_pid(None, 'sleep'))

    def test_is_ours_matches_the_cmdline_not_just_a_live_pid(self):
        p = self.spawn()
        self.assertTrue(self.mod.is_ours(p.pid, 'sleep'))
        self.assertFalse(self.mod.is_ours(p.pid, 'nex-controller'))
        self.assertFalse(self.mod.is_ours(0, 'sleep'))

    def test_pidfile_is_removed_after_a_cut_and_a_missing_one_is_handled(self):
        fake = self.tmp / 'qemu-system-x86_64'
        shutil.copy(shutil.which('sleep'), fake)  # argv[0] names a qemu-system binary
        p = self.spawn(str(fake))
        pidfile = self.tmp / 'qemu.pid'
        pidfile.write_text(str(p.pid))
        self.assertEqual(self.mod.read_pidfile(str(pidfile), 'qemu-system'), p.pid)
        self.assertTrue(self.mod.kill_qemu(str(pidfile)))
        self.assertFalse(pidfile.exists())
        p.wait(timeout=5)
        self.assertFalse(self.mod.kill_qemu(str(pidfile)))  # nothing to cut, no exception
        self.assertIsNone(self.mod.read_pidfile(str(pidfile), 'qemu-system'))

    def test_a_pidfile_naming_a_foreign_process_is_neither_killed_nor_trusted(self):
        p = self.spawn()  # plain sleep: the pid a stale qemu.pid now points at
        pidfile = self.tmp / 'qemu.pid'
        pidfile.write_text(str(p.pid))
        self.assertIsNone(self.mod.read_pidfile(str(pidfile), 'qemu-system'))
        self.assertFalse(self.mod.kill_qemu(str(pidfile)))
        time.sleep(0.05)
        self.assertIsNone(p.poll())
        self.assertFalse(pidfile.exists())
        pidfile.write_text('not a pid')
        self.assertIsNone(self.mod.read_pidfile(str(pidfile), 'qemu-system'))

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
        calls, saved = self.resume({'hosts': [{}, {}, {}], 'bmc_pid': 123, 'pids': [1]})
        self.assertEqual(calls, ['services', 'bmc', 'controller'])
        calls, _ = self.resume({'hosts': [{}, {}, {}], 'pids': [1]})
        self.assertEqual(calls, ['services', 'controller'])


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

    def test_empty_or_disabled_output_is_neither_enforcing_nor_a_valid_off(self):
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

    def test_a_missing_tampered_fixture_is_a_failed_check_and_boots_nothing(self):
        m = self.mod
        results = []
        with mock.patch.object(m, 'check', side_effect=lambda ok, what, detail='': results.append(ok)), \
                mock.patch.object(m, 'refused_boot') as boot, mock.patch.object(m, 'put_uki') as put:
            m.check_tampered(mock.Mock(), '/work', '/nonexistent/release', 'nex.efi')
        self.assertEqual(results, [False])
        boot.assert_not_called()
        put.assert_not_called()


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
