# SPDX-License-Identifier: AGPL-3.0-only
import hashlib
import importlib.util
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('netinst_manifest', ROOT / 'installer/netinst-manifest.py')
manifest = importlib.util.module_from_spec(spec)
spec.loader.exec_module(manifest)


class NetinstTests(unittest.TestCase):
    def fixtures(self, root):
        image = root / 'image'
        (image / 'esp').mkdir(parents=True)
        for name in ('root.erofs', 'root.verity', 'roothash', 'manifest.json', 'nex-core_dev_0.efi', 'esp/loader.conf'):
            (image / name).write_bytes(name.encode())
        appliance = root / 'appliance'
        appliance.mkdir()
        (appliance / 'controller-system.img').write_bytes(b'controller system image')
        (root / 'stage/nex').mkdir(parents=True)
        return image, appliance

    def test_rows_are_hashed_files_and_payloads_keep_outside_the_slot(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            image, appliance = self.fixtures(root)
            manifest.build_manifest(root / 'stage', 'https://downloads.example.org/1', root / 'out', image, appliance)
            rows = [row.split('\t') for row in (root / 'stage/nex/payloads.tsv').read_text().splitlines()]
            by_destination = {destination: (checksum, url) for checksum, destination, url in rows}
            self.assertEqual(len(rows), len(by_destination))
            self.assertEqual(sorted(d for d in by_destination if d.startswith('image/')),
                             ['image/esp/loader.conf', 'image/manifest.json', 'image/nex-core_dev_0.efi',
                              'image/root.erofs', 'image/root.verity', 'image/roothash'])
            primary = hashlib.sha256(b'controller system image').hexdigest()
            self.assertEqual(sorted(d for d in by_destination if d.startswith('payload/')),
                             sorted([f'payload/{primary}/controller-system.img',
                                     f'payload/{manifest.VIRTIO_SHA256}/virtio-win.iso']))
            for destination, (checksum, url) in by_destination.items():
                self.assertTrue(url.startswith('https://'), destination)
                if url.startswith('https://downloads.example.org/1/'):
                    self.assertEqual(checksum, hashlib.sha256((root / 'out' / url.rsplit('/', 1)[1]).read_bytes()).hexdigest())
            self.assertIn('https://fedorapeople.org/', by_destination[f'payload/{manifest.VIRTIO_SHA256}/virtio-win.iso'][1])

    def test_manifest_refuses_plain_http_and_an_incomplete_image(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            image, appliance = self.fixtures(root)
            with self.assertRaises(ValueError):
                manifest.build_manifest(root / 'stage', 'http://downloads.example.org/1', root / 'out', image, appliance)
            (image / 'root.verity').unlink()
            with self.assertRaises(ValueError):
                manifest.build_manifest(root / 'stage', 'https://downloads.example.org/1', root / 'out', image, appliance)

    def fetch_environment(self, root, pin, script):
        wget = root / 'wget'
        wget.write_text(script)
        wget.chmod(0o755)
        return dict(os.environ, PATH=f'{root}:{os.environ["PATH"]}', NEX_NETINST_MANIFEST=str(pin))

    def test_fetch_selects_rows_by_prefix_and_rejects_any_other_destination(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            payload = root / 'payload'
            payload.write_bytes(b'image file')
            checksum = hashlib.sha256(payload.read_bytes()).hexdigest()
            pin = root / 'manifest'
            pin.write_text(f'{checksum}\timage/root.erofs\thttps://downloads.example.org/root.erofs\n'
                           f'{checksum}\tpayload/{checksum}/controller-system.img\thttps://downloads.example.org/c.img\n')
            env = dict(self.fetch_environment(root, pin, '#!/bin/sh\nwhile [ "$1" != -O ]; do shift; done\ncp "$PAYLOAD" "$2"\n'),
                       PAYLOAD=str(payload))
            fetch = ['sh', str(ROOT / 'installer/netinst-fetch.sh')]
            subprocess.run([*fetch, str(root / 'one')], env=dict(env, NEX_NETINST_ROWS='image/'), check=True, capture_output=True)
            self.assertTrue((root / 'one/image/root.erofs').is_file())
            self.assertFalse((root / 'one/payload').exists())
            subprocess.run([*fetch, str(root / 'two')], env=dict(env, NEX_NETINST_ROWS='payload/'), check=True, capture_output=True)
            self.assertTrue((root / f'two/payload/{checksum}/controller-system.img').is_file())
            self.assertFalse((root / 'two/image').exists())
            subprocess.run([*fetch, str(root / 'three')], env=env, check=True, capture_output=True)
            self.assertTrue((root / 'three/image/root.erofs').is_file())
            self.assertTrue((root / f'three/payload/{checksum}/controller-system.img').is_file())
            for destination in ('repo/example.deb', 'media/virtio-win.iso', 'image/../escape'):
                pin.write_text(f'{checksum}\t{destination}\thttps://downloads.example.org/x\n')
                result = subprocess.run([*fetch, str(root / 'four')], env=env, capture_output=True)
                self.assertNotEqual(result.returncode, 0, destination)
            self.assertFalse((root / 'four').exists())

    def test_fetch_verifies_retries_and_reuses_only_valid_downloads(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            payload = root / 'payload'
            payload.write_bytes(b'authenticated image file')
            checksum = hashlib.sha256(payload.read_bytes()).hexdigest()
            pin = root / 'manifest'
            pin.write_text(f'{checksum}\timage/root.erofs\thttps://downloads.example.org/root.erofs\n')
            env = dict(self.fetch_environment(root, pin, '#!/bin/sh\necho called >> "$CALLS"\nwhile [ "$1" != -O ]; do shift; done\ncp "$PAYLOAD" "$2"\n'),
                       PAYLOAD=str(payload), CALLS=str(root / 'calls'))
            command = ['sh', str(ROOT / 'installer/netinst-fetch.sh'), str(root / 'destination')]
            subprocess.run(command, env=env, check=True, capture_output=True)
            subprocess.run(command, env=env, check=True, capture_output=True)
            self.assertEqual((root / 'calls').read_text().count('called'), 1)
            (root / 'destination/image/root.erofs').unlink()
            payload.write_bytes(b'corrupted')
            self.assertNotEqual(subprocess.run(command, env=env, capture_output=True).returncode, 0)
            self.assertFalse((root / 'destination/image/root.erofs').exists())
            self.assertFalse((root / 'destination/image/root.erofs.partial').exists())
            self.assertEqual((root / 'calls').read_text().count('called'), 4)

    def test_module_loading_reloads_udev_before_replaying_device_events(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            log = root / 'calls'
            ask = root / 'user-setup-ask'
            stubs = {
                'anna': 'echo anna >> "$CALLS"',
                'anna-install': 'echo install >> "$CALLS"; touch "$ASK"',
                'depmod': 'test \"$*\" = -a; echo depmod >> \"$CALLS\"',
                'udevadm': 'test "$*" = "control --reload"; echo reload >> "$CALLS"',
                'update-dev': 'echo trigger >> "$CALLS"',
            }
            for name, body in stubs.items():
                path = root / name
                path.write_text('#!/bin/sh\nset -eu\n' + body + '\n')
                path.chmod(0o755)
            script = (ROOT / 'installer/root-account.sh').read_text().split("if ! grep -q 'NEX_ROOT_PASSWORD_REQUIRED'", 1)[0]
            script = script.replace('/usr/bin/user-setup-ask', str(ask))
            env = dict(os.environ, PATH=f'{root}:{os.environ["PATH"]}', CALLS=str(log), ASK=str(ask))
            subprocess.run(['sh', '-ec', script], env=env, check=True)
            self.assertEqual(log.read_text().splitlines(), ['anna', 'install', 'depmod', 'reload', 'trigger'])
            log.write_text('')
            subprocess.run(['sh', '-ec', script], env=env, check=True)
            self.assertEqual(log.read_text().splitlines(), ['depmod', 'reload', 'trigger'])

    def test_preflight_checks_each_nex_payload_and_each_upstream_once(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            rows = [
                ('image/root.erofs', 'https://downloads.example.org/root.erofs'),
                ('image/root.verity', 'https://downloads.example.org/root.verity'),
                ('payload/a/controller-system.img', 'https://downloads.example.org/controller'),
                ('payload/b/virtio-win.iso', 'https://fedorapeople.org/virtio.iso'),
                ('payload/b/virtio-win-other.iso', 'https://fedorapeople.org/other.iso'),
            ]
            pin = root / 'manifest'
            pin.write_text(''.join(f'{"0" * 64}\t{name}\t{url}\n' for name, url in rows))
            env = dict(self.fetch_environment(root, pin, '#!/bin/sh\necho "$*" >> "$CALLS"\n[ "${FAIL:-}" != yes ]\n'),
                       CALLS=str(root / 'calls'))
            command = ['sh', str(ROOT / 'installer/netinst-fetch.sh'), '--check']
            subprocess.run(command, env=env, check=True, capture_output=True)
            self.assertEqual(len((root / 'calls').read_text().splitlines()), 4)
            self.assertNotEqual(subprocess.run(command, env=dict(env, FAIL='yes'), capture_output=True).returncode, 0)

    def test_fetch_resumes_interrupted_transfer(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            pin = root / 'manifest'
            checksum = hashlib.sha256(b'first-second').hexdigest()
            pin.write_text(f'{checksum}\timage/root.erofs\thttps://downloads.example.org/root.erofs\n')
            env = self.fetch_environment(root, pin, '#!/bin/sh\nwhile [ "$1" != -O ]; do shift; done\nif [ -f "$2" ]; then printf second >> "$2"; else printf first- > "$2"; exit 1; fi\n')
            subprocess.run(['sh', str(ROOT / 'installer/netinst-fetch.sh'), str(root / 'destination')], env=env, check=True, capture_output=True)
            self.assertEqual((root / 'destination/image/root.erofs').read_bytes(), b'first-second')


if __name__ == '__main__':
    unittest.main()
