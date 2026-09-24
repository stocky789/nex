# SPDX-License-Identifier: AGPL-3.0-only
import base64
import hashlib
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('build_source', Path(__file__).parents[1] / 'scripts/build-source.py')
source = importlib.util.module_from_spec(spec)
spec.loader.exec_module(source)


class SourceArchiveTests(unittest.TestCase):
    def test_archive_metadata_does_not_retain_builder_identity(self):
        original = tarfile.TarFile.gettarinfo
        def private_metadata(archive, *args, **kwargs):
            info = original(archive, *args, **kwargs)
            info.uid, info.gid = 12345, 23456
            info.uname, info.gname = 'private-builder', 'private-group'
            info.pax_headers = {'uname': 'private-builder', 'gid': '23456'}
            return info
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'tree'
            target.mkdir()
            (target / 'LICENSE').write_text('retained notice')
            output = Path(directory) / 'source.tar.gz'
            with mock.patch.object(tarfile.TarFile, 'gettarinfo', private_metadata):
                source.write_archive(target, output)
            with tarfile.open(output) as archive:
                self.assertEqual(archive.extractfile('nex-source/LICENSE').read(), b'retained notice')
                for info in archive.getmembers():
                    self.assertEqual((info.uid, info.gid, info.uname, info.gname), (0, 0, '', ''))
                    self.assertFalse({'uid', 'gid', 'uname', 'gname'} & info.pax_headers.keys())

    def test_allowlist_excludes_runtime_installer_and_symlinks(self):
        with tempfile.TemporaryDirectory() as directory:
            root, target = Path(directory) / 'root', Path(directory) / 'out'
            for name in source.FILES + source.SCRIPTS + ('cmd/controller/main.go', 'internal/server.go', 'web/public/nex-symbol-small.svg',
                         'internal/server.key', 'installer/private.go', '.env', '.cache/password',
                         'appliance/firstboot.py', 'appliance/signing.key',
                         'packaging/core-local/build.py', 'packaging/core-local/nex-root-auth.c',
                         'packaging/core-local/nex-core-local.pam', 'packaging/core-local/libvirtd-apparmor-policy.py', 'docs/api/local-openapi.json'):
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text('source')
            source.copy_sources(root, target)
            self.assertTrue((target / 'cmd/controller/main.go').exists())
            self.assertTrue((target / 'web/public/nex-symbol-small.svg').exists())
            self.assertTrue((target / 'ARTWORK-LICENSE.md').exists())
            self.assertTrue((target / 'web/public/third-party-notices.txt').exists())
            self.assertTrue((target / 'docs/brand/README.md').exists())
            self.assertTrue((target / 'scripts/build-source-bundle.py').exists())
            self.assertTrue((target / 'scripts/prune-iso-packages.py').exists())
            self.assertTrue((target / 'scripts/release-publish.py').exists())
            for name in ('appliance/firstboot.py', 'packaging/core-local/build.py',
                         'packaging/core-local/nex-root-auth.c', 'packaging/core-local/nex-core-local.pam',
                         'packaging/core-local/libvirtd-apparmor-policy.py', 'docs/api/local-openapi.json'):
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

    def test_cluster_workload_runner_is_retained(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'source'
            source.copy_sources(Path(__file__).parents[1], target)
            result = subprocess.run(
                [sys.executable, str(target / 'tests/integration/cluster_storage_workloads.py'), '--help'],
                cwd=target, capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn('--execute', result.stdout)
            for name in ('test_cluster_storage_workloads.py', 'test_cluster_ha_lab.py'):
                self.assertTrue((target / 'tests' / name).is_file())

    def test_go_license_falls_back_when_goroot_lacks_one(self):
        with tempfile.TemporaryDirectory() as directory:
            goroot = Path(directory) / 'goroot'
            goroot.mkdir()
            system = Path(directory) / 'system-license'
            with self.assertRaises(RuntimeError):
                source.go_license_path(goroot, system_candidates=(system,))
            system.write_text('packaged go license')
            self.assertEqual(source.go_license_path(goroot, system_candidates=(system,)), system)
            (goroot / 'LICENSE').write_text('go license')
            self.assertEqual(source.go_license_path(goroot, system_candidates=(system,)), goroot / 'LICENSE')

    def test_go_license_override_env_wins(self):
        with tempfile.TemporaryDirectory() as directory:
            override = Path(directory) / 'override-license'
            override.write_text('override')
            with mock.patch.dict(os.environ, {'GO_LICENSE': str(override)}):
                self.assertEqual(source.go_license_path(Path(directory) / 'missing-goroot'), override)


class MaintainerScriptRetentionTests(unittest.TestCase):
    """Every packaging path a package builder installs must reach the source archive.

    Extensionless files under packaging/ are easy to miss: TREES only matches suffixes,
    so they must be listed in FILES. Missing them builds a fine archive and fails hours
    later inside the builder with "install: cannot stat ...".
    """

    def test_installed_maintainer_scripts_are_in_the_allowlist(self):
        root = Path(__file__).parents[1]
        referenced = set()
        for builder in root.glob('packaging/*/build-inside.sh'):
            for token in builder.read_text().split():
                token = token.strip('"\'')
                if token.startswith('packaging/') and (root / token).is_file():
                    referenced.add(token)
        self.assertTrue(referenced, 'no packaging paths found; the builders or this check moved')
        missing = sorted(p for p in referenced if p not in source.FILES and not self.covered_by_tree(p))
        self.assertEqual(missing, [], f'referenced by a package builder but absent from the source archive: {missing}')

    def test_package_delivery_sources_are_retained(self):
        root = Path(__file__).parents[1]
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'source'
            source.copy_sources(root, target)
            for path in ('packaging/cluster-storage/Dockerfile',
                         'packaging/cluster-storage/build-matched-dlm.sh',
                         'packaging/cluster-storage/cluster-iscsi-session',
                         'packaging/core-local/nex-sensors.conf'):
                self.assertEqual((target / path).read_bytes(), (root / path).read_bytes())

    @staticmethod
    def covered_by_tree(path):
        for tree, suffixes in source.TREES.items():
            if path.startswith(tree + '/') and Path(path).suffix in suffixes:
                return True
        return False


if __name__ == '__main__':
    unittest.main()
