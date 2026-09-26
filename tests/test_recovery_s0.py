import importlib.util
from pathlib import Path
import unittest


SCRIPT = Path(__file__).parent / "integration" / "recovery_s0.py"
SPEC = importlib.util.spec_from_file_location("recovery_s0", SCRIPT)
recovery_s0 = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(recovery_s0)


class ExactMountValidationTests(unittest.TestCase):
    def test_domain_type_is_limited_to_supported_lab_modes(self):
        self.assertEqual(recovery_s0.configured_domain_type("kvm"), "kvm")
        self.assertEqual(recovery_s0.configured_domain_type("qemu"), "qemu")
        with self.assertRaises(ValueError):
            recovery_s0.configured_domain_type("xen")

    def test_apparmor_evidence_requires_this_domain_in_enforce_mode(self):
        domain_uuid = "b595bb0b-f453-4eef-a89c-8ae2fae0d554"
        self.assertEqual(
            recovery_s0.require_enforcing_domain_profile(
                f"libvirt-{domain_uuid} (enforce)\n", domain_uuid),
            f"libvirt-{domain_uuid} (enforce)")
        for context in (f"libvirt-{domain_uuid} (complain)",
                        "unconfined", f"libvirt-{domain_uuid}-other (enforce)",
                        f"libvirt-{domain_uuid} other (enforce)"):
            with self.subTest(context=context), self.assertRaises(RuntimeError):
                recovery_s0.require_enforcing_domain_profile(context, domain_uuid)

    def test_snapshot_delete_flags_distinguish_active_from_explicit_false(self):
        self.assertEqual(recovery_s0.snapshot_delete_flags(
            "<domainsnapshot><snapshotDeleteInProgress>true</snapshotDeleteInProgress></domainsnapshot>"),
            [{"tag": "snapshotDeleteInProgress", "value": "true"}])
        self.assertEqual(recovery_s0.snapshot_delete_flags(
            "<domainsnapshot><snapshotDeleteInProgress>false</snapshotDeleteInProgress></domainsnapshot>"),
            [])
        self.assertEqual(recovery_s0.snapshot_delete_flags("<domainsnapshot/>") , [])

    def test_delete_apparmor_gate_requires_a_process_observed_during_delete(self):
        self.assertTrue(recovery_s0.delete_apparmor_context_observed(
            {"qemu_process_context": {"required": False, "observed": False}}))
        self.assertTrue(recovery_s0.delete_apparmor_context_observed(
            {"qemu_process_context": {"required": True, "observed": True}}))
        self.assertFalse(recovery_s0.delete_apparmor_context_observed(
            {"qemu_process_context": {"required": True, "observed": False}}))
        self.assertEqual(
            recovery_s0.delete_apparmor_context_status(
                {"qemu_process_context": {"required": False, "observed": False}}),
            {"required": False, "observed": False, "verified": None})
        self.assertEqual(
            recovery_s0.delete_apparmor_context_status(
                {"qemu_process_context": {"required": True, "observed": True}}),
            {"required": True, "observed": True, "verified": True})

    def test_chain_usage_counts_unique_images_and_actual_bytes(self):
        self.assertEqual(
            recovery_s0.chain_usage([
                {"filename": "/scratch/head.qcow2", "actual-size": 120},
                {"filename": "/scratch/base.qcow2", "actual-size": 80},
                {"filename": "/scratch/base.qcow2", "actual-size": 80},
            ]),
            {"image_count": 2, "actual_bytes": 200})

    def test_accepts_exact_target_type_and_source(self):
        self.assertEqual(
            recovery_s0.require_exact_mount(
                "/mnt/scratch nfs4 192.0.2.10:/exports/scratch\n",
                "/mnt/scratch", "nfs4", "192.0.2.10:/exports/scratch"),
            ["/mnt/scratch", "nfs4", "192.0.2.10:/exports/scratch"])

    def test_rejects_another_target_even_with_matching_type_and_source(self):
        with self.assertRaises(RuntimeError):
            recovery_s0.require_exact_mount(
                "/mnt/other nfs4 192.0.2.10:/exports/scratch\n",
                "/mnt/scratch", "nfs4", "192.0.2.10:/exports/scratch")

    def test_rejects_mismatched_type_or_source(self):
        for observed in ("/mnt/scratch cifs //192.0.2.10/share\n",
                         "/mnt/scratch nfs4 192.0.2.10:/exports/other\n"):
            with self.subTest(observed=observed), self.assertRaises(RuntimeError):
                recovery_s0.require_exact_mount(
                    observed, "/mnt/scratch", "nfs4", "192.0.2.10:/exports/scratch")

    def test_iscsi_attachment_must_match_target_and_mounted_device(self):
        output = """Target: iqn.example:scratch (non-flash)
  Attached SCSI devices:
    Attached scsi disk sda\t\tState: running
Target: iqn.example:other
  Attached SCSI devices:
    Attached scsi disk sdb State: running
"""
        recovery_s0.require_iscsi_attachment(output, "iqn.example:scratch", "/dev/sda")
        with self.assertRaises(RuntimeError):
            recovery_s0.require_iscsi_attachment(output, "iqn.example:scratch", "/dev/sdb")
        with self.assertRaises(RuntimeError):
            recovery_s0.require_iscsi_attachment(output, "iqn.example:missing", "/dev/sda")

    def test_snapshot_marker_source_comes_from_saved_domain(self):
        saved, post_point = recovery_s0.snapshot_disk_paths("""<domainsnapshot>
          <disks><disk name='vda' snapshot='external'><source file='/scratch/next.qcow2'/></disk></disks>
          <domain><devices><disk device='disk'><source file='/scratch/point.qcow2'/><target dev='vda'/></disk></devices></domain>
        </domainsnapshot>""")
        self.assertEqual(saved, {"vda": "/scratch/point.qcow2"})
        self.assertEqual(post_point, {"vda": "/scratch/next.qcow2"})

    def test_revert_overlay_suffix_must_be_a_canonical_uuid(self):
        value = "b595bb0b-f453-4eef-a89c-8ae2fae0d554"
        self.assertEqual(recovery_s0.revert_overlay_uuid(f"/scratch/disk-0.{value}"), value)
        for path in ("/scratch/disk-0.1790307311", f"/scratch/disk-0.{value.upper()}"):
            with self.subTest(path=path), self.assertRaises(RuntimeError):
                recovery_s0.revert_overlay_uuid(path)


if __name__ == "__main__":
    unittest.main()
