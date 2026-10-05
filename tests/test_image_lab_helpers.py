#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""tests/integration/image_boot.py helpers shared by the image boot labs: where a run keeps its files and what it mounts."""
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('image_boot', ROOT / 'tests/integration/image_boot.py')
image_boot = importlib.util.module_from_spec(spec)
spec.loader.exec_module(image_boot)


class LabHelperTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='nex-lab-helpers-')
        self.addCleanup(self.tmp.cleanup)
        self.out = Path(self.tmp.name) / 'out'
        (self.out / 'release').mkdir(parents=True)

    def test_work_dir_is_created_beside_the_release_not_under_the_repository(self):
        work = Path(image_boot.scratch_dir(str(self.out / 'release'), 'lab-'))
        self.assertEqual(work.parent, self.out)
        self.assertTrue(work.is_dir())

    def test_hostimage_is_found_beside_the_release_and_a_missing_one_is_refused(self):
        release = str(self.out / 'release')
        with self.assertRaisesRegex(SystemExit, 'no nex-hostimage'):
            image_boot.hostimage_for(release)  # docker would otherwise create a root-owned directory there
        (self.out / 'nex-hostimage').write_text('x')
        self.assertEqual(image_boot.hostimage_for(release), str(self.out / 'nex-hostimage'))
        other = self.out / 'tool'
        other.write_text('x')
        self.assertEqual(image_boot.hostimage_for(release, str(other)), str(other))

    def test_caps_apply_only_when_the_environment_sets_them(self):
        with mock.patch.dict(os.environ, clear=False):
            os.environ.pop('NEX_DOCKER_CPUS', None)
            os.environ.pop('NEX_DOCKER_MEMORY', None)
            self.assertEqual(image_boot.docker_caps(), [])
            os.environ.update(NEX_DOCKER_CPUS='3', NEX_DOCKER_MEMORY='6g')
            self.assertEqual(image_boot.docker_caps(), ['--cpus', '3', '--memory', '6g'])

    def test_accelerator_switch_selects_kvm_or_emulation_and_stretches_waits(self):
        with mock.patch.object(image_boot, 'ACCEL', 'kvm'), mock.patch.object(image_boot, 'SLOWDOWN', 1.0):
            args = image_boot.qemu_accel()
            self.assertIn('-enable-kvm', args)
            self.assertIn('host,-vmx', args)  # a guest is never offered VMX: nothing in a lab starts a nested guest
            self.assertEqual(image_boot.scaled(240), 240)
        with mock.patch.object(image_boot, 'ACCEL', 'tcg'), mock.patch.object(image_boot, 'SLOWDOWN', 8.0):
            args = image_boot.qemu_accel()
            self.assertNotIn('-enable-kvm', args)
            self.assertIn('tcg,thread=multi', args)
            self.assertEqual(image_boot.scaled(240), 1920)
            image_boot.need_accelerator()  # emulation needs no /dev/kvm

    def test_mount_sources_follow_the_docker_hosts_view_of_the_workspace(self):
        with mock.patch.dict(os.environ, {'GITHUB_WORKSPACE': '/job/ws', 'NEX_HOST_WORKSPACE': '/var/lib/docker/volumes/v/_data'}):
            self.assertEqual(image_boot.host_mount('/job/ws/.cache/out'), '/var/lib/docker/volumes/v/_data/.cache/out')
            with self.assertRaisesRegex(SystemExit, 'only paths under /job/ws'):
                image_boot.host_mount('/tmp/elsewhere')
        with mock.patch.dict(os.environ):
            os.environ.pop('NEX_HOST_WORKSPACE', None)
            self.assertEqual(image_boot.host_mount('/anywhere/x'), '/anywhere/x')


if __name__ == '__main__':
    unittest.main()
