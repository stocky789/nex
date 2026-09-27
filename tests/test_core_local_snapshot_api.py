import importlib.util
import ast
import hashlib
import io
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import time
import unittest
from contextlib import redirect_stdout
from unittest.mock import patch


MODULE_PATH = Path(__file__).parent / "integration" / "core_local_snapshot_api.py"
SPEC = importlib.util.spec_from_file_location("core_local_snapshot_api", MODULE_PATH)
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)


class SnapshotAPIRunnerTests(unittest.TestCase):
    def test_postnative_wrapper_gates_only_the_operation_named_native_create(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            gate = root / "private gate"
            invocations = root / "virsh-invocations"
            fake_virsh = root / "fake virsh"
            fake_virsh.write_text("#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shlex.quote(str(invocations)) + "\n")
            fake_virsh.chmod(0o700)
            operation_id = "recovery-s0-crash-0123456789abcdef"
            display_name = "Post-native crash proof"
            native_name = "nex-" + operation_id
            wrapper = root / "virsh-wrapper"
            wrapper.write_text(runner.postnative_snapshot_wrapper(operation_id, gate, str(fake_virsh)))
            wrapper.chmod(0o700)

            planned = ["--connect", "qemu:///system", "snapshot-create-as", "vm-uuid", native_name,
                       "--description", display_name, "--disk-only", "--atomic",
                       "--diskspec", "vda,snapshot=external,file=/var/lib/nex/vda.qcow2"]
            wrong_target = planned.copy()
            wrong_target[wrong_target.index(native_name)] = display_name
            subprocess.run([str(wrapper), *wrong_target], check=True, timeout=3)
            wrong_target_with_later_match = wrong_target.copy()
            wrong_target_with_later_match[wrong_target_with_later_match.index("--description") + 1] = native_name
            subprocess.run([str(wrapper), *wrong_target_with_later_match], check=True, timeout=3)
            metadata = ["--connect", "qemu:///system", "snapshot-info", "vm-uuid", native_name]
            subprocess.run([str(wrapper), *metadata], check=True, timeout=3)
            self.assertFalse(gate.exists())
            self.assertEqual(invocations.read_text().splitlines(), [" ".join(wrong_target),
                                                                     " ".join(wrong_target_with_later_match),
                                                                     " ".join(metadata)])

            first = subprocess.Popen([str(wrapper), *planned], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                deadline = time.monotonic() + 3
                while time.monotonic() < deadline and not (gate / "native-complete").exists():
                    time.sleep(0.01)
                self.assertTrue((gate / "native-complete").is_file(), "wrapper did not gate after successful virsh")
                self.assertEqual((gate / "virsh-exit").read_text().strip(), "0")
                (gate / "release").touch()
                self.assertEqual(first.wait(timeout=3), 0)

                duplicate = subprocess.run([str(wrapper), *planned], capture_output=True, text=True, timeout=3)
                self.assertEqual(duplicate.returncode, 98)
                self.assertIn("refused duplicate", (gate / "duplicate-refused").read_text())
                self.assertEqual(invocations.read_text().splitlines().count(" ".join(planned)), 1)
                self.assertEqual((gate / "count").read_text().strip(), "2")
            finally:
                if first.poll() is None:
                    (gate / "release").touch()
                    first.kill()
                    first.wait(timeout=3)

    def test_postnative_wrapper_embeds_derived_name_and_rejects_invalid_operation_id(self):
        operation_id = "recovery-s0-crash-0123456789abcdef"
        wrapper = runner.postnative_snapshot_wrapper(operation_id, "/run/nex-snapshot-gate")
        self.assertIn("expected=nex-" + operation_id, wrapper)
        self.assertNotIn('expected=$(cat "$gate/target-name")', wrapper)
        with self.assertRaisesRegex(ValueError, "invalid snapshot operation ID"):
            runner.postnative_snapshot_wrapper("bad/operation", "/run/nex-snapshot-gate")

    def test_remote_payload_compiles_without_running(self):
        compile(runner.REMOTE, "remote-snapshot-api-payload", "exec")
        compile(runner.CANDIDATE_CHECK, "candidate-check-payload", "exec")

    def test_native_vm_lookup_uses_manifest_name_and_still_checks_uuid(self):
        self.assertIn('virsh("domuuid", config["vm_name"]).lower() != config["vm_id"].lower()', runner.REMOTE)
        self.assertIn('"domuuid",vm_name', runner.CANDIDATE_CHECK)
        self.assertIn('.stdout.strip().lower() != vm.lower()', runner.CANDIDATE_CHECK)
        self.assertIn('"vm_name": vm["name"]', Path(runner.__file__).read_text())

    def test_ssh_uses_recovery_lab_validator_for_selected_recovery_prefix(self):
        cache = runner.ROOT / ".cache"
        cache.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="recovery-s0-core.test.", dir=cache) as temporary:
            lab = Path(temporary)
            (lab / "lab.json").write_text(json.dumps({"disposable": True, "fixture": "cluster-ha-v1"}))
            (lab / "id_ed25519").write_text("fixture key")
            (lab / "known_hosts").write_text("fixture host")
            (lab / "ports.json").write_text(json.dumps({"core1": 10022}))
            (lab / "id_ed25519").chmod(0o600)
            self.assertEqual(runner.ssh_run_lab(lab), lab.resolve())
            args = runner.ssh_run_args(lab, "core1")
            self.assertIn("StrictHostKeyChecking=yes", args)
            self.assertIn("UserKnownHostsFile=" + str(lab.resolve() / "known_hosts"), args)

    def test_controller_identity_requires_exact_private_state_and_tls_files(self):
        with tempfile.TemporaryDirectory() as temporary:
            prep = Path(temporary)
            (prep / "nex-controller").touch()
            (prep / "controller-state").mkdir()
            for name in ("tls.crt", "tls.key", "password", "nex-source.tar.gz"):
                (prep / name).touch()
            cwd = prep
            executable = str((prep / "nex-controller").resolve())
            argv = [
                executable, "-data-dir", "controller-state", "-tls-cert", "tls.crt",
                "-tls-key", "tls.key", "-admin-password-file", "password",
                "-source-archive", "nex-source.tar.gz", "-listen", "127.0.0.1:8443",
            ]
            self.assertTrue(runner.controller_owned(prep, argv, cwd, executable, "127.0.0.1:8443"))
            wrong_state = argv.copy()
            wrong_state[wrong_state.index("controller-state")] = "/tmp/other-controller-state"
            self.assertFalse(runner.controller_owned(prep, wrong_state, cwd, executable, "127.0.0.1:8443"))
            self.assertFalse(runner.controller_owned(prep, argv, cwd, "/usr/bin/nex-controller", "127.0.0.1:8443"))
            self.assertFalse(runner.controller_owned(prep, argv, cwd, executable, "127.0.0.1:9443"))

    def test_adoption_requires_fresh_master_report_with_exact_snapshot_identity(self):
        host = {
                "id": "host-1", "pool_id": "pool-1", "connection": "fresh",
                "inventory": {"managed": {"pool_vm_snapshots_reported": True, "pool_vm_snapshots": [
                {"id": "snapshot-1", "vm_id": "vm-1", "name": "accepted", "description": "desc"},
            ]}},
        }
        self.assertIsNotNone(runner.adopted_catalog(host, "host-1", "pool-1", "snapshot-1", "vm-1", "accepted", "desc"))
        stale = dict(host, connection="stale")
        self.assertIsNone(runner.adopted_catalog(stale, "host-1", "pool-1", "snapshot-1", "vm-1", "accepted", "desc"))
        self.assertIsNone(runner.adopted_catalog(host, "host-1", "pool-1", "snapshot-1", "vm-2", "accepted", "desc"))
        self.assertIsNone(runner.adopted_catalog(host, "host-1", "pool-1", "snapshot-1", "vm-1", "accepted", "other"))
        unreported = {"id": "host-1", "pool_id": "pool-1", "connection": "fresh",
                      "inventory": {"managed": {"pool_vm_snapshots": host["inventory"]["managed"]["pool_vm_snapshots"]}}}
        self.assertIsNone(runner.adopted_catalog(unreported, "host-1", "pool-1", "snapshot-1", "vm-1", "accepted", "desc"))

    def test_preflight_vm_identity_uses_fresh_managed_vms_check_and_reported_inventory(self):
        host = {
            "connection": "fresh",
            "inventory": {
                "checks": [{"name": "managed-vms", "status": "healthy", "freshness": "fresh"}],
                "vms": [{"id": "vm-1", "name": "prepared", "state": "shut off"}],
                "managed": {"authority": {"generation": 1}},
            },
        }
        self.assertTrue(runner.host_reports_managed_vm(host, "vm-1"))
        self.assertFalse(runner.host_reports_managed_vm(host, "vm-2"))
        stale = {**host, "connection": "stale"}
        self.assertFalse(runner.host_reports_managed_vm(stale, "vm-1"))
        unhealthy = {**host, "inventory": {**host["inventory"], "checks": [
            {"name": "managed-vms", "status": "failed", "freshness": "stale"}]}}
        self.assertFalse(runner.host_reports_managed_vm(unhealthy, "vm-1"))
        stale_error = runner.managed_vm_inventory_error(stale, "vm-1")
        self.assertIn("stale or unavailable", stale_error)
        unknown_freshness = {**host, "inventory": {**host["inventory"], "checks": [
            {"name": "managed-vms", "status": "healthy", "freshness": "unknown"}]}}
        self.assertIn("unverified", runner.managed_vm_inventory_error(unknown_freshness, "vm-1"))
        self.assertIn("absent", runner.managed_vm_inventory_error(host, "vm-2"))

    def test_result_artifact_is_created_private_and_not_overwritten(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "result.json"
            runner.save_private_result(path, {"status": "passed"})
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            with self.assertRaises(FileExistsError):
                runner.save_private_result(path, {"status": "failed"})

    def test_binary_is_staged_and_verified_before_atomic_replacement(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "candidate"
            installed = root / "nex-core-local"
            staged = root / ".nex-core-local.staged"
            source.write_bytes(b"new candidate")
            installed.write_bytes(b"old installed binary")
            source_hash = hashlib.sha256(source.read_bytes()).hexdigest()
            old_hash = hashlib.sha256(installed.read_bytes()).hexdigest()

            result = runner.stage_binary_for_replace(source, installed, source_hash, staged)
            self.assertEqual(result, staged)
            self.assertEqual(staged.stat().st_mode & 0o777, 0o755)
            self.assertEqual(hashlib.sha256(staged.read_bytes()).hexdigest(), source_hash)
            self.assertEqual(installed.read_bytes(), b"old installed binary")

            runner.replace_staged_binary(staged, installed, source_hash, old_hash)
            self.assertFalse(staged.exists())
            self.assertEqual(installed.stat().st_mode & 0o777, 0o755)
            self.assertEqual(hashlib.sha256(installed.read_bytes()).hexdigest(), source_hash)

    def test_binary_replacement_refuses_unexpected_installed_target(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "candidate"
            installed = root / "nex-core-local"
            source.write_bytes(b"new candidate")
            installed.write_bytes(b"unexpected target")
            candidate_hash = hashlib.sha256(source.read_bytes()).hexdigest()
            staged = runner.stage_binary_for_replace(source, installed, candidate_hash)
            with self.assertRaisesRegex(RuntimeError, "changed before replacement"):
                runner.replace_staged_binary(staged, installed, candidate_hash, "0" * 64)
            self.assertEqual(installed.read_bytes(), b"unexpected target")
            self.assertTrue(staged.exists())

    def test_result_only_collision_refuses_attempt_before_candidate_check(self):
        cache = runner.ROOT / ".cache"
        cache.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="recovery-corea-snapshot-api-prep-test-", dir=cache) as temporary:
            prep = Path(temporary)
            result = prep / "core-local-snapshot-api-r4-resume-result.json"
            result.write_text('{"status":"incomplete"}\n')
            result.chmod(0o600)
            request = prep / "core-local-snapshot-api-r4-resume-request.json"
            self.assertFalse(request.exists())
            argv = ["core_local_snapshot_api.py", "--lab-dir", str(cache / "no-lab-call"),
                    "--prep-dir", str(prep), "--expected-core-binary-sha256", "a" * 64,
                    "--expected-installed-package-version", "fixture", "--attempt-label", "r4-resume", "--execute"]
            with patch("sys.argv", argv), patch.object(runner, "ssh_run") as candidate_check:
                with self.assertRaisesRegex(RuntimeError, "result or request file already exists"):
                    runner.main()
                candidate_check.assert_not_called()

    def test_preserved_request_is_bound_to_exact_vm_and_body(self):
        with tempfile.TemporaryDirectory() as temporary:
            prep = Path(temporary)
            request_path = prep / "r3-request.json"
            body = {"operation_id": "api-s0-existing", "name": "snapshot-existing", "description": "prepared"}
            request_path.write_text(json.dumps({"operation_id": body["operation_id"], "vm_id": "vm-1",
                                                "snapshot_name": body["name"], "description": body["description"],
                                                "body": body}))
            request_path.chmod(0o600)
            loaded = runner.load_snapshot_request(request_path, prep, "vm-1")
            self.assertEqual(loaded["operation_id"], body["operation_id"])
            self.assertEqual(loaded["body"], body)
            with self.assertRaisesRegex(RuntimeError, "selected VM"):
                runner.load_snapshot_request(request_path, prep, "vm-2")

    def test_preserved_request_rejects_outside_path_and_symlink(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            prep = root / "prep"
            prep.mkdir()
            outside = root / "request.json"
            body = {"operation_id": "op-1", "name": "snapshot", "description": "prepared"}
            outside.write_text(json.dumps({"operation_id": "op-1", "vm_id": "vm-1",
                                           "snapshot_name": "snapshot", "description": "prepared", "body": body}))
            outside.chmod(0o600)
            with self.assertRaisesRegex(RuntimeError, "inside"):
                runner.load_snapshot_request(outside, prep, "vm-1")
            link = prep / "request.json"
            link.symlink_to(outside)
            with self.assertRaisesRegex(RuntimeError, "symlink"):
                runner.load_snapshot_request(link, prep, "vm-1")

    def test_core_restart_replay_is_part_of_the_remote_acceptance(self):
        compile(runner.REMOTE, "remote-snapshot-api-payload", "exec")
        self.assertIn('"status": "passed"', runner.REMOTE)
        self.assertIn('systemctl", "restart", "nex-core-local.service', runner.REMOTE)
        self.assertIn('replay_after_restart = call(path, "POST", body', runner.REMOTE)
        self.assertIn('operation_after_restart.get("status") != "succeeded"', runner.REMOTE)
        self.assertIn('markers_after_restart.append', runner.REMOTE)

    def test_core_restart_readiness_waits_through_wrapper_until_candidate_and_tls_are_ready(self):
        tree = ast.parse(runner.REMOTE)
        function = next(node for node in tree.body
                        if isinstance(node, ast.FunctionDef) and node.name == "wait_for_service_restart")
        namespace = {"time": __import__("time"), "RuntimeError": RuntimeError, "ValueError": ValueError,
                     "OSError": OSError, "subprocess": subprocess}
        exec(compile(ast.Module(body=[function], type_ignores=[]), "restart-readiness", "exec"), namespace)
        now = [0.0]
        identities = [RuntimeError("setup wrapper is still MainPID"),
                      {"pid": 22, "executable": "/usr/lib/nex/nex-core-local",
                       "binary_sha256": "candidate", "package_version": "candidate-version"},
                      {"pid": 22, "executable": "/usr/lib/nex/nex-core-local",
                       "binary_sha256": "candidate", "package_version": "candidate-version"}]
        tls = [False, True]
        observed_tls = []

        def identity():
            item = identities.pop(0)
            if isinstance(item, Exception):
                raise item
            self.assertEqual(item["executable"], "/usr/lib/nex/nex-core-local")
            self.assertEqual(item["binary_sha256"], "candidate")
            self.assertEqual(item["package_version"], "candidate-version")
            return item

        def https_ready():
            value = tls.pop(0)
            observed_tls.append(value)
            return value

        def sleep(seconds):
            now[0] += seconds

        result = namespace["wait_for_service_restart"](
            11, 2.0, identity, https_ready, now_fn=lambda: now[0], sleep_fn=sleep)
        self.assertEqual(result["pid"], 22)
        self.assertEqual(observed_tls, [False, True])

    def test_core_restart_readiness_does_not_accept_wrong_process_identity(self):
        tree = ast.parse(runner.REMOTE)
        function = next(node for node in tree.body
                        if isinstance(node, ast.FunctionDef) and node.name == "wait_for_service_restart")
        now = [0.0]
        namespace = {"time": __import__("time"), "RuntimeError": RuntimeError, "ValueError": ValueError,
                     "OSError": OSError, "subprocess": subprocess}
        exec(compile(ast.Module(body=[function], type_ignores=[]), "restart-readiness", "exec"), namespace)

        def identity():
            raise RuntimeError("wrong executable hash")

        def sleep(seconds):
            now[0] += seconds

        with self.assertRaisesRegex(RuntimeError, "verified executable and pinned HTTPS readiness"):
            namespace["wait_for_service_restart"](11, 0.5, identity, lambda: True,
                                                   now_fn=lambda: now[0], sleep_fn=sleep)

    def test_candidate_check_preserves_binary_path_after_resume_chain_check(self):
        vm_id = "vm-1"
        storage = "/var/lib/nex/storage"
        head = storage + "/nex-vms/vm-1/snapshot-op-1-vda.qcow2"
        base = storage + "/nex-vms/vm-1/disk-0.qcow2"
        xml = ("<domain><devices><disk><source file=\"" + head + "\"/><target dev=\"vda\"/>"
               "</disk><disk><source file=\"" + storage + "/nex-vms/vm-1/snapshot-op-1-vdb.qcow2\"/>"
               "<target dev=\"vdb\"/></disk></devices></domain>")
        replies = [
            "0.3.6~recovery.s0", "active", "12", vm_id, "shut off", "nex-op-1", xml,
            json.dumps([{"filename": head}, {"filename": base}]),
            json.dumps([{"filename": storage + "/nex-vms/vm-1/snapshot-op-1-vdb.qcow2"},
                        {"filename": storage + "/nex-vms/vm-1/disk-1.qcow2"}]),
        ]

        def fake_run(*_args, **_kwargs):
            return subprocess.CompletedProcess([], 0, replies.pop(0), "")

        original_realpath = os.path.realpath

        def fake_realpath(path):
            return "/usr/lib/nex/nex-core-local" if str(path).startswith("/proc/12/exe") else original_realpath(path)

        config = {"package_version": "0.3.6~recovery.s0", "binary_sha256": __import__("hashlib").sha256(b"candidate").hexdigest(),
                  "vm_id": vm_id, "vm_name": "prepared", "storage_root": storage,
                  "resume": True, "operation_id": "op-1"}
        output = io.StringIO()
        with patch("sys.stdin", io.StringIO(json.dumps(config) + "\n")), \
                patch("sys.stdout", output), patch.object(runner.subprocess, "run", side_effect=fake_run), \
                patch.object(runner.os.path, "realpath", side_effect=fake_realpath), \
                patch("builtins.open", return_value=io.BytesIO(b"candidate")):
            exec(compile(runner.CANDIDATE_CHECK, "candidate-check", "exec"), {})
        result = json.loads(output.getvalue())
        self.assertEqual(result["binary_path"], "/usr/lib/nex/nex-core-local")
        self.assertEqual(replies, [])


if __name__ == "__main__":
    unittest.main()
