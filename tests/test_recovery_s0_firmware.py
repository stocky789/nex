import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
import xml.etree.ElementTree as ET


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "recovery_s0_firmware", ROOT / "tests/integration/recovery_s0_firmware.py")
FIRMWARE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIRMWARE)


class RecoveryS0FirmwareTests(unittest.TestCase):
    def test_public_deletion_requires_secure_boot_vtpm_and_tcg(self):
        self.assertEqual(FIRMWARE.public_delete_domain_type("uefi-secure-boot-vtpm20", "qemu"), "qemu")
        for profile, domain_type in (("uefi-secure-boot-vtpm20", "kvm"),
                                     ("uefi-secure-boot-on", "qemu"),
                                     ("bios", "qemu")):
            with self.subTest(profile=profile, domain_type=domain_type):
                with self.assertRaisesRegex(ValueError, "requires Secure Boot/vTPM"):
                    FIRMWARE.public_delete_domain_type(profile, domain_type)

    def test_public_blockcommit_is_exact_two_disk_no_delete_command(self):
        command = FIRMWARE.public_blockcommit_argv("fixture", "vdb", "/run/a-vdb", "/run/d-vdb")
        self.assertEqual(command, ["blockcommit", "fixture", "vdb", "--top", "/run/a-vdb",
                                   "--base", "/run/d-vdb", "--wait", "--verbose"])
        self.assertNotIn("--delete", command)
        with self.assertRaisesRegex(ValueError, "selected two disks"):
            FIRMWARE.public_blockcommit_argv("fixture", "vdc", "/run/a", "/run/d")

    def test_blockcommit_compare_receipt_is_json_safe_for_path_arguments(self):
        comparison = subprocess.CompletedProcess([], 0, "", "")
        entries = (
            ("point_a", Path("/run/A-vda.qcow2"), Path("/run/A-reference.qcow2")),
            ("point_b", Path("/run/B-vda.qcow2"), Path("/run/B-reference.qcow2")),
            ("current_c", Path("/run/C-vda.qcow2"), Path("/run/C-reference.qcow2")),
            ("committed_d_equals_point_a", Path("/run/D-vda.qcow2"), Path("/run/A-reference.qcow2")),
            ("rebased_b_equals_point_b", Path("/run/B-vda.qcow2"), Path("/run/B-reference.qcow2")),
            ("current_c_equals_preconsolidation", Path("/run/C-vda.qcow2"), Path("/run/C-reference.qcow2")),
        )
        result = {"logical_reference_checks": {"vda": {
            label: FIRMWARE.firmware_compare_receipt(
                ["qemu-img", "compare", "-f", "qcow2", "-F", "qcow2", image, reference], comparison)
            for label, image, reference in entries}}}
        record = result["logical_reference_checks"]["vda"]["committed_d_equals_point_a"]
        self.assertEqual(record["argv"][-2:], ["/run/D-vda.qcow2", "/run/A-reference.qcow2"])
        for record in result["logical_reference_checks"]["vda"].values():
            self.assertTrue(all(isinstance(argument, str) for argument in record["argv"]))
        self.assertEqual(json.loads(json.dumps(result)), result)

    def test_firmware_receipt_is_atomically_persisted_before_cleanup(self):
        with tempfile.TemporaryDirectory() as temporary:
            receipt = Path(temporary) / "probe-result.json"
            FIRMWARE.persist_firmware_receipt(receipt, {"status": "passed", "steps": ["reopen"]})
            self.assertEqual(json.loads(receipt.read_text()), {"status": "passed", "steps": ["reopen"]})
            self.assertEqual(receipt.stat().st_mode & 0o777, 0o600)
            self.assertFalse(receipt.with_name("probe-result.json.tmp").exists())

    def test_receipt_failure_diagnostic_identifies_nested_path_fields(self):
        result = {"checks": {"vda": {"argv": ["qemu-img", Path("/run/D-vda.qcow2")]}}}
        fields = FIRMWARE.unserializable_firmware_paths(result)
        diagnostic = {"status": "incomplete", "unserializable_fields": fields}
        self.assertEqual(fields, [{"field": "$.checks.vda.argv[1]", "value": "/run/D-vda.qcow2"}])
        self.assertEqual(json.loads(json.dumps(diagnostic)), diagnostic)

    def test_post_b_writable_head_is_unlabeled_and_covers_both_disks(self):
        command = FIRMWARE.public_current_head_argv(
            "fixture", {"vda": "/run/current-a", "vdb": "/run/current-b"})
        self.assertEqual(command[:4], ["snapshot-create-as", "fixture", "--no-metadata", "--disk-only"])
        self.assertIn("vda,snapshot=external,file=/run/current-a", command)
        self.assertIn("vdb,snapshot=external,file=/run/current-b", command)
        with self.assertRaisesRegex(ValueError, "both selected disk heads"):
            FIRMWARE.public_current_head_argv("fixture", {"vda": "/run/current-a"})

    def test_expected_firmware_chain_includes_the_complete_parent_suffix(self):
        b_chain = FIRMWARE.firmware_chain_with_backing(
            "/run/B-vdb", ["/run/D-vdb", "/run/base.qcow2"])
        self.assertEqual(b_chain, ["/run/B-vdb", "/run/D-vdb", "/run/base.qcow2"])
        self.assertEqual(FIRMWARE.firmware_chain_with_backing("/run/current-vdb", b_chain),
                         ["/run/current-vdb", "/run/B-vdb", "/run/D-vdb", "/run/base.qcow2"])
        with self.assertRaisesRegex(ValueError, "exact parent image"):
            FIRMWARE.firmware_chain_with_backing("/run/B-vdb", [])

    def test_retained_snapshot_rewrite_checks_parent_and_exact_sources(self):
        raw = ("<domainsnapshot><name>point-b</name><parent><name>point-a</name></parent>"
               "<domain><devices>" +
               "<disk device='disk'><source file='/run/a-vda'/><target dev='vda'/></disk>" +
               "<disk device='disk'><source file='/run/a-vdb'/><target dev='vdb'/></disk>" +
               "</devices></domain><disks><disk name='vda'><source file='/run/b-vda'/></disk>"
               "<disk name='vdb'><source file='/run/b-vdb'/></disk></disks></domainsnapshot>")
        rewritten, replaced = FIRMWARE.rewrite_firmware_retained_snapshot(
            raw, snapshot_name="point-b", expected_parent="point-a",
            a_sources={"vda": "/run/a-vda", "vdb": "/run/a-vdb"},
            d_sources={"vda": "/run/d-vda", "vdb": "/run/d-vdb"})
        self.assertEqual(set(replaced), {"vda", "vdb"})
        self.assertNotIn("<parent>", rewritten)
        self.assertIn("/run/d-vda", rewritten)
        self.assertIn("/run/d-vdb", rewritten)
        with self.assertRaisesRegex(ValueError, "parent differs"):
            FIRMWARE.rewrite_firmware_retained_snapshot(
                raw.replace("point-a</name>", "point-x</name>"), snapshot_name="point-b",
                expected_parent="point-a", a_sources={"vda": "/run/a-vda", "vdb": "/run/a-vdb"},
                d_sources={"vda": "/run/d-vda", "vdb": "/run/d-vdb"})
        with self.assertRaisesRegex(ValueError, "saved source differs"):
            FIRMWARE.rewrite_firmware_retained_snapshot(
                raw.replace("/run/a-vda", "/run/foreign"), snapshot_name="point-b",
                expected_parent="point-a", a_sources={"vda": "/run/a-vda", "vdb": "/run/a-vdb"},
                d_sources={"vda": "/run/d-vda", "vdb": "/run/d-vdb"})

    def test_snapshot_disk_mapping_reads_saved_and_post_sources_separately(self):
        raw = ("<domainsnapshot><name>point-b</name><domain><devices>"
               "<disk device='disk'><source file='/run/a-vda'/><target dev='vda'/></disk>"
               "<disk device='disk'><source file='/run/a-vdb'/><target dev='vdb'/></disk>"
               "</devices></domain><disks>"
               "<disk name='vda'><source file='/run/b-vda'/></disk>"
               "<disk name='vdb'><source file='/run/b-vdb'/></disk>"
               "</disks></domainsnapshot>")
        self.assertEqual(FIRMWARE.firmware_snapshot_disk_paths(raw),
                         ({"vda": "/run/a-vda", "vdb": "/run/a-vdb"},
                          {"vda": "/run/b-vda", "vdb": "/run/b-vdb"}))

    def test_preconsolidation_snapshot_mapping_requires_exact_two_point_graph(self):
        def xml(name, parent, saved, post):
            parent_xml = f"<parent><name>{parent}</name></parent>" if parent else ""
            disks = "".join(
                f"<disk device='disk'><source file='{saved[target]}'/><target dev='{target}'/></disk>"
                for target in ("vda", "vdb"))
            points = "".join(
                f"<disk name='{target}'><source file='{post[target]}'/></disk>"
                for target in ("vda", "vdb"))
            return (f"<domainsnapshot><name>{name}</name>{parent_xml}<domain><devices>{disks}"
                    f"</devices></domain><disks>{points}</disks></domainsnapshot>")

        d = {"vda": "/run/d-vda", "vdb": "/run/d-vdb"}
        a = {"vda": "/run/a-vda", "vdb": "/run/a-vdb"}
        b = {"vda": "/run/b-vda", "vdb": "/run/b-vdb"}
        docs = {"point-a": xml("point-a", None, d, a), "point-b": xml("point-b", "point-a", a, b)}
        self.assertEqual(set(FIRMWARE.verify_firmware_snapshot_mapping(docs, d, a, b)),
                         {"point-a", "point-b"})
        with self.assertRaisesRegex(ValueError, "does not map"):
            FIRMWARE.verify_firmware_snapshot_mapping(
                {**docs, "point-b": docs["point-b"].replace("/run/b-vdb", "/run/foreign")}, d, a, b)

    def test_deb_metadata_parser_matches_dpkg_deb_field_output(self):
        self.assertEqual(FIRMWARE.parse_deb_metadata(
            "Package: libvirt-daemon-config-network\nVersion: 11.3.0-3+deb13u3+nex3\nArchitecture: all\n"),
            {"Package": "libvirt-daemon-config-network",
             "Version": "11.3.0-3+deb13u3+nex3", "Architecture": "all"})
        with self.assertRaisesRegex(ValueError, "malformed"):
            FIRMWARE.parse_deb_metadata("Package libvirt-daemon")

    def test_cloud_init_and_secure_boot_checks_use_real_emitted_lines(self):
        user_data, meta_data = FIRMWARE.firmware_cloud_init("vm-fixture", "ssh-ed25519 example")
        self.assertTrue(user_data.startswith("#cloud-config\n"))
        self.assertIn("\npackages:\n", user_data)
        self.assertIn("instance-id: vm-fixture\n", meta_data)
        self.assertNotIn("\\n", user_data + meta_data)
        self.assertTrue(FIRMWARE.secure_boot_is_enabled("1\nSecureBoot enabled\n"))
        self.assertFalse(FIRMWARE.secure_boot_is_enabled("1\\nSecureBoot enabled"))

    def test_qemu_confinement_is_bound_to_exact_domain_uuid(self):
        expected = "libvirt-12345678-abcd (enforce)"
        self.assertEqual(FIRMWARE.require_qemu_confinement(expected, "12345678-abcd"), expected)
        with self.assertRaisesRegex(ValueError, "differs from the domain profile"):
            FIRMWARE.require_qemu_confinement("libvirt-another-domain (enforce)", "12345678-abcd")

    def test_generated_public_tcg_domain_omits_invalid_cpu_mode_and_keeps_selected_devices(self):
        xml = FIRMWARE.public_firmware_domain_xml(
            "fixture", "12345678-abcd", "/run/vars.fd", "/run/vda.qcow2",
            "/run/vdb.qcow2", "/run/seed.iso")
        root = ET.fromstring(xml)
        self.assertEqual(root.get("type"), "qemu")
        self.assertIsNone(root.find("cpu"))
        self.assertEqual({disk.find("target").get("dev")
                          for disk in root.findall("./devices/disk[@device='disk']")}, {"vda", "vdb"})
        cdrom = root.find("./devices/disk[@device='cdrom']")
        self.assertIsNotNone(cdrom)
        self.assertEqual(cdrom.get("type"), "file")
        self.assertIsNotNone(root.find("./devices/tpm[@model='tpm-crb']"))
        self.assertEqual(root.find("./os/firmware/feature[@name='secure-boot']").get("enabled"), "yes")

    def test_marker_readback_shell_emits_separate_source_and_marker_lines(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            findmnt = bin_dir / "findmnt"
            findmnt.write_text("#!/bin/sh\nprintf '/dev/vdb\\n'\n")
            findmnt.chmod(0o755)
            root_marker = root / "root-marker"
            data_marker = root / "data-marker"
            root_marker.write_text("point-b-root")
            data_marker.write_text("point-b-data")
            result = subprocess.run(
                ["sh", "-ec", FIRMWARE.firmware_marker_readback_command(root_marker, data_marker)],
                capture_output=True, text=True, check=True,
                env={**os.environ, "PATH": str(bin_dir) + os.pathsep + os.environ.get("PATH", "")})
            self.assertEqual(result.stdout, "source=/dev/vdb\nroot=point-b-root\ndata=point-b-data\n")

    def test_data_disk_mount_shell_mounts_and_checks_the_selected_device(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            mountpoint = bin_dir / "mountpoint"
            mountpoint.write_text("#!/bin/sh\nexit 1\n")
            mountpoint.chmod(0o755)
            mount = bin_dir / "mount"
            mount.write_text("#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$MOUNT_CALL_LOG\"\n")
            mount.chmod(0o755)
            findmnt = bin_dir / "findmnt"
            findmnt.write_text("#!/bin/sh\nprintf '%s\\n' \"$FINDMNT_SOURCE\"\n")
            findmnt.chmod(0o755)
            environment = {**os.environ, "PATH": str(bin_dir) + os.pathsep + os.environ.get("PATH", ""),
                           "MOUNT_CALL_LOG": str(root / "mount.log"), "FINDMNT_SOURCE": "/dev/vdb"}
            result = subprocess.run(["sh", "-ec", FIRMWARE.firmware_data_disk_mount_command()],
                                    capture_output=True, text=True, env=environment)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((root / "mount.log").read_text(), "/dev/vdb /mnt/recovery\n")
            environment["FINDMNT_SOURCE"] = "/dev/vda"
            wrong_device = subprocess.run(["sh", "-ec", FIRMWARE.firmware_data_disk_mount_command()],
                                          capture_output=True, text=True, env=environment)
            self.assertNotEqual(wrong_device.returncode, 0)
    def test_exact_unlink_refuses_symlink_hardlink_and_outside_path(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            a_top = work / "a-vda.qcow2"
            a_top.write_bytes(b"point A")
            self.assertEqual(FIRMWARE.exact_unlink_precondition(a_top, work), 7)
            linked = work / "linked.qcow2"
            linked.hardlink_to(a_top)
            with self.assertRaisesRegex(ValueError, "unique regular"):
                FIRMWARE.exact_unlink_precondition(a_top, work)
            linked.unlink()
            symlink = work / "symlink.qcow2"
            symlink.symlink_to(a_top)
            with self.assertRaisesRegex(ValueError, "unique regular"):
                FIRMWARE.exact_unlink_precondition(symlink, work)
            outside = work.parent / (work.name + "-outside")
            outside.write_bytes(b"outside")
            try:
                with self.assertRaisesRegex(ValueError, "unique regular"):
                    FIRMWARE.exact_unlink_precondition(outside, work)
            finally:
                outside.unlink()


if __name__ == "__main__":
    unittest.main()
