# SPDX-License-Identifier: AGPL-3.0-only
"""Check the packaged agent permits Linux network inventory's required socket."""
from pathlib import Path
import unittest


class AgentServiceTests(unittest.TestCase):
    def test_network_inventory_can_open_netlink(self):
        unit = (Path(__file__).resolve().parents[1] /
                'packaging/systemd/nex-core-agent.service').read_text()
        families = next(line.split('=', 1)[1].split() for line in unit.splitlines()
                        if line.startswith('RestrictAddressFamilies='))
        self.assertIn('AF_NETLINK', families,
                      'Go net.Interfaces uses netlink on Linux; blocking it loses host inventory')

    def test_qemu_access_probe_can_drop_to_libvirt_user(self):
        unit = (Path(__file__).resolve().parents[1] /
                'packaging/systemd/nex-core-agent.service').read_text()
        settings = dict(line.split('=', 1) for line in unit.splitlines()
                        if '=' in line and not line.startswith('#'))
        self.assertEqual(settings.get('AmbientCapabilities'), 'CAP_SETUID',
                         'runuser needs CAP_SETUID to verify libvirt-qemu repository access')
        self.assertEqual(settings.get('NoNewPrivileges'), 'yes')
        self.assertEqual(settings.get('RestrictSUIDSGID'), 'yes')


if __name__ == '__main__':
    unittest.main()
