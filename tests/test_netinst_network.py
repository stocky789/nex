# SPDX-License-Identifier: AGPL-3.0-only
"""Installer network command fixtures: never run host network commands."""
import os
import shutil
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class NetinstNetwork(unittest.TestCase):
    def test_management_activation_and_retry(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state = root / 'state'
            log = root / 'commands'
            resolver = root / 'resolv.conf'
            helper = root / 'network'
            source = (ROOT / 'installer/gui/netinst-network.sh').read_text()
            source = source.replace('/run/nex-installer', str(state))
            source = source.replace('/etc/resolv.conf', str(resolver))
            source = source.replace('/etc/udhcpc/default.script', str(root / 'dhcp-script'))
            source = source.replace('/usr/bin/busybox', str(root / 'busybox'))
            helper.write_text(source)
            dhcp_script = root / 'dhcp-script'
            dhcp_script.write_text(f'#!/bin/sh\necho "nameserver 203.0.113.53" > {resolver}\n')
            dhcp_script.chmod(0o755)
            for name in ('ip', 'busybox'):
                stub = root / name
                stub.write_text('#!/bin/sh\nprintf "%s\\n" "$0 $*" >> "$COMMAND_LOG"\n'
                                'case "$0" in *busybox) '
                                '[ "${DHCP_STATUS:-0}" = 0 ] && echo "nameserver 198.51.100.53" > "$RESOLVER"; '
                                'exit "${DHCP_STATUS:-0}";; esac\n')
                stub.chmod(0o755)
            env = {**os.environ, 'PATH': str(root) + ':/usr/bin:/bin', 'COMMAND_LOG': str(log),
                   'RESOLVER': str(resolver)}

            def run(mode, vlan='0', address='', gateway='', dns='', **extra):
                return subprocess.run(['sh', str(helper), 'eth0', mode, vlan, address, gateway, dns],
                                      env={**env, **extra}, capture_output=True, text=True)

            self.assertEqual(run('static', '42', '192.0.2.10/24', '192.0.2.1', '192.0.2.53').returncode, 0)
            commands = log.read_text()
            self.assertIn('link add link eth0 name nxinstall type vlan id 42', commands)
            self.assertIn('addr replace 192.0.2.10/24 dev nxinstall', commands)
            self.assertIn('route replace default via 192.0.2.1 dev nxinstall', commands)
            self.assertEqual(resolver.read_text(), 'nameserver 192.0.2.53\n')
            self.assertEqual((state / 'netinst-resolv.conf').read_text(), resolver.read_text())
            self.assertEqual(run('dhcp', DHCP_STATUS='1').returncode, 1)
            self.assertIn('link delete dev nxinstall', log.read_text())
            self.assertEqual(run('dhcp', dns='198.51.100.53').returncode, 0)
            self.assertEqual(resolver.read_text(), 'nameserver 198.51.100.53\n')
            self.assertEqual((state / 'netinst-resolv.conf').read_text(), resolver.read_text())
            self.assertIn('udhcpc -n -t 5 -T 3 -i eth0', log.read_text())
            self.assertNotIn(' -q ', log.read_text(), 'DHCP must renew during long downloads')
            self.assertEqual(subprocess.run(['sh', str(helper), 'renew'], env=env).returncode, 0)
            self.assertEqual((state / 'netinst-resolv.conf').read_text(), 'nameserver 203.0.113.53\n')
            self.assertEqual(run('static', address='192.0.2.20/24').returncode, 0)
            self.assertEqual(resolver.read_text(), '', 'a retry must not retain stale DNS')
            self.assertEqual((state / 'netinst-resolv.conf').read_text(), '')

    def test_netcfg_resolver_removal_before_partitioning(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state = root / 'run/nex-installer'
            state.mkdir(parents=True)
            (state / 'active').touch()
            (state / 'config.ini').write_text('[Host]\n')
            (state / 'image.env').write_text('mode=install\n')
            (state / 'netinst-resolv.conf').write_text('nameserver 192.0.2.53\n')
            resolver = root / 'resolv.conf'
            media = root / 'cdrom/nex'
            media.mkdir(parents=True)
            (media / 'payloads.tsv').touch()
            installer = media / 'nex-image-install.sh'
            installer.write_text(f'#!/bin/sh\necho "$1" >> {root / "ran"}\n')
            fetch = media / 'netinst-fetch.sh'
            fetch.write_text(f'#!/bin/sh\ntest "$(cat {resolver})" = "nameserver 192.0.2.53"\n')
            check = root / 'nex-config'
            check.write_text('#!/bin/sh\nexit 0\n')
            check.chmod(0o755)
            source = (ROOT / 'installer/gui/recheck.sh').read_text()
            for original, replacement in (('/run/nex-installer', state),
                                           ('/cdrom/nex', media),
                                           ('/etc/resolv.conf', resolver),
                                           ('/usr/bin/nex-config', check)):
                source = source.replace(original, str(replacement))
            result = subprocess.run(['sh', '-c', source], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(resolver.read_text(), 'nameserver 192.0.2.53\n')
            self.assertEqual((root / 'ran').read_text(), 'preflight\ninstall\n')
            (state / 'netinst-resolv.conf').unlink()
            resolver.unlink()
            result = subprocess.run(['sh', '-c', source], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0, 'missing validated DNS must stop before partitioning')
            self.assertEqual((root / 'ran').read_text(), 'preflight\ninstall\n', 'the image installer must not run without validated DNS')

    def test_config_network_gate_and_preflight(self):
        if not shutil.which('cc') or not shutil.which('pkg-config'):
            self.skipTest('C compiler and GLib development files required')
        flags = subprocess.run(['pkg-config', '--cflags', '--libs', 'glib-2.0'], capture_output=True, text=True)
        if flags.returncode:
            self.skipTest('GLib development files required')
        source = (ROOT / 'installer/gui/nex-config.c').read_text()
        # Simulate only device ownership, identity and paths; execute real main,
        # network validation, helper argv, preflight and answer generation.
        start = source.index('static guint64 validate_disk(')
        end = source.index('static void validate(', start)
        source = source[:start] + ('static guint64 validate_disk(const char *a, const char *b, '
                  'const char *c, const char *d) { return 17179869184ULL; }\n') + source[end:]
        source = source.replace('st.st_uid!=0', 'st.st_uid!=getuid()')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state = root / 'run/nex-installer'
            state.mkdir(parents=True)
            for original in ('/cdrom', '/run/nex-installer', '/sys', '/usr/share/nex-installer', '/usr/lib/nex-installer'):
                source = source.replace('"' + original, '"' + str(root) + original)
            def write(relative, content, executable=False):
                path = root / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(content)
                path.chmod(0o700 if executable else 0o600)
                return path
            write('cdrom/nex/payloads.tsv', '')
            write('usr/share/nex-installer/timezones', 'UTC\n')
            write('sys/class/net/eth0/address', '02:00:00:00:00:01\n')
            write('usr/lib/nex-installer/netinst-network',
                          '#!/bin/sh\nprintf "%s\\n" "$#" "$@" > ' + str(root / 'args') + '\n', True)
            check = write('cdrom/nex/netinst-fetch.sh', 'exit 0\n')
            compiled = write('config.c', source)
            subprocess.run(['cc', '-I' + str(ROOT / 'installer/gui'), str(compiled),
                            str(ROOT / 'installer/gui/nex-network.c'), '-o', str(root / 'config'),
                            *flags.stdout.split(), '-lcrypt'], check=True, capture_output=True)
            template = ('[Host]\nhostname=nexcore\ntimezone=UTC\nkeymap=us\npassword={password}\n'
                        'disk=/dev/sda;\ndisk_size=17179869184;\ndisk_serial=example;\ndisk_major_minor=8:0;\n'
                        '[Network]\nmanagement={management}\n[NIC:eth0]\n'
                        'mac=02:00:00:00:00:01\nmode={mode}\nvlan=0\n')
            def run(management='eth0', mode='dhcp', password='example-pass'):
                config = write('run/nex-installer/config.ini', template.format(management=management, mode=mode, password=password))
                return subprocess.run([str(root / 'config'), '--apply', str(config)], capture_output=True, text=True, timeout=10)
            for management, mode in (('', 'dhcp'), ('eth0', 'disabled')):
                self.assertNotEqual(run(management, mode).returncode, 0)
                self.assertFalse((root / 'args').exists())
                self.assertFalse((state / 'answers.cfg').exists())
            result = run(password='x')
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((state / 'answers.cfg').exists())
            (state / 'answers.cfg').unlink()
            result = run(password='')
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse((state / 'answers.cfg').exists())
            result = run()
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((root / 'args').read_text().splitlines(), ['6', 'eth0', 'dhcp', '0', '', '', ''])
            self.assertTrue((state / 'answers.cfg').exists())
            (state / 'answers.cfg').unlink()
            check.write_text('exit 1\n')
            result = run()
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('sources are unavailable', result.stderr)
            self.assertFalse((state / 'answers.cfg').exists())
            self.assertNotIn('example-pass', result.stderr)
            (root / 'cdrom/nex/payloads.tsv').unlink()
            (root / 'args').unlink()
            result = run('', 'disabled')
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((state / 'answers.cfg').exists())
            self.assertFalse((root / 'args').exists(), 'offline ISO must not activate networking')

    def test_network_label_waits_for_media_discovery(self):
        source = (ROOT / 'installer/gui/nex-wizard.c').read_text()
        discovery = source[source.index('static void discover(void)'):source.index('static void prepare_done(')]
        self.assertIn('gtk_label_set_text(GTK_LABEL(network_intro)', discovery)
        self.assertIn('The net installer uses the management adapter to download software.', discovery)
        # Widgets exist before prepare mounts /cdrom; the marker is not readable yet.
        self.assertNotIn('g_file_test("/cdrom/nex/payloads.tsv"', source[source.index('int main('):])

    def test_preflight_precedes_destructive_operations(self):
        recheck = (ROOT / 'installer/gui/recheck.sh').read_text()
        self.assertLess(recheck.index('netinst-fetch.sh --check'), recheck.rindex('if [ -f /run/nex-installer/active ]'))
        self.assertLess(recheck.index('netinst-fetch.sh --check'), recheck.index('nex-image-install.sh preflight'))
        config = (ROOT / 'installer/gui/nex-config.c').read_text()
        self.assertLess(config.index('netinst-network'), config.index('netinst-fetch.sh'))
        self.assertLess(config.index('netinst-fetch.sh'), config.index('protected_write("/run/nex-installer/answers.cfg"'))
        self.assertIn('The net installer requires a management network.', config)
        build = (ROOT / 'installer/build-gui.sh').read_text()
        self.assertIn('install -m 755 installer/gui/netinst-network.sh', build)


if __name__ == '__main__':
    unittest.main()
