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


if __name__ == '__main__':
    unittest.main()
