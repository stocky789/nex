# SPDX-License-Identifier: AGPL-3.0-only
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

    def test_installer_uses_gzip_and_keeps_extracted_source_off_media(self):
        build = (ROOT / 'installer/build.sh').read_text()
        self.assertIn('gzip -9n -c "$fetch/$iso_name" > "$fetch/$iso_name.gz.partial"', build)
        self.assertIn('cp "$fetch/$iso_name.gz" "$stage/nex/media/virtio-win.iso.gz"', build)
        self.assertIn('$stage/nex/media/virtio-win.iso.gz', build)
        self.assertIn('local retained_source=$cache/virtio-win-retained-source', build)
        self.assertIn('bsdtar -xf "$retained_source/source/$src_rpm"', build)
        self.assertNotIn('bsdtar -xf "$fetch/$src_rpm" -C "$third_party/source"', build)
        # The installer expands the compressed driver ISO in the initrd, so BusyBox needs gunzip.
        self.assertIn('grep -Fx gunzip', build)
        # The driver ISO travels once, gzip-compressed, as a hashed payload of nex-data.
        self.assertIn('cp "$stage/nex/media/virtio-win.iso.gz" "$stage/nex/payload/$virtio_sha/virtio-win.iso.gz"', build)
if __name__ == '__main__':
    unittest.main()
