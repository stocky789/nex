# SPDX-License-Identifier: AGPL-3.0-only
"""Reject archive amplification and timed-out reception before deployment changes."""
import fcntl
import gzip
import hashlib
import importlib.machinery
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import runpy
import select
import signal
import stat
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch

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



def docker_archive(revision, layers=None, nonce='', tag=None, labels=None, extra_members=(),
                   config_extra=None, manifest_extra=None):
    """Build a gzip docker-save stream like the CI publisher sends."""
    layers = [b'layer'] if layers is None else layers
    config = {'os': 'linux', 'architecture': 'amd64', 'nonce': nonce,
              'config': {'Labels': {'org.opencontainers.image.revision': revision}
                         if labels is None else labels}}
    config.update(config_extra or {})
    config_bytes = json.dumps(config).encode()
    entry = {'Config': 'config.json', 'RepoTags': [tag or 'nex-demo:' + revision],
             'Layers': [f'{index}/layer.tar' for index in range(len(layers))]}
    entry.update(manifest_extra or {})
    raw = io.BytesIO()
    with tarfile.open(fileobj=raw, mode='w') as archive:
        members = [('config.json', config_bytes), ('manifest.json', json.dumps([entry]).encode())]
        members += [(name, data) for name, data in zip(entry['Layers'], layers)]
        for name, data in members:
            member = tarfile.TarInfo(name)
            member.size = len(data)
            archive.addfile(member, io.BytesIO(data))
        for member in extra_members:
            archive.addfile(member)
    return gzip.compress(raw.getvalue(), 1)


class FakeDocker:
    """Just enough Docker state to run nex-demo-deploy's real control flow."""

    def __init__(self):
        self.images = {}
        self.container = None
        self.unhealthy = set()
        self.image_size = 1000

    def __call__(self, *args, check=True):
        words = list(args[1:])
        ok = lambda out='': SimpleNamespace(returncode=0, stdout=out, stderr='')
        missing = SimpleNamespace(returncode=1, stdout='', stderr='')
        if words[:2] == ['image', 'inspect']:
            if words[-1] not in self.images:
                return missing
            return ok(str(self.image_size) if '{{.Size}}' in words else '')
        if words[:2] == ['container', 'inspect']:
            if self.container is None:
                return missing
            return ok('true' if self.container not in self.unhealthy else 'false') \
                if '{{.State.Running}}' in words else ok(self.container + '\n')
        if words[:3] == ['container', 'rm', '--force']:
            self.container = None
            return ok()
        if words[0] == 'run':
            self.container = next(word for word in words if re.fullmatch(r'nex-demo:[0-9a-f]{40}', word))
            assert self.container in self.images
            return ok()
        if words[:2] == ['image', 'rm']:
            self.images.pop(words[2], None)
            return ok()
        if words[:2] == ['image', 'load']:
            with tarfile.open(words[3]) as archive:
                entry = json.load(archive.extractfile('manifest.json'))[0]
                config = archive.extractfile(entry['Config']).read()
            self.images[entry['RepoTags'][0]] = 'sha256:' + hashlib.sha256(config).hexdigest()
            return ok()
        raise AssertionError(f'unexpected docker invocation {words}')


