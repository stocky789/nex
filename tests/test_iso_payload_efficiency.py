# SPDX-License-Identifier: AGPL-3.0-only
import hashlib
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


class IsoPayloadEfficiencyTests(unittest.TestCase):
    def test_pinned_debian_iso_metadata_uses_archive_with_retries(self):
        build = (ROOT / 'installer/build.sh').read_text()
        fetch = build.split('iso=debian-13.6.0-amd64-netinst.iso', 1)[1].split('gpgv --keyring', 1)[0]
        self.assertIn('base=https://cdimage.debian.org/cdimage/archive/13.6.0/amd64/iso-cd', fetch)
        for name in ('SHA256SUMS', 'SHA256SUMS.sign'):
            self.assertIn(f'curl -fsSL --retry 5 --retry-all-errors --retry-delay 2 "$base/{name}"', fetch)

    def test_late_install_restores_only_verified_virtio_media(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            cdrom, target = root / 'cdrom', root / 'target'
            media = cdrom / 'nex' / 'media'
            media.mkdir(parents=True); (target / 'etc').mkdir(parents=True)
            original = root / 'virtio-win.iso'
            original.write_bytes((b'CD001' + bytes(range(256))) * 4096)
            compressed = media / 'virtio-win.iso.gz'
            with compressed.open('wb') as output:
                subprocess.run(['gzip', '-9n', '-c', str(original)], check=True, stdout=output)
            self.assertLess(compressed.stat().st_size, original.stat().st_size)
            expected = hashlib.sha256(original.read_bytes()).hexdigest()
            checksum = media / 'virtio-win.iso.sha256'
            checksum.write_text(f'{expected}  virtio-win.iso\n')
            late = (ROOT / 'installer/late-install.sh').read_text()
            block = late.split('[ -f /cdrom/nex/media/virtio-win.iso.gz ]', 1)[1]
            block = '[ -f /cdrom/nex/media/virtio-win.iso.gz ]' + block.split('mkdir -p /target/var/cache/nex-install', 1)[0]
            script = 'set -eu\n' + block.replace('/cdrom', str(cdrom)).replace('/target', str(target))

            result = subprocess.run(['sh', '-c', script], text=True, capture_output=True)
            restored = target / 'usr' / 'share' / 'nex' / 'media' / 'virtio-win.iso'
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(hashlib.sha256(restored.read_bytes()).hexdigest(), expected)

            restored.unlink()
            checksum.write_text('0' * 64 + '  virtio-win.iso\n')
            result = subprocess.run(['sh', '-c', script], text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(restored.exists(), 'a checksum mismatch left a driver ISO behind')

            checksum.write_text(f'{expected}  virtio-win.iso\n')
            compressed.write_bytes(compressed.read_bytes()[:-1])
            result = subprocess.run(['sh', '-c', script], text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(restored.exists(), 'a truncated payload left a driver ISO behind')

            compressed.unlink()
            result = subprocess.run(['sh', '-c', script], text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(restored.exists(), 'missing media left a driver ISO behind')

    def test_installer_uses_gzip_and_keeps_extracted_source_off_media(self):
        build = (ROOT / 'installer/build.sh').read_text()
        late = (ROOT / 'installer/late-install.sh').read_text()
        self.assertIn('gzip -9n -c "$fetch/$iso_name" > "$fetch/$iso_name.gz.partial"', build)
        self.assertIn('cp "$fetch/$iso_name.gz" "$stage/nex/media/virtio-win.iso.gz"', build)
        self.assertIn('$stage/nex/media/virtio-win.iso.gz', build)
        self.assertIn('local retained_source=$cache/virtio-win-retained-source', build)
        self.assertIn('bsdtar -xf "$retained_source/source/$src_rpm"', build)
        self.assertNotIn('bsdtar -xf "$fetch/$src_rpm" -C "$third_party/source"', build)
        # The closure is resolved against an empty directory but downloaded into the kept
        # one, and only the resolved files reach the media: stale debs in the cache never do.
        self.assertIn('--print-uris -o Dir::Cache::archives="$resolve"', build)
        self.assertIn('-o Dir::Cache::archives="$downloads/debs" install', build)
        self.assertIn('xargs -a "$cache/debs.list" -I{} cp "$downloads/debs/{}" "$stage/nex/repo/"', build)
        self.assertIn('gunzip -c /cdrom/nex/media/virtio-win.iso.gz > "$virtio_iso"', late)
        self.assertIn("trap 'rm -f \"$virtio_iso\"' 0 1 2 15", late)
        self.assertIn('sha256sum -c /cdrom/nex/media/virtio-win.iso.sha256', late)
        self.assertIn('grep -Fx gunzip', build)


if __name__ == '__main__':
    unittest.main()
