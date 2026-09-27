# SPDX-License-Identifier: AGPL-3.0-only
"""Publish both installer variants with their exact downloadable Nex inputs."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from test_release_publish import ROOT, make_iso, make_source, make_manifest, publish
from test_apt_publish import HAS_DPKG_DEB, apt, build_minimal_deb


class NetinstReleaseTests(unittest.TestCase):
    def test_complete_publication_and_reject_changed_or_missing_payload(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            root = directory / 'downloads'
            version = '1.0.0'
            iso = make_iso(directory, version)
            source = make_source(directory, version)
            manifest = make_manifest(directory, version)
            netinst = directory / f'nex-core-{version}-amd64-netinst.iso'
            netinst.write_bytes(b'network installer')
            payload = directory / 'package.deb'
            payload.write_bytes(b'compiled Nex package with appliance')
            digest = publish.sha256(payload)
            payload = payload.rename(directory / f'nex-core-local-{digest}.deb')
            libvirt_payload = directory / 'libvirt0-input.deb'
            libvirt_payload.write_bytes(b'matched libvirt candidate')
            libvirt_digest = publish.sha256(libvirt_payload)
            libvirt_payload = libvirt_payload.rename(directory / f'libvirt0-{libvirt_digest}.deb')
            network_manifest = directory / f'nex-core-{version}-netinst-compliance-manifest.json'
            network_manifest.write_text(json.dumps({
                'version': version,
                'iso': {'file': netinst.name, 'sha256': publish.sha256(netinst)},
                'source_bundle': {'file': source.name, 'sha256': publish.sha256(source)},
                'netinst_payloads': [{'file': payload.name, 'sha256': digest},
                                     {'file': libvirt_payload.name, 'sha256': libvirt_digest}],
            }))
            args = (root, iso, source, manifest, 3, netinst, network_manifest, [payload, libvirt_payload])
            publish.publish(*args, check=True)
            self.assertEqual(list(root.iterdir()), [])
            publish.publish(*args)
            page = (root / version / 'index.html').read_text()
            self.assertIn(iso.name, page)
            self.assertIn(netinst.name, page)
            self.assertIn(source.name, page)
            self.assertNotIn(payload.name, page)
            self.assertNotIn(libvirt_payload.name, page)
            for asset in (iso, source, manifest, netinst, network_manifest, payload, libvirt_payload):
                self.assertEqual((root / version / asset.name).read_bytes(), asset.read_bytes())
                self.assertTrue((root / version / (asset.name + '.sha256')).is_file())
            payload.write_bytes(b'changed package')
            with self.assertRaisesRegex(publish.ReleasePublishError, 'payload binding'):
                publish.publish(*args)
            self.assertEqual((root / version / payload.name).read_bytes(), b'compiled Nex package with appliance')
            with self.assertRaisesRegex(publish.ReleasePublishError, 'together'):
                publish.publish(root, iso, source, manifest, 3, netinst, network_manifest, [])
            netinst.write_bytes(b'changed ISO')
            payload.write_bytes(b'compiled Nex package with appliance')
            with self.assertRaisesRegex(publish.ReleasePublishError, 'iso binding'):
                publish.publish(*args)

    @unittest.skipUnless(HAS_DPKG_DEB, 'dpkg-deb required')
    def test_remote_core_link_reuses_download_for_apt(self):
        workflow = (ROOT / '.github/workflows/release.yml').read_text()
        link = next(line.strip() for line in workflow.splitlines() if line.strip().startswith("ln '$staging'/nex-core-local-"))
        with tempfile.TemporaryDirectory() as temporary:
            staging = Path(temporary)
            canonical = build_minimal_deb(staging, 'nex-core-local', '1.0.0')
            digest = publish.sha256(canonical)
            payload = canonical.rename(staging / f'nex-core-local-{digest}.deb')
            result = subprocess.run(['bash', '-euc', 'bash -euc "' + link + '"'],
                env={**os.environ, 'staging': temporary, 'NEX_VERSION': '1.0.0'},
                capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(canonical.stat().st_ino, payload.stat().st_ino)
            self.assertEqual(publish.sha256(canonical), digest)
            self.assertEqual(apt.collect_debs([canonical]), [canonical])
            relative = apt.copy_deb(canonical, staging / 'apt', 'main')
            self.assertEqual(publish.sha256(staging / 'apt' / relative), digest)

    def test_workflows_build_netinst_for_download_site(self):
        for provider in ('.gitea', '.github'):
            workflow = (ROOT / provider / 'workflows/release.yml').read_text()
            self.assertIn('export NEX_NETINST_BASE_URL=', workflow)
            self.assertLess(workflow.index('export NEX_NETINST_BASE_URL='), workflow.index('bash installer/build.sh'))
            self.assertIn('${NEX_PUBLIC_URL%/}/downloads/${NEX_VERSION}', workflow)
            self.assertIn('--netinst-iso', workflow)
            self.assertIn('dist/netinst/*.deb', workflow)
            transfer = workflow.split('          scp $ssh_opts', 1)[1].split('"$target:$staging/"', 1)[0]
            self.assertEqual(transfer.count('dist/netinst/*.deb'), 1)
            self.assertNotIn('dist/core-local/', transfer)
            self.assertIn("ln '$staging'/nex-core-local-*.deb '$staging/nex-core-local_${NEX_VERSION}_amd64.deb'", workflow)
            self.assertIn('scripts/release-publish.py --check', workflow)
            # Syntax-check every embedded shell program without running Docker or publishing.
            for block in workflow.split('        run: |\n')[1:]:
                lines = []
                for line in block.splitlines():
                    if line and not line.startswith('          '):
                        break
                    lines.append(line[10:])
                result = subprocess.run(['bash', '-n'], input='\n'.join(lines), text=True, capture_output=True)
                self.assertEqual(result.returncode, 0, result.stderr)
