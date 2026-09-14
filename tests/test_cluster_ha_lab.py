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


if __name__ == "__main__":
    unittest.main()
