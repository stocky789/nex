# SPDX-License-Identifier: AGPL-3.0-only
"""Unit checks for the guest-continuity harness's pure helpers.

The point of these is narrow: the comparator must call a restart a restart. A continuity
check that cannot fail proves nothing about the operations it is pointed at, so the
negative cases below matter more than the positive one.
"""
import importlib.util
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).parent / "integration"))
spec = importlib.util.spec_from_file_location(
    "pool_continuity", Path(__file__).parent / "integration/pool_continuity.py")
continuity = importlib.util.module_from_spec(spec)
spec.loader.exec_module(continuity)

VM = "0813319f-e307-401e-a874-84d094a1f117"

# One line of `qemu-io -c "read -v ..."` output: offset, hex columns, then the ascii column.
DUMP = (
    "00100000:  31 37 38 39 39 35 33 33 39 36 2e 34 31 30 30 32  1789953396.41002\n"
    "00100010:  35 37 30 39 20 31 66 62 62 38 62 30 34 0a 00 00  5709 1fbb8b04...\n"
    "read 32/32 bytes at offset 1048576\n"
)


def snapshot(pid="1218", starttime="3645", domid="1", boot_id="guest-a",
             host_boot_id="host-a", stamp=100.0, domstate="running (booted)"):
    return {"guests": {VM: {"host": "core3",
                            "hostside": {"host_boot_id": host_boot_id, "pid": pid,
                                         "starttime": starttime, "domid": domid,
                                         "domstate": domstate},
                            "guestside": {"boot_id": boot_id, "stamp": stamp}}}}


class ParseHeartbeatTests(unittest.TestCase):
    def test_decodes_hex_columns_not_the_ascii_column(self):
        # The ascii column renders unprintable bytes as '.', which would corrupt both the
        # timestamp and the uuid. Decoding must come from the hex columns.
        beat = continuity.parse_heartbeat(DUMP)
        self.assertEqual(beat["raw"], "1789953396.410025709 1fbb8b04")
        self.assertEqual(beat["boot_id"], "1fbb8b04")
        self.assertAlmostEqual(beat["stamp"], 1789953396.410025709)

    def test_stops_at_the_first_nul(self):
        self.assertEqual(continuity.parse_heartbeat(
            "00000000:  41 42 00 43  AB.C\n")["raw"], "AB")

    def test_tolerates_an_unreadable_slot(self):
        beat = continuity.parse_heartbeat("")
        self.assertIsNone(beat["stamp"])
        self.assertIsNone(beat["boot_id"])


class CompareTests(unittest.TestCase):
    def test_unchanged_is_continuous(self):
        result = continuity.compare(snapshot(), snapshot(stamp=160.0), VM)
        self.assertTrue(result["continuous"])
        self.assertEqual(result["changed"], [])
        self.assertEqual(result["heartbeat_advance_s"], 60.0)

    def test_new_qemu_process_is_a_restart(self):
        result = continuity.compare(snapshot(), snapshot(pid="4099", starttime="9001"), VM)
        self.assertFalse(result["continuous"])
        self.assertIn("pid", result["changed"])
        self.assertIn("starttime", result["changed"])

    def test_pid_reuse_is_still_caught_by_starttime(self):
        # The hole pid alone leaves: same number, different process.
        result = continuity.compare(snapshot(), snapshot(starttime="9001"), VM)
        self.assertFalse(result["continuous"])
        self.assertEqual(result["changed"], ["starttime"])

    def test_in_guest_reboot_is_caught_by_guest_boot_id(self):
        # No host-side signal moves when the guest reboots inside the same QEMU process.
        result = continuity.compare(snapshot(), snapshot(boot_id="guest-b"), VM)
        self.assertFalse(result["continuous"])
        self.assertEqual(result["changed"], ["guest_boot_id"])

    def test_host_reboot_is_caught(self):
        result = continuity.compare(snapshot(), snapshot(host_boot_id="host-b"), VM)
        self.assertFalse(result["continuous"])
        self.assertIn("host_boot_id", result["changed"])

    def test_domstate_alone_would_have_passed_a_restart(self):
        # Observed in the lab: a destroy+start leaves `running (booted)` unchanged. This
        # test pins the reason domstate is recorded but never a pass criterion.
        before, after = snapshot(), snapshot(pid="4099", starttime="9001", domid="2",
                                             boot_id="guest-b")
        result = continuity.compare(before, after, VM)
        self.assertEqual(result["domstate_before"], result["domstate_after"])
        self.assertFalse(result["continuous"])


if __name__ == "__main__":
    unittest.main()
