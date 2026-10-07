# SPDX-License-Identifier: AGPL-3.0-only
"""Host package checks run without installing onto or mutating this workstation."""
import importlib.machinery
import importlib.util
import json
import sys
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
    def test_firstboot_keeps_tls_and_creates_no_credentials(self):
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
                cert = (root / 'state/tls.crt').read_bytes()
                setup.initialize({'address': '192.0.2.5'})
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
                patch.object(setup, 'WAITING', Path(directory) / 'run-nex/waiting-for-address'), \
                patch.object(setup.subprocess, 'run'), \
                patch.object(setup.os, 'geteuid', return_value=0), \
                patch.object(setup.os, 'umask'), \
                patch.object(setup, 'discover', side_effect=ValueError('offline')):
            setup.console_notice('https://192.0.2.5:9443')
            with patch.object(sys, 'argv', ['core-local-setup', 'init']):
                with self.assertRaisesRegex(ValueError, 'offline'):
                    setup.main()
            self.assertNotIn('https://', setup.ISSUE.read_text())
            # No management address yet is a host still starting: nex-boot-check must not roll the image back for it.
            self.assertTrue(setup.WAITING.exists())
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

    def test_controller_profile_digest_selects_the_two_disk_flags_and_survives_reconfiguration(self):
        digest = 'ab' * 32
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            config = state / 'config'
            with patch.object(setup, 'CONFIG', config), patch.object(setup, 'STATE', state), patch.object(setup, 'PAYLOAD', state / 'payload'):
                setup.configure('192.0.2.5', directory, '', controller_image_sha256=digest)
                setup.configure('192.0.2.6', directory, '', automatic=True)
                self.assertEqual(json.loads(config.read_text())['controller_image_sha256'], digest)
                with patch.object(setup.os, 'geteuid', return_value=0), patch.object(setup.os, 'umask'), \
                        patch.object(setup.os, 'execv') as execute, patch.object(sys, 'argv', ['core-local-setup', 'run']):
                    setup.main()
                    argv = execute.call_args.args[1]
                self.assertEqual(argv[argv.index('--controller-system-image') + 1], str(state / 'payload' / digest / 'controller-system.img'))
                self.assertEqual(argv[argv.index('--controller-system-image-sha256') + 1], digest)
                for single_disk in ('--image', '--manifest', '--signature', '--trusted-key'):
                    self.assertNotIn(single_disk, argv)
                with self.assertRaises(ValueError):
                    setup.configure('192.0.2.5', directory, '', controller_image_sha256='not-a-digest')
                setup.configure('192.0.2.5', directory, '', controller_image_sha256='')
                self.assertNotIn('controller_image_sha256', json.loads(config.read_text()))

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
        """The image must get every tool Core execs from this package's dependencies."""
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
        self.assertIn('UMask=0077', service)

    def test_local_service_restarts_after_a_clean_exit_for_the_network_rollback(self):
        """The embedded agent rolls a management-network change back only while it runs.

        Restart=on-failure would leave an exit 0 (or a signal reaped as success) with nothing
        armed during the rollback window; `systemctl stop`
        still stops a Restart=always unit.
        """
        service = (ROOT / 'packaging/core-local/nex-core-local.service').read_text()
        settings = dict(line.split('=', 1) for line in service.splitlines()
                        if '=' in line and not line.startswith('#'))
        self.assertEqual(settings.get('Restart'), 'always')


if __name__ == '__main__':
    unittest.main()
