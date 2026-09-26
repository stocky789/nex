import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from types import SimpleNamespace


ROOT = Path(__file__).resolve().parents[1]


def load(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


INTERRUPT = load(ROOT / "tests/integration/recovery_s0_interrupt.py", "recovery_s0_interrupt")
CONSOLIDATE = load(ROOT / "tests/integration/recovery_s0_consolidate.py", "recovery_s0_consolidate")


class RecoveryS0ConsolidateTests(unittest.TestCase):
    def test_storage_selection_is_limited_to_exact_known_roots_and_types(self):
        local = CONSOLIDATE.validate_storage_selection(
            CONSOLIDATE.IMAGE_ROOT, None, None, None, None)
        self.assertEqual(local["validation"], "local-default")
        nfs = CONSOLIDATE.validate_storage_selection(
            "/mnt/recovery-s0-vm-nfs", "nfs4", "example.invalid:/vm-nfs", None, None)
        self.assertEqual(nfs["fstype"], "nfs4")
        with self.assertRaisesRegex(ValueError, "previously selected"):
            CONSOLIDATE.validate_storage_selection("/mnt/unselected", "nfs4", "src", None, None)
        with self.assertRaisesRegex(ValueError, "requires an exact cifs"):
            CONSOLIDATE.validate_storage_selection(
                "/mnt/recovery-s0-vm-smb", "nfs4", "src", None, None)
        with self.assertRaisesRegex(ValueError, "exact target and 2G"):
            CONSOLIDATE.validate_storage_selection(
                "/mnt/recovery-s0-iscsi-exclusive", "ext4", "/dev/sda", None, None)
        iscsi = CONSOLIDATE.validate_storage_selection(
            "/mnt/recovery-s0-iscsi-exclusive", "ext4", "/dev/sda", "target-iqn", "2G")
        self.assertEqual(iscsi["device_size"], "2G")

    def test_candidate_identity_accepts_only_paired_explicit_overrides(self):
        manifest = "a" * 64
        self.assertEqual(CONSOLIDATE.candidate_identity("default", manifest, None, None),
                         ("default", manifest))
        self.assertEqual(CONSOLIDATE.candidate_identity("default", manifest, "11.3+nex3", "b" * 64),
                         ("11.3+nex3", "b" * 64))
        with self.assertRaisesRegex(ValueError, "supplied together"):
            CONSOLIDATE.candidate_identity("default", manifest, "11.3+nex3", None)
        with self.assertRaisesRegex(ValueError, "lowercase SHA256"):
            CONSOLIDATE.candidate_identity("default", manifest, "11.3+nex3", "B" * 64)

    def remote_namespace(self, campaign=None):
        package_config = {
            "packages": list(INTERRUPT.PACKAGE_SET),
            "package_version": INTERRUPT.PACKAGE_VERSION,
            "package_manifest_sha256": INTERRUPT.PACKAGE_MANIFEST_SHA256,
            "sentinel": INTERRUPT.SENTINEL,
            "staged_package_dir": "/tmp/recovery-s0-test-packages",
        }
        config = {"image_root": "/var/lib/libvirt/images/nex-recovery-s0-consolidate",
                  "work": "/var/lib/libvirt/images/nex-recovery-s0-consolidate/test-domain",
                  "domain": "test-domain", "mode": "normal", "sentinel": INTERRUPT.SENTINEL,
                  "storage_observation": {"requested_root": "/var/lib/libvirt/images/nex-recovery-s0-consolidate",
                                          "validation": "local-default"},
                  "allowed_storage_roots": list(CONSOLIDATE.STORAGE_ROOTS),
                  "boundary_timeout_seconds": 10, "bandwidth_mib": 1,
                  "run_campaign": False}
        source = CONSOLIDATE.build_remote_payload(
            CONSOLIDATE.native_helper_source(), config,
            campaign or INTERRUPT.REMOTE_CAMPAIGN.replace("__INTERRUPT_CONFIG__", repr(package_config)))
        namespace = {"__name__": "recovery_s0_consolidate_remote_test"}
        exec(compile(source, "recovery_s0_consolidate_remote.py", "exec"), namespace)
        return namespace

    def test_remote_payload_compiles_and_keeps_real_job_parser(self):
        namespace = self.remote_namespace()
        self.assertTrue(namespace["active_blockjob"]("Block Commit: [ 25.39 %]"))
        self.assertFalse(namespace["active_blockjob"]("No current block job for vda"))
        self.assertEqual(namespace["blockjob_state"](type("Reply", (), {
            "returncode": 0, "stdout": "", "stderr": "Block Commit: [ 0.00 %]"})()), "active")
        self.assertEqual(namespace["blockjob_state"](type("Reply", (), {
            "returncode": 1, "stdout": "", "stderr": "unexpected error"})()), "unknown")

    def test_marker_overwrite_and_final_data_mapping_are_explicit(self):
        namespace = self.remote_namespace()
        values = {
            "base": {"marker": 0x10, "payload": 0x50, "offset": 16, "length": 4},
            "point-a": {"marker": 0x20, "payload": 0x60, "offset": 32, "length": 80},
            "point-b": {"marker": 0x30, "payload": 0x70, "offset": 8, "length": 4},
            "point-c": {"marker": 0x40, "payload": 0x80, "offset": 112, "length": 4},
            "current": {"marker": 0x50, "payload": 0x90, "offset": 120, "length": 4},
        }
        namespace.update({"targets": ["vda"], "D": {"vda": "D"}, "B": {"vda": "B"},
                          "C": {"vda": "C"}, "result": {"values": values}})
        observed = []

        def read(path, marker, payload, offset, length):
            observed.append((path, marker, payload, offset, length))
            return {"ok": True}

        namespace["read_extent"] = read
        final = namespace["verify_final_data"]()
        self.assertEqual([row[0] for row in observed], ["D", "B", "B", "C", "C"])
        self.assertEqual([row[1] for row in observed], [0x20, 0x30, 0x30, 0x50, 0x50])
        self.assertTrue(namespace["read_rows_ok"](final["vda"]))
        self.assertFalse(namespace["read_rows_ok"]({"baseline": {"ok": False}}))

    def test_chain_shape_requires_list_and_reads_filename_entries(self):
        namespace = self.remote_namespace()
        images = [{"filename": "/fixture/C"}, {"filename": "/fixture/B"},
                  {"filename": "/fixture/D"}]
        self.assertEqual(namespace["chain_filenames"](images),
                         ["/fixture/C", "/fixture/B", "/fixture/D"])
        observation = {"command": ["qemu-img", "info"], "images": images}
        self.assertEqual(namespace["observed_chain_filenames"](observation),
                         ["/fixture/C", "/fixture/B", "/fixture/D"])
        with self.assertRaisesRegex(RuntimeError, "not a list"):
            namespace["chain_filenames"](observation)
        with self.assertRaisesRegex(RuntimeError, "missing its image list"):
            namespace["observed_chain_filenames"]({"images": {"filename": "/fixture/C"}})

    def test_qemu_chain_observer_uses_snapshot_sources_and_marks_only_unreferenced_stale_postpoint(self):
        namespace = self.remote_namespace()
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            paths = {name: work / name for name in
                     ("base", "a", "b", "saved-c", "current")}
            for path in paths.values():
                path.write_bytes(b"fixture")
            missing = work / "abandoned-c-post.qcow2"

            def snapshot_xml(label, saved, post, revert=None):
                revert_xml = (f"<revertdisks><disk name='vda'><source file='{revert}'/>"
                              "</disk></revertdisks>" if revert else "")
                return (f"<domainsnapshot><name>{label}</name><domain><devices>"
                        f"<disk device='disk'><source file='{saved}'/><target dev='vda'/></disk>"
                        f"</devices></domain><disks><disk name='vda'><source file='{post}'/>"
                        f"</disk></disks>{revert_xml}</domainsnapshot>")

            docs = {
                "labels": ["point-a", "point-b", "point-c"],
                "current": "point-b",
                "xml": {
                    "point-a": snapshot_xml("point-a", paths["base"], paths["a"]),
                    "point-b": snapshot_xml("point-b", paths["a"], paths["b"]),
                    "point-c": snapshot_xml("point-c", paths["saved-c"], missing),
                },
            }
            observed_commands = []

            def fake_run(*args, **_kwargs):
                observed_commands.append(args)
                return SimpleNamespace(returncode=0, stdout=__import__("json").dumps(
                    [{"filename": str(args[-1])}]), stderr="")

            namespace.update({"targets": ["vda"], "name": "fixture",
                              "D": {"vda": str(paths["base"])},
                              "snapshot_docs": lambda: docs,
                              "source_paths": lambda *_args: {"vda": str(paths["b"])},
                              "run": fake_run})
            observed = namespace["qemu_chains"]()
            self.assertTrue(all("--force-share" in command for command in observed_commands))
            self.assertEqual(observed["vda"]["snapshots"]["point-c"]["post"], {
                "head": str(missing), "state": "absent-unreferenced-postpoint"})
            classify = namespace["snapshot_postpoint_state"]
            self.assertEqual(classify(observed["vda"]["snapshots"]["point-a"]["post"]),
                             "present")
            self.assertEqual(classify(observed["vda"]["snapshots"]["point-c"]["post"]),
                             "absent-unreferenced-postpoint")
            self.assertEqual(observed["vda"]["current"]["head"], str(paths["b"]))

            docs["xml"]["point-c"] = snapshot_xml(
                "point-c", missing, missing, revert=missing)
            with self.assertRaisesRegex(RuntimeError, "referenced qemu-img chain head is missing"):
                namespace["qemu_chains"]()

    def test_snapshot_postpoint_refuses_incomplete_observations(self):
        classify = self.remote_namespace()["snapshot_postpoint_state"]
        with self.assertRaisesRegex(RuntimeError, "observation is incomplete"):
            classify({"head": "/fixture/post.qcow2", "images": []})

    def test_branch_command_reply_is_checkpointed_before_chain_observation(self):
        namespace = self.remote_namespace()
        with tempfile.TemporaryDirectory() as temporary:
            report = Path(temporary) / "result.json"
            namespace.update({"result": {"steps": []}, "REPORT": report})
            record = namespace["record_branch_commit_reply"](
                "vda", ["blockcommit", "fixture", "vda"],
                SimpleNamespace(returncode=0, stdout="Commit complete", stderr=""))
            self.assertEqual(record["returncode"], 0)
            saved = __import__("json").loads(report.read_text())
            self.assertEqual(saved["steps"], ["branch-blockcommit-vda-command-returned"])
            self.assertEqual(saved["branch_commits"]["vda"]["stdout"], "Commit complete")

    def test_terminal_job_gate_requires_all_native_observations(self):
        namespace = self.remote_namespace()
        empty = {"ok": True, "return": []}
        persisted = {"exists": True, "active": False, "count": 0}
        self.assertTrue(namespace["jobs_are_clear"]("none", True, empty, empty, persisted))
        self.assertFalse(namespace["jobs_are_clear"]("unknown", True, empty, empty, persisted))
        self.assertFalse(namespace["jobs_are_clear"]("none", True,
                                                       {"ok": False, "return": None}, empty, persisted))
        self.assertFalse(namespace["jobs_are_clear"]("none", True, empty, empty,
                                                       {"exists": False, "active": None, "count": None}))

    def test_reopen_verification_collects_qmp_without_shadowing_query_function(self):
        namespace = self.remote_namespace()
        calls = []

        def fake_virsh(*args, **kwargs):
            calls.append(args)
            if args[:1] == ("domuuid",):
                return SimpleNamespace(stdout="fixture-uuid\n")
            if args[:1] == ("dumpxml",):
                return SimpleNamespace(stdout="<domain><name>fixture</name></domain>")
            if args[:1] == ("domstate",):
                return SimpleNamespace(stdout="running\n")
            raise AssertionError(args)

        namespace["virsh"] = fake_virsh
        namespace["qmp"] = lambda query: {"ok": True, "return": []}
        namespace["qemu_chains"] = lambda: {"vda": {"images": []}}
        observed = namespace["verify_reopen"]("fixture", "fixture-uuid", ["/invalid-A"])
        self.assertEqual(observed["state"], "running")
        self.assertEqual(len(observed["qmp"]), 4)
        self.assertEqual(len(calls), 3)
        namespace["virsh"] = lambda *args, **kwargs: SimpleNamespace(
            stdout="fixture-uuid\n" if args[:1] == ("domuuid",) else
            "<domain>/invalid-A</domain>" if args[:1] == ("dumpxml",) else "running\n")
        with self.assertRaisesRegex(RuntimeError, "still refers to invalidated A"):
            namespace["verify_reopen"]("fixture", "fixture-uuid", ["/invalid-A"])

    def test_system_guest_remote_payload_defines_midcommit_helpers_without_interrupt_campaign(self):
        namespace = self.remote_namespace("def verify_candidate():\n    return {}\n")
        self.assertTrue(namespace["active_blockjob"]("Block Commit: [ 25.39 %]"))
        self.assertTrue(namespace["active_blockjob"]("Active Block Commit: [100.00 %]"))
        self.assertFalse(namespace["active_blockjob"]("No current block job for vda"))
        self.assertTrue(namespace["sys"].version_info)

    def test_branch_mode_remote_payload_is_bounded_to_public_metadata_delete(self):
        namespace = self.remote_namespace()
        self.assertTrue(callable(namespace["run_branch_fixture"]))
        self.assertEqual(namespace["CONFIG"]["mode"], "normal")
        branch_config = dict(namespace["CONSOLIDATE_CONFIG"], mode="branch", run_campaign=False)
        branch_payload = CONSOLIDATE.build_remote_payload(
            CONSOLIDATE.native_helper_source(), branch_config,
            "def verify_candidate():\n    return {'version_match': True}\n")
        branch_namespace = {"__name__": "branch_payload_test"}
        exec(compile(branch_payload, "branch_payload_test.py", "exec"), branch_namespace)
        self.assertEqual(branch_namespace["CONFIG"]["mode"], "branch")
        self.assertTrue(callable(branch_namespace["snapshot_postpoint_state"]))
        start = CONSOLIDATE.REMOTE_CAMPAIGN.index("def run_branch_fixture():")
        end = CONSOLIDATE.REMOTE_CAMPAIGN.index("def run_fixture():", start)
        branch_source = CONSOLIDATE.REMOTE_CAMPAIGN[start:end]
        reopen_source = branch_source.split('reopened = {"uuid"', 1)[1].split(
            'if reopened["uuid"]', 1)[0]
        self.assertIn('"chains": qemu_chains()', reopen_source)
        self.assertNotIn('"chains": current_chains()', reopen_source)
        self.assertIn('"snapshot-delete", name, "point-a", "--metadata"', branch_source)
        self.assertNotIn('"snapshot-delete", name, check=False', branch_source)
        self.assertIn('"rebase", "-u"', branch_source)
        self.assertIn("branch_before_commit", branch_source)
        self.assertIn("snapshot_delete_flags", branch_source)

    def test_rebase_targets_must_be_unique_files_in_the_exact_fixture(self):
        namespace = self.remote_namespace()
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            namespace["work"] = work
            inside = work / "image.qcow2"
            inside.write_bytes(b"fixture")
            self.assertEqual(namespace["require_fixture_file"](str(inside)), inside)
            outside = work.parent / f"{work.name}-outside.qcow2"
            outside.write_bytes(b"external")
            link = work / "link.qcow2"
            link.symlink_to(outside)
            hardlink = work / "hardlink.qcow2"
            os.link(inside, hardlink)
            try:
                with self.assertRaisesRegex(RuntimeError, "exact fixture directory"):
                    namespace["require_fixture_file"](str(outside))
                with self.assertRaisesRegex(RuntimeError, "exact fixture directory"):
                    namespace["require_fixture_file"](str(link))
                with self.assertRaisesRegex(RuntimeError, "exact fixture directory"):
                    namespace["require_fixture_file"](str(inside))
            finally:
                outside.unlink()

    def test_global_reference_parser_includes_media_unknown_devices_backing_and_revert_sources(self):
        namespace = self.remote_namespace()
        xml = """<domain><devices>
          <disk device='disk'><source file='/fixture/head.qcow2'/><backingStore><source file='/fixture/base.qcow2'/></backingStore><target dev='vda'/></disk>
          <disk device='cdrom'><source file='/fixture/install.iso'/><target dev='sda'/></disk>
          <disk device='future-writable'><source file='/fixture/other.qcow2'/><target dev='vdz'/></disk>
        </devices><snapshots><snapshot><revertdisks>
          <disk name='vdb'><source file='/fixture/revert.qcow2'/></disk>
        </revertdisks></snapshot></snapshots></domain>"""
        sources = namespace["disk_file_sources"](xml)
        self.assertIn(("vda", "/fixture/head.qcow2"), sources)
        self.assertIn(("vda", "/fixture/base.qcow2"), sources)
        self.assertIn(("vdb", "/fixture/revert.qcow2"), sources)
        self.assertIn(("sda", "/fixture/install.iso"), sources)
        self.assertIn(("vdz", "/fixture/other.qcow2"), sources)
        self.assertEqual(namespace["require_stopped_foreign_domain"]("other", "shut off\n"),
                         "shut off")
        for state in ("running", "paused", "unknown"):
            with self.assertRaisesRegex(RuntimeError, "refusing A-top unlink"):
                namespace["require_stopped_foreign_domain"]("other", state)


if __name__ == "__main__":
    unittest.main()
