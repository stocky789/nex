# SPDX-License-Identifier: AGPL-3.0-only
"""Host package checks run without installing onto or mutating this workstation."""
import importlib.machinery
import importlib.util
import json
import hashlib
import os
import shutil
import sys
import tarfile
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
loader = importlib.machinery.SourceFileLoader('core_local_setup', str(ROOT / 'packaging/core-local/core-local-setup'))
spec = importlib.util.spec_from_loader(loader.name, loader)
setup = importlib.util.module_from_spec(spec)
loader.exec_module(setup)


class CoreLocalPackageTests(unittest.TestCase):
    def test_firstboot_root_login_removes_legacy_credentials_and_keeps_tls(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            real_path = Path
            def relocated(value, *rest):
                if str(value) == '/etc/motd.d':
                    return root / 'motd'
                if str(value) == '/etc/motd.d/50-nex-local':
                    return root / 'motd/50-nex-local'
                return real_path(value, *rest)
            with patch.object(setup, 'STATE', root / 'state'), patch.object(setup, 'ISSUE', root / 'issue/50-nex-local.issue'), patch.object(setup, 'Path', relocated), patch.object(setup, 'console_notice') as notice:
                setup.initialize({'address': '192.0.2.5'})
                self.assertIn('https://192.0.2.5:9443', notice.call_args.args[0])
                self.assertIn('Local web UI:', notice.call_args.args[0])
                self.assertEqual(notice.call_args.args[0], 'Local web UI: https://192.0.2.5:9443\n')
                self.assertEqual((root / 'motd/50-nex-local').read_text(), notice.call_args.args[0])
                self.assertNotIn('initial-password', notice.call_args.args[0])
                self.assertFalse((root / 'state/initial-password').exists())
                self.assertFalse((root / 'state/credentials.json').exists())
                self.assertEqual((root / 'state').stat().st_mode & 0o777, 0o700)
                (root / 'state/initial-password').write_text('legacy-password')
                (root / 'state/credentials.json').write_text('{"legacy": true}')
                cert = (root / 'state/tls.crt').read_bytes()
                setup.initialize({'address': '192.0.2.5'})
                self.assertFalse((root / 'state/initial-password').exists())
                self.assertFalse((root / 'state/credentials.json').exists())
                self.assertEqual(cert, (root / 'state/tls.crt').read_bytes())
                details = subprocess.check_output(['openssl', 'x509', '-in', str(root / 'state/tls.crt'), '-noout', '-ext', 'subjectAltName'], text=True)
                self.assertIn('192.0.2.5', details)
                setup.initialize({'address': '192.0.2.6'})
                self.assertNotEqual(cert, (root / 'state/tls.crt').read_bytes())
                details = subprocess.check_output(['openssl', 'x509', '-in', str(root / 'state/tls.crt'), '-noout', '-ext', 'subjectAltName'], text=True)
                self.assertIn('192.0.2.6', details)
                self.assertFalse((root / 'state/initial-password').exists())
                self.assertFalse((root / 'state/credentials.json').exists())

    def test_console_notice_is_public_atomic_and_refreshes_getty(self):
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(setup, 'ISSUE', Path(directory) / 'issue/50-nex-local.issue'), \
                patch.object(setup.subprocess, 'run') as run:
            setup.console_notice('Local web UI: https://192.0.2.5:9443\n')
            self.assertIn('https://192.0.2.5:9443', setup.ISSUE.read_text())
            self.assertEqual(setup.ISSUE.stat().st_mode & 0o777, 0o644)
            self.assertTrue(any('--reload' in call.args[0] for call in run.call_args_list))
            setup.console_notice('Local web UI: unavailable\n')
            self.assertNotIn('https://', setup.ISSUE.read_text())

    def test_failed_discovery_and_stop_clear_previous_console_url(self):
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(setup, 'ISSUE', Path(directory) / 'issue/50-nex-local.issue'), \
                patch.object(setup, 'CONFIG', Path(directory) / 'missing-config'), \
                patch.object(setup.subprocess, 'run'), \
                patch.object(setup.os, 'geteuid', return_value=0), \
                patch.object(setup.os, 'umask'), \
                patch.object(setup, 'discover', side_effect=ValueError('offline')):
            setup.console_notice('https://192.0.2.5:9443')
            with patch.object(sys, 'argv', ['core-local-setup', 'init']):
                with self.assertRaisesRegex(ValueError, 'offline'):
                    setup.main()
            self.assertNotIn('https://', setup.ISSUE.read_text())
            with patch.object(sys, 'argv', ['core-local-setup', 'offline']):
                setup.main()
            self.assertIn('unavailable', setup.ISSUE.read_text())
            self.assertNotIn('https://', setup.ISSUE.read_text())
        service = (ROOT / 'packaging/core-local/nex-core-local.service').read_text()
        self.assertIn('ExecStopPost=/usr/lib/nex/core-local-setup offline', service)

    def test_configuration_rejects_wildcard_and_missing_storage_before_write(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(setup, 'CONFIG', Path(directory) / 'config'):
            for address in ['0.0.0.0', '::', '224.0.0.1', 'not an address']:
                with self.assertRaises(ValueError):
                    setup.configure(address, directory, 'br0')
            with self.assertRaises(ValueError):
                setup.configure('192.0.2.5', '/no-such-nex-test-storage', 'br0')
            self.assertFalse(setup.CONFIG.exists())

    def test_explicit_configuration_disables_automatic_address_selection(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(setup, 'CONFIG', Path(directory) / 'config'):
            setup.configure('192.0.2.5', directory, '', automatic=True)
            self.assertTrue(json.loads(setup.CONFIG.read_text())['automatic'])
            setup.configure('192.0.2.6', directory, '')
            self.assertFalse(json.loads(setup.CONFIG.read_text())['automatic'])
            self.assertEqual(setup.CONFIG.stat().st_mode & 0o777, 0o600)

    def test_recovery_command_reuses_configured_paths_and_forwards_private_request(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            config = state / 'config'
            config.write_text(json.dumps({'address': '192.0.2.5', 'storage': directory, 'bridge': 'br0'}))
            with patch.object(setup, 'CONFIG', config), patch.object(setup, 'STATE', state), \
                    patch.object(setup.os, 'geteuid', return_value=0), patch.object(setup.os, 'umask'), \
                    patch.object(setup.os, 'execv') as execute, \
                    patch.object(sys, 'argv', ['core-local-setup', 'prepare-recovery', '--request-file', '/root/recovery.json']):
                setup.main()
                program, argv = execute.call_args.args
                self.assertEqual(program, '/usr/lib/nex/nex-core-local')
                self.assertNotIn('--admin-password-file', argv)
                self.assertEqual(argv[argv.index('--prepare-recovery-file') + 1], '/root/recovery.json')
                self.assertEqual(argv[argv.index('--storage-dir') + 1], directory)
                self.assertEqual(argv[argv.index('--bridge') + 1], 'br0')
                self.assertEqual(argv[argv.index('--listen') + 1], '192.0.2.5:9443')

    def test_management_discovery_selects_default_interface_and_refuses_offline(self):
        routes = [{'dev': 'eth1'}]
        addresses = [{'ifname': 'eth0', 'addr_info': [{'scope': 'global', 'local': '192.0.2.9'}]},
                     {'ifname': 'eth1', 'addr_info': [{'scope': 'global', 'local': '192.0.2.5'}]}]
        with patch.object(setup.subprocess, 'check_output', side_effect=[json.dumps(routes), json.dumps(addresses)]):
            self.assertEqual(setup.discover(), ('192.0.2.5', ''))
        with patch.object(setup.subprocess, 'check_output', side_effect=['[]', json.dumps(addresses)]):
            with self.assertRaisesRegex(ValueError, 'root must run'):
                setup.discover()

    def test_package_rejects_tampered_bundle_before_docker(self):
        with tempfile.TemporaryDirectory() as directory:
            stage = Path(directory)
            subprocess.run(['openssl', 'genpkey', '-algorithm', 'ED25519', '-out', str(stage / 'private.pem')], check=True)
            public = subprocess.check_output(['openssl', 'pkey', '-in', str(stage / 'private.pem'), '-pubout', '-outform', 'DER'])
            (stage / 'public.raw').write_bytes(public[-32:])
            (stage / 'manifest.json').write_text(json.dumps({'image_sha256': hashlib.sha256(b'original').hexdigest()}))
            subprocess.run(['openssl', 'pkeyutl', '-sign', '-inkey', str(stage / 'private.pem'), '-rawin',
                            '-in', str(stage / 'manifest.json'), '-out', str(stage / 'manifest.sig')], check=True)
            (stage / 'controller.qcow2').write_bytes(b'tampered')
            (stage / 'source.tar.gz').write_bytes(b'not reached')
            for name in ['nex-source.tar.gz', 'components.tsv', 'LICENSE', 'NOTICE', 'ARTWORK-LICENSE.md', 'build-inputs.json']:
                (stage / name).write_bytes(b'not reached')
            result = subprocess.run([sys.executable, str(ROOT / 'packaging/core-local/build.py'),
                                     '--source', str(stage / 'source.tar.gz'), '--bundle', str(stage),
                                     '--trusted-key', str(stage / 'public.raw'), '--output', str(stage / 'output'),
                                     '--docker', '/must-not-execute-docker'], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('digest does not match', result.stderr)
            (stage / 'public.raw').write_bytes(b'0' * 32)
            result = subprocess.run([sys.executable, str(ROOT / 'packaging/core-local/build.py'),
                                     '--source', str(stage / 'source.tar.gz'), '--bundle', str(stage),
                                     '--trusted-key', str(stage / 'public.raw'), '--output', str(stage / 'output'),
                                     '--docker', '/must-not-execute-docker'], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertNotIn('must-not-execute-docker', result.stderr)
            (stage / 'public.raw').write_bytes(public[-32:])
            (stage / 'controller.qcow2').write_bytes(b'original')
            with tarfile.open(stage / 'source.tar.gz', 'w:gz') as archive:
                for name in ['Dockerfile', 'build-inside.sh']:
                    archive.add(ROOT / 'packaging/core-local' / name, arcname='nex-source/packaging/core-local/' + name)
            shutil.copyfile(stage / 'source.tar.gz', stage / 'nex-source.tar.gz')
            docker = stage / 'docker-check'
            docker.write_text('#!/usr/bin/python3\nimport sys\nfrom pathlib import Path\n'
                              'if sys.argv[1] == "run":\n'
                              '    mount = next(x for x in sys.argv if x.endswith(":/input:ro"))\n'
                              '    assert (Path(mount.removesuffix(":/input:ro")) / "bundle").is_dir()\n')
            docker.chmod(0o755)
            result = subprocess.run([sys.executable, str(ROOT / 'packaging/core-local/build.py'),
                                     '--source', str(stage / 'source.tar.gz'), '--bundle', str(stage),
                                     '--trusted-key', str(stage / 'public.raw'), '--output', str(stage / 'output'),
                                     '--docker', str(docker)], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_package_rejects_source_mismatch_before_docker(self):
        with tempfile.TemporaryDirectory() as directory:
            stage = Path(directory)
            subprocess.run(['openssl', 'genpkey', '-algorithm', 'ED25519', '-out', str(stage / 'private.pem')], check=True)
            public = subprocess.check_output(['openssl', 'pkey', '-in', str(stage / 'private.pem'), '-pubout', '-outform', 'DER'])
            (stage / 'public.raw').write_bytes(public[-32:])
            (stage / 'controller.qcow2').write_bytes(b'image')
            (stage / 'manifest.json').write_text(json.dumps({'image_sha256': hashlib.sha256(b'image').hexdigest()}))
            subprocess.run(['openssl', 'pkeyutl', '-sign', '-inkey', str(stage / 'private.pem'), '-rawin',
                            '-in', str(stage / 'manifest.json'), '-out', str(stage / 'manifest.sig')], check=True)
            (stage / 'source.tar.gz').write_bytes(b'core source')
            (stage / 'nex-source.tar.gz').write_bytes(b'appliance source')
            for name in ['components.tsv', 'LICENSE', 'NOTICE', 'ARTWORK-LICENSE.md', 'build-inputs.json']:
                (stage / name).write_bytes(b'present')
            result = subprocess.run([sys.executable, str(ROOT / 'packaging/core-local/build.py'),
                                     '--source', str(stage / 'source.tar.gz'), '--bundle', str(stage),
                                     '--trusted-key', str(stage / 'public.raw'), '--output', str(stage / 'output'),
                                     '--docker', '/must-not-execute-docker'], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('does not match appliance bundle source', result.stderr)
            self.assertNotIn('must-not-execute-docker', result.stderr)

    def test_package_source_path_reuses_verified_appliance_archive(self):
        build = (ROOT / 'packaging/core-local/build-inside.sh').read_text()
        self.assertIn('ln -s ../appliance/nex-source.tar.gz "$pkg/usr/share/nex/core-local/nex-source.tar.gz"', build)
        with tempfile.TemporaryDirectory() as directory:
            package = Path(directory) / 'package'
            appliance_source = package / 'usr/share/nex/appliance/nex-source.tar.gz'
            core_source = package / 'usr/share/nex/core-local/nex-source.tar.gz'
            appliance_source.parent.mkdir(parents=True)
            core_source.parent.mkdir(parents=True)
            appliance_source.write_bytes(b'matching source')
            core_source.symlink_to('../appliance/nex-source.tar.gz')
            control = package / 'DEBIAN/control'
            control.parent.mkdir()
            control.write_text('Package: nex-test\nVersion: 1\nArchitecture: all\nDescription: test\n')
            deb = Path(directory) / 'nex-test.deb'
            subprocess.run(['dpkg-deb', '--root-owner-group', '--build', str(package), str(deb)], check=True, capture_output=True)
            extracted = Path(directory) / 'extracted'
            subprocess.run(['dpkg-deb', '-x', str(deb), str(extracted)], check=True, capture_output=True)
            source = extracted / 'usr/share/nex/core-local/nex-source.tar.gz'
            self.assertTrue(source.is_symlink())
            self.assertEqual(source.readlink(), Path('../appliance/nex-source.tar.gz'))
            self.assertEqual(source.read_bytes(), b'matching source')


    def test_postinst_owns_release_identity_from_package_version(self):
        for tool in ('dpkg-divert', 'dpkg-query'):
            if not shutil.which(tool):
                self.skipTest(f'{tool} missing: Debian dpkg tools are required for this check')
        with tempfile.TemporaryDirectory(prefix='nex-postinst-') as directory:
            root = Path(directory)
            for sub in ('usr/lib', 'etc', 'var/lib/dpkg', 'bin'):
                (root / sub).mkdir(parents=True)
            (root / 'usr/lib/os-release').write_text('PRETTY_NAME="Debian GNU/Linux 13 (trixie)"\nID=debian\nVERSION_CODENAME=trixie\n')
            (root / 'etc/os-release').symlink_to('../usr/lib/os-release')
            (root / 'etc/motd').write_text('Nex Core 0.1.0-alpha1 — Debian 13 virtualization host\n')
            (root / 'etc/issue').write_text('Nex Core 0.1.0-alpha1 (Debian 13)\nHost: \\n  Console: \\l\n\n')
            helper = root / 'bin/deb-systemd-helper'
            helper.write_text('#!/bin/sh\nexit 0\n')
            helper.chmod(0o755)

            def configure(version):
                (root / 'var/lib/dpkg/status').write_text(
                    f'Package: nex-core-local\nStatus: install ok half-configured\nArchitecture: amd64\nVersion: {version}\n\n')
                env = dict(os.environ, DPKG_ROOT=str(root), DPKG_ADMINDIR=str(root / 'var/lib/dpkg'),
                           PATH=f"{root / 'bin'}:{os.environ['PATH']}")
                subprocess.run(['sh', str(ROOT / 'packaging/core-local/postinst'), 'configure'],
                               env=env, check=True, capture_output=True, text=True)
                return (root / 'etc/os-release').read_text()

            release = configure('1.2.3~alpha1')
            self.assertFalse((root / 'etc/os-release').is_symlink())
            for line in ('NAME="Nex Core"', 'PRETTY_NAME="Nex Core 1.2.3~alpha1"', 'ID=nex-core', 'ID_LIKE=debian',
                         'VERSION="1.2.3~alpha1"', 'VERSION_ID=1.2.3~alpha1', 'DEBIAN_CODENAME=trixie'):
                self.assertIn(line + '\n', release)
            # Debian's symlink survives under the diverted name; upstream metadata is untouched.
            self.assertTrue((root / 'etc/os-release.debian').is_symlink())
            self.assertIn('ID=debian\n', (root / 'usr/lib/os-release').read_text())
            self.assertEqual((root / 'etc/motd').read_text(), 'Nex Core 1.2.3~alpha1 — Debian 13 virtualization host\n')
            self.assertTrue((root / 'etc/issue').read_text().startswith('Nex Core 1.2.3~alpha1 (Debian 13)\nHost: \\n  Console: \\l\n'))
            # Upgrade: identity follows the new package version without a second diversion.
            release = configure('1.2.4')
            self.assertIn('PRETTY_NAME="Nex Core 1.2.4"\n', release)
            self.assertIn('VERSION_ID=1.2.4\n', release)
            self.assertTrue((root / 'etc/os-release.debian').is_symlink())
            self.assertEqual((root / 'etc/motd').read_text(), 'Nex Core 1.2.4 — Debian 13 virtualization host\n')
            self.assertEqual((root / 'etc/nex/updates.conf').read_text(), 'repo_url=https://nexvirtualization.com/apt\n')
            # The Nex APT source is configured out of the box, signed by the packaged key.
            sources = (root / 'etc/apt/sources.list.d/nex.sources').read_text()
            for line in ('Types: deb', 'URIs: https://nexvirtualization.com/apt', 'Suites: nex',
                         'Components: main', 'Signed-By: /usr/share/keyrings/nex-archive-keyring.gpg'):
                self.assertIn(line + '\n', sources)
            # Hosts provisioned before the install guard shipped get the system stamp
            # backfilled, so their next upgrade is not refused by preinst.
            self.assertEqual((root / 'etc/nex/core-release').read_text(),
                             'NEX_CORE=1\nINSTALLED_BY=package-upgrade\n')
            grub_cfg = (root / 'etc/default/grub.d/nex-core.cfg').read_text()
            self.assertIn('GRUB_DISTRIBUTOR="Nex Core"\n', grub_cfg)
            self.assertIn('systemd.ssh_auto=no', grub_cfg)

    def test_postinst_keeps_the_installer_provenance_stamp(self):
        for tool in ('dpkg-divert', 'dpkg-query'):
            if not shutil.which(tool):
                self.skipTest(f'{tool} missing: Debian dpkg tools are required for this check')
        with tempfile.TemporaryDirectory(prefix='nex-stamp-') as directory:
            root = Path(directory)
            for sub in ('usr/lib', 'etc/nex', 'var/lib/dpkg', 'bin'):
                (root / sub).mkdir(parents=True)
            (root / 'usr/lib/os-release').write_text('ID=debian\nVERSION_CODENAME=trixie\n')
            (root / 'etc/os-release').symlink_to('../usr/lib/os-release')
            # An ISO-installed host records that provenance; postinst must not rewrite it.
            (root / 'etc/nex/core-release').write_text('NEX_CORE=1\nINSTALLED_BY=nex-core-iso\n')
            helper = root / 'bin/deb-systemd-helper'
            helper.write_text('#!/bin/sh\nexit 0\n')
            helper.chmod(0o755)
            (root / 'var/lib/dpkg/status').write_text(
                'Package: nex-core-local\nStatus: install ok half-configured\nArchitecture: amd64\nVersion: 0.1.5~alpha1\n\n')
            env = dict(os.environ, DPKG_ROOT=str(root), DPKG_ADMINDIR=str(root / 'var/lib/dpkg'),
                       PATH=f"{root / 'bin'}:{os.environ['PATH']}")
            subprocess.run(['sh', str(ROOT / 'packaging/core-local/postinst'), 'configure'],
                           env=env, check=True, capture_output=True, text=True)
            self.assertEqual((root / 'etc/nex/core-release').read_text(),
                             'NEX_CORE=1\nINSTALLED_BY=nex-core-iso\n')

    def test_apt_source_uri_follows_configured_repo_url(self):
        for tool in ('dpkg-divert', 'dpkg-query'):
            if not shutil.which(tool):
                self.skipTest(f'{tool} missing: Debian dpkg tools are required for this check')
        with tempfile.TemporaryDirectory(prefix='nex-aptsrc-') as directory:
            root = Path(directory)
            for sub in ('usr/lib', 'etc/nex', 'var/lib/dpkg', 'bin'):
                (root / sub).mkdir(parents=True)
            (root / 'usr/lib/os-release').write_text('ID=debian\nVERSION_CODENAME=trixie\n')
            (root / 'etc/os-release').symlink_to('../usr/lib/os-release')
            (root / 'etc/nex/updates.conf').write_text('repo_url=https://apt.example.test/nex\n')
            helper = root / 'bin/deb-systemd-helper'
            helper.write_text('#!/bin/sh\nexit 0\n')
            helper.chmod(0o755)
            (root / 'var/lib/dpkg/status').write_text(
                'Package: nex-core-local\nStatus: install ok half-configured\nArchitecture: amd64\nVersion: 0.1.5~alpha1\n\n')
            env = dict(os.environ, DPKG_ROOT=str(root), DPKG_ADMINDIR=str(root / 'var/lib/dpkg'),
                       PATH=f"{root / 'bin'}:{os.environ['PATH']}")
            subprocess.run(['sh', str(ROOT / 'packaging/core-local/postinst'), 'configure'],
                           env=env, check=True, capture_output=True, text=True)
            # A non-default (public) repo_url is honored; a private/lab URL would be reset earlier.
            self.assertIn('URIs: https://apt.example.test/nex\n',
                          (root / 'etc/apt/sources.list.d/nex.sources').read_text())

    def test_package_ships_archive_keyring_and_builds_the_apt_source(self):
        keyring = ROOT / 'packaging/core-local/nex-archive-keyring.gpg'
        self.assertTrue(keyring.is_file(), 'the Nex APT archive keyring must be committed')
        head = keyring.read_bytes()[:1]
        self.assertIn(head, (b'\x98', b'\x99'), 'keyring must be a dearmored (binary) GPG public key packet, not ASCII armor')
        build = (ROOT / 'packaging/core-local/build-inside.sh').read_text()
        self.assertIn('/usr/share/keyrings/nex-archive-keyring.gpg', build)
        # The prerm removes the generated source so removal leaves no source with a missing key.
        self.assertIn('/etc/apt/sources.list.d/nex.sources', (ROOT / 'packaging/core-local/prerm').read_text())

    def test_core_package_retains_compiled_go_notices(self):
        build = (ROOT / 'packaging/core-local/build-inside.sh').read_text()
        start = build.index('for file in \\\n    vendor/github.com/aws/aws-sdk-go-v2/LICENSE.txt')
        end = build.index('\ndone', start) + len('\ndone')
        copy_notices = 'set -euo pipefail\npkg="$1"\n' + build[start:end]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(['go', 'mod', 'vendor', '-o', str(root / 'vendor')], cwd=ROOT, check=True)
            package = root / 'package'
            def run_copy():
                return subprocess.run(['bash', '-c', copy_notices, 'copy-notices', str(package)],
                                      cwd=root, capture_output=True, text=True)
            self.assertEqual(run_copy().returncode, 0)
            notices = package / 'usr/share/doc/nex-core-local/go-dependencies'
            self.assertEqual(len(list(notices.rglob('*LICENSE*'))) + len(list(notices.rglob('*NOTICE*'))), 19)
            self.assertTrue((notices / 'github.com/aws/aws-sdk-go-v2/NOTICE.txt').is_file())
            self.assertTrue((notices / 'github.com/aws/smithy-go/NOTICE').is_file())
            self.assertTrue((notices / 'github.com/aws/aws-sdk-go-v2/internal/sync/singleflight/LICENSE').is_file())
            self.assertTrue((notices / 'github.com/aws/smithy-go/internal/sync/singleflight/LICENSE').is_file())
            self.assertFalse((notices / 'golang.org/x/sys').exists())
            (root / 'vendor/github.com/aws/smithy-go/NOTICE').unlink()
            self.assertNotEqual(run_copy().returncode, 0)

    def test_depends_cover_what_core_runs(self):
        """A host that installs this package on its own must get the tools Core execs.

        The ISO can mask a missing package dependency, but hosts upgraded through
        the package must receive every command the agent invokes.
        """
        import re
        build = (ROOT / 'packaging/core-local/build-inside.sh').read_text()
        line = re.search(r'^Depends: (.*)$', build, re.M)
        self.assertIsNotNone(line, 'the package must declare Depends')
        depends = {field.strip().split()[0] for field in line.group(1).split(',')}
        vlan = (ROOT / 'internal/vmhost/vlan.go').read_text()
        management = (ROOT / 'internal/vmhost/management_network.go').read_text()
        hardware = (ROOT / 'internal/agent/hardware.go').read_text()
        # Each entry is (package, the thing in Core that needs it, where that thing is).
        required = [
            ('iproute2', '"ip", "link"', vlan),
            ('ifupdown', '"ifup"', management),
            ('bridge-utils', 'bridge_ports', vlan),
            ('bridge-utils', 'bridge_ports', management),
            ('vlan', 'vlan-raw-device', management),
            ('dmidecode', '"dmidecode", "-t", "memory"', hardware),
            ('pciutils', '"lspci", "-mm", "-nn", "-D"', hardware),
        ]
        for package, needed, source in required:
            with self.subTest(package=package, needs=needed):
                self.assertIn(needed, source, 'this check is stale: Core no longer uses ' + needed)
                self.assertIn(package, depends, needed + ' needs the ' + package + ' package at runtime')

    def test_removal_never_erases_guest_or_state(self):
        import re
        script = (ROOT / 'packaging/core-local/prerm').read_text()
        self.assertIn('deb-systemd-invoke stop nex-core-local.service', script)
        self.assertNotIn('virsh ', script)
        # Removal may clean up the generated apt source, but never guest disks, VM
        # definitions, configuration, credentials or any recursive delete.
        for forbidden in ('/var/lib/libvirt', '/var/lib/nex', '/etc/nex', 'images', 'rm -rf', 'rm -r '):
            self.assertNotIn(forbidden, script)
        for removal in re.findall(r'\brm\b[^\n]*', script):
            self.assertIn('/etc/apt/sources.list.d/nex.sources', removal)
        service = (ROOT / 'packaging/core-local/nex-core-local.service').read_text()
        self.assertIn('UMask=0077', service)

    def test_embedded_agent_keeps_the_qemu_probe_identity_capability(self):
        """BUG-0093: the embedded agent uses runuser during ISO storage registration."""
        service = (ROOT / 'packaging/core-local/nex-core-local.service').read_text()
        self.assertIn('AmbientCapabilities=CAP_SETUID', service)
        self.assertIn('NoNewPrivileges=yes', service)
        self.assertIn('RestrictSUIDSGID=yes', service)

    def test_local_service_creates_the_embedded_agent_state_directory_privately(self):
        """BUG-0149: the local service must not leave its agent store at mode 0755."""
        service = (ROOT / 'packaging/core-local/nex-core-local.service').read_text()
        self.assertIn('StateDirectory=nex/core-local nex-core-agent', service)
        self.assertIn('StateDirectoryMode=0700', service)

    def test_local_service_restarts_after_a_clean_exit_for_the_network_rollback(self):
        """The embedded agent rolls a management-network change back only while it runs.

        Restart=on-failure would leave an exit 0 (or a signal reaped as success) with nothing
        armed during the rollback window; `systemctl stop`, which prerm and upgrades use,
        still stops a Restart=always unit.
        """
        service = (ROOT / 'packaging/core-local/nex-core-local.service').read_text()
        settings = dict(line.split('=', 1) for line in service.splitlines()
                        if '=' in line and not line.startswith('#'))
        self.assertEqual(settings.get('Restart'), 'always')


if __name__ == '__main__':
    unittest.main()
