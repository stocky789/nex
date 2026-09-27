import importlib.util
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import ssl
import subprocess
import tempfile
import threading
import unittest
from unittest import mock


MODULE = Path(__file__).parent / "integration" / "core_snapshot_controller_replacement.py"
SPEC = importlib.util.spec_from_file_location("core_snapshot_controller_replacement", MODULE)
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)


class CoreSnapshotControllerReplacementTests(unittest.TestCase):
    def test_remote_payloads_compile_without_running(self):
        compile(runner.REMOTE_PREFLIGHT, "snapshot-controller-preflight", "exec")
        compile(runner.REMOTE, "snapshot-controller-switch", "exec")
        compile(runner.REMOTE_MARKER_VERIFY, "snapshot-controller-markers", "exec")

    def test_producer_marker_readbacks_drive_the_remote_disk_verification(self):
        markers = runner.marker_readback_config([
            {"target": "vda", "pattern": "0x31", "source": "/var/lib/images/a.qcow2",
             "readback": "read 4096/4096 bytes at offset 4096\n4 KiB, 1 ops"},
            {"target": "vdb", "pattern": "0x42", "source": "/var/lib/images/b.qcow2",
             "readback": "read 8192/8192 bytes at offset 8192\n8 KiB, 1 ops"},
        ])
        commands = []

        def run(args, capture_output, text, timeout):
            commands.append(args)
            length = args[4].split()[-1]
            offset = args[4].split()[-2]
            return subprocess.CompletedProcess(args, 0,
                stdout=f"read {length}/{length} bytes at offset {offset}\n", stderr="")

        namespace = {"config": {"markers": markers}, "subprocess": mock.Mock(run=run), "RuntimeError": RuntimeError}
        exec(runner.REMOTE_MARKER_VERIFY, namespace)
        namespace["verify_markers"]()
        self.assertEqual(commands, [
            ["qemu-io", "-f", "qcow2", "-c", "read -P 0x31 4096 4096", "/var/lib/images/a.qcow2"],
            ["qemu-io", "-f", "qcow2", "-c", "read -P 0x42 8192 8192", "/var/lib/images/b.qcow2"],
        ])

    def test_controller_operation_response_is_the_snapshot_outcome_projection(self):
        host_id, snapshot_id = "host-1", "snapshot-op-1"
        operations = [{"host_id": host_id, "kind": "snapshot-create", "status": "succeeded",
                       "request_id": f"adopt-outcome:{host_id}:{snapshot_id}"}]
        self.assertEqual(runner.adopted_snapshot_operation(operations, host_id, snapshot_id), operations[0])
        self.assertIsNone(runner.adopted_snapshot_operation(operations, "host-2", snapshot_id))
        self.assertIsNone(runner.adopted_snapshot_operation([dict(operations[0], status="failed")], host_id, snapshot_id))

    def test_snapshot_fixture_requires_private_success_and_restart_evidence(self):
        cache = runner.ROOT / ".cache"
        cache.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="recovery-corea-snapshot-api-prep-test.", dir=cache) as temp:
            prep = Path(temp)
            prep.chmod(0o700)
            op_id, vm_id = "snapshot-op-1", "vm-1"
            hash_value, version = "a" * 64, "1.2.3"
            request = {"operation_id": op_id, "vm_id": vm_id, "snapshot_name": "Before change",
                       "description": "replacement proof", "body": {"operation_id": op_id,
                       "name": "Before change", "description": "replacement proof"}}
            core = {"core_service_before_restart": {"pid": 31},
                    "core_service_after_restart": {"pid": 32, "state": "active", "binary_sha256": hash_value},
                    "exact_replay_after_restart": {"operation_id": op_id},
                    "operation_after_restart": {"status": "succeeded"},
                    "master_catalog_after_restart": {"id": op_id, "vm_id": vm_id,
                        "name": "Before change", "description": "replacement proof"},
                    "markers_after_restart": [
                        {"target": "vda", "pattern": "0x31", "source": "/var/lib/images/nex-vms/vm-1/disk-0.qcow2",
                         "readback": "read 4096/4096 bytes at offset 4096\n4 KiB, 1 ops"},
                        {"target": "vdb", "pattern": "0x42", "source": "/var/lib/images/nex-vms/vm-1/disk-1.qcow2",
                         "readback": "read 8192/8192 bytes at offset 8192\n8 KiB, 1 ops"}],
                    "markers": [{}, {}], "vm_state": "shut off"}
            result = {"status": "passed", "controller_restart": {"pid": 55},
                      "core_candidate": {"binary_sha256": hash_value, "version": version},
                      "core_result": core}
            vm = {"vm_id": vm_id, "host_id": "host-1", "pool_id": "pool-1", "name": "vm-one"}
            ids = {"host_id": "host-1", "pool_id": "pool-1"}
            for name, value in (("core-local-snapshot-api-replace-request.json", request),
                                ("core-local-snapshot-api-replace-result.json", result),
                                ("managed-vm.json", vm), ("corea-ids.json", ids),
                                ("resources.json", {"storage_path": "/var/lib/images"})):
                path = prep / name
                path.write_text(json.dumps(value))
                path.chmod(0o600)
            loaded = runner.snapshot_fixture(prep, prep / "core-local-snapshot-api-replace-result.json",
                                             hash_value, version)
            self.assertEqual(loaded["request"]["operation_id"], op_id)
            self.assertEqual(loaded["vm"]["host_id"], "host-1")
            self.assertEqual([(item["target"], item["pattern"], item["offset"], item["length"])
                              for item in loaded["markers"]],
                             [("vda", "0x31", 4096, 4096), ("vdb", "0x42", 8192, 8192)])

            result["status"] = "incomplete"
            (prep / "core-local-snapshot-api-replace-result.json").write_text(json.dumps(result))
            with self.assertRaisesRegex(RuntimeError, "successful Core snapshot walkthrough"):
                runner.snapshot_fixture(prep, prep / "core-local-snapshot-api-replace-result.json",
                                        hash_value, version)
            result["status"] = "passed"
            request["body"]["name"] = "different"
            (prep / "core-local-snapshot-api-replace-result.json").write_text(json.dumps(result))
            (prep / "core-local-snapshot-api-replace-request.json").write_text(json.dumps(request))
            with self.assertRaisesRegex(RuntimeError, "exact successful catalog entry"):
                runner.snapshot_fixture(prep, prep / "core-local-snapshot-api-replace-result.json",
                                        hash_value, version)

    def test_snapshot_fixture_rejects_changed_candidate_or_missing_restart(self):
        cache = runner.ROOT / ".cache"
        cache.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="recovery-corea-snapshot-api-prep-test.", dir=cache) as temp:
            prep = Path(temp)
            prep.chmod(0o700)
            op_id, vm_id = "snapshot-op-2", "vm-2"
            request = {"operation_id": op_id, "vm_id": vm_id, "snapshot_name": "Saved",
                       "description": "", "body": {"operation_id": op_id, "name": "Saved", "description": ""}}
            result = {"status": "passed", "controller_restart": {},
                "core_candidate": {"binary_sha256": "b" * 64, "version": "1.0"},
                "core_result": {"core_service_before_restart": {"pid": 1},
                    "core_service_after_restart": {"pid": 2, "state": "active", "binary_sha256": "b" * 64},
                    "exact_replay_after_restart": {"operation_id": op_id},
                    "operation_after_restart": {"status": "succeeded"},
                    "master_catalog_after_restart": {"id": op_id, "vm_id": vm_id, "name": "Saved", "description": ""},
                    "markers_after_restart": [
                        {"target": "vda", "pattern": "0x31", "source": "/var/lib/images/a.qcow2",
                         "readback": "read 4096/4096 bytes at offset 4096"},
                        {"target": "vdb", "pattern": "0x42", "source": "/var/lib/images/b.qcow2",
                         "readback": "read 4096/4096 bytes at offset 4096"}],
                    "markers": [{}, {}], "vm_state": "shut off"}}
            for name, value in (("core-local-snapshot-api-replace-request.json", request),
                                ("managed-vm.json", {"vm_id": vm_id, "host_id": "host-2", "pool_id": "pool-2"}),
                                ("corea-ids.json", {"host_id": "host-2", "pool_id": "pool-2"}),
                                ("core-local-snapshot-api-replace-result.json", result)):
                path = prep / name
                path.write_text(json.dumps(value))
                path.chmod(0o600)
            evidence = prep / "core-local-snapshot-api-replace-result.json"
            with self.assertRaisesRegex(RuntimeError, "candidate identity"):
                runner.snapshot_fixture(prep, evidence, "c" * 64, "1.0")
            with self.assertRaisesRegex(RuntimeError, "candidate identity"):
                runner.snapshot_fixture(prep, evidence, "b" * 64, "2.0")

    def test_replacement_directory_must_be_new_private_cache_child(self):
        cache = runner.ROOT / ".cache"
        cache.mkdir(exist_ok=True)
        candidate = cache / "core-snapshot-controller-b-test"
        self.assertEqual(runner.verify_private_dir(candidate), candidate.resolve())
        candidate.mkdir(mode=0o700)
        with self.assertRaisesRegex(RuntimeError, "already exists"):
            runner.verify_private_dir(candidate)
        candidate.rmdir()

    def test_preflight_failure_artifact_uses_existing_private_prep_parent(self):
        with tempfile.TemporaryDirectory() as temp:
            prep = Path(temp) / "prep"
            prep.mkdir(mode=0o700)
            replacement = Path(temp) / "core-snapshot-controller-b-new"
            failure_path = runner.preflight_failure_path(prep, replacement)
            self.assertEqual(failure_path.parent, prep)
            self.assertFalse(replacement.exists())

    def test_original_controller_state_fingerprint_detects_changes(self):
        with tempfile.TemporaryDirectory() as temp:
            state = Path(temp) / "controller-state"
            state.mkdir()
            database = state / "controller.db"
            database.write_bytes(b"original")
            original = runner.private_tree_digest(state)
            database.write_bytes(b"changed")
            self.assertNotEqual(runner.private_tree_digest(state), original)

    def test_controller_a_fingerprint_covers_database_ca_and_tls_key(self):
        with tempfile.TemporaryDirectory() as temp:
            prep = Path(temp)
            (prep / "controller-state").mkdir()
            (prep / "controller-state" / "host-ca.key").write_bytes(b"host-ca")
            (prep / "tls.crt").write_bytes(b"controller-ca")
            (prep / "tls.key").write_bytes(b"controller-key")
            original = runner.controller_a_fingerprint(prep)
            self.assertIn("tls.key", original["tls"])
            (prep / "tls.key").write_bytes(b"changed-controller-key")
            self.assertNotEqual(runner.controller_a_fingerprint(prep), original)

    def test_controller_candidate_hash_is_pinned_and_symlinks_are_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            candidate = root / "nex-controller"
            candidate.write_bytes(b"candidate")
            expected = hashlib.sha256(b"candidate").hexdigest()
            self.assertEqual(runner.file_sha256(candidate, expected), expected)
            with self.assertRaisesRegex(RuntimeError, "hash differs"):
                runner.file_sha256(candidate, "0" * 64)
            alias = root / "alias"
            alias.symlink_to(candidate)
            with self.assertRaisesRegex(RuntimeError, "non-symlink"):
                runner.file_sha256(alias, expected)

    def test_lab_fingerprint_binds_exact_path_and_manifest_inputs(self):
        with tempfile.TemporaryDirectory() as temp:
            lab = Path(temp)
            for name, value in (("lab.json", b'{"disposable":true}'),
                                ("known_hosts", b"host-key"), ("ports.json", b"{}"),
                                ("id_ed25519", b"private-key-fixture")):
                (lab / name).write_bytes(value)
            first = runner.lab_fingerprint(lab)
            self.assertEqual(len(first), 64)
            (lab / "ports.json").write_bytes(b'{"core1":10022}')
            self.assertNotEqual(runner.lab_fingerprint(lab), first)

    def test_controller_origin_requires_https_origin_without_credentials_or_path(self):
        self.assertEqual(runner.validate_origin("https://192.0.2.20:9443"), ("192.0.2.20", 9443))
        for value in ("http://192.0.2.20:9443", "https://user@host:9443", "https://host:9443/path"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                runner.validate_origin(value)

    def test_b_advertised_and_host_local_poll_endpoints_are_separate(self):
        self.assertEqual(runner.controller_endpoints("https://192.0.2.20:9443", "0.0.0.0:9443"),
                         ("192.0.2.20", "https://192.0.2.20:9443", "127.0.0.1:9443",
                          "https://127.0.0.1:9443", 9443))
        with self.assertRaisesRegex(RuntimeError, "listen port"):
            runner.controller_endpoints("https://192.0.2.20:9443", "0.0.0.0:9444")

    def test_controller_login_cookie_reaches_next_authenticated_request(self):
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                self.rfile.read(int(self.headers.get("Content-Length", "0")))
                self.send_response(200)
                self.send_header("Set-Cookie", "nex_session=accepted; Path=/; HttpOnly")
                self.send_header("Content-Type", "application/json")
                body = b'{"csrf_token":"csrf-value"}'
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self):
                status = 200 if "nex_session=accepted" in self.headers.get("Cookie", "") else 401
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                body = b"[]" if status == 200 else b'{"error":"unauthorized"}'
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, _format, *_args):
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        context = ssl.create_default_context()
        try:
            with mock.patch.object(runner.ssl, "create_default_context", return_value=context):
                base = "http://127.0.0.1:" + str(server.server_port)
                jar, csrf = runner.admin_session(base, Path("unused-cert"), "test-password")
                self.assertEqual(csrf, "csrf-value")
                self.assertEqual(len(jar), 1)
                self.assertEqual(runner.controller_api(base, Path("unused-cert"), "test-password",
                    "GET", "/api/v1/hosts", jar=jar, csrf=csrf), [])
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)

    def test_remote_core_cookie_setup_executes_and_reuses_session_cookie(self):
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                self.rfile.read(int(self.headers.get("Content-Length", "0")))
                self.send_response(200)
                self.send_header("Set-Cookie", "nex_session=accepted; Path=/; HttpOnly")
                self.send_header("Content-Length", "0")
                self.end_headers()

            def do_GET(self):
                status = 200 if "nex_session=accepted" in self.headers.get("Cookie", "") else 401
                self.send_response(status)
                self.send_header("Content-Length", "0")
                self.end_headers()

            def log_message(self, _format, *_args):
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        namespace = {"http": runner.http, "urllib": runner.urllib, "context": ssl.create_default_context()}
        try:
            exec(runner.REMOTE_COOKIE_SETUP, namespace)
            base = "http://127.0.0.1:" + str(server.server_port)
            request = runner.urllib.request.Request(base + "/api/local/v1/session", data=b"{}",
                headers={"Content-Type": "application/json"}, method="POST")
            with namespace["opener"].open(request, timeout=5) as response:
                self.assertEqual(response.status, 200)
            request = runner.urllib.request.Request(base + "/api/local/v1/pool/vms/vm-1/snapshots")
            with namespace["opener"].open(request, timeout=5) as response:
                self.assertEqual(response.status, 200)
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)

    def test_remote_root_login_relocks_after_success_and_api_failure(self):
        def setup_login(fail_login):
            state = {"locked": True}
            calls = []

            def fake_run(args, input=None, check=False, capture_output=False, text=False, timeout=None):
                calls.append((args, input))
                if args == ["passwd", "-S", "root"]:
                    status = "L" if state["locked"] else "P"
                    return subprocess.CompletedProcess(args, 0, stdout="root " + status + " 01/01/2026\n")
                if args == ["chpasswd"]:
                    self.assertEqual(input, "root:test-password\n")
                    state["locked"] = False
                    return subprocess.CompletedProcess(args, 0, stdout="")
                if args == ["passwd", "-l", "root"]:
                    state["locked"] = True
                    return subprocess.CompletedProcess(args, 0, stdout="")
                self.fail("unexpected PAM command: " + repr(args))

            def call(path, method, body, expected):
                self.assertEqual((path, method, body["password"], expected),
                                 ("/api/local/v1/session", "POST", "test-password", 200))
                if fail_login:
                    raise RuntimeError("local session rejected")
                return {"csrf_token": "csrf"}

            namespace = {"subprocess": type("Subprocess", (), {"run": staticmethod(fake_run)}), "call": call}
            exec(runner.REMOTE_ROOT_LOGIN_SETUP, namespace)
            return namespace, state, calls

        namespace, state, calls = setup_login(False)
        login = namespace["login_with_temporary_root_password"]("test-password")
        self.assertEqual(login["csrf_token"], "csrf")
        self.assertTrue(state["locked"])
        self.assertEqual(calls[-2][0], ["passwd", "-l", "root"])
        namespace, state, _calls = setup_login(True)
        with self.assertRaisesRegex(RuntimeError, "local session rejected"):
            namespace["login_with_temporary_root_password"]("test-password")
        self.assertTrue(state["locked"])

    def test_controller_a_snapshot_precondition_matches_full_descriptor(self):
        managed = {"pool_vm_snapshots_reported": True, "pool_vm_snapshots": [
            {"id": "snapshot-1", "vm_id": "vm-1", "name": "accepted", "description": "desc"}]}
        self.assertTrue(runner.controller_has_snapshot(managed, "snapshot-1", "vm-1", "accepted", "desc"))
        self.assertFalse(runner.controller_has_snapshot(managed, "snapshot-1", "vm-2", "accepted", "desc"))
        self.assertFalse(runner.controller_has_snapshot(managed, "snapshot-1", "vm-1", "other", "desc"))
        self.assertFalse(runner.controller_has_snapshot(managed, "snapshot-1", "vm-1", "accepted", "other"))


if __name__ == "__main__":
    unittest.main()
