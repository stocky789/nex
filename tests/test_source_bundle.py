# SPDX-License-Identifier: AGPL-3.0-only
import importlib.machinery
import importlib.util
import tempfile
import unittest
from pathlib import Path
import json
import os
import subprocess
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
loader = importlib.machinery.SourceFileLoader('source_bundle', str(ROOT / 'scripts/build-source-bundle.py'))
spec = importlib.util.spec_from_loader(loader.name, loader)
source_bundle = importlib.util.module_from_spec(spec)
loader.exec_module(source_bundle)


class SourceBundleTests(unittest.TestCase):
    def setUp(self):
        # dpkg-deb rejects 0700 control dirs; do not inherit a private umask from other tests.
        self.addCleanup(os.umask, os.umask(0o022))

    def test_source_downloads_are_batched_without_extraction(self):
        with mock.patch.object(source_bundle.subprocess, 'run') as run:
            source_bundle.fetch_sources([('one', '1'), ('two', '2')], Path('/tmp'), None, batch_size=1)
        self.assertEqual(run.call_count, 2)
        self.assertEqual(run.call_args.args[0][-3:], ['source', '--download-only', 'two=2'])

    def test_retained_source_requires_dsc_identity_and_complete_checksums(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            artifact = root / 'sample_1.orig.tar.gz'
            artifact.write_bytes(b'source')
            dsc = root / 'sample_1.dsc'
            dsc.write_text(
                'Source: sample\nVersion: 1\nChecksums-Sha256:\n'
                f' {source_bundle.digest(artifact)} {artifact.stat().st_size} {artifact.name}\n'
                '-----BEGIN PGP SIGNATURE-----\nVersion: GnuPG v2\n')
            self.assertEqual(source_bundle.retained_dsc(root, 'sample', '1'), dsc)
            artifact.write_bytes(b'tampered')
            self.assertIsNone(source_bundle.retained_dsc(root, 'sample', '1'))

    def test_deb_member_handles_large_trailing_content_after_match(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            package = root / 'package'
            (package / 'DEBIAN').mkdir(parents=True)
            (package / 'usr/share/nex/appliance').mkdir(parents=True)
            (package / 'usr/share/nex/appliance/components.tsv').write_text('header\n')
            (package / 'usr/share/nex/appliance/trailing').write_bytes(b'x' * 1024 * 1024)
            deb = root / 'nex-controller.deb'
            (package / 'DEBIAN/control').write_text(
                'Package: nex-controller\nVersion: 1\nArchitecture: all\nDescription: test\n')
            subprocess.run(['dpkg-deb', '--build', str(package), str(deb)], check=True,
                           stdout=subprocess.DEVNULL)
            self.assertEqual(source_bundle.deb_member(
                deb, '/usr/share/nex/appliance/components.tsv'), b'header\n')

    def test_core_package_adds_appliance_package_records(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            repo, tree, package = root / 'repo', root / 'tree', root / 'package'
            repo.mkdir(); tree.mkdir(); (package / 'DEBIAN').mkdir(parents=True)
            (package / 'usr/share/nex/appliance').mkdir(parents=True)
            (package / 'DEBIAN/control').write_text(
                'Package: nex-core-local\nVersion: 1\nArchitecture: all\nMaintainer: test\nDescription: test\n')
            (package / 'usr/share/nex/appliance/components.tsv').write_text(
                'libsample\t2\tamd64\tsample\t2\n')
            subprocess.run(['dpkg-deb', '--build', str(package), str(repo / 'nex-core-local.deb')],
                           check=True, stdout=subprocess.DEVNULL)
            _, components = source_bundle.components_for_tree(repo, tree)
            self.assertEqual(components[-1]['source_package'], 'sample')

    def test_source_field_keeps_the_exact_source_version(self):
        self.assertEqual(source_bundle.source_name_version('edk2 (2026.05-2)', 'ignored'), ('edk2', '2026.05-2'))
        self.assertEqual(source_bundle.source_name_version('libnfs', '5.0.2-1+b2'), ('libnfs', '5.0.2-1+b2'))
        self.assertEqual(source_bundle.source_name_version('', '1'), (None, None))
        self.assertEqual(source_bundle.source_name_version('', '1+b2', 'example'), ('example', '1'))

    def test_archive_contains_all_retained_files_under_one_root(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / 'input'
            root.mkdir()
            (root / 'notice').write_text('notice')
            archive = Path(temporary) / 'bundle.tar.gz'
            source_bundle.archive_directory(root, archive)
            self.assertTrue(archive.is_file())
            with source_bundle.tarfile.open(archive) as retained:
                self.assertTrue(all((m.uid, m.gid, m.uname, m.gname) == (0, 0, '', '')
                                    for m in retained.getmembers()))

    def test_staged_virtio_win_requires_retained_fedora_source(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            repo, tree = root / 'repo', root / 'tree'
            repo.mkdir()
            media = tree / 'nex' / 'media'
            media.mkdir(parents=True)
            (media / 'virtio-win.iso').write_bytes(b'iso')
            (tree / 'nex' / 'repo').mkdir(parents=True)
            control = root / 'control'
            (control / 'DEBIAN').mkdir(parents=True)
            (control / 'DEBIAN' / 'control').write_text(
                'Package: sample\nVersion: 1\nArchitecture: all\nMaintainer: test\nDescription: test\n')
            subprocess.run(['dpkg-deb', '--build', str(control), str(repo / 'sample_1_all.deb')],
                           check=True, stdout=subprocess.DEVNULL)
            iso = root / 'base.iso'
            iso.write_bytes(b'base')
            out = root / 'source.tar.gz'
            with mock.patch.object(source_bundle, 'update_sources'), \
                    mock.patch.object(source_bundle, 'fetch_sources'), \
                    mock.patch.object(source_bundle, 'retained_dsc', return_value=root / 'fake.dsc'):
                with self.assertRaisesRegex(RuntimeError, 'without retained Fedora corresponding source'):
                    source_bundle.main([
                        '--version', '0.3.2', '--iso', str(iso), '--tree', str(tree),
                        '--repository', str(repo), '--output', str(out),
                    ])

    def test_compressed_virtio_win_uses_external_complete_source_tree(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            repo, tree, retained = root / 'repo', root / 'tree', root / 'virtio-win-retained-source'
            repo.mkdir(); (tree / 'nex' / 'repo').mkdir(parents=True)
            media = tree / 'nex' / 'media'; media.mkdir(parents=True)
            (media / 'virtio-win.iso.gz').write_bytes(b'compressed')
            third_party = tree / 'nex' / 'third-party' / 'virtio-win' / 'source'
            third_party.mkdir(parents=True)
            (third_party / 'virtio-win-0.1.302-1.src.rpm').write_bytes(b'rpm')
            (third_party.parent / 'NOTICE').write_text('notice')
            source = retained / 'source'; source.mkdir(parents=True)
            for name in ('virtio-win-prewhql-0.1-302-sources.zip',
                         'mingw-qemu-ga-win-110.2.3-2.el10.src.rpm',
                         'spice-vdagent-win-0.10.0-5-sources.zip',
                         'spice-qxl-wddm-dod-0.21-2-sources.zip',
                         'virtio-win-guest-tools-installer-0.1.302.tar.gz',
                         'winfsp-master-sources.zip'):
                (source / name).write_bytes(name.encode())
            (source / 'mingw-qemu-ga-win').mkdir()
            control = root / 'control'; (control / 'DEBIAN').mkdir(parents=True)
            (control / 'DEBIAN' / 'control').write_text(
                'Package: sample\nVersion: 1\nArchitecture: all\nMaintainer: test\nDescription: test\n')
            subprocess.run(['dpkg-deb', '--build', str(control), str(repo / 'sample_1_all.deb')],
                           check=True, stdout=subprocess.DEVNULL)
            iso, out = root / 'base.iso', root / 'source.tar.gz'; iso.write_bytes(b'base')
            with mock.patch.object(source_bundle, 'update_sources'), \
                    mock.patch.object(source_bundle, 'fetch_sources'), \
                    mock.patch.object(source_bundle, 'retained_dsc', return_value=root / 'fake.dsc'):
                source_bundle.main([
                    '--version', '0.3.2', '--iso', str(iso), '--tree', str(tree),
                    '--repository', str(repo), '--source-input', str(retained),
                    '--source-input', str(tree / 'nex' / 'third-party'), '--output', str(out),
                ])
            with source_bundle.tarfile.open(out) as archive:
                self.assertIn('nex-iso-source/provided-sources/virtio-win-retained-source/source/winfsp-master-sources.zip',
                              archive.getnames())

    def test_iso_build_exports_complete_release_evidence(self):
        build = (ROOT / 'installer/build.sh').read_text()
        self.assertIn('snapshot.debian.org/archive/debian/', build)
        self.assertIn('scripts/build-source-bundle.py', build)
        self.assertIn('nex-core-${version}-source.tar.gz', build)
        self.assertIn('nex-core-${version}-compliance-manifest.json', build)
        self.assertIn('ARTWORK-LICENSE.md', build)
        self.assertIn('stage_virtio_win', build)
        self.assertIn('virtio-win-0.1.302-1.src.rpm', build)
        self.assertIn('virtio-win corresponding source RPM missing', build)
        self.assertIn('virtio-win.iso.gz', build)
        self.assertNotIn('refusing to publish virtio-win.iso without complete corresponding-source and legal accounting', build)
        # Binary closure and source-bundle pins must stay on the same snapshots.
        # Live trixie advanced past 20260916 and left unbound 1.26.1-0+deb13u1
        # without a matching source (release run 4223).
        self.assertIn('snapshot-sources.list', build)
        self.assertIn('20260920T000000Z', build)
        self.assertLess(build.index('snapshot-sources.list'), build.index('apt-get "${apt_snapshot[@]}" update'))
        self.assertIn('export NEX_SNAPSHOT_SOURCES=', build)
        gui = (ROOT / 'installer/build-gui.sh').read_text()
        self.assertIn('NEX_SNAPSHOT_SOURCES', gui)
        self.assertIn('apt-get "${apt_snapshot[@]}" source --download-only cdebconf=0.280', gui)
        for package in ('dahdi-firmware-nonfree', 'atmel-firmware', 'bluez-firmware',
                        'firmware-sof-signed', 'firmware-b43-installer',
                        'firmware-b43legacy-installer'):
            self.assertIn(package, build)

    def test_compliance_manifest_binds_final_iso_and_source_bundle(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            repo, tree, control = root / 'repo', root / 'tree', root / 'control'
            repo.mkdir(); tree.mkdir(); (control / 'DEBIAN').mkdir(parents=True)
            (control / 'DEBIAN' / 'control').write_text('Package: sample\nVersion: 1\nArchitecture: all\nMaintainer: test\nDescription: test\n')
            deb = repo / 'sample_1_all.deb'
            subprocess.run(['dpkg-deb', '--build', str(control), str(deb)], check=True, stdout=subprocess.DEVNULL)
            iso, base, source = root / 'nex.iso', root / 'base-source.tar', root / 'nex-source.tar.gz'
            iso.write_bytes(b'final'); base.write_bytes(b'base')
            bundle = root / 'bundle'; bundle.mkdir(); source_bundle.archive_directory(bundle, source)
            manifest = root / 'manifest.json'
            source_bundle.main(['--version', '0.2.9', '--iso', str(iso), '--tree', str(tree), '--repository', str(repo),
                                '--source-bundle', str(source),
                                '--compliance-manifest', str(manifest)])
            result = json.loads(manifest.read_text())
            self.assertEqual(result['version'], '0.2.9')
            self.assertEqual(result['iso']['sha256'], source_bundle.digest(iso))
            self.assertEqual(result['source_bundle']['sha256'], source_bundle.digest(source))
            self.assertEqual(result['components'][0]['source_package'], 'sample')
            self.assertFalse(any('sample_1' in notice for notice in result['notices']))


if __name__ == '__main__':
    unittest.main()