class DemoDeployFlowTests(unittest.TestCase):
    def setUp(self):
        self.deployer = load_deployer()
        self.docker = FakeDocker()
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.state = Path(self.directory.name)
        self.deployer.STATE = self.state
        self.deployer.RUNTIME = self.state
        self.deployer.UID = 0
        self.deployer.command = self.docker
        self.deployer.image_config_id = lambda tag: self.docker.images.get(tag)
        self.deployer.healthy = lambda: self.docker.container not in self.docker.unhealthy
        for name in ('SIGTERM', 'SIGINT'):
            self.addCleanup(signal.signal, getattr(signal, name), signal.getsignal(getattr(signal, name)))
        patches = [
            patch.object(self.deployer.os, 'geteuid', return_value=0),
            patch.object(self.deployer, 'public_origin', return_value='https://demo.example'),
            patch.object(self.deployer.Path, 'lstat',
                         return_value=SimpleNamespace(st_mode=stat.S_IFDIR | 0o700, st_uid=0)),
            patch.object(self.deployer.Path, 'is_mount', return_value=True),
        ]
        for item in patches:
            item.start()
            self.addCleanup(item.stop)

    def deploy(self, revision, archive=None):
        archive = docker_archive(revision) if archive is None else archive
        with tempfile.TemporaryFile() as incoming, patch.object(sys, 'argv', ['nex-demo-deploy', revision]), \
                patch.object(sys, 'stdin', SimpleNamespace(buffer=incoming)):
            incoming.write(archive)
            incoming.seek(0)
            self.deployer.main()

    def releases(self):
        return json.loads((self.state / 'releases.json').read_text())

    def test_rollback_retention_and_replay_of_current_release(self):
        a, b, c = ('a' * 40, 'b' * 40, 'c' * 40)
        for revision in (a, b):
            self.deploy(revision)
        self.assertEqual(self.releases(), {'current': 'nex-demo:' + b, 'previous': 'nex-demo:' + a})
        # Replaying the running release must keep the rollback image and state.
        self.deploy(b, docker_archive(b, nonce='rebuilt'))
        self.assertEqual(self.releases(), {'current': 'nex-demo:' + b, 'previous': 'nex-demo:' + a})
        self.assertIn('nex-demo:' + a, self.docker.images)
        self.assertEqual(self.docker.container, 'nex-demo:' + b)
        # A new release retains only the one before it.
        self.deploy(c)
        self.assertEqual(self.releases(), {'current': 'nex-demo:' + c, 'previous': 'nex-demo:' + b})
        self.assertEqual(set(self.docker.images), {'nex-demo:' + b, 'nex-demo:' + c})

    def test_unhealthy_candidate_rolls_back_and_is_discarded(self):
        a, b = ('a' * 40, 'b' * 40)
        self.deploy(a)
        before = (self.state / 'releases.json').read_text()
        self.docker.unhealthy.add('nex-demo:' + b)
        with self.assertRaises(RuntimeError):
            self.deploy(b)
        self.assertEqual(self.docker.container, 'nex-demo:' + a)
        self.assertNotIn('nex-demo:' + b, self.docker.images)
        self.assertEqual((self.state / 'releases.json').read_text(), before)

    def test_different_image_under_a_retained_tag_is_refused(self):
        a, b = ('a' * 40, 'b' * 40)
        self.deploy(a)
        self.deploy(b)
        with self.assertRaisesRegex(ValueError, 'immutable'):
            self.deploy(a, docker_archive(a, nonce='tampered'))
        self.assertEqual(self.docker.container, 'nex-demo:' + b)

    def test_invalid_archives_are_rejected_before_any_container_change(self):
        a, b = ('a' * 40, 'b' * 40)
        self.deploy(a)
        link = tarfile.TarInfo('link')
        link.type = tarfile.SYMTYPE
        link.linkname = '/etc/passwd'
        cases = {
            'wrong revision label': docker_archive(b, labels={'org.opencontainers.image.revision': a}),
            'wrong tag': docker_archive(b, tag='nex-demo:latest'),
            'extra tag': docker_archive(b, manifest_extra={'RepoTags': ['nex-demo:' + b, 'nex-demo:evil']}),
            'symlink member': docker_archive(b, extra_members=[link]),
            'declared volumes': docker_archive(b, config_extra={'config': {
                'Volumes': {'/data': {}}, 'Labels': {'org.opencontainers.image.revision': b}}}),
            'wrong architecture': docker_archive(b, config_extra={'architecture': 'arm64'}),
            'no layers': docker_archive(b, layers=[]),
            'path traversal': docker_archive(b, manifest_extra={'Layers': ['../layer.tar']}),
        }
        for name, archive in cases.items():
            with self.subTest(name), self.assertRaises((ValueError, KeyError)):
                self.deploy(b, archive)
            self.assertEqual(self.docker.container, 'nex-demo:' + a, name)
            self.assertNotIn('nex-demo:' + b, self.docker.images, name)

    def test_compressed_layer_cannot_inflate_past_the_bound(self):
        self.deployer.MAX_INFLATED = 1024 * 1024
        bomb = gzip.compress(b'\0' * (4 * 1024 * 1024), 1)
        self.assertLess(len(bomb), 64 * 1024)
        with self.assertRaisesRegex(ValueError, 'inflate'):
            self.deploy('a' * 40, docker_archive('a' * 40, layers=[bomb]))
        self.assertEqual(self.docker.images, {})

    def test_inflation_budget_is_shared_across_layers(self):
        self.deployer.MAX_INFLATED = 1024 * 1024
        layers = [gzip.compress(b'\0' * (600 * 1024), 1)] * 2
        with self.assertRaisesRegex(ValueError, 'inflate'):
            self.deploy('a' * 40, docker_archive('a' * 40, layers=layers))

    def test_member_count_is_bounded(self):
        self.deployer.MAX_MEMBERS = 8
        extra = []
        for index in range(20):
            member = tarfile.TarInfo(f'extra-{index}')
            extra.append(member)
        with self.assertRaisesRegex(ValueError, 'too many'):
            self.deploy('a' * 40, docker_archive('a' * 40, extra_members=extra))

    def test_loaded_image_over_size_cap_is_removed(self):
        self.docker.image_size = self.deployer.MAX_IMAGE_SIZE + 1
        with self.assertRaisesRegex(ValueError, 'size'):
            self.deploy('a' * 40)
        self.assertEqual(self.docker.images, {})
        self.assertIsNone(self.docker.container)

    def test_stale_import_directories_are_swept(self):
        stale = self.state / 'import-crashed'
        stale.mkdir()
        (stale / 'image.tar').write_bytes(b'x' * 10)
        self.deploy('a' * 40)
        self.assertFalse(stale.exists())

    def test_release_argument_must_be_a_full_lowercase_sha(self):
        for argument in ('A' * 40, 'a' * 39, 'a' * 41, 'main', '../' + 'a' * 37, ''):
            with self.subTest(argument), patch.object(sys, 'argv', ['nex-demo-deploy', argument]):
                with self.assertRaisesRegex(ValueError, 'usage'):
                    self.deployer.main()
        with patch.object(sys, 'argv', ['nex-demo-deploy']), self.assertRaisesRegex(ValueError, 'usage'):
            self.deployer.main()


