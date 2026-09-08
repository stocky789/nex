# SPDX-License-Identifier: AGPL-3.0-only
import base64
import hashlib
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('build_source', Path(__file__).parents[1] / 'scripts/build-source.py')
source = importlib.util.module_from_spec(spec)
spec.loader.exec_module(source)


class SourceArchiveTests(unittest.TestCase):
    def test_allowlist_excludes_runtime_installer_and_symlinks(self):
        with tempfile.TemporaryDirectory() as directory:
            root, target = Path(directory) / 'root', Path(directory) / 'out'
            for name in source.FILES + source.SCRIPTS + ('cmd/controller/main.go', 'internal/server.go',
                         'internal/server.key', 'installer/private.go', '.env', '.cache/password',
                         'appliance/firstboot.py', 'appliance/signing.key',
                         'packaging/core-local/build.py', 'packaging/core-local/nex-root-auth.c',
                         'packaging/core-local/nex-core-local.pam', 'docs/api/local-openapi.json'):
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text('source')
            source.copy_sources(root, target)
            self.assertTrue((target / 'cmd/controller/main.go').exists())
            self.assertTrue((target / 'web/public/nex-brand.png').exists())
            self.assertTrue((target / 'web/public/third-party-notices.txt').exists())
            self.assertTrue((target / 'docs/brand/README.md').exists())
            for name in ('appliance/firstboot.py', 'packaging/core-local/build.py',
                         'packaging/core-local/nex-root-auth.c', 'packaging/core-local/nex-core-local.pam',
                         'docs/api/local-openapi.json'):
                self.assertTrue((target / name).exists())
            for name in ('internal/server.key', 'installer/private.go', '.env', '.cache/password',
                         'appliance/signing.key'):
                self.assertFalse((target / name).exists())
            (root / 'internal/leak.go').symlink_to(root / '.env')
            with self.assertRaises(ValueError):
                source.copy_sources(root, target)

    def test_dependency_integrity(self):
        integrity = 'sha512-' + base64.b64encode(hashlib.sha512(b'package').digest()).decode()
        source.verify(b'package', integrity)
        with self.assertRaises(ValueError):
            source.verify(b'changed', integrity)


if __name__ == '__main__':
    unittest.main()
