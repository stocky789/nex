# SPDX-License-Identifier: AGPL-3.0-only
"""Phase 2 gate guest heartbeat gap detection, run without a lab."""
import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location(
    'phase2_gate_guest', Path(__file__).parent / 'integration/phase2_gate_guest.py')
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


class HeartbeatGapTests(unittest.TestCase):
    def test_steady_heartbeat_has_no_gap(self):
        self.assertEqual(gate.heartbeat_gaps('\n'.join(str(100 + i) for i in range(10)), 3), (10, []))

    def test_outage_gap_is_reported(self):
        samples, gaps = gate.heartbeat_gaps('100\n101\n102\n110\n111\n', 3)
        self.assertEqual(samples, 5)
        self.assertEqual(gaps, [{'from': 102.0, 'to': 110.0, 'seconds': 8.0}])

    def test_window_edges_catch_a_writer_that_stopped(self):
        samples, gaps = gate.heartbeat_gaps('100\n101\n102\n', 3, since=99, until=120)
        self.assertEqual(samples, 3)
        self.assertEqual(gaps, [{'from': 102.0, 'to': 120, 'seconds': 18.0}])

    def test_window_excludes_samples_outside_it(self):
        self.assertEqual(gate.heartbeat_gaps('1\n50\n100\n101\n200\n', 3, since=99.5, until=101.5), (2, []))

    def test_write_refuses_an_existing_marker_and_quotes_the_token(self):
        script = gate.write_script("nex 'x")
        self.assertTrue(script.startswith('test ! -e /var/lib/nex-gate/marker;'))
        self.assertIn("'nex '\"'\"'x'", script)

    def test_heartbeat_restart_keeps_the_marker_and_skips_an_active_unit(self):
        script = gate.heartbeat_script()
        self.assertNotIn('marker', script)
        self.assertIn('if ! systemctl is-active --quiet nex-gate-heartbeat; then', script)
        self.assertTrue(gate.write_script('x').endswith(script + '; cat /var/lib/nex-gate/marker.sha256'))

    def test_guest_clock_offset_is_measured_against_the_round_trip(self):
        now = gate.time.time()
        clock = gate.guest_clock(lambda command: str(now + 2.5))
        self.assertAlmostEqual(clock['offset_seconds'], 2.5, delta=0.05)
        self.assertGreaterEqual(clock['uncertainty_seconds'], 0)


if __name__ == '__main__':
    unittest.main()
