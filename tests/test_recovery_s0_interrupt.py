import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from types import SimpleNamespace


SPEC = importlib.util.spec_from_file_location(
    "recovery_s0_interrupt", Path(__file__).parent / "integration" / "recovery_s0_interrupt.py")
INTERRUPT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(INTERRUPT)


class RecoveryS0InterruptPayloadTests(unittest.TestCase):
    def test_remote_campaign_exit_status_uses_its_own_sys_import(self):
        config = {"packages": list(INTERRUPT.PACKAGE_SET),
                  "package_version": INTERRUPT.PACKAGE_VERSION,
                  "package_manifest_sha256": INTERRUPT.PACKAGE_MANIFEST_SHA256,
                  "sentinel": INTERRUPT.SENTINEL}
        source = INTERRUPT.build_remote_payload(INTERRUPT.native_helper_source(), config)
        namespace = {"__name__": "recovery_s0_interrupt_exit_test"}
        exec(compile(source, "recovery_s0_interrupt_remote.py", "exec"), namespace)
        namespace["interrupt_campaign"] = lambda _: {"status": "passed"}
        exec("result = interrupt_campaign(INTERRUPT_CONFIG)\n"
             "print(json.dumps(result, indent=2))\n"
             "if result.get('status') != 'passed': sys.exit(1)\n", namespace)
        namespace["interrupt_campaign"] = lambda _: {"status": "failed"}
        with self.assertRaises(SystemExit) as raised:
            exec("result = interrupt_campaign(INTERRUPT_CONFIG)\n"
                 "print(json.dumps(result, indent=2))\n"
                 "if result.get('status') != 'passed': sys.exit(1)\n", namespace)
        self.assertEqual(raised.exception.code, 1)

    def test_reconnect_recovery_requires_persisted_delete_marker_and_terminal_jobs(self):
        config = {"packages": list(INTERRUPT.PACKAGE_SET),
                  "package_version": INTERRUPT.PACKAGE_VERSION,
                  "package_manifest_sha256": INTERRUPT.PACKAGE_MANIFEST_SHA256,
                  "sentinel": INTERRUPT.SENTINEL}
        source = INTERRUPT.build_remote_payload(INTERRUPT.native_helper_source(), config)
        namespace = {"__name__": "recovery_s0_interrupt_recovery_test"}
        exec(compile(source, "recovery_s0_interrupt_remote.py", "exec"), namespace)
        status = ("<domain><privateData><job><snapshotDelete/></job></privateData>"
                  "<blockjobs active='no'/></domain>")
        self.assertTrue(namespace["snapshot_delete_marker"](status))
        self.assertFalse(namespace["snapshot_delete_marker"](
            "<domain><privateData><job/></privateData></domain>"))
        terminal = {"terminal": True, "domain_state": "paused"}
        self.assertTrue(namespace["restart_recovery_ready"](True, terminal))
        self.assertFalse(namespace["restart_recovery_ready"](False, terminal))
        self.assertFalse(namespace["restart_recovery_ready"](
            True, {"terminal": False, "domain_state": "paused"}))
        self.assertFalse(namespace["restart_recovery_ready"](
            True, {"terminal": True, "domain_state": "running"}))
        parents = {"point-a": None, "point-b": "point-a", "point-c": "point-b"}
        labels = ("point-a", "point-b", "point-c")
        flags = {label: [] for label in labels}
        self.assertTrue(namespace["oldest_delete_retry_ready"](
            True, True, labels, "point-c", parents, flags, True, []))
        self.assertFalse(namespace["oldest_delete_retry_ready"](
            False, True, labels, "point-c", parents, flags, True, []))
        self.assertFalse(namespace["oldest_delete_retry_ready"](
            True, False, labels, "point-c", parents, flags, True, []))
        self.assertFalse(namespace["oldest_delete_retry_ready"](
            True, True, labels, "point-c", parents, {**flags, "point-a": [True]}, True, []))
        self.assertFalse(namespace["oldest_delete_retry_ready"](
            True, True, labels, "point-c", parents, flags, True, ["point-b/vda failed"]))
        self.assertFalse(namespace["oldest_delete_retry_ready"](
            True, True, labels, "point-b", parents, flags, True, []))
        failed_marker_read = {"ok": False,
                              "output": "Pattern verification failed at offset 4096, 4096 bytes\n"
                                        "read 4096/4096 bytes at offset 4096",
                              "error": ""}
        self.assertTrue(namespace["disk_read_completed"](failed_marker_read, 4096, 4096))
        self.assertFalse(namespace["disk_read_completed"](
            {"ok": False, "output": "", "error": "qemu-io failed"}, 4096, 4096))
        self.assertFalse(namespace["oldest_delete_retry_ready"](
            True, True, labels, "point-c", parents, flags, False, []))

    def test_assembled_remote_payload_verifies_exact_staged_package_manifest(self):
        with tempfile.TemporaryDirectory() as temporary:
            staged = Path(temporary)
            config = {
                "root": str(staged),
                "domain": "nex-recovery-s0-interrupt-test",
                "boundary_timeout_seconds": 15,
                "packages": list(INTERRUPT.PACKAGE_SET),
                "package_version": INTERRUPT.PACKAGE_VERSION,
                "package_manifest_sha256": INTERRUPT.PACKAGE_MANIFEST_SHA256,
                "sentinel": INTERRUPT.SENTINEL,
                "staged_package_dir": str(staged),
            }
            rows = {}
            for index, package in enumerate(INTERRUPT.PACKAGE_SET):
                arch = "all" if package.startswith("libvirt-daemon-config-") else "amd64"
                filename = f"{package}_{INTERRUPT.PACKAGE_VERSION}_{arch}.deb"
                content = f"candidate package {index}".encode()
                (staged / filename).write_bytes(content)
                rows[filename] = {"package": package, "version": INTERRUPT.PACKAGE_VERSION,
                                  "architecture": arch, "sha256": hashlib.sha256(content).hexdigest()}
            (staged / "selected-packages.json").write_text(json.dumps({
                "artifact_manifest_sha256": INTERRUPT.PACKAGE_MANIFEST_SHA256,
                "packages": rows,
            }))

            source = INTERRUPT.build_remote_payload(INTERRUPT.native_helper_source(), config)
            namespace = {"__name__": "recovery_s0_interrupt_remote_test"}
            exec(compile(source, "recovery_s0_interrupt_remote.py", "exec"), namespace)

            def fake_run(*args, check=True, **kwargs):
                if args[0] == "dpkg-query":
                    output = "\n".join(
                        f"{package}{':amd64' if package == 'libvirt0' else ''} {INTERRUPT.PACKAGE_VERSION}"
                        for package in INTERRUPT.PACKAGE_SET)
                    return SimpleNamespace(returncode=0, stdout=output, stderr="")
                if args[0] == "dpkg-deb":
                    row = rows[Path(args[2]).name]
                    output = (f"Package: {row['package']}\nVersion: {row['version']}\n"
                              f"Architecture: {row['architecture']}\n")
                    return SimpleNamespace(returncode=0, stdout=output, stderr="")
                if args[:3] == ("virsh", "-c", "qemu:///system"):
                    return SimpleNamespace(returncode=0, stdout="libvirt 11.3.0\n", stderr="")
                self.fail(f"unexpected mocked remote command: {args}")

            namespace["run"] = fake_run
            result = namespace["verify_candidate"]()
            self.assertEqual(result["staged_package_hashes_verified"], 15)
            self.assertEqual(result["artifact_manifest_sha256"], INTERRUPT.PACKAGE_MANIFEST_SHA256)
            self.assertTrue(namespace["active_blockjob"]("Block Commit: [ 0.00 %]"))
            self.assertFalse(namespace["active_blockjob"]("No current block job for vda"))
            self.assertEqual(namespace["blockjob_state"](SimpleNamespace(
                returncode=0, stdout="Block Commit: [ 1.00 %]", stderr="")), "active")
            self.assertEqual(namespace["blockjob_state"](SimpleNamespace(
                returncode=0, stdout="", stderr="Block Commit: [ 0.00 %]")), "active")
            self.assertEqual(namespace["blockjob_state"](SimpleNamespace(
                returncode=1, stdout="", stderr="No current block job for vda")), "inactive")
            self.assertEqual(namespace["blockjob_state"](SimpleNamespace(
                returncode=1, stdout="", stderr="unexpected libvirt error")), "unknown")

            def fake_virsh(*args, check=True):
                if args[:1] == ("domstate",):
                    return SimpleNamespace(returncode=0, stdout="shut off\n", stderr="")
                if args[:2] == ("snapshot-list", "test-domain"):
                    return SimpleNamespace(returncode=0, stdout="point-a\npoint-b\npoint-c\n", stderr="")
                if args[:2] == ("snapshot-current", "test-domain"):
                    return SimpleNamespace(returncode=0, stdout="point-c\n", stderr="")
                if args[:1] == ("blockjob",) and args[2] == "vda":
                    return SimpleNamespace(returncode=1, stdout="", stderr="unexpected query failure")
                if args[:1] == ("blockjob",):
                    return SimpleNamespace(returncode=1, stdout="", stderr="No current block job for vdb")
                if args[:1] == ("snapshot-dumpxml",):
                    return SimpleNamespace(returncode=0, stdout=(
                        "<domainsnapshot><name>point-a</name><disks><disk name='vda'>"
                        "<snapshotDeleteInProgress>false</snapshotDeleteInProgress>"
                        "</disk></disks></domainsnapshot>"), stderr="")
                self.fail(f"unexpected mocked virsh command: {args}")

            namespace["virsh"] = fake_virsh
            namespace["source_paths"] = lambda *_: self.fail("unknown job state must skip image reads")
            observed = namespace["inspect_domain"](
                "test-domain", ["vda", "vdb"], ("point-a", "point-b", "point-c"), {}, {})
            self.assertIn("blockjob observation failed for vda", observed["integrity_errors"])
            self.assertTrue(any("image reads were skipped" in error for error in observed["integrity_errors"]))
            self.assertEqual(observed["snapshots"]["point-a"]["delete_flags"], {"vda": False})

            extents = [namespace["point_values"](index, 0) for index in range(3)]
            extents.append(namespace["current_values"](["vda"])["vda"])
            ranges = sorted((extent["offset"], extent["offset"] + extent["length"])
                            for extent in extents)
            self.assertEqual(extents[1]["length"], 80 << 20)
            self.assertTrue(all(end <= 128 << 20 for _, end in ranges))
            self.assertTrue(all(left[1] <= right[0] for left, right in zip(ranges, ranges[1:])))

            first_package = next(iter(rows))
            (staged / first_package).write_text("changed")
            with self.assertRaisesRegex(RuntimeError, "staged package hash differs"):
                namespace["verify_candidate"]()


if __name__ == "__main__":
    unittest.main()