class DemoPublishEntrypointTests(unittest.TestCase):
    def forced(self, command):
        calls = []
        environment = {'SSH_ORIGINAL_COMMAND': command}
        with patch.dict(os.environ, environment), patch('os.execv', side_effect=lambda *a: calls.append(a)):
            try:
                runpy.run_path(str(ROOT / 'packaging/demo/nex-demo-ssh-deploy'), run_name='__main__')
                return 0, calls
            except SystemExit as error:
                return error.code, calls

    def test_forced_command_only_accepts_one_exact_commit_publication(self):
        sha = 'a' * 40
        code, calls = self.forced(f'sudo -n /usr/local/sbin/nex-demo-deploy {sha}')
        self.assertEqual((code, calls[0][1]),
                         (0, ('sudo', '-n', '/usr/local/sbin/nex-demo-deploy', sha)))
        for command in ('', 'bash', f'sudo -n /usr/local/sbin/nex-demo-deploy {sha} ; id',
                        f'sudo -n /usr/local/sbin/nex-demo-deploy {sha}\n', 'sudo -n /usr/local/sbin/nex-demo-deploy ' + 'A' * 40,
                        'sudo -n /usr/local/sbin/nex-demo-deploy ' + 'a' * 39,
                        f'sudo /usr/local/sbin/nex-demo-deploy {sha}',
                        f'sudo -n /usr/local/sbin/nex-demo-deploy  {sha}'):
            with self.subTest(command):
                self.assertEqual(self.forced(command), (1, []))


class DemoCIDeployScriptTests(unittest.TestCase):
    SHA = 'b' * 40

    def run_script(self, **overrides):
        with tempfile.TemporaryDirectory() as directory:
            bin_dir = Path(directory)
            log = bin_dir / 'ssh.log'
            (bin_dir / 'docker').write_text(
                '#!/bin/sh\n'
                'if [ "$2" = inspect ]; then printf "%s" "$FAKE_REVISION"; else printf "image-bytes"; fi\n')
            (bin_dir / 'ssh').write_text(
                '#!/bin/bash\nprintf "%s\\n" "$*" >"$FAKE_LOG"; cat >"$FAKE_LOG.stdin"\n')
            for name in ('docker', 'ssh'):
                (bin_dir / name).chmod(0o755)
            environment = {'PATH': f'{bin_dir}:/usr/bin:/bin', 'FAKE_LOG': str(log),
                           'FAKE_REVISION': self.SHA, 'GITHUB_SHA': self.SHA,
                           'NEX_DEPLOY_HOST': 'demo.example.test', 'NEX_DEPLOY_USER': 'deploy',
                           'NEX_DEPLOY_KEY': 'key', 'NEX_DEPLOY_KNOWN_HOSTS': 'hosts'}
            environment.update(overrides)
            environment = {key: value for key, value in environment.items() if value is not None}
            result = subprocess.run(['bash', str(ROOT / 'scripts/demo-ci-deploy.sh')], env=environment,
                                    capture_output=True, text=True, timeout=30)
            ssh_args = log.read_text() if log.exists() else None
            sent = gzip.decompress(Path(str(log) + '.stdin').read_bytes()) \
                if Path(str(log) + '.stdin').exists() else None
            return result, ssh_args, sent

    def test_valid_release_streams_image_to_the_fixed_wrapper(self):
        result, ssh_args, sent = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(ssh_args.strip().endswith(f'sudo -n /usr/local/sbin/nex-demo-deploy {self.SHA}'))
        self.assertIn('deploy@demo.example.test', ssh_args)
        self.assertIn('StrictHostKeyChecking=yes', ssh_args)
        self.assertEqual(sent, b'image-bytes')

    def test_invalid_inputs_never_reach_ssh(self):
        cases = {
            'missing secret': {'NEX_DEPLOY_KEY': None},
            'empty secret': {'NEX_DEPLOY_KNOWN_HOSTS': ''},
            'short sha': {'GITHUB_SHA': 'b' * 39},
            'uppercase sha': {'GITHUB_SHA': 'B' * 40, 'FAKE_REVISION': 'B' * 40},
            'option-like host': {'NEX_DEPLOY_HOST': '-oProxyCommand=x'},
            'host with command': {'NEX_DEPLOY_HOST': 'h;id'},
            'bad user': {'NEX_DEPLOY_USER': 'a b'},
            'image revision mismatch': {'FAKE_REVISION': 'c' * 40},
        }
        for name, overrides in cases.items():
            with self.subTest(name):
                result, ssh_args, _ = self.run_script(**overrides)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIsNone(ssh_args)


if __name__ == '__main__':
    unittest.main()
