# SPDX-License-Identifier: AGPL-3.0-only
import hashlib
import importlib.util
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('netinst_manifest', ROOT / 'installer/netinst-manifest.py')
manifest = importlib.util.module_from_spec(spec)
spec.loader.exec_module(manifest)


class NetinstTests(unittest.TestCase):
    @staticmethod
    def deb(directory, package, version='1', architecture='amd64', source=None):
        package_root = directory / f'{package}-{version}'
        control = package_root / 'DEBIAN'
        control.mkdir(parents=True)
        fields = [f'Package: {package}', f'Version: {version}', f'Architecture: {architecture}',
                  'Maintainer: Test <test@example.org>', 'Description: test package']
        if source:
            fields.insert(3, f'Source: {source}')
        (control / 'control').write_text('\n'.join(fields) + '\n')
        output = directory / f'{package}_{version}_{architecture}.deb'
        subprocess.run(['dpkg-deb', '--build', str(package_root), str(output)],
                       check=True, capture_output=True)
        return output

    def test_matched_libvirt_builder_uses_init_and_pinned_apt_cache(self):
        build = (ROOT / 'installer/build.sh').read_text()
        command = build.split('build_matched_libvirt() {', 1)[1].split('\n}', 1)[0]
        self.assertIn('"$docker_bin" run --rm --init --platform linux/amd64', command)
        self.assertIn('-v nex-build-cache:/apt-cache', command)

    @unittest.skipUnless(shutil.which('dpkg-deb'), 'requires dpkg-deb')
    def test_installer_replaces_only_matching_libvirt_source_package_slots(self):
        build = (ROOT / 'installer/build.sh').read_text()
        helper = build.split('python3 - "$repository" "$build_output" "$third_party" <<\'PYLIBVIRT\'\n', 1)[1].split(
            '\nPYLIBVIRT\n', 1)[0]
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            repo = root / 'repo'
            repo.mkdir()
            output = root / 'output'
            for name in ('source', 'notices', 'recipe'):
                (output / name).mkdir(parents=True, exist_ok=True)
                (output / name / 'retained.txt').write_text(name)
            for name in ('binary-packages.txt', 'build-inputs.txt', 'build.log'):
                (output / name).write_text(name)
            third_party = root / 'third-party/libvirt'
            old = self.deb(root, 'libvirt0', '11.3.0-3+deb13u3', source='libvirt (11.3.0-3+deb13u3)')
            old.rename(repo / old.name)
            upstream = self.deb(root, 'unrelated', '1', source='other-source (1)')
            upstream.rename(repo / upstream.name)
            candidate = self.deb(root, 'libvirt0', '11.3.0-3+deb13u3+nex4', source='libvirt (11.3.0-3+deb13u3+nex4)')
            candidate.rename(output / candidate.name)
            unused = self.deb(root, 'libvirt-daemon-driver-xen', '11.3.0-3+deb13u3+nex4',
                              source='libvirt (11.3.0-3+deb13u3+nex4)')
            unused.rename(output / unused.name)

            subprocess.run([sys.executable, '-c', helper, str(repo), str(output), str(third_party)], check=True)
            self.assertEqual({path.name for path in repo.glob('*.deb')},
                             {upstream.name, candidate.name})
            self.assertFalse((third_party / candidate.name).exists())
            self.assertEqual((third_party / 'replaced-packages.tsv').read_text().splitlines()[1].split('\t')[:2],
                             ['libvirt0', 'amd64'])
            checks = subprocess.run(['sha256sum', '--check', 'SHA256SUMS'], cwd=third_party,
                                    capture_output=True, text=True)
            self.assertEqual(checks.returncode, 0, checks.stdout + checks.stderr)

    def test_manifest_pins_upstream_and_hosts_hash_pinned_release_payloads(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            repo = root / 'stage/nex/repo'
            repo.mkdir(parents=True)
            for package, arch, source in (('example', 'all', None),
                                          ('nex-core-local', 'amd64', None),
                                          ('dlm-controld', 'amd64', 'dlm (4.3.0-3+nex1)'),
                                          ('libvirt0', 'amd64', 'libvirt (11.3.0-3+deb13u3+nex4)')):
                built = self.deb(root, package, architecture=arch, source=source)
                built.rename(repo / built.name)
            uris = root / 'uris'
            uris.write_text("'https://snapshot.debian.org/archive/debian/20260920T000000Z/pool/example.deb' example_1_all.deb 17 SHA256:unused\n")
            manifest.build_manifest(root / 'stage', uris, 'https://downloads.example.org/1', root / 'out')
            rows = (repo.parent / 'netinst.tsv').read_text().splitlines()
            self.assertEqual(len(rows), 5)
            payloads = list((root / 'out').glob('*.deb'))
            self.assertEqual(len(payloads), 3)
            self.assertTrue(any(path.name.startswith('libvirt0-') for path in payloads))
            for row in rows[:-1]:
                checksum, name, url = row.split('\t')
                self.assertEqual(checksum, hashlib.sha256((repo.parent / name).read_bytes()).hexdigest())
                self.assertTrue(url.startswith('https://'))
            self.assertIn('https://fedorapeople.org/', rows[-1])
            with self.assertRaises(ValueError):
                manifest.build_manifest(root / 'stage', uris, 'http://downloads.example.org', root / 'out')

    def test_fetch_verifies_retries_and_reuses_only_valid_downloads(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            payload = root / 'payload'
            payload.write_bytes(b'authenticated package')
            checksum = hashlib.sha256(payload.read_bytes()).hexdigest()
            pin = root / 'manifest'
            pin.write_text(f'{checksum}\trepo/example.deb\thttps://downloads.example.org/example.deb\n')
            wget = root / 'wget'
            wget.write_text('#!/bin/sh\necho called >> "$CALLS"\nwhile [ "$1" != -O ]; do shift; done\ncp "$PAYLOAD" "$2"\n')
            wget.chmod(0o755)
            env = dict(os.environ, PATH=f'{root}:{os.environ["PATH"]}', NEX_NETINST_MANIFEST=str(pin), PAYLOAD=str(payload), CALLS=str(root / 'calls'))
            command = ['sh', str(ROOT / 'installer/netinst-fetch.sh'), str(root / 'destination')]
            subprocess.run(command, env=env, check=True, capture_output=True)
            subprocess.run(command, env=env, check=True, capture_output=True)
            self.assertEqual((root / 'calls').read_text().count('called'), 1)
            (root / 'destination/repo/example.deb').unlink()
            payload.write_bytes(b'corrupted')
            self.assertNotEqual(subprocess.run(command, env=env, capture_output=True).returncode, 0)
            self.assertFalse((root / 'destination/repo/example.deb').exists())
            self.assertFalse((root / 'destination/repo/example.deb.partial').exists())
            self.assertEqual((root / 'calls').read_text().count('called'), 4)

    def test_late_retry_reuses_installed_media_after_repeated_package_failure(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            installed = root / 'installed.iso'
            installed.write_bytes(b'verified media')
            late = (ROOT / 'installer/late-install.sh').read_text()
            block = '    if [ -f "$virtio_iso" ]; then' + late.split('    if [ -f "$virtio_iso" ]; then', 1)[1].split('    /bin/sh /cdrom/nex/netinst-fetch.sh', 1)[0]
            block = block.replace('/target/var/cache/nex-install', str(root / 'cache'))
            for _ in range(3):
                subprocess.run(['sh', '-ec', 'virtio_iso="$1"\n' + block, 'sh', str(installed)], check=True)
                self.assertEqual((root / 'cache/media/virtio-win.iso').stat().st_ino, installed.stat().st_ino)
                self.assertEqual(installed.read_bytes(), b'verified media')

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

    def test_native_target_logging_preserves_package_download_plan(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            # Debian in-target sends stdout to syslog unless --pass-stdout is used.
            wrapper = root / 'in-target'
            wrapper.write_text('#!/bin/sh\nif [ "$1" = --pass-stdout ]; then shift; exec "$@"; fi\n"$@" >/dev/null\n')
            wrapper.chmod(0o755)
            apt = root / 'apt-get'
            apt.write_text('#!/bin/sh\nprintf "\'file:/repo/nex-core-local.deb\' nex-core-local.deb 42 SHA256:test\\n"\n')
            apt.chmod(0o755)
            packages = root / 'packages.txt'
            packages.write_text('nex-core-local\n')
            late = (ROOT / 'installer/late-install.sh').read_text()
            block = late.split("    # Only fetch APT's actual plan, rather than downloading the installed base twice.\n", 1)[1].split('    if [ -f "$virtio_iso" ]; then', 1)[0]
            block = block.replace('/cdrom/nex/packages.txt', str(packages)).replace('/run/nex-netinst-plan', str(root / 'plan')).replace('/run/nex-netinst-packages', str(root / 'selected'))
            env = dict(os.environ, PATH=f'{root}:{os.environ["PATH"]}')
            subprocess.run(['sh', '-ec', block], env=env, check=True)
            self.assertEqual((root / 'selected').read_text(), 'nex-core-local.deb\n')
            subprocess.run(['sh', '-ec', block.replace('--pass-stdout ', '')], env=env, check=True)
            self.assertEqual((root / 'selected').read_text(), '', 'the native logging behavior must reproduce the original loss')

    def test_preflight_checks_each_nex_payload_and_each_upstream_once(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            rows = [
                ('repo/debian-a.deb', 'https://snapshot.debian.org/archive/debian/pool/a'),
                ('repo/debian-b.deb', 'https://snapshot.debian.org/archive/debian/pool/b'),
                ('repo/security.deb', 'https://snapshot.debian.org/archive/debian-security/pool/a'),
                ('repo/nex-core-local.deb', 'https://downloads.example.org/core'),
                ('repo/dlm.deb', 'https://downloads.example.org/dlm'),
                ('media/virtio-win.iso', 'https://fedorapeople.org/virtio.iso'),
            ]
            pin = root / 'manifest'
            pin.write_text(''.join(f'{"0" * 64}\t{name}\t{url}\n' for name, url in rows))
            wget = root / 'wget'
            wget.write_text('#!/bin/sh\necho "$*" >> "$CALLS"\n[ "${FAIL:-}" != yes ]\n')
            wget.chmod(0o755)
            env = dict(os.environ, PATH=f'{root}:{os.environ["PATH"]}', NEX_NETINST_MANIFEST=str(pin), CALLS=str(root / 'calls'))
            command = ['sh', str(ROOT / 'installer/netinst-fetch.sh'), '--check']
            subprocess.run(command, env=env, check=True, capture_output=True)
            self.assertEqual(len((root / 'calls').read_text().splitlines()), 5)
            self.assertNotEqual(subprocess.run(command, env=dict(env, FAIL='yes'), capture_output=True).returncode, 0)

    def test_fetch_resumes_interrupted_transfer(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            pin = root / 'manifest'
            checksum = hashlib.sha256(b'first-second').hexdigest()
            pin.write_text(f'{checksum}\trepo/example.deb\thttps://downloads.example.org/example.deb\n')
            wget = root / 'wget'
            wget.write_text('#!/bin/sh\nwhile [ "$1" != -O ]; do shift; done\nif [ -f "$2" ]; then printf second >> "$2"; else printf first- > "$2"; exit 1; fi\n')
            wget.chmod(0o755)
            env = dict(os.environ, PATH=f'{root}:{os.environ["PATH"]}', NEX_NETINST_MANIFEST=str(pin))
            subprocess.run(['sh', str(ROOT / 'installer/netinst-fetch.sh'), str(root / 'destination')], env=env, check=True, capture_output=True)
            self.assertEqual((root / 'destination/repo/example.deb').read_bytes(), b'first-second')

    @unittest.skipUnless(shutil.which('apt-get') and shutil.which('dpkg-scanpackages'), 'requires Debian APT tools')
    def test_real_apt_plan_matches_manifest_names_and_skips_installed_base(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            root.chmod(0o755)
            repo = root / 'repo'
            repo.mkdir()
            status = ''
            for package, version in [('nex-example', '1:2.0+test1'), ('nex-installed', '1.0')]:
                control = f'Package: {package}\nVersion: {version}\nArchitecture: all\nMaintainer: Operator <operator@example.org>\nDescription: test package\n'
                package_root = root / package
                (package_root / 'DEBIAN').mkdir(parents=True)
                (package_root / 'DEBIAN/control').write_text(control)
                filename = f'{package}_{version.replace(":", "%3a")}_all.deb'
                subprocess.run(['dpkg-deb', '--build', str(package_root), str(repo / filename)], check=True, capture_output=True)
                if package == 'nex-installed':
                    status += control + 'Status: install ok installed\n\n'
            result = subprocess.run(['dpkg-scanpackages', '.', '/dev/null'], cwd=repo, text=True, capture_output=True, check=True)
            (repo / 'Packages').write_text(result.stdout)
            names = {p.name for p in repo.glob('*.deb')}
            for deb in repo.glob('*.deb'):
                deb.unlink()
            (root / 'sources').write_text(f'deb [trusted=yes] file:{repo} ./\n')
            (root / 'status').write_text(status)
            (root / 'lists/partial').mkdir(parents=True)
            (root / 'archives/partial').mkdir(parents=True)
            apt = ['apt-get', '-o', f'Dir::Etc::sourcelist={root}/sources', '-o', 'Dir::Etc::sourceparts=-', '-o', f'Dir::State::status={root}/status', '-o', f'Dir::State::lists={root}/lists', '-o', f'Dir::Cache::archives={root}/archives', '-o', 'Debug::NoLocking=1']
            subprocess.run(apt + ['update'], check=True, capture_output=True)
            result = subprocess.run(apt + ['--print-uris', '--yes', '--no-install-recommends', 'install', 'nex-example', 'nex-installed'], check=True, text=True, capture_output=True)
            planned = [line.split()[1] for line in result.stdout.splitlines() if line.startswith("'")]
            self.assertEqual(planned, ['nex-example_1%3a2.0+test1_all.deb'])
            self.assertTrue(set(planned) <= names)
