#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""packaging/core-local/build.py needs no appliance and pins the builder like the image."""
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
BUILD = ROOT / 'packaging/core-local/build.py'


class CorePackageBuildTest(unittest.TestCase):
    def run_build(self, directory, *extra):
        stage = Path(directory)
        with tarfile.open(stage / 'source.tar.gz', 'w:gz') as archive:
            for name in ['Dockerfile', 'build-inside.sh']:
                archive.add(ROOT / 'packaging/core-local' / name, arcname='nex-source/packaging/core-local/' + name)
        log = stage / 'docker.log'
        docker = stage / 'docker'
        docker.write_text('#!/usr/bin/python3\nimport json, sys\n'
                          f'open({str(log)!r}, "a").write(json.dumps(sys.argv[1:]) + "\\n")\n')
        docker.chmod(0o755)
        result = subprocess.run([sys.executable, str(BUILD), '--source', str(stage / 'source.tar.gz'),
                                 '--output', str(stage / 'output'), '--docker', str(docker), *extra],
                                capture_output=True, text=True)
        calls = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
        return result, calls

    def test_builds_from_the_source_archive_alone_and_pins_snapshot_and_epoch(self):
        with tempfile.TemporaryDirectory() as directory:
            result, calls = self.run_build(directory)
            self.assertEqual(result.returncode, 0, result.stderr)
            build, run = calls
            snapshot = (ROOT / 'image/snapshot').read_text().strip()
            self.assertIn('SNAPSHOT=' + snapshot, build)
            self.assertFalse([arg for arg in run if arg.startswith('NEX_EDITION=') or arg.endswith(':/input/bundle:ro')])
            epoch = next(arg for arg in run if arg.startswith('SOURCE_DATE_EPOCH='))
            self.assertRegex(epoch, r'^SOURCE_DATE_EPOCH=\d+$')

    def test_the_removed_appliance_bundle_options_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            result, calls = self.run_build(directory, '--bundle', directory)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(calls, [])


if __name__ == '__main__':
    unittest.main()
