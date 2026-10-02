# SPDX-License-Identifier: AGPL-3.0-only
"""Reject archive amplification and timed-out reception before deployment changes."""
import fcntl
import gzip
import importlib.machinery
import importlib.util
import io
import json
from pathlib import Path
import select
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
from types import SimpleNamespace
import unittest

ROOT = Path(__file__).resolve().parents[1]


DEPLOY_PROBE = r"""
import importlib.machinery
import importlib.util
import pathlib
import stat
import sys
from types import SimpleNamespace
from unittest.mock import patch

loader = importlib.machinery.SourceFileLoader('deployer', sys.argv[1])
spec = importlib.util.spec_from_loader(loader.name, loader)
deployer = importlib.util.module_from_spec(spec)
loader.exec_module(deployer)
deployer.STATE = pathlib.Path(sys.argv[2])
deployer.RUNTIME = deployer.STATE
deployer.UID = 0
deployer.ARCHIVE_IDLE_TIMEOUT = float(sys.argv[3])
deployer.ARCHIVE_TOTAL_TIMEOUT = float(sys.argv[4])
sys.argv = ['nex-demo-deploy', '0' * 40]

def forbidden_command(*args, **kwargs):
    (deployer.STATE / 'docker-invoked').touch()
    raise AssertionError('Docker must not run before reception succeeds')

deployer.command = forbidden_command
flock = deployer.fcntl.flock

def acquired(*args):
    flock(*args)
    print('locked', flush=True)

# Substitute only privileged setup checks; reception, locking and cleanup are real.
with patch.object(deployer.os, 'geteuid', return_value=0), \
        patch.object(deployer, 'public_origin', return_value='https://demo.example'), \
        patch.object(deployer.Path, 'lstat',
                     return_value=SimpleNamespace(st_mode=stat.S_IFDIR | 0o700, st_uid=0)), \
        patch.object(deployer.Path, 'is_mount', return_value=True), \
        patch.object(deployer.fcntl, 'flock', side_effect=acquired):
    try:
        deployer.main()
    except TimeoutError as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
"""


def load_deployer():
    loader = importlib.machinery.SourceFileLoader(
        'demo_archive_deployer', str(ROOT / 'packaging/demo/nex-demo-deploy'))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    deployer = importlib.util.module_from_spec(spec)
    loader.exec_module(deployer)
    return deployer


class DemoArchiveTests(unittest.TestCase):
    def assert_reception_timeout(self, idle, total, trickle=False):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            retained = state / 'releases.json'
            original = json.dumps({'current': 'nex-demo:' + '0' * 40, 'previous': None})
            retained.write_text(original)
            process = subprocess.Popen(
                [sys.executable, '-c', DEPLOY_PROBE,
                 str(ROOT / 'packaging/demo/nex-demo-deploy'), directory, str(idle), str(total)],
                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            stopped = threading.Event()

            def send_slowly():
                # Valid gzip prefix, but never enough data to finish reception.
                for byte in gzip.compress(b'x' * 100000):
                    if stopped.wait(0.02):
                        break
                    try:
                        process.stdin.write(bytes([byte]))
                        process.stdin.flush()
                    except BrokenPipeError:
                        break

            writer = None
            try:
                self.assertTrue(select.select([process.stdout], [], [], 3)[0],
                                'disposable publisher did not acquire the lock')
                self.assertEqual(process.stdout.readline(), b'locked\n')
                if trickle:
                    writer = threading.Thread(target=send_slowly)
                    writer.start()
                started = time.monotonic()
                self.assertEqual(process.wait(timeout=3), 1)
                elapsed = time.monotonic() - started
                self.assertIn(b'reception deadline exceeded', process.stderr.read())
                if trickle:
                    self.assertGreater(elapsed, idle, 'trickled input must reach the total deadline')
                self.assertFalse((state / 'docker-invoked').exists())
                self.assertEqual(retained.read_text(), original)
                self.assertEqual(list(state.glob('import-*')), [])
                with (state / 'deploy.lock').open('a') as lock:
                    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            finally:
                stopped.set()
                if writer is not None:
                    writer.join(timeout=3)
                if process.poll() is None:
                    process.kill()
                process.wait(timeout=3)
                for stream in (process.stdin, process.stdout, process.stderr):
                    try:
                        stream.close()
                    except BrokenPipeError:
                        pass

    def test_idle_archive_input_releases_lock_without_changing_deployment(self):
        self.assert_reception_timeout(idle=0.15, total=2)

    def test_trickled_archive_input_cannot_extend_total_deadline(self):
        self.assert_reception_timeout(idle=0.3, total=0.6, trickle=True)

    def test_compressed_archive_cannot_exceed_uncompressed_limit(self):
        deployer = load_deployer()
        deployer.MAX_ARCHIVE = 1024
        original_stdin = sys.stdin
        with tempfile.TemporaryDirectory() as directory, tempfile.TemporaryFile() as incoming:
            temporary = Path(directory)
            incoming.write(gzip.compress(b'x' * 2048))
            incoming.seek(0)
            try:
                sys.stdin = SimpleNamespace(buffer=incoming)
                with self.assertRaises(ValueError):
                    deployer.receive_archive(temporary)
            finally:
                sys.stdin = original_stdin
            self.assertLessEqual((temporary / 'image.tar').stat().st_size, deployer.MAX_ARCHIVE)

    def test_repeated_layer_references_cannot_amplify_repacked_output(self):
        deployer = load_deployer()
        revision = '0' * 40
        config = json.dumps({'os': 'linux', 'architecture': 'amd64', 'config': {
            'Labels': {'org.opencontainers.image.revision': revision}}}).encode()
        layer = io.BytesIO()
        with tarfile.open(fileobj=layer, mode='w') as archive:
            member = tarfile.TarInfo('fixture')
            member.size = 1
            archive.addfile(member, io.BytesIO(b'x'))
        source = io.BytesIO()
        manifest = json.dumps([{'Config': 'config.json', 'RepoTags': ['nex-demo:' + revision],
                                'Layers': ['layer.tar'] * 3}]).encode()
        with tarfile.open(fileobj=source, mode='w') as archive:
            for name, data in [('config.json', config), ('manifest.json', manifest),
                               ('layer.tar', layer.getvalue())]:
                member = tarfile.TarInfo(name)
                member.size = len(data)
                archive.addfile(member, io.BytesIO(data))
        # The input fits exactly. Repeated references plus tar overhead do not.
        deployer.MAX_ARCHIVE = len(source.getvalue())
        original_stdin = sys.stdin
        with tempfile.TemporaryDirectory() as directory, tempfile.TemporaryFile() as incoming:
            temporary = Path(directory)
            incoming.write(gzip.compress(source.getvalue()))
            incoming.seek(0)
            try:
                sys.stdin = SimpleNamespace(buffer=incoming)
                deployer.receive_archive(temporary)
                with self.assertRaises(ValueError):
                    deployer.import_image(revision, temporary)
            finally:
                sys.stdin = original_stdin
            self.assertFalse((temporary / 'validated.tar').exists(),
                             'oversized repack must be rejected before writing any output')


if __name__ == '__main__':
    unittest.main()
