# SPDX-License-Identifier: AGPL-3.0-only
import importlib.util
from contextlib import closing
import io
import hashlib
import json
import os
import re
import subprocess
import sqlite3
from pathlib import Path
import tarfile
import unittest
from unittest.mock import patch, MagicMock
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def module(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / 'appliance' / (name + '.py'))
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


class ApplianceTests(unittest.TestCase):
    def test_both_debian_builders_verify_supported_go_before_extracting(self):
        expected = '63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445'
        for name in ('appliance/Dockerfile', 'packaging/core-local/Dockerfile'):
            with self.subTest(builder=name), tempfile.TemporaryDirectory() as temporary:
                dockerfile = (ROOT / name).read_text()
                self.assertIn('go1.27.1.linux-amd64.tar.gz', dockerfile)
                self.assertIn('GOTOOLCHAIN=local', dockerfile)
                self.assertNotIn('install -y --no-install-recommends golang-go', dockerfile)
                command = re.search(r'echo "[^"\n]+" \| sha256sum -c -', dockerfile).group(0)
                self.assertIn(expected, command)
                self.assertIn(command + ' && tar ', dockerfile)
                archive = Path(temporary) / 'go.tar.gz'
                archive.write_bytes(b'tampered download')
                result = subprocess.run(['/bin/sh', '-c', command.replace('/tmp/go.tar.gz', str(archive))], capture_output=True)
                self.assertNotEqual(result.returncode, 0)
                valid = command.replace(expected, hashlib.sha256(archive.read_bytes()).hexdigest()).replace('/tmp/go.tar.gz', str(archive))
                self.assertEqual(subprocess.run(['/bin/sh', '-c', valid], capture_output=True).returncode, 0)

    def test_public_guest_assets_are_readable_by_service_user(self):
        script = (ROOT / 'appliance/build-inside.sh').read_text()
        command = re.search(r'chmod -R [^;]+ /usr/local/share/nex', script).group(0)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            share = root / 'share'
            share.mkdir(mode=0o700)
            (share / 'index.html').touch(mode=0o600)
            (share / 'nex-source.tar.gz').touch(mode=0o600)
            subprocess.run(['/bin/sh', '-c', command.replace('/usr/local/share/nex', str(share))], check=True)
            self.assertEqual(share.stat().st_mode & 0o005, 0o005)
            self.assertTrue(all(path.stat().st_mode & 0o004 for path in share.iterdir()))

    def test_firstboot_has_no_debian_cloud_final_ordering_cycle(self):
        # Debian13 cloud-final and cloud-init.target both run after multi-user.target.
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            units = {'multi-user.target': '[Unit]\nDescription=Multi user\n',
                     'cloud-init.target': '[Unit]\nDescription=Cloud init\nAfter=multi-user.target\nWants=cloud-final.service\n',
                     'cloud-final.service': '[Unit]\nDescription=Cloud final\nAfter=multi-user.target\n[Service]\nType=oneshot\nExecStart=/usr/bin/true\n'}
            for name, content in units.items():
                (root / name).write_text(content)
            unit = (ROOT / 'appliance/nex-appliance-init.service').read_text()
            (root / 'nex-appliance-init.service').write_text(unit.replace('/usr/local/lib/nex/appliance-firstboot', '/usr/bin/true'))
            target = re.search(r'^WantedBy=(.+)$', unit, re.M).group(1)
            directory = root / (target + '.wants')
            directory.mkdir()
            (directory / 'nex-appliance-init.service').symlink_to(root / 'nex-appliance-init.service')
            result = subprocess.run(['systemd-analyze', 'verify', '--man=no', str(root / 'cloud-init.target'), str(root / 'multi-user.target')], env=dict(os.environ, SYSTEMD_UNIT_PATH=str(root) + ':/usr/lib/systemd/system', LC_ALL='C'), text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertNotIn('ordering cycle', result.stderr.lower())

    def test_guest_inventory_preserves_dpkg_format_variables(self):
        script = (ROOT / 'appliance/build-inside.sh').read_text()
        command = re.search(r"--run-command '(dpkg-query[^']+)'", script).group(1)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            query = root / 'dpkg-query'
            query.write_text('#!/bin/sh\nprintf "%s" "$2"\n')
            query.chmod(0o755)
            target = root / 'inventory'
            command = command.replace('/usr/local/share/nex/components.tsv', str(target))
            subprocess.run(['/bin/sh', '-c', command], env=dict(os.environ, PATH=temporary), check=True)
            self.assertIn('${source:Package}', target.read_text())
            self.assertIn('${Version}', target.read_text())

    def test_bootstrap_rejects_missing_and_multiline_password(self):
        firstboot = module('firstboot')
        valid = {'admin_password': 'long-secret-password', 'tls_certificate': '-----BEGIN CERTIFICATE-----', 'tls_private_key': '-----BEGIN PRIVATE KEY-----'}
        for password in ('a', 'é', ' 🔑 ', ' ', 'é' * 512):
            firstboot.validate(dict(valid, admin_password=password))
        for password in ('', '\ud800', 'é' * 513, 'long-secret-password\n', 'long-secret-password\r', 'long-secret-password\x00', 'x' * 1025, None):
            with self.assertRaises(ValueError):
                firstboot.validate(dict(valid, admin_password=password))
        with self.assertRaises(ValueError):
            firstboot.validate({})

    def test_firstboot_resumes_after_bootstrap_cleanup_interruption(self):
        firstboot = module('firstboot')
        self.addCleanup(os.umask, os.umask(0o022))  # firstboot.main() sets a process-wide 077 umask
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            config, state = root / 'etc/nex-controller', root / 'var/lib/nex-controller'
            config.mkdir(parents=True)
            state.mkdir(parents=True)
            (root / 'etc/cloud').mkdir()
            (root / 'usr/local/share/nex/dist').mkdir(parents=True)
            (root / 'run/cloud-init').mkdir(parents=True)
            (root / 'run/cloud-init/instance-data-sensitive.json').write_text('private-bootstrap')
            for target in (config / 'tls.crt', config / 'tls.key', state / 'controller.db'):
                target.touch()
            original_rmtree = firstboot.shutil.rmtree
            response = MagicMock()
            response.__enter__.return_value.status = 200
            with patch.object(firstboot, 'CONFIG', config), patch.object(firstboot, 'STATE', state), patch.object(firstboot, 'Path', side_effect=lambda name: root / name.lstrip('/')), patch.object(firstboot.subprocess, 'run'), patch.object(firstboot.shutil, 'rmtree', side_effect=lambda name, **kwargs: original_rmtree(root / name.lstrip('/'), **kwargs)) as cleanup, patch.object(firstboot.ssl, 'create_default_context'), patch.object(firstboot.urllib.request, 'urlopen', return_value=response):
                delete = cleanup.side_effect
                cleanup.side_effect = PermissionError('simulated cleanup failure')
                with self.assertRaises(PermissionError):
                    firstboot.main()
                self.assertFalse((root / 'usr/local/share/nex/dist/appliance-ready.json').exists())
                cleanup.side_effect = delete
                firstboot.main()
            self.assertTrue((state / 'appliance-initialized').exists())
            self.assertEqual(json.loads((root / 'usr/local/share/nex/dist/appliance-ready.json').read_text()), {'bootstrap_version': 1, 'status': 'ready'})
            self.assertEqual((root / 'usr/local/share/nex/dist/appliance-ready.json').stat().st_mode & 0o777, 0o644)
            self.assertTrue((root / 'etc/cloud/cloud-init.disabled').exists())
            self.assertFalse((root / 'run/cloud-init').exists())

    def test_overwrite_refuses_inventory_pending_adoption_and_bootstrap(self):
        recovery = module('recovery')
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            state, config = root / 'state', root / 'config'
            state.mkdir()
            config.mkdir()
            with closing(sqlite3.connect(state / 'controller.db')) as database, database:
                for table in ('admins', 'sites', 'pools', 'hosts', 'operations', 'appliance_bootstraps', 'appliances'):
                    database.execute('CREATE TABLE ' + table + '(id TEXT)')
                database.execute("INSERT INTO admins VALUES('initial-admin')")
            recovery.require_empty_controller(state, config)
            for table in ('sites', 'hosts', 'operations', 'appliance_bootstraps', 'appliances'):
                with closing(sqlite3.connect(state / 'controller.db')) as database, database:
                    database.execute('INSERT INTO ' + table + " VALUES('existing')")
                with self.assertRaises(ValueError):
                    recovery.require_empty_controller(state, config)
                with closing(sqlite3.connect(state / 'controller.db')) as database, database:
                    database.execute('DELETE FROM ' + table)
            (config / 'bootstrap.json').touch()
            with self.assertRaises(ValueError):
                recovery.require_empty_controller(state, config)

    def test_recovery_requires_complete_untampered_identity(self):
        recovery = module('recovery')
        for variation in ('complete', 'missing-ca', 'tampered'):
            contents = {name: b'private-content' for name in recovery.REQUIRED}
            if variation == 'missing-ca':
                del contents['var/lib/nex-controller/host-ca.pem']
            metadata = {'source_sha256': 'version', 'files': {name: hashlib.sha256(data).hexdigest() for name, data in contents.items()}}
            if variation == 'tampered':
                contents['var/lib/nex-controller/controller.db'] = b'corrupted'
            contents['recovery.json'] = json.dumps(metadata).encode()
            stream = io.BytesIO()
            with tarfile.open(fileobj=stream, mode='w') as archive:
                for name, data in contents.items():
                    info = tarfile.TarInfo(name)
                    info.size = len(data)
                    archive.addfile(info, io.BytesIO(data))
            stream.seek(0)
            with tarfile.open(fileobj=stream) as archive:
                if variation == 'complete':
                    recovery.verify_archive(archive, 'version')
                else:
                    with self.assertRaises(ValueError):
                        recovery.verify_archive(archive, 'version')

    def test_restore_reassigns_private_state_to_service_account(self):
        recovery = module('recovery')
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'db').touch(mode=0o600)
            (root / 'keys').mkdir()
            (root / 'keys' / 'tls.key').touch(mode=0o600)
            with patch.object(recovery.os, 'chown') as chown:
                recovery.restore_ownership([root], 123, 456)
            self.assertEqual({call.args[0] for call in chown.call_args_list}, {root, *root.rglob('*')})
            self.assertTrue(all(call.args[1:] == (123, 456) for call in chown.call_args_list))
            self.assertTrue(all(path.stat().st_mode & 0o077 == 0 for path in [root, *root.rglob('*')]))

    def test_restore_rejects_traversal_links_and_unexpected_files(self):
        recovery = module('recovery')
        for name, kind in [('var/lib/nex-controller/db', tarfile.REGTYPE), ('../../etc/shadow', tarfile.REGTYPE), ('etc/shadow', tarfile.REGTYPE), ('var/lib/nex-controller/controller.lock', tarfile.REGTYPE), ('var/lib/nex-controller/link', tarfile.SYMTYPE)]:
            stream = io.BytesIO()
            with tarfile.open(fileobj=stream, mode='w') as archive:
                info = tarfile.TarInfo(name)
                info.type = kind
                archive.addfile(info)
            stream.seek(0)
            with tarfile.open(fileobj=stream) as archive:
                if name == 'var/lib/nex-controller/db':
                    self.assertEqual(len(recovery.checked_members(archive)), 1)
                else:
                    with self.assertRaises(ValueError):
                        recovery.checked_members(archive)


if __name__ == '__main__':
    unittest.main()
