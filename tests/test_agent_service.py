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

    def test_agent_restarts_after_a_clean_exit_so_a_network_rollback_stays_armed(self):
        """A management-network change is confirmed or rolled back by the running agent.

        With Restart=on-failure an exit 0 (or a signal reaped as success) leaves the host on
        an unconfirmed management network with nothing left to roll it back. `systemctl stop`
        and dpkg's upgrade stop still stop a Restart=always unit, so upgrades are unaffected.
        """
        unit = (Path(__file__).resolve().parents[1] /
                'packaging/systemd/nex-core-agent.service').read_text()
        settings = dict(line.split('=', 1) for line in unit.splitlines()
                        if '=' in line and not line.startswith('#'))
        self.assertEqual(settings.get('Restart'), 'always')


if __name__ == '__main__':
    unittest.main()
