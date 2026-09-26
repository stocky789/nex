import importlib.util
import hashlib
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest import mock


SCRIPT = Path(__file__).parent / "integration" / "recovery_s0_backup.py"
SPEC = importlib.util.spec_from_file_location("recovery_s0_backup", SCRIPT)
backup = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(backup)


class CompletedBackupResultTests(unittest.TestCase):
    def test_local_candidate_pin_matches_selected_debs_to_full_manifest(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            package_dir = root / "packages"
            package_dir.mkdir()
            rows = []
            full_entries = []
            metadata = {}
            for name in backup.NEX3_LIBVIRT_PACKAGES:
                architecture = "all" if name in backup.NEX3_ARCHITECTURE_INDEPENDENT else "amd64"
                filename = f"{name}_{backup.NEX3_PACKAGE_VERSION}_{architecture}.deb"
                payload = f"package:{name}".encode()
                (package_dir / filename).write_bytes(payload)
                digest = hashlib.sha256(payload).hexdigest()
                rows.append(f"{digest}  {filename}")
                full_entries.append(f"{digest}  {filename}")
                metadata[filename] = f"Package: {name}\nVersion: {backup.NEX3_PACKAGE_VERSION}\nArchitecture: {architecture}\n"
            selected = "\n".join(rows) + "\n"
            full = "\n".join(full_entries) + "\n"
            (package_dir / "SHA256SUMS.selected").write_text(selected)
            source_manifest = root / "SHA256SUMS"
            source_manifest.write_text(full)
            with mock.patch.object(backup, "run", side_effect=lambda args: SimpleNamespace(stdout=metadata[Path(args[2]).name])):
                pin = backup.local_candidate_package_pin(
                    package_dir, source_manifest, hashlib.sha256(full.encode()).hexdigest(),
                    backup.NEX3_PACKAGE_VERSION, "/var/tmp/staged-candidate")
            self.assertEqual(len(pin["packages"]), len(backup.NEX3_LIBVIRT_PACKAGES))
            self.assertEqual(pin["manifest_sha256"], hashlib.sha256(full.encode()).hexdigest())
            self.assertEqual({row["architecture"] for row in pin["packages"]}, {"amd64", "all"})
            config_package = next(row for row in pin["packages"] if row["package"] == "libvirt-daemon-config-network")
            metadata[config_package["file"]] = (
                "Package: libvirt-daemon-config-network\n"
                f"Version: {backup.NEX3_PACKAGE_VERSION}\nArchitecture: arm64\n")
            with mock.patch.object(backup, "run", side_effect=lambda args: SimpleNamespace(stdout=metadata[Path(args[2]).name])):
                with self.assertRaisesRegex(RuntimeError, "metadata differs"):
                    backup.local_candidate_package_pin(
                        package_dir, source_manifest, hashlib.sha256(full.encode()).hexdigest(),
                        backup.NEX3_PACKAGE_VERSION, "/var/tmp/staged-candidate")

    def test_candidate_pin_rejects_wrong_source_manifest_or_staged_package_hash(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            package_dir = root / "packages"
            package_dir.mkdir()
            name = backup.NEX3_LIBVIRT_PACKAGES[0]
            filename = f"{name}_{backup.NEX3_PACKAGE_VERSION}_amd64.deb"
            payload = b"selected package bytes"
            (package_dir / filename).write_bytes(payload)
            digest = hashlib.sha256(payload).hexdigest()
            source_manifest = root / "SHA256SUMS"
            source_manifest.write_text(f"{digest}  {filename}\n")
            (package_dir / "SHA256SUMS.selected").write_text(f"{digest}  {filename}\n")
            with self.assertRaisesRegex(RuntimeError, "full candidate manifest digest differs"):
                backup.local_candidate_package_pin(package_dir, source_manifest, "0" * 64,
                                                   backup.NEX3_PACKAGE_VERSION, "/var/tmp/staged")
            (package_dir / filename).write_bytes(b"replaced bytes")
            with mock.patch.object(backup, "run") as package_info:
                with self.assertRaisesRegex(RuntimeError, "selected package differs"):
                    backup.local_candidate_package_pin(
                        package_dir, source_manifest, hashlib.sha256(source_manifest.read_bytes()).hexdigest(),
                        backup.NEX3_PACKAGE_VERSION, "/var/tmp/staged")
                package_info.assert_not_called()

    def test_installed_version_and_runtime_file_checks_reject_mismatches(self):
        expected = {name: backup.NEX3_PACKAGE_VERSION for name in backup.NEX3_LIBVIRT_PACKAGES}
        self.assertEqual(backup.require_installed_versions(
            expected, backup.NEX3_LIBVIRT_PACKAGES, backup.NEX3_PACKAGE_VERSION), expected)
        with self.assertRaisesRegex(RuntimeError, "versions differ"):
            backup.require_installed_versions({**expected, "libvirt0": "wrong"},
                                              backup.NEX3_LIBVIRT_PACKAGES, backup.NEX3_PACKAGE_VERSION)
        with tempfile.TemporaryDirectory() as temporary:
            runtime_file = Path(temporary) / "libvirt-runtime"
            runtime_file.write_bytes(b"candidate runtime")
            digest = hashlib.md5(runtime_file.read_bytes()).hexdigest()
            self.assertEqual(backup.require_file_digest(runtime_file, digest, "md5"), digest)
            with self.assertRaisesRegex(RuntimeError, "differs from selected package"):
                backup.require_file_digest(runtime_file, "0" * 32, "md5")

    def test_probe_names_must_not_replace_any_existing_domain(self):
        backup.require_unclaimed_domains(("foreign-vm",), ("probe", "restore", "convert"))
        with self.assertRaisesRegex(RuntimeError, "already in use"):
            backup.require_unclaimed_domains(("foreign-vm", "probe"), ("probe", "restore"))

    def test_checkpoint_restore_mode_skips_capacity_and_interruption_probes(self):
        self.assertFalse(backup.run_backup_failure_probes({"restore_from_checkpoint_backup": True}))
        self.assertTrue(backup.run_backup_failure_probes({"restore_from_checkpoint_backup": False}))
        self.assertTrue(backup.run_backup_failure_probes({}))

    def test_nfs_mount_requires_exact_target_source_rw_nfs42(self):
        observed = json.dumps({"filesystems": [{"target": "/mnt/recovery-s0-nfs-coreb",
            "source": "192.0.2.20:/srv/recovery/nfs", "fstype": "nfs4",
            "options": "rw,vers=4.2,hard,proto=tcp"}]})
        mount = backup.require_nfs_mount(observed, "/mnt/recovery-s0-nfs-coreb",
                                         "192.0.2.20:/srv/recovery/nfs")
        self.assertEqual(mount["fstype"], "nfs4")
        for target, source, fstype, options in (
            ("/mnt/recovery-s0-nfs-corec", "192.0.2.20:/srv/recovery/nfs", "nfs4", "rw,vers=4.2"),
            ("/mnt/recovery-s0-nfs-coreb", "192.0.2.21:/srv/recovery/nfs", "nfs4", "rw,vers=4.2"),
            ("/mnt/recovery-s0-nfs-coreb", "192.0.2.20:/srv/recovery/nfs", "nfs", "rw,vers=4.2"),
            ("/mnt/recovery-s0-nfs-coreb", "192.0.2.20:/srv/recovery/nfs", "nfs4", "ro,vers=4.2"),
        ):
            payload = json.dumps({"filesystems": [{"target": target, "source": source,
                "fstype": fstype, "options": options}]})
            with self.subTest(target=target, source=source, fstype=fstype, options=options), \
                    self.assertRaisesRegex(RuntimeError, "exact writable"):
                backup.require_nfs_mount(payload, "/mnt/recovery-s0-nfs-coreb",
                                         "192.0.2.20:/srv/recovery/nfs")

    def test_nfs_destination_is_confined_to_unique_child_of_dedicated_mount(self):
        destination, target = backup.require_nfs_destination(
            "/mnt/recovery-s0-nfs-coreb/backup-run", "/mnt/recovery-s0-nfs-coreb")
        self.assertEqual(str(destination), "/mnt/recovery-s0-nfs-coreb/backup-run")
        self.assertEqual(str(target), "/mnt/recovery-s0-nfs-coreb")
        for destination, target in (
            ("/mnt/recovery-s0-nfs-coreb/nested/backup", "/mnt/recovery-s0-nfs-coreb"),
            ("/mnt/recovery-s0-nfs-other/backup", "/mnt/recovery-s0-nfs-coreb"),
            ("/tmp/backup", "/tmp/recovery-s0-nfs-coreb"),
        ):
            with self.subTest(destination=destination), self.assertRaisesRegex(ValueError, "unique child"):
                backup.require_nfs_destination(destination, target)

    def test_checkpoint_restore_selects_checkpoint_outputs_and_post_backup_markers(self):
        output = Path("/assigned/backup")
        paths = backup.restore_backup_paths(output, ("vda", "vdb"), True)
        self.assertEqual(paths, {
            "vda": output / "checkpoint-present-vda-private.qcow2",
            "vdb": output / "checkpoint-present-vdb-private.qcow2",
        })
        self.assertEqual(backup.restore_marker(True), backup.MARKERS["after-backup-begin"])

    def test_ordinary_restore_keeps_the_uncheckpointed_backup_and_marker(self):
        output = Path("/assigned/backup")
        paths = backup.restore_backup_paths(output, ("vda", "vdb"), False)
        self.assertEqual(paths["vda"], output / "vda-private.qcow2")
        self.assertEqual(backup.restore_marker(False), backup.MARKERS["before-backup"])

    def test_checkpoint_restore_requires_the_exact_installed_libvirt_version(self):
        packages = "nex-core-local=0.3.6~recovery.s0\nlibvirt-daemon-system=11.3.0-3+deb13u3+nex3\n"
        self.assertEqual(backup.require_candidate_libvirt_version(
            packages, "11.3.0-3+deb13u3+nex3"), "11.3.0-3+deb13u3+nex3")
        with self.assertRaisesRegex(RuntimeError, "version differs"):
            backup.require_candidate_libvirt_version(packages, "11.3.0-3+deb13u3+nex2")

    def test_checkpoint_restore_refuses_missing_or_dropped_checkpoint_metadata(self):
        self.assertEqual(backup.require_checkpoint_listed("Name\ncheckpoint-123\n", "checkpoint-123"),
                         "Name\ncheckpoint-123")
        for lookalike in ("checkpoint-123-other", "prefix-checkpoint-123"):
            with self.subTest(lookalike=lookalike), self.assertRaisesRegex(RuntimeError, "expected checkpoint is absent"):
                backup.require_checkpoint_listed(f"Name\n{lookalike}\n", "checkpoint-123")

    def test_owner_only_output_check_requires_qemu_owner_and_mode_0600(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "backup.qcow2"
            path.write_bytes(b"complete output")
            path.chmod(0o600)
            metadata = backup.owner_only_metadata(path, path.stat().st_uid)
            self.assertEqual(metadata["mode"], "0o600")
            self.assertEqual(metadata["size"], len(b"complete output"))
            with self.assertRaisesRegex(RuntimeError, "not QEMU-owned"):
                backup.owner_only_metadata(path, path.stat().st_uid + 1)

    def test_job_disappearance_without_completed_statistics_is_not_success(self):
        with self.assertRaisesRegex(RuntimeError, "no completed-job statistics"):
            backup.completed_backup_result("Job type: 0\n", "Job type: 0\n")

    def test_missing_job_query_is_not_success(self):
        with self.assertRaisesRegex(RuntimeError, "unrecognized domjobinfo"):
            backup.completed_backup_result("", "")

    def test_completed_job_requires_native_success_and_all_bytes_processed(self):
        current = "Job type: 0\n"
        for stats in (
            "Job type: 3\nsuccess: 0\ndisk_processed: 10\ndisk_remaining: 0\n",
            "Job type: 3\nsuccess: 1\ndisk_processed: 0\ndisk_remaining: 10\n",
            "Job type: 3\nsuccess: 1\ndisk_processed: 10\ndisk_remaining: 1\n",
        ):
            with self.subTest(stats=stats), self.assertRaises(RuntimeError):
                backup.completed_backup_result(current, stats)

    def test_cancelled_job_is_not_success_even_when_image_files_are_valid(self):
        current = "Job type: 0\n"
        cancelled = (
            "Job type: 5\noperation: 9\ndisk_total: 100\ndisk_processed: 70\n"
            "disk_remaining: 30\nsuccess: 0\nerrmsg: Operation canceled\n"
        )
        with self.assertRaisesRegex(RuntimeError, "no completed-job statistics"):
            backup.completed_backup_result(current, cancelled)

    def test_failed_job_is_not_success_when_processed_equals_total(self):
        current = "Job type: 0\n"
        failed = (
            "Job type: 4\noperation: 9\ndisk_total: 100\ndisk_processed: 100\n"
            "disk_remaining: 0\nsuccess: 0\n"
        )
        with self.assertRaisesRegex(RuntimeError, "no completed-job statistics"):
            backup.completed_backup_result(current, failed)

    def test_completed_successful_full_backup_is_accepted(self):
        current = "Job type: 0\n"
        completed = (
            "Job type: 3\noperation: 9\ntime_elapsed: 100\n"
            "disk_total: 10\ndisk_processed: 10\ndisk_remaining: 0\nsuccess: 1\n"
        )
        result = backup.completed_backup_result(current, completed)
        self.assertEqual(result["current"], current.strip())
        self.assertEqual(result["completed"], completed.strip())

    def test_enospc_root_accepts_only_direct_children_of_assigned_capacity_mounts(self):
        path = backup.assigned_capacity_root("/mnt/recovery-s0-capacity-sixpatch/backup-enospc")
        self.assertEqual(str(path), "/mnt/recovery-s0-capacity-sixpatch/backup-enospc")
        for invalid in (
            "/mnt/recovery-s0-capacity-sixpatch/../backup-enospc",
            "/mnt/recovery-s0-capacity-sixpatch/backup-enospc/extra",
            "/mnt/recovery-s0-nfs/backup-enospc",
        ):
            with self.subTest(path=invalid), self.assertRaisesRegex(ValueError, "direct child"):
                backup.assigned_capacity_root(invalid)

    def test_enospc_source_state_allows_observed_paused_and_running_outcomes(self):
        self.assertEqual(backup.enospc_recovery_action("paused (I/O error)", "vda: no space"), "resume")
        self.assertEqual(backup.enospc_recovery_action("running (booted)", "No errors found"), "continue-running")
        for state, error in (("paused (I/O error)", "No errors found"),
                             ("running (booted)", "vda: no space"),
                             ("shut off", "No errors found")):
            with self.subTest(state=state, error=error), self.assertRaisesRegex(RuntimeError, "unrecognized source state"):
                backup.enospc_recovery_action(state, error)

    def test_normalized_domstate_output_identifies_running_source(self):
        self.assertTrue(backup.domain_is_running("running"))
        self.assertFalse(backup.domain_is_running("paused (I/O error)"))
        self.assertFalse(backup.domain_is_running("shut off (shutdown)"))

    def test_successful_enospc_only_finalizer_cleans_scratch_and_preserves_failure_outputs(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            work, held, converted, output = (root / name for name in ("work", "held", "converted", "output"))
            for path in (work, held, converted, output):
                path.mkdir()
                (path / "evidence").write_text("retained")
            observed_domains = []
            stopped_networks = []

            def state(domain):
                observed_domains.append(domain)
                return "absent"

            def virsh(*args, **kwargs):
                stopped_networks.append((args, kwargs))

            backup.finalize_successful_probe(("converted", "restored", "source"), state, virsh,
                                             True, work, held, converted, output, True)

            self.assertEqual(observed_domains, ["converted", "restored", "source"])
            self.assertEqual(len(stopped_networks), 1)
            self.assertFalse(work.exists())
            self.assertFalse(held.exists())
            self.assertFalse(converted.exists())
            self.assertTrue((output / "evidence").is_file())



if __name__ == "__main__":
    unittest.main()
