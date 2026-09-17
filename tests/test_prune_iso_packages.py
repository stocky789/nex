# SPDX-License-Identifier: AGPL-3.0-only
import gzip
import hashlib
import importlib.machinery
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
prune = importlib.machinery.SourceFileLoader(
    'prune_iso_packages', str(ROOT / 'scripts/prune-iso-packages.py')).load_module()


class PruneISOTests(unittest.TestCase):
    def test_filters_package_index_and_refreshes_release(self):
        with tempfile.TemporaryDirectory() as tmp:
            tree = Path(tmp)
            index = tree / 'dists/trixie/main/binary-amd64/Packages.gz'
            index.parent.mkdir(parents=True)
            index.write_bytes(gzip.compress(
                b'Package: keep\nVersion: 1\n\nPackage: blocked\nVersion: 1\n'))
            release = tree / 'dists/trixie/Release'
            release.write_text(
                f'SHA256:\n {"0" * 64} {index.stat().st_size:8d} main/binary-amd64/Packages.gz\n')
            prune.prune(tree, {'blocked'})
            self.assertNotIn(b'Package: blocked', gzip.decompress(index.read_bytes()))
            expected = hashlib.sha256(index.read_bytes()).hexdigest()
            self.assertIn(expected, release.read_text())


if __name__ == '__main__':
    unittest.main()
