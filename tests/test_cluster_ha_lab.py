# SPDX-License-Identifier: AGPL-3.0-only
"""Safety checks for the isolated shared-block lab fixture."""
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import xml.etree.ElementTree as ET


spec = importlib.util.spec_from_file_location(
    "cluster_ha_lab", Path(__file__).parent / "integration/cluster_ha_lab.py")
lab = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lab)


class ClusterHALabTests(unittest.TestCase):
    def fixture(self, root):
        cache = root / ".cache"
        cache.mkdir()
        target = cache / "nex-cluster-ha-lab.test"
        target.mkdir()
        (target / "lab.json").write_text(json.dumps({"disposable": True, "fixture": "cluster-ha-v1"}))
        return target

    def test_refuses_non_fixture_cleanup_target(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = self.fixture(root)
            with patch.object(lab, "CACHE", root / ".cache"):
                self.assertEqual(lab.check_lab(target), target)
                (target / "lab.json").write_text(json.dumps({"disposable": False}))
                with self.assertRaises(AssertionError):
                    lab.check_lab(target)

    def test_pressure_requires_low_memory_and_actual_swap_churn(self):
        before = {"pswpout": 100}
        with patch.object(lab, "memory_snapshot", return_value={"available": lab.PRESSURE_AVAILABLE - 1, "pswpout": 100}):
            self.assertFalse(lab.under_real_pressure(before)[0])
        with patch.object(lab, "memory_snapshot", return_value={"available": lab.PRESSURE_AVAILABLE - 1, "pswpout": 100 + lab.MAX_SWAPOUT_PAGES + 1}):
            self.assertTrue(lab.under_real_pressure(before)[0])

    def test_cleanup_refuses_reused_qemu_pid(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = self.fixture(root)
            command = ["qemu-system-x86_64", f"file={target / 'core1.qcow2'}"]
            (target / "core1-command.json").write_text(json.dumps(command))
            (target / "core1-qemu.pid").write_text(f"{os.getpid()}\n")
            (target / "core1-qemu.process.json").write_text(json.dumps(
                {"pid": os.getpid(), "starttime": 1, "command": command}))
            with patch.object(lab, "CACHE", root / ".cache"):
                with self.assertRaises(AssertionError):
                    lab.quit_owned(target, "core1")

    def test_start_budget_is_measured(self):
        with patch.object(lab, "memory_snapshot", return_value={"available": lab.MIN_START_AVAILABLE - 1, "pswpout": 0, "pswpin": 0, "swap_free": 0}):
            with self.assertRaises(AssertionError):
                lab.require_start_budget()

    def test_pristine_marker_uses_shipped_helper_and_lab_state(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = self.fixture(root)
            with patch.object(lab, "CACHE", root / ".cache"), patch.object(lab, "ssh") as remote:
                lab.prepare_pristine_corosync(target, "core1")
            command = remote.call_args.args[2]
            self.assertIn("/usr/lib/nex/corosync-pristine", command)
            self.assertIn("/var/lib/nex-core-agent/storage-cluster/corosync-unconfigured", command)
            self.assertIn("/var/lib/nex-cluster-agent/storage-cluster/corosync-unconfigured", command)
            self.assertIn("NO resources configured", command)
            self.assertIn("virsh -c qemu:///system", command)
            self.assertIn("nex-cluster-local.service corosync.service pacemaker.service dlm.service", command)
            self.assertIn("still active", command)
            self.assertIn("systemctl unmask dlm.service", command)
            self.assertIn("systemctl disable dlm.service", command)
            self.assertIn('test "$(systemctl is-enabled dlm.service)" = disabled', command)
            self.assertNotIn("dlm_controld.service", command)

    def test_pristine_marker_refuses_non_core_target(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = self.fixture(root)
            with patch.object(lab, "CACHE", root / ".cache"), patch.object(lab, "ssh") as remote:
                with self.assertRaises(AssertionError):
                    lab.prepare_pristine_corosync(target, "storage")
            remote.assert_not_called()

    def test_outer_domains_enable_smp_firmware_and_headless_vga_without_changing_identity(self):
        domain_id = "00000000-0000-0000-0000-000000000004"
        root = ET.fromstring(lab.domain_xml(Path("/lab"), "storage", domain_id, 54413))
        self.assertEqual(root.findtext("uuid"), domain_id)
        features = root.find("features")
        self.assertIsNotNone(features)
        self.assertIsNotNone(features.find("acpi"))
        self.assertIsNotNone(features.find("apic"))
        self.assertEqual(root.find("devices/video/model").get("type"), "vga")
        self.assertEqual(root.find("devices/disk/source").get("file"), "/lab/storage.qcow2")
        self.assertEqual(root.find("devices/disk/target").get("dev"), "vda")
        self.assertEqual(root.findall("devices/graphics"), [])
        arguments = [arg.get("value") for arg in root.findall("{http://libvirt.org/schemas/domain/qemu/1.0}commandline/{http://libvirt.org/schemas/domain/qemu/1.0}arg")]
        self.assertIn("virtio-net-pci,netdev=nat,mac=52:54:00:96:10:04,addr=0x10", arguments)
        self.assertIn("virtio-net-pci,netdev=private,mac=52:54:00:96:11:04,addr=0x11", arguments)


class PristineCorosyncGuardTests(unittest.TestCase):
    """BUG-0404: the guard aborted before it could guard anything.

    It ran `virsh` and `pcs`, which seed() does not install, and stopped
    nex-cluster-local.service, which does not exist on this fixture. Under `set -eu` each
    of those ended prepare() outright. It then named the units in one systemctl call with
    corosync ahead of pacemaker, so even once reached the shutdown left both running.
    """

    def setUp(self):
        self.command = lab.pristine_corosync_command()

    def test_absent_tools_do_not_abort_the_guard(self):
        # Each external tool must be reached only behind an existence check, because an
        # absent tool means the condition it guards cannot be occurring.
        for tool in ("virsh", "pcs"):
            guard = self.command.index(f"command -v {tool}")
            self.assertLess(guard, self.command.index(f"{tool} "),
                            f"{tool} is invoked before its existence is checked")

    def test_units_are_stopped_only_when_installed(self):
        self.assertIn("systemctl list-unit-files", self.command)
        self.assertNotIn("systemctl stop nex-cluster-local.service corosync.service",
                         self.command)

    def test_pacemaker_stops_before_corosync(self):
        # Corosync is pacemaker's messaging layer. Taking it away first leaves pacemaker
        # unable to finish stopping, and both units stay active.
        order = lab.PRISTINE_STOP_ORDER
        self.assertLess(order.index("pacemaker.service"), order.index("corosync.service"))
        self.assertLess(self.command.index("pacemaker.service"),
                        self.command.index("corosync.service"))

    def test_each_stop_is_bounded_and_waits_for_the_unit_to_go_down(self):
        # Bounded so a wedged unit cannot hang prepare() forever, and confirmed so the next
        # unit is not torn down mid-shutdown of the previous one.
        self.assertIn("timeout 60 systemctl stop", self.command)
        self.assertIn("systemctl is-active --quiet \"$unit\" || break", self.command)

    def test_still_fails_closed_on_a_unit_that_would_not_stop(self):
        self.assertIn("still active", self.command)
        self.assertIn("exit 1", self.command)

    def test_a_running_nested_guest_is_still_refused(self):
        # The guard's actual purpose; tolerating absent tools must not weaken it.
        self.assertIn("running-nested-vm", self.command)


if __name__ == "__main__":
    unittest.main()
