#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""scripts/image-lock.py: Nex-built debs are locked by origin and file hash."""
import importlib.util
import io
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from contextlib import redirect_stdout

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('image_lock', ROOT / 'scripts/image-lock.py')
image_lock = importlib.util.module_from_spec(spec)
spec.loader.exec_module(image_lock)


@unittest.skipUnless(shutil.which('dpkg-deb'), 'dpkg-deb is required to build the test package')
class NexOriginLockTest(unittest.TestCase):
    def setUp(self):
        self.directory = Path(tempfile.mkdtemp(prefix='nex-lock-'))
        self.addCleanup(shutil.rmtree, self.directory)
        (self.directory / 'image').mkdir()
        (self.directory / 'image/base.packages').write_text('bash\nnex-core-local\n')
        (self.directory / 'image/denylist.txt').write_text('')
        image_lock.IMAGE = str(self.directory / 'image')
        image_lock.LOCK = str(self.directory / 'image/packages.lock')
        self.debs = self.directory / 'debs'
        self.debs.mkdir()
        self.make_deb('1.0')
        self.dpkg_list = self.directory / 'list'
        self.dpkg_list.write_text('bash\t5.2\nnex-core-local\t1.0\n')

    def make_deb(self, version, payload='a'):
        tree = self.directory / 'tree'
        shutil.rmtree(tree, ignore_errors=True)
        (tree / 'DEBIAN').mkdir(parents=True)
        (tree / 'DEBIAN/control').write_text(
            f'Package: nex-core-local\nVersion: {version}\nArchitecture: amd64\nMaintainer: x\nDescription: x\n')
        (tree / 'payload').write_text(payload)
        for old in self.debs.glob('*.deb'):
            old.unlink()
        subprocess.run(['dpkg-deb', '--root-owner-group', '--build', str(tree), str(self.debs / 'nex-core-local.deb')],
                       check=True, capture_output=True)

    def run_lock(self, action, *flags):
        with redirect_stdout(io.StringIO()):
            image_lock.main([action, '--debs', str(self.debs), *flags, str(self.dpkg_list)])

    def test_write_records_origin_and_hash_and_check_catches_a_changed_deb(self):
        self.run_lock('write')
        lock = Path(image_lock.LOCK).read_text().splitlines()
        self.assertEqual(lock[0], 'bash\t5.2')
        origin = lock[1].split('\t')
        self.assertEqual(origin[:3], ['nex-core-local', '1.0', 'origin=nex'])
        self.assertRegex(origin[3], r'^sha256=[0-9a-f]{64}$')
        self.run_lock('check')
        self.make_deb('1.0', payload='rebuilt with other content')
        with self.assertRaisesRegex(SystemExit, 'nex-core-local: lock has'):
            self.run_lock('check')

    def test_full_check_requires_every_locked_nex_artifact(self):
        self.run_lock('write')
        (self.debs / 'nex-core-local.deb').unlink()
        with self.assertRaisesRegex(SystemExit, r'nex-core-local: the locked Nex \.deb is missing'):
            self.run_lock('check')
        self.run_lock('check', '--snapshot-only')

    def test_check_refuses_a_build_that_installed_another_version_than_the_deb(self):
        self.make_deb('1.1')
        with self.assertRaisesRegex(SystemExit, r'the \.deb is 1\.1, the build installed 1\.0'):
            self.run_lock('write')

    def test_check_refuses_a_denylisted_package_even_when_the_lock_matches(self):
        self.run_lock('write')
        (self.directory / 'image/denylist.txt').write_text('# never\nbash\n')
        with self.assertRaisesRegex(SystemExit, 'denylisted package bash is in the build'):
            self.run_lock('check')

    def test_check_refuses_a_requested_package_missing_from_the_build(self):
        self.run_lock('write')
        (self.directory / 'image/base.packages').write_text('bash\nnex-core-local\nlibvirt-daemon\n')
        with self.assertRaisesRegex(SystemExit, 'requested package libvirt-daemon is not in the build'):
            self.run_lock('check')

    def test_snapshot_only_ignores_nex_packages_but_still_catches_snapshot_drift(self):
        self.run_lock('write')
        # A freshly built deb differs from the locked bytes: the full check fails, the snapshot gate does not.
        self.make_deb('1.0', payload='rebuilt with other content')
        with self.assertRaisesRegex(SystemExit, 'nex-core-local: lock has'):
            self.run_lock('check')
        self.run_lock('check', '--snapshot-only')
        self.dpkg_list.write_text('bash\t5.3\nnex-core-local\t1.0\n')
        with self.assertRaisesRegex(SystemExit, 'bash: lock has 5.2, build has 5.3'):
            self.run_lock('check', '--snapshot-only')

    def test_snapshot_only_accepts_a_build_without_the_nex_packages(self):
        self.run_lock('write')
        (self.directory / 'image/base.packages').write_text('bash\n')
        self.dpkg_list.write_text('bash\t5.2\n')
        shutil.rmtree(self.debs)
        self.debs.mkdir()
        self.run_lock('check', '--snapshot-only')
        with self.assertRaisesRegex(SystemExit, 'nex-core-local: lock has 1.0, build has None'):
            self.run_lock('check')

    def test_snapshot_only_refuses_a_deb_that_carries_a_snapshot_package_name(self):
        # The lock records nex-core-local as a snapshot package (no .deb at write time): a .deb of that name
        # must not slip through the gate that ignores Nex-built packages.
        wrong = self.debs / 'nex-core-local.deb'
        saved = wrong.read_bytes()
        wrong.unlink()
        self.run_lock('write')
        self.assertNotIn('origin=nex', Path(image_lock.LOCK).read_text())
        wrong.write_bytes(saved)
        with self.assertRaisesRegex(SystemExit, 'nex-core-local: lock has'):
            self.run_lock('check', '--snapshot-only')



if __name__ == '__main__':
    unittest.main()
