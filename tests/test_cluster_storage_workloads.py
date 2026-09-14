# SPDX-License-Identifier: AGPL-3.0-only
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from concurrent.futures import Future
from types import SimpleNamespace


spec = importlib.util.spec_from_file_location(
    "cluster_storage_workloads", Path(__file__).parent / "integration/cluster_storage_workloads.py")
workloads = importlib.util.module_from_spec(spec)
spec.loader.exec_module(workloads)


class ClusterStorageWorkloadTests(unittest.TestCase):
    def ids(self):
        return {"controller": "https://127.0.0.1:44443", "password_file": "/private/password", "pool_id": "pool-1", "outer_core_memory_mib": 4096,
                "hosts": [{"id": "host-1", "lab_name": "core1", "network_id": "net-1"}, {"id": "host-2", "lab_name": "core2", "network_id": "net-2"}, {"id": "host-3", "lab_name": "core3", "network_id": "net-3"}],
                "peer_origins": {"core1": "https://master.example.test:9444", "core2": "https://member2.example.test:9444", "core3": "https://member3.example.test:9444"},
                "guest": {"image_path": "/private/guest.qcow2", "ssh_key": "/root/.ssh/guest"},
                "backends": {name: {"storage_id": name + "-storage", "storage_path": "/mnt/" + name} for name in workloads.BACKENDS}}

    def test_wait_for_cluster_retries_only_typed_503(self):
        member = {"status": {"healthy": True, "quorate": True, "mounted": True, "fencing_verified": True, "status": "healthy"}}
        healthy = {"status": "accepted", "operations": [{"config": {"repository_id": "repo"}, "status": "healthy", "members": [member] * 3}]}
        class API:
            def __init__(self): self.calls = 0
            def request(self, *_):
                self.calls += 1
                if self.calls == 1: raise workloads.HTTPError(503, "GET /storage: 503 capability unavailable")
                return healthy
        api, original = API(), workloads.time.sleep
        workloads.time.sleep = lambda _: None
        try:
            self.assertEqual(workloads.wait_for_cluster(api, "pool", "repo", timeout=1)["status"], "healthy")
            self.assertEqual(api.calls, 2)
            class Unauthorized:
                def request(self, *_): raise workloads.HTTPError(401, "GET /storage: 401")
            with self.assertRaises(workloads.HTTPError): workloads.wait_for_cluster(Unauthorized(), "pool", "repo", timeout=1)
        finally:
            workloads.time.sleep = original

    def test_api_operation_remains_on_api_not_http_error(self):
        api = workloads.API.__new__(workloads.API)
        api.request = lambda *_: [{"id": "op", "status": "succeeded"}]
        self.assertEqual(api.operation("op", timeout=1)["id"], "op")
        self.assertFalse(hasattr(workloads.HTTPError, "operation"))

    def test_vm_body_is_bios_one_disk_and_512_mib(self):
        ids, host = self.ids(), self.ids()["hosts"][0]
        body = workloads.vm_body(ids, host, "nfs", 1, 32)
        self.assertEqual(body["memory"]["size_mib"], 512)
        self.assertEqual(body["memory"]["mode"], "maximum")
        self.assertEqual(body["boot"]["firmware"], "bios")
        self.assertEqual(body["disks"], [{"storage_id": "nfs-storage", "name": "system", "size_gib": 32, "bus": "virtio", "thin": True, "boot_order": 1}])
        self.assertEqual(body["cpu"], {"count": 1, "sockets": 1, "cores": 1, "threads": 1, "model": "host-passthrough"})
        self.assertEqual(body["nics"], [{"network_id": "net-1", "model": "virtio"}])
        self.assertEqual(body["host_id"], "host-1")

    def test_competing_qemu_probe_covers_every_shared_backend(self):
        self.assertEqual(workloads.LOCK_PROBE_BACKENDS, workloads.BACKENDS)

    def test_master_agent_outage_binds_only_the_observed_core1_master(self):
        ids = self.ids()
        hosts = []
        for host in ids["hosts"]:
            hosts.append({"id": host["id"], "pool_id": ids["pool_id"], "inventory": {"managed": {"membership": {"master_host_id": "host-1", "members": [
                {"host_id": member["id"], "status": "ready"} for member in ids["hosts"]]}}}})
        master = workloads.require_master_agent_target(ids, hosts)
        self.assertEqual(master["id"], "host-1")
        command = workloads.master_agent_outage_command(ids, master, "nex-master-agent-a1b2c3d4e5f60708")
        self.assertIn("systemctl stop nex-cluster-local.service", command)
        self.assertIn("--on-active=180s", command)
        hosts[1]["inventory"]["managed"]["membership"]["master_host_id"] = "host-2"
        with self.assertRaisesRegex(RuntimeError, "observed current pool master"):
            workloads.require_master_agent_target(ids, hosts)
        with self.assertRaises(ValueError):
            workloads.master_agent_outage_command(ids, {"lab_name": "core2"}, "nex-master-agent-a1b2c3d4e5f60708")

    def test_refused_start_preserves_stopped_vm_and_requires_failed_operation(self):
        calls, original_power, original_wait, original_sleep = [], workloads.power, workloads.wait_for_vm_state, workloads.time.sleep
        fixture = {"id": "vm-2", "pool_id": "pool-1", "host_id": "host-2", "name": "admission", "spec": {"disks": []}, "state": "shut off"}
        class API:
            def request(self, method, path, body=None):
                calls.append((method, path, body))
                if method == "POST":
                    return {"operation": {"id": "refusal"}}
                return [{"id": "refusal", "status": "failed", "error": "pool master unavailable"}]
        workloads.power = lambda api, vm_id, action: calls.append(("power", vm_id, action))
        workloads.wait_for_vm_state = lambda api, vm_id, state: {**fixture, "id": vm_id, "state": state}
        workloads.time.sleep = lambda _: None
        try:
            outcome = workloads.refused_start_during_master_outage(API(), fixture, "https://master.example.test:9444")
        finally:
            workloads.power, workloads.wait_for_vm_state, workloads.time.sleep = original_power, original_wait, original_sleep
        self.assertEqual(outcome["operation"]["status"], "failed")
        self.assertEqual(calls[0][2]["action"], "start")

    def test_refused_start_rejects_successful_or_unexplained_operation(self):
        original_power, original_wait, original_sleep = workloads.power, workloads.wait_for_vm_state, workloads.time.sleep
        class API:
            def request(self, method, path, body=None):
                if method == "POST":
                    return {"operation": {"id": "bad"}}
                return [{"id": "bad", "status": "succeeded"}]
        workloads.power = lambda *_: None
        workloads.wait_for_vm_state = lambda _api, vm_id, state: {"id": vm_id, "state": state}
        workloads.time.sleep = lambda _: None
        try:
            with self.assertRaisesRegex(RuntimeError, "not safely refused"):
                workloads.refused_start_during_master_outage(API(), {"id": "vm-2"}, "https://master.example.test:9444")
        finally:
            workloads.power, workloads.wait_for_vm_state, workloads.time.sleep = original_power, original_wait, original_sleep

    def test_refused_start_accepts_only_exact_observed_master_inventory_refusal(self):
        origin = "https://master.example.test:9444"
        fixture = {"id": "vm-2", "pool_id": "pool-1", "host_id": "host-2", "name": "admission", "spec": {}, "state": "shut off"}
        class API:
            def request(self, method, path, body=None):
                if method == "POST": return {"operation": {"id": "refusal"}}
                return [{"id": "refusal", "status": "failed", "error": 'Post "https://master.example.test:9444/api/v1/pool/vms/inventory": dial tcp master.example.test:9444: connect: connection refused'}]
        original_wait, original_sleep = workloads.wait_for_vm_state, workloads.time.sleep
        workloads.wait_for_vm_state, workloads.time.sleep = lambda *_: fixture, lambda _: None
        try:
            self.assertEqual(workloads.refused_start_during_master_outage(API(), fixture, origin)["vm_id"], "vm-2")
        finally:
            workloads.wait_for_vm_state, workloads.time.sleep = original_wait, original_sleep

    def test_master_agent_outage_requires_master_list_failure_from_both_members(self):
        ids = self.ids()
        def host(host_id, check):
            return {"id": host_id, "connection": "fresh", "inventory": {"checks": [check]}}
        class API:
            def request(self, *_):
                return [host("host-1", {}),
                        host("host-2", {"name": "pool-storage-reconciliation", "freshness": "fresh", "status": "failed", "error": "repository deadline"}),
                        host("host-3", {"name": "pool-storage-reconciliation", "freshness": "fresh", "status": "failed", "error": "master-list unavailable"})]
        original_monotonic, original_sleep = workloads.time.monotonic, workloads.time.sleep
        ticks = iter((0, 0, 91))
        workloads.time.monotonic, workloads.time.sleep = lambda: next(ticks), lambda _: None
        try:
            with self.assertRaisesRegex(RuntimeError, "fresh failed"):
                workloads.wait_for_member_reconciliation(API(), ids, "host-1", False, after=0, timeout=90)
        finally:
            workloads.time.monotonic, workloads.time.sleep = original_monotonic, original_sleep

    def test_writer_progress_requires_every_fsynced_writer_to_advance(self):
        guests = [{"vm_id": "one", "host": {}}, {"vm_id": "two", "host": {}}]
        values = {"one": ["3", "4"], "two": ["2", "3"]}
        original_guest = workloads.guest
        def guest(_host, vm_id, _config, _command):
            return values[vm_id].pop(0)
        workloads.guest = guest
        try:
            before = workloads.writer_progress(guests, "iscsi_gfs2", {})
            after = workloads.writer_progress(guests, "iscsi_gfs2", {}, before)
        finally:
            workloads.guest = original_guest
        self.assertEqual(before, {"one": 3, "two": 2})
        self.assertEqual(after, {"one": 4, "two": 3})

    def test_interruption_writer_resets_fsynced_progress_before_publishing_marker(self):
        item, command = {"host": {}, "vm_id": "writer"}, []
        original_guest = workloads.guest
        workloads.guest = lambda *_args: command.append(_args[-1]) or ""
        try:
            workloads.interruption_write(item, "iscsi_gfs2", {}, 0)
        finally:
            workloads.guest = original_guest
        original_path = "/root/nex-cluster-iscsi_gfs2-writer.interruption.bin"
        with tempfile.TemporaryDirectory() as directory:
            root, stubs = Path(directory), Path(directory) / "stubs"
            stubs.mkdir()
            payload = root / "interruption.bin"
            progress, marker = Path(str(payload) + ".progress"), Path(str(payload) + ".started")
            progress.write_text("600\n")
            (stubs / "dd").write_text("#!/bin/sh\nprintf x\n")
            (stubs / "sleep").write_text("#!/bin/sh\nexit 0\n")
            (stubs / "sync").write_text("#!/bin/sh\nif test \"$1\" = -f && test \"$2\" = \"$MARKER\"; then test \"$(cat \"$PROGRESS\")\" = 0; fi\n")
            for stub in stubs.iterdir(): stub.chmod(0o755)
            environment = {**os.environ, "PATH": str(stubs) + ":" + os.environ["PATH"],
                           "MARKER": str(marker), "PROGRESS": str(progress)}
            result = subprocess.run(["sh", "-c", command[0].replace(original_path, str(payload))],
                                    capture_output=True, text=True, env=environment)
            final_progress = progress.read_text()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(final_progress, "600\n")

    def test_marker_poll_ignores_old_marker_until_the_fresh_barrier_marker_arrives(self):
        original_guest, original_clock, original_sleep = workloads.guest, workloads.time.monotonic, workloads.time.sleep
        values = iter(("9.0", "10.1"))
        workloads.guest, workloads.time.monotonic, workloads.time.sleep = (lambda *_: next(values)), (lambda: 0), (lambda _: None)
        try:
            markers = workloads.poll_interruption_markers([{"host": {}, "vm_id": "vm-1"}], "iscsi_gfs2", {}, 10)
        finally:
            workloads.guest, workloads.time.monotonic, workloads.time.sleep = original_guest, original_clock, original_sleep
        self.assertEqual(markers, {"vm-1": 10.1})

    def test_admission_vm_must_be_a_stopped_member_owned_gfs2_fixture(self):
        ids = self.ids()
        class API:
            def request(self, *_):
                return [{"id": "admission", "pool_id": "pool-1", "host_id": "host-2", "status": "succeeded", "freshness": "fresh", "state": "shut off",
                         "spec": {"disks": [{"storage_id": "iscsi_gfs2-storage"}]}}]
        self.assertEqual(workloads.bind_stopped_admission_vm(API(), ids, "admission", "host-1", "iscsi_gfs2-storage")["id"], "admission")
        with self.assertRaisesRegex(RuntimeError, "fresh stopped"):
            workloads.bind_stopped_admission_vm(API(), ids, "admission", "host-2", "iscsi_gfs2-storage")

    def test_master_agent_outage_restores_after_post_stop_progress_failure(self):
        ids = self.ids()
        ids["lab"] = Path("/private/lab")
        guests = [{"vm_id": f"vm-{number}", "host": {"id": f"host-{number // 2 + 1}", "lab_name": f"core{number // 2 + 1}"}}
                  for number in range(6)]
        order, originals = [], {name: getattr(workloads, name) for name in (
            "require_master_agent_target", "bind_stopped_admission_vm", "interruption_write", "poll_interruption_markers",
            "writer_progress", "guest_boot_ids", "ssh", "wait_for_member_reconciliation", "require_master_agent_inactive",
            "restore_master_agent")}
        class API:
            def request(self, *_): return []
        try:
            workloads.require_master_agent_target = lambda *_: {"id": "host-1", "lab_name": "core1"}
            workloads.bind_stopped_admission_vm = lambda *_: {"vm_id": "admission"}
            workloads.interruption_write = lambda *_: "1 2"
            workloads.poll_interruption_markers = lambda *_: {guest["vm_id"]: 1 for guest in guests}
            calls = 0
            def progress(*_):
                nonlocal calls
                calls += 1
                order.append("progress-before" if calls == 1 else "progress-after")
                if calls > 1: raise RuntimeError("writer stalled")
                return {guest["vm_id"]: 1 for guest in guests}
            workloads.writer_progress = progress
            workloads.guest_boot_ids = lambda *_: {guest["vm_id"]: "boot" for guest in guests}
            workloads.ssh = lambda *_: order.append("stop")
            workloads.wait_for_member_reconciliation = lambda _api, _ids, _master, healthy, *_args, **_kwargs: order.append("recovered" if healthy else "failed") or []
            workloads.require_master_agent_inactive = lambda *_: order.append("inactive")
            workloads.restore_master_agent = lambda *_: order.append("restore")
            with self.assertRaisesRegex(RuntimeError, "writer stalled"):
                workloads.master_agent_outage(API(), ids, guests, "iscsi_gfs2", {}, "admission")
        finally:
            for name, value in originals.items(): setattr(workloads, name, value)
        self.assertEqual(order, ["progress-before", "stop", "failed", "inactive", "progress-after", "restore"])

    def test_master_agent_outage_accepts_real_api_shaped_admission_record(self):
        ids = self.ids()
        ids["lab"] = Path("/private/lab")
        members = [{"host_id": host["id"], "status": "ready"} for host in ids["hosts"]]
        hosts = [{"id": host["id"], "pool_id": "pool-1", "inventory": {"managed": {"membership": {"master_host_id": "host-1", "members": members}}}}
                 for host in ids["hosts"]]
        admission = {"id": "admission", "pool_id": "pool-1", "host_id": "host-2", "name": "admission", "status": "succeeded", "freshness": "fresh", "state": "shut off",
                     "spec": {"disks": [{"storage_id": "iscsi_gfs2-storage"}]}}
        guests = [{"vm_id": f"vm-{number}", "host": {"id": f"host-{number // 2 + 1}", "lab_name": f"core{number // 2 + 1}"}}
                  for number in range(6)]
        class API:
            def request(self, method, path, body=None):
                if path == "/api/v1/hosts": return hosts
                if path == "/api/v1/vms": return [admission]
                if method == "POST": return {"operation": {"id": "refusal"}}
                if path == "/api/v1/operations": return [{"id": "refusal", "status": "failed", "error": "pool master unavailable"}]
                raise AssertionError((method, path, body))
        order, originals = [], {name: getattr(workloads, name) for name in (
            "interruption_write", "poll_interruption_markers", "writer_progress", "guest_boot_ids", "ssh",
            "wait_for_member_reconciliation", "require_master_agent_inactive", "restore_master_agent", "ThreadPoolExecutor")}
        class Future:
            def done(self): return False
            def result(self, timeout=None):
                order.append("result")
                return "1 2"
        class Pool:
            def __init__(self, **_): pass
            def __enter__(self): return self
            def __exit__(self, *_): return False
            def submit(self, *_): return Future()
        try:
            workloads.interruption_write = lambda *_: "1 2"
            workloads.ThreadPoolExecutor = Pool
            workloads.poll_interruption_markers = lambda *_: {guest["vm_id"]: 1 for guest in guests}
            progress = iter(({guest["vm_id"]: 1 for guest in guests}, {guest["vm_id"]: 2 for guest in guests}))
            workloads.writer_progress = lambda *_: next(progress)
            workloads.guest_boot_ids = lambda *_: {guest["vm_id"]: "boot" for guest in guests}
            workloads.ssh = lambda *_: order.append("stop")
            workloads.wait_for_member_reconciliation = lambda _api, _ids, _master, healthy, *_args, **_kwargs: order.append("recovered" if healthy else "failed") or []
            workloads.require_master_agent_inactive = lambda *_: order.append("inactive")
            workloads.restore_master_agent = lambda *_: order.append("restore")
            result = workloads.master_agent_outage(API(), ids, guests, "iscsi_gfs2", {}, "admission")
        finally:
            for name, value in originals.items(): setattr(workloads, name, value)
        self.assertEqual(result["start_refusal"]["vm_id"], "admission")
        self.assertEqual(order, ["stop", "failed", "inactive", "inactive", "inactive", "restore", "result", "result", "result", "result", "result", "result", "recovered"])

    def test_master_agent_outage_restores_before_post_recovery_writer_wait(self):
        order = []
        class Future:
            def result(self, timeout=None): order.append(("result", timeout)); return "1 2"
        original = workloads.time.monotonic
        ticks = iter((0, 1, 2, 3, 4, 5, 6, 7))
        workloads.time.monotonic = lambda: next(ticks)
        try:
            self.assertEqual(workloads.await_writer_outputs([Future(), Future()], timeout=20), ["1 2", "1 2"])
        finally:
            workloads.time.monotonic = original
        self.assertGreater(order[0][1], 0)

    def test_post_recovery_writer_timeout_is_bounded(self):
        class Future:
            def result(self, timeout=None): raise workloads.FutureTimeout()
        with self.assertRaisesRegex(RuntimeError, "post-recovery global budget"):
            workloads.await_writer_outputs([Future()], timeout=1)

    def test_pre_fence_stage_preserves_completed_baseline_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            result = {"backend": "iscsi_gfs2", "completed": True, "write_windows": [{"started": 1, "ended": 2}],
                      "reboot_hashes": "passed", "competing_writer_probe": {"holder_pid": "42"}}
            workloads.save_workload_stage(Path(directory), result)
            saved = json.loads((Path(directory) / "iscsi_gfs2-workload-stage.json").read_text())
        self.assertFalse(saved["completed"])
        self.assertEqual(saved["write_windows"], result["write_windows"])
        self.assertEqual(saved["reboot_hashes"], "passed")
        self.assertEqual(saved["competing_writer_probe"], result["competing_writer_probe"])

    def test_competing_qemu_probe_captures_holder_pid_before_release(self):
        first, second = {"host": {"id": "one", "storage_path": "/mnt/shared"}, "disk": "/mnt/shared/vm1"}, {"host": {"id": "two", "storage_path": "/mnt/shared"}, "disk": "/mnt/shared/vm2"}
        calls, original_shell, original_result, probe = [], workloads.shell, workloads.shell_result, ""
        def shell(host, command, check=True):
            nonlocal probe
            calls.append(command)
            if command.startswith("id -u libvirt-qemu"): return "64055\n992\n992 64055\n"
            if "mktemp -d /run/nex-lock-probe." in command: return "/run/nex-lock-probe.a1B2c3\n"
            if command.startswith("cat "): return "1234\n"
            if "qemu-img create -f qcow2" in command:
                probe = command.split("qemu-img create -f qcow2 ", 1)[1].split(" 64M", 1)[0]
            if command.startswith("lslocks"): return '{"locks":[{"path":"' + probe + '"}]}'
            return ""
        qemu_calls = 0
        def shell_result(host, command):
            nonlocal qemu_calls
            if command.startswith("test -f"): return SimpleNamespace(returncode=0, stdout="", stderr="")
            qemu_calls += 1
            return (SimpleNamespace(returncode=1, stdout="", stderr="write lock refused") if qemu_calls == 1 else
                    SimpleNamespace(returncode=0, stdout="released", stderr=""))
        workloads.shell, workloads.shell_result = shell, shell_result
        try:
            evidence = workloads.competing_writer_probe([first, second], "nfs")
            self.assertEqual(evidence["holder_pid"], "1234")
            self.assertEqual(evidence["qemu_identity"], {"uid": "64055", "gid": "992", "groups": ["992", "64055"]})
            self.assertLess(next(i for i, call in enumerate(calls) if call.startswith("cat ")),
                            next(i for i, call in enumerate(calls) if "kill 1234;" in call))
            stop = next(call for call in calls if "kill 1234;" in call)
            self.assertIn("readlink -f /proc/1234/exe", stop)
            self.assertIn("/proc/1234/cmdline | grep -Fqx -- file=" + probe, stop)
            self.assertIn("while test -d /proc/1234", stop)
            self.assertNotIn("-o comm=", stop)
            holder = next(call for call in calls if "qemu-system-x86_64" in call)
            self.assertIn("--reuid=64055 --regid=992 --groups=992,64055", holder)
            self.assertNotIn("--clear-groups", holder)
            self.assertIn("-pidfile /run/nex-lock-probe.a1B2c3/qemu.pid", holder)
            self.assertNotIn(probe + ".qemu.pid", holder)
            self.assertTrue(any("rmdir /run/nex-lock-probe.a1B2c3" in call for call in calls))
        finally:
            workloads.shell, workloads.shell_result = original_shell, original_result

    def test_competing_qemu_probe_rejects_nonprivate_runtime_path(self):
        guests = [{"host": {"id": "one", "storage_path": "/mnt/shared"}, "disk": "/mnt/shared/vm1"},
                  {"host": {"id": "two", "storage_path": "/mnt/shared"}, "disk": "/mnt/shared/vm2"}]
        original = workloads.shell
        workloads.shell = lambda host, command, check=True: "64055\n992\n992 64055\n" if command.startswith("id -u libvirt-qemu") else "/tmp/nex-lock-probe.bad\n"
        try:
            with self.assertRaisesRegex(RuntimeError, "private host-local"):
                workloads.competing_writer_probe(guests, "nfs")
        finally:
            workloads.shell = original

    def test_competing_qemu_probe_refuses_mismatched_qemu_identity(self):
        guests = [{"host": {"id": "one", "storage_path": "/mnt/shared"}, "disk": "/mnt/shared/vm1"},
                  {"host": {"id": "two", "storage_path": "/mnt/shared"}, "disk": "/mnt/shared/vm2"}]
        original = workloads.shell
        workloads.shell = lambda host, command, check=True: ("64055\n992\n992 64055\n" if host["id"] == "one" else "64055\n992\n992\n")
        try:
            with self.assertRaisesRegex(RuntimeError, "consistent libvirt-qemu identity"):
                workloads.competing_writer_probe(guests, "iscsi_gfs2")
        finally:
            workloads.shell = original

    def test_competing_qemu_probe_preserves_failed_artifacts_and_stops_known_holder(self):
        guests = [{"host": {"id": "one", "storage_path": "/mnt/shared"}, "disk": "/mnt/shared/vm1"},
                  {"host": {"id": "two", "storage_path": "/mnt/shared"}, "disk": "/mnt/shared/vm2"}]
        for failure in ("launch", "release", "invalid-pid"):
            with self.subTest(failure=failure):
                original, original_result = workloads.shell, workloads.shell_result
                calls, probe = [], ""
                def shell(host, command, check=True):
                    nonlocal probe
                    calls.append(command)
                    if command.startswith("id -u libvirt-qemu"): return "64055\n992\n992 64055\n"
                    if "mktemp -d" in command: return "/run/nex-lock-probe.a1B2c3\n"
                    if command.startswith("cat "): return "invalid\n" if failure == "invalid-pid" else "1234\n"
                    if "qemu-img create" in command:
                        probe = command.split("qemu-img create -f qcow2 ", 1)[1].split(" 64M", 1)[0]
                        if failure == "launch": raise RuntimeError("launch lost after daemonization")
                    if command.startswith("lslocks"): return json.dumps({"locks": [{"path": probe}]})
                    return ""
                workloads.shell = shell
                workloads.shell_result = lambda host, command: SimpleNamespace(
                    returncode=0 if command.startswith("test -f") else 1, stdout="", stderr="write lock refused")
                try:
                    with self.assertRaises(RuntimeError):
                        workloads.competing_writer_probe(guests, "iscsi_gfs2")
                    self.assertFalse(any("rm -f" in call or "rmdir" in call for call in calls))
                    stops = [call for call in calls if "kill 1234;" in call]
                    self.assertEqual(len(stops), 0 if failure == "invalid-pid" else 1)
                    if stops: self.assertIn("grep -Fqx -- file=" + probe, stops[0])
                finally:
                    workloads.shell, workloads.shell_result = original, original_result

    def test_competing_qemu_probe_does_not_signal_a_different_live_process(self):
        guests = [{"host": {"id": "one", "storage_path": "/mnt/shared"}, "disk": "/mnt/shared/vm1"},
                  {"host": {"id": "two", "storage_path": "/mnt/shared"}, "disk": "/mnt/shared/vm2"}]
        original, original_result = workloads.shell, workloads.shell_result
        child = subprocess.Popen(["sleep", "30"])
        probe = ""
        def shell(host, command, check=True):
            nonlocal probe
            if command.startswith("id -u libvirt-qemu"): return "64055\n992\n992 64055\n"
            if "mktemp -d" in command: return "/run/nex-lock-probe.a1B2c3\n"
            if command.startswith("cat "): return str(child.pid)
            if "qemu-img create" in command:
                probe = command.split("qemu-img create -f qcow2 ", 1)[1].split(" 64M", 1)[0]
            if command.startswith("lslocks"): return json.dumps({"locks": [{"path": probe}]})
            if "readlink -f /proc/" in command:
                result = subprocess.run(["sh", "-c", command], capture_output=True, text=True, timeout=5)
                if result.returncode: raise RuntimeError("holder identity did not match")
            return ""
        workloads.shell = shell
        workloads.shell_result = lambda host, command: SimpleNamespace(
            returncode=0 if command.startswith("test -f") else 1, stdout="", stderr="write lock refused")
        try:
            with self.assertRaisesRegex(RuntimeError, "holder identity"):
                workloads.competing_writer_probe(guests, "iscsi_gfs2")
            self.assertIsNone(child.poll())
        finally:
            workloads.shell, workloads.shell_result = original, original_result
            child.terminate()
            child.wait(timeout=5)

    def test_public_fence_runner_refuses_master_target(self):
        membership = {"master_host_id": "master", "members": [{"host_id": "master", "status": "ready"}, {"host_id": "member", "status": "ready"}]}
        hosts = [{"id": "master", "pool_id": "pool-1", "inventory": {"managed": {"membership": membership}}},
                 {"id": "member", "pool_id": "pool-1", "inventory": {"managed": {"membership": membership}}}]
        workloads.require_member_fence_target(hosts, "pool-1", "member")
        with self.assertRaises(RuntimeError):
            workloads.require_member_fence_target(hosts, "pool-1", "master")
        with self.assertRaises(RuntimeError):
            workloads.require_member_fence_target(hosts, "pool-1", "ejected")

    def test_missing_slots_rejects_two_vm_ids_for_one_host_ordinal(self):
        host = {"id": "host-1"}
        with self.assertRaises(RuntimeError):
            workloads.missing_slots([{"host": host, "ordinal": 1}, {"host": host, "ordinal": 1}], [host])
        hosts = [host, {"id": "host-2"}, {"id": "host-3"}]
        self.assertEqual(len(workloads.missing_slots([{"host": host, "ordinal": 1}], hosts)), 5)

    def test_run_backend_partial_creates_only_missing_slots_and_never_recopies_retained(self):
        ids = self.ids()
        with tempfile.TemporaryDirectory() as directory:
            ids["lab"] = Path(directory)
            resume = ids["lab"] / "resume.json"; resume.write_text("{}")
            retained = {"host": {**ids["hosts"][0], "lab": ids["lab"], "storage_path": "/mnt/nfs"}, "vm_id": "retained", "ordinal": 1, "disk": "/mnt/nfs/retained"}
            calls, image_copies, original = [], [], {name: getattr(workloads, name) for name in ("guest_image_info", "resume_guests", "fixture_capacity", "install_guest_image", "power", "shell", "wait_repository_host_ready")}
            class API:
                def request(self, method, path, body=None):
                    if method == "GET": return []
                    if method == "POST": calls.append(body); return {"operation": {"id": "op"}, "vm_id": "new-" + str(len(calls))}
                def operation(self, operation_id): return {}
            workloads.guest_image_info = lambda host, image: {"virtual-size": 3, "actual-size": 1}
            workloads.resume_guests = lambda *args: [retained]
            workloads.fixture_capacity = lambda *args: None
            workloads.install_guest_image = lambda host, vm, image, size: (image_copies.append(vm) or "/mnt/nfs/" + vm)
            workloads.shell = lambda *args, **kwargs: json.dumps({"actual-size": 1})
            workloads.wait_repository_host_ready = lambda *args: None
            workloads.power = lambda *args: (_ for _ in ()).throw(RuntimeError("stop after provision"))
            try:
                with self.assertRaisesRegex(RuntimeError, "stop after provision"):
                    workloads.run_backend(API(), ids, "nfs", resume=resume)
                self.assertEqual(len(calls), 5)
                self.assertNotIn("retained", [body["name"] for body in calls])
                self.assertEqual(image_copies, [f"new-{number}" for number in range(1, 6)])
            finally:
                for name, value in original.items(): setattr(workloads, name, value)

    def test_run_backend_preflight_failures_issue_no_post(self):
        ids = self.ids(); ids["lab"] = Path("/tmp/lab")
        calls, original = [], {name: getattr(workloads, name) for name in ("guest_image_info", "fixture_capacity")}
        class API:
            def request(self, method, path, body=None):
                if method == "POST": calls.append(body)
                return [{"name": "cluster-nfs-core1-1", "id": "orphan"}] if method == "GET" else {}
        workloads.guest_image_info = lambda host, image: {"virtual-size": 3, "actual-size": 1}
        try:
            workloads.fixture_capacity = lambda *args: (_ for _ in ()).throw(RuntimeError("capacity"))
            with self.assertRaisesRegex(RuntimeError, "capacity"):
                workloads.run_backend(API(), ids, "nfs")
            self.assertEqual(calls, [])
            workloads.fixture_capacity = lambda *args: None
            with self.assertRaisesRegex(RuntimeError, "planned VM name"):
                workloads.run_backend(API(), ids, "nfs")
            self.assertEqual(calls, [])
        finally:
            for name, value in original.items(): setattr(workloads, name, value)

    def test_run_backend_partial_preflight_failures_issue_no_post(self):
        ids = self.ids()
        with tempfile.TemporaryDirectory() as directory:
            ids["lab"] = Path(directory)
            resume = ids["lab"] / "resume.json"; resume.write_text("{}")
            retained = {"host": {**ids["hosts"][0], "lab": ids["lab"], "storage_path": "/mnt/nfs"},
                        "vm_id": "retained", "ordinal": 1, "disk": "/mnt/nfs/retained"}
            calls, original = [], {name: getattr(workloads, name) for name in
                                   ("guest_image_info", "resume_guests", "fixture_capacity", "shell")}
            class API:
                def request(self, method, path, body=None):
                    if method == "POST": calls.append(body)
                    if method == "GET": return [{"name": "cluster-nfs-core1-2", "id": "orphan"}]
                    return {}
            workloads.guest_image_info = lambda host, image: {"virtual-size": 3, "actual-size": 1}
            workloads.resume_guests = lambda *args: [retained]
            workloads.shell = lambda *args, **kwargs: json.dumps({"actual-size": 1})
            try:
                workloads.fixture_capacity = lambda *args: (_ for _ in ()).throw(RuntimeError("capacity"))
                with self.assertRaisesRegex(RuntimeError, "capacity"):
                    workloads.run_backend(API(), ids, "nfs", resume=resume)
                self.assertEqual(calls, [])
                workloads.fixture_capacity = lambda *args: None
                with self.assertRaisesRegex(RuntimeError, "planned VM name"):
                    workloads.run_backend(API(), ids, "nfs", resume=resume)
                self.assertEqual(calls, [])
            finally:
                for name, value in original.items(): setattr(workloads, name, value)

    def test_run_backend_waits_for_fresh_admission_before_its_first_post(self):
        ids = self.ids(); ids["lab"] = Path("/tmp/lab")
        calls, original = [], {name: getattr(workloads, name) for name in
                               ("guest_image_info", "fixture_capacity", "install_guest_image", "stop_guests", "time")}
        fresh = workloads.datetime.now().astimezone().isoformat()
        def storage(observed):
            return {"id": "nfs-storage", "host_id": "host-1", "status": "succeeded", "generation": 1,
                    "identity": {"nfs": {"fixture": "same"}}, "capabilities": {"vm_disk": True},
                    "access": {"status": "healthy", "readable": True, "writable": True, "observed_at": observed}}
        class API:
            storage_reads = 0
            def request(self, method, path, body=None):
                if path == "/api/v1/storage":
                    self.storage_reads += 1
                    return [storage("1970-01-01T00:00:00Z" if self.storage_reads == 1 else fresh)]
                if method == "GET": return []
                calls.append(body); return {"operation": {"id": "op"}, "vm_id": "created"}
            def operation(self, operation_id): return {}
        workloads.guest_image_info = lambda *args: {"virtual-size": 3, "actual-size": 1}
        workloads.fixture_capacity = lambda *args: None
        workloads.install_guest_image = lambda *args: (_ for _ in ()).throw(RuntimeError("halt after first image"))
        workloads.stop_guests = lambda *args: None
        original_sleep = workloads.time.sleep
        workloads.time.sleep = lambda *args: None
        try:
            with self.assertRaisesRegex(RuntimeError, "halt after first image"):
                workloads.run_backend(API(), ids, "nfs")
            self.assertEqual(len(calls), 1)
        finally:
            workloads.time.sleep = original_sleep
            for name, value in original.items():
                if name != "time": setattr(workloads, name, value)

    def test_run_backend_admission_timeout_issues_zero_post(self):
        ids = self.ids(); ids["lab"] = Path("/tmp/lab")
        calls, original = [], {name: getattr(workloads, name) for name in
                               ("guest_image_info", "fixture_capacity", "time")}
        class API:
            def request(self, method, path, body=None):
                if method == "POST": calls.append(body)
                if path == "/api/v1/storage":
                    return [{"id": "nfs-storage", "host_id": "host-1", "status": "succeeded",
                             "capabilities": {"vm_disk": True},
                             "access": {"status": "healthy", "readable": True, "writable": True,
                                        "observed_at": "1970-01-01T00:00:00Z"}}]
                return []
        workloads.guest_image_info = lambda *args: {"virtual-size": 3, "actual-size": 1}
        workloads.fixture_capacity = lambda *args: None
        original_monotonic, original_sleep = workloads.time.monotonic, workloads.time.sleep
        ticks = iter((0, 121))
        workloads.time.monotonic, workloads.time.sleep = lambda: next(ticks), lambda *args: None
        try:
            with self.assertRaisesRegex(RuntimeError, "not currently admitted"):
                workloads.run_backend(API(), ids, "nfs")
            self.assertEqual(calls, [])
        finally:
            workloads.time.monotonic, workloads.time.sleep = original_monotonic, original_sleep
            for name, value in original.items():
                if name != "time": setattr(workloads, name, value)

    def test_run_backend_repeat_resume_six_retained_replacement_has_zero_post_or_copy(self):
        ids = self.ids()
        with tempfile.TemporaryDirectory() as directory:
            ids["lab"] = Path(directory); resume = ids["lab"] / "resume.json"; resume.write_text("{}")
            guests = [{"host": {**host, "lab": ids["lab"], "storage_path": "/mnt/nfs"}, "vm_id": f"retained-{number}",
                       "ordinal": ordinal, "name": (f"cluster-nfs-{host['lab_name']}-{ordinal}-replacement-deadbeef" if number == 5 else f"cluster-nfs-{host['lab_name']}-{ordinal}"),
                       "disk": f"/mnt/nfs/retained-{number}"}
                      for number, (host, ordinal) in enumerate(((host, ordinal) for host in ids["hosts"] for ordinal in (1, 2)), 1)]
            calls, copies = [], []
            original = {name: getattr(workloads, name) for name in ("guest_image_info", "resume_guests", "fixture_capacity", "shell", "wait_repository_host_ready", "install_guest_image", "power", "stop_guests")}
            class API:
                def request(self, method, path, body=None):
                    if method == "POST": calls.append(body)
                    return []
            workloads.guest_image_info = lambda *args: {"virtual-size": 3, "actual-size": 1}
            workloads.resume_guests = lambda *args: guests
            workloads.fixture_capacity = lambda *args: None
            workloads.shell = lambda *args, **kwargs: json.dumps({"actual-size": 1})
            workloads.wait_repository_host_ready = lambda *args: None
            workloads.install_guest_image = lambda *args: copies.append(args[1])
            workloads.power = lambda *args: (_ for _ in ()).throw(RuntimeError("stop after retained resume"))
            workloads.stop_guests = lambda *args: None
            try:
                with self.assertRaisesRegex(RuntimeError, "retained resume"):
                    workloads.run_backend(API(), ids, "nfs", resume=resume)
                self.assertEqual(calls, [])
                self.assertEqual(copies, [])
            finally:
                for name, value in original.items(): setattr(workloads, name, value)

    def test_resume_guests_binds_persisted_replacement_name_to_its_exclusion(self):
        ids = self.ids(); ids["lab"] = Path("/tmp/lab")
        host, vm_id, failed = ids["hosts"][0], "retained", "deadbeef-0000-4000-a000-000000000000"
        canonical, replacement = "cluster-nfs-core1-1", "cluster-nfs-core1-1-replacement-deadbeef"
        saved = {"backend": "nfs", "storage_id": "nfs-storage",
                 "failed_create_exclusions": [{"host_id": host["id"], "ordinal": 1, "vm_id": failed}],
                 "vms": [{"vm_id": vm_id, "host_id": host["id"], "lab_name": "core1", "network_id": "net-1",
                          "disk": "/mnt/nfs/disk", "ordinal": 1, "name": replacement}]}
        class API:
            def request(self, method, path):
                return [{"id": vm_id, "host_id": host["id"], "name": replacement, "status": "succeeded", "state": "shut off"}]
        original = workloads.shell
        workloads.shell = lambda host, command: "/mnt/nfs/disk\n" if "domblklist" in command else json.dumps({"virtual-size": 3})
        try:
            with tempfile.TemporaryDirectory() as directory:
                checkpoint = Path(directory) / "resume.json"; checkpoint.write_text(json.dumps(saved))
                self.assertEqual(workloads.resume_guests(API(), ids, "nfs", checkpoint, 3)[0]["name"], replacement)
                saved["vms"][0]["name"] = canonical + "-replacement-wrong"
                checkpoint.write_text(json.dumps(saved))
                with self.assertRaises(RuntimeError):
                    workloads.resume_guests(API(), ids, "nfs", checkpoint, 3)
        finally:
            workloads.shell = original

    def test_gfs2_admission_requires_exact_fresh_member_evidence(self):
        observed = workloads.datetime.now().astimezone().isoformat()
        identity = {"iscsi": {"wwid": "fixture"}}
        storage = {"id": "store", "host_id": "master", "status": "succeeded", "generation": 7,
                   "identity": identity, "capabilities": {"vm_disk": True, "eligible_host_ids": ["member"]},
                   "access": {"status": "healthy", "readable": True, "writable": True, "observed_at": observed},
                   "member_access": [{"repository_id": "store", "host_id": "member", "generation": 7,
                                      "identity": json.dumps(identity), "readable": True, "writable": True,
                                      "qemu_writable": True, "locking": True, "observed_at": observed}]}
        self.assertTrue(workloads.repository_host_ready(storage, "store", "member", "iscsi_gfs2"))
        storage["member_access"][0]["generation"] = 6
        self.assertFalse(workloads.repository_host_ready(storage, "store", "member", "iscsi_gfs2"))

    def test_smb_admission_uses_durable_marker_not_client_filesystem_id(self):
        observed = workloads.datetime.now().astimezone().isoformat()
        identity = {"smb": {"server": "files.example.test", "share": "vms", "marker_id": "marker-a",
                            "filesystem_id": "client-one"}}
        storage = {"id": "store", "host_id": "master", "status": "succeeded", "generation": 7,
                   "identity": identity, "capabilities": {"vm_disk": True},
                   "access": {"status": "healthy", "readable": True, "writable": True, "observed_at": observed},
                   "member_access": [{"repository_id": "store", "host_id": "member", "generation": 7,
                                      "identity": json.dumps({"smb": {**identity["smb"], "filesystem_id": "client-two"}}),
                                      "readable": True, "writable": True, "qemu_writable": True, "locking": True,
                                      "observed_at": observed}]}
        self.assertTrue(workloads.repository_host_ready(storage, "store", "member", "smb"))
        for field, value in (("marker_id", "marker-b"), ("server", "other.example.test"), ("share", "other")):
            with self.subTest(field=field):
                member = json.loads(storage["member_access"][0]["identity"])
                member["smb"][field] = value
                storage["member_access"][0]["identity"] = json.dumps(member)
                self.assertFalse(workloads.repository_host_ready(storage, "store", "member", "smb"))
                storage["member_access"][0]["identity"] = json.dumps({"smb": {**identity["smb"], "filesystem_id": "client-two"}})
        legacy = {"smb": {"server": "files.example.test", "share": "vms", "filesystem_id": "legacy"}}
        self.assertTrue(workloads.storage_identity_matches(legacy, legacy))
        self.assertFalse(workloads.storage_identity_matches(legacy, {"smb": {**legacy["smb"], "filesystem_id": "different"}}))
        self.assertFalse(workloads.storage_identity_matches({}, {}))
        for field in ("server", "share"):
            with self.subTest(blank_field=field):
                invalid = {"smb": {**identity["smb"], field: ""}}
                self.assertFalse(workloads.storage_identity_matches(invalid, invalid))
        self.assertFalse(workloads.storage_identity_matches(
            {"smb": {"server": "files.example.test", "share": "vms", "filesystem_id": ""}},
            {"smb": {"server": "files.example.test", "share": "vms", "filesystem_id": ""}}))

    def test_failed_create_exclusion_requires_terminal_absent_exact_slot(self):
        ids = self.ids(); ids["lab"] = Path("/tmp/lab")
        hosts = [{**host, "lab": ids["lab"], "storage_path": "/mnt/nfs"} for host in ids["hosts"]]
        host, vm_id, operation_id = hosts[2], "11111111-1111-4111-a111-111111111111", "operation"
        name, request = "cluster-iscsi_gfs2-core3-2", "cluster-iscsi_gfs2-core3-2-request"
        exclusion = {"backend": "iscsi_gfs2", "vm_id": vm_id, "host_id": host["id"], "ordinal": 2, "name": name,
                     "request_id": request, "operation_id": operation_id}
        observed = [{"id": vm_id, "host_id": host["id"], "name": name, "status": "failed", "state": "unknown",
                     "operation_id": operation_id}]
        class API:
            status = "failed"
            fresh = True
            def request(self, method, path):
                if path.endswith("operations"): return [{"id": operation_id, "kind": "vm-create", "host_id": host["id"], "status": self.status, "request_id": request}]
                if path.endswith("storage"): return [{"id": "iscsi_gfs2-storage", "identity": {"iscsi": {"filesystem_uuid": "fixture-uuid"}}}]
                return [{"id": item["id"], "connection": "fresh" if self.fresh else "stale"} for item in hosts]
        with tempfile.TemporaryDirectory() as directory:
            checkpoint = Path(directory) / "resume.json"
            checkpoint.write_text(json.dumps({"failed_create_exclusions": [exclusion]}))
            original = workloads.shell_result
            workloads.shell_result = lambda host, command: (SimpleNamespace(returncode=1, stderr="failed to get domain", stdout="")
                                                             if "domstate" in command else
                                                             SimpleNamespace(returncode=0, stderr="", stdout=f"{host['storage_path']} gfs2 fixture-uuid\n")
                                                             if "findmnt" in command else SimpleNamespace(returncode=0, stderr="", stdout=""))
            try:
                valid_shell = workloads.shell_result
                result = workloads.failed_create_exclusions(API(), ids, "iscsi_gfs2", checkpoint, hosts, observed, [(host, 2)])
                self.assertEqual(result[(host["id"], 2)]["vm_id"], vm_id)
                class AdoptedAPI(API):
                    request_id = "adopt-outcome:" + host["id"] + ":" + operation_id
                    def request(self, method, path):
                        if path.endswith("operations"):
                            return [{"id": operation_id, "kind": "vm-create", "host_id": host["id"], "status": self.status, "request_id": self.request_id}]
                        return super().request(method, path)
                adopted = AdoptedAPI()
                self.assertEqual(workloads.failed_create_exclusions(adopted, ids, "iscsi_gfs2", checkpoint, hosts, [], [(host, 2)])[(host["id"], 2)]["vm_id"], vm_id)
                adopted.request_id = "adopt-outcome:wrong:" + operation_id
                with self.assertRaisesRegex(RuntimeError, "terminal"):
                    workloads.failed_create_exclusions(adopted, ids, "iscsi_gfs2", checkpoint, hosts, [], [(host, 2)])
                adopted.request_id, adopted.status = "adopt-outcome:" + host["id"] + ":" + operation_id, "succeeded"
                with self.assertRaisesRegex(RuntimeError, "terminal"):
                    workloads.failed_create_exclusions(adopted, ids, "iscsi_gfs2", checkpoint, hosts, [], [(host, 2)])
                adopted.status = "failed"
                workloads.shell_result = lambda candidate, command: (SimpleNamespace(returncode=0, stderr="", stdout="running\n")
                                                                      if "domstate" in command else valid_shell(candidate, command))
                with self.assertRaisesRegex(RuntimeError, "native execution"):
                    workloads.failed_create_exclusions(adopted, ids, "iscsi_gfs2", checkpoint, hosts, [], [(host, 2)])
                workloads.shell_result = valid_shell
                class MissingOperationAPI(AdoptedAPI):
                    def request(self, method, path):
                        if path.endswith("operations"): return []
                        return super().request(method, path)
                with self.assertRaisesRegex(RuntimeError, "terminal"):
                    workloads.failed_create_exclusions(MissingOperationAPI(), ids, "iscsi_gfs2", checkpoint, hosts, [], [(host, 2)])
                bad_host = {**exclusion, "host_id": None}
                checkpoint.write_text(json.dumps({"failed_create_exclusions": [bad_host]}))
                with self.assertRaisesRegex(RuntimeError, "terminal"):
                    workloads.failed_create_exclusions(adopted, ids, "iscsi_gfs2", checkpoint, hosts, [], [(host, 2)])
                checkpoint.write_text(json.dumps({"failed_create_exclusions": [exclusion]}))
                # A later resume still validates the preserved exclusion even
                # when its replacement has filled the physical slot.
                self.assertEqual(workloads.failed_create_exclusions(API(), ids, "iscsi_gfs2", checkpoint, hosts, observed,
                                 [(candidate, ordinal) for candidate in hosts for ordinal in (1, 2)])[(host["id"], 2)]["vm_id"], vm_id)
                API.fresh = False
                with self.assertRaisesRegex(RuntimeError, "terminal"):
                    workloads.failed_create_exclusions(API(), ids, "iscsi_gfs2", checkpoint, hosts, observed, [(host, 2)])
                API.fresh = True
                observed[0]["state"] = "running"
                with self.assertRaisesRegex(RuntimeError, "terminal"):
                    workloads.failed_create_exclusions(API(), ids, "iscsi_gfs2", checkpoint, hosts, observed, [(host, 2)])
                observed[0]["state"] = "unknown"
                workloads.shell_result = lambda candidate, command: (SimpleNamespace(returncode=0, stderr="", stdout="running\n")
                                                                      if "domstate" in command else valid_shell(candidate, command))
                with self.assertRaisesRegex(RuntimeError, "native execution"):
                    workloads.failed_create_exclusions(API(), ids, "iscsi_gfs2", checkpoint, hosts, observed, [(host, 2)])
                workloads.shell_result = lambda candidate, command: (SimpleNamespace(returncode=1, stderr="connection refused", stdout="")
                                                                      if candidate["id"] == hosts[0]["id"] and "domstate" in command else valid_shell(candidate, command))
                with self.assertRaisesRegex(RuntimeError, "inaccessible Core"):
                    workloads.failed_create_exclusions(API(), ids, "iscsi_gfs2", checkpoint, hosts, observed, [(host, 2)])
                workloads.shell_result = lambda candidate, command: (SimpleNamespace(returncode=1, stderr="failed to get domain", stdout="")
                                                                      if "domstate" in command else
                                                                      SimpleNamespace(returncode=0, stderr="", stdout=f"{candidate['storage_path']} gfs2 wrong-uuid\n")
                                                                      if "findmnt" in command else SimpleNamespace(returncode=0, stderr="", stdout=""))
                with self.assertRaisesRegex(RuntimeError, "trusted mounted"):
                    workloads.failed_create_exclusions(API(), ids, "iscsi_gfs2", checkpoint, hosts, observed, [(host, 2)])
                workloads.shell_result = lambda candidate, command: (SimpleNamespace(returncode=1, stderr="failed to get domain", stdout="")
                                                                      if "domstate" in command else
                                                                      SimpleNamespace(returncode=1, stderr="", stdout="")
                                                                      if "test !" in command else
                                                                      SimpleNamespace(returncode=0, stderr="", stdout=f"{candidate['storage_path']} gfs2 fixture-uuid\n"))
                with self.assertRaisesRegex(RuntimeError, "assigned disk"):
                    workloads.failed_create_exclusions(API(), ids, "iscsi_gfs2", checkpoint, hosts, observed, [(host, 2)])
                API.status = "queued"
                with self.assertRaisesRegex(RuntimeError, "terminal"):
                    workloads.failed_create_exclusions(API(), ids, "iscsi_gfs2", checkpoint, hosts, observed, [(host, 2)])
            finally:
                workloads.shell_result = original

    def test_cluster_health_reads_actual_operation_member_shape(self):
        reply = {"status": "accepted", "operations": [{"status": "healthy", "config": {"repository_id": "iscsi-storage"},
                 "members": [{"host_id": "host", "stage": "ready", "status": {"status": "healthy", "healthy": True, "quorate": True, "mounted": True, "fencing_verified": True}}] * 3}]}
        self.assertEqual(workloads.healthy_cluster(reply, "iscsi-storage")["status"], "healthy")
        reply["operations"][0]["members"][2]["status"]["mounted"] = False
        with self.assertRaises(RuntimeError):
            workloads.healthy_cluster(reply, "iscsi-storage")
        reply["operations"][0]["members"][2]["status"]["mounted"] = True
        reply["operations"][0]["members"][0] = {"healthy": True, "quorate": True, "mounted": True, "fencing_verified": True}
        with self.assertRaises(RuntimeError):
            workloads.healthy_cluster(reply, "iscsi-storage")

    def test_write_windows_require_real_overlap(self):
        self.assertEqual(len(workloads.windows(["ok\n10.0 12.0", "ok\n11.0 13.0"])), 2)
        with self.assertRaises(RuntimeError):
            workloads.windows(["10.0 11.0", "11.0 12.0"])

    def test_refuses_duplicated_guest_identity(self):
        workloads.require_distinct(["boot-a", "boot-b"], "guest boot IDs")
        with self.assertRaises(RuntimeError):
            workloads.require_distinct(["boot-a", "boot-a"], "guest boot IDs")

    def test_fence_proof_must_overlap_each_survivor_write(self):
        outputs = ["10.0 20.0", "11.0 19.0"]
        self.assertEqual(len(workloads.proof_overlaps_survivors("1970-01-01T00:00:12Z", outputs)), 2)
        with self.assertRaises(RuntimeError):
            workloads.proof_overlaps_survivors("1970-01-01T00:00:20Z", outputs)

    def test_fence_refuses_target_write_that_failed_before_dispatch(self):
        pending = Future()
        workloads.require_pending_target_writes([pending])
        failed = Future()
        failed.set_exception(RuntimeError("guest SSH failed before fence"))
        with self.assertRaises(RuntimeError):
            workloads.require_pending_target_writes([failed])

    def test_fence_stale_interruption_marker_times_out_without_fresh_replacement(self):
        original_guest, original_clock, original_sleep = workloads.guest, workloads.time.monotonic, workloads.time.sleep
        ticks = iter((0, 0, 11))
        workloads.guest, workloads.time.monotonic, workloads.time.sleep = (lambda *_: "9.0"), (lambda: next(ticks)), (lambda _: None)
        try:
            with self.assertRaisesRegex(RuntimeError, "not durably observed"):
                workloads.poll_interruption_markers([{"host": {}, "vm_id": "vm-1"}], "iscsi_gfs2", {}, 10, timeout=10)
        finally:
            workloads.guest, workloads.time.monotonic, workloads.time.sleep = original_guest, original_clock, original_sleep

    def test_fenced_outer_return_is_bound_to_the_profile_uuid(self):
        with tempfile.TemporaryDirectory() as directory:
            lab = Path(directory)
            (lab / "fence-profile.json").write_text(json.dumps({"disposable": True, "container": "nex-cluster-ha-libvirt-example",
                "domains": {"core2": "123e4567-e89b-12d3-a456-426614174000"}}))
            self.assertEqual(workloads.fenced_outer_domain(lab, "core2"),
                             ("nex-cluster-ha-libvirt-example", "123e4567-e89b-12d3-a456-426614174000"))
            with self.assertRaises(RuntimeError):
                workloads.fenced_outer_domain(lab, "core1")

    def test_existing_survivor_admission_requires_fresh_unchanged_identity(self):
        ids = self.ids()
        item = {"vm_id": "vm-1", "name": "cluster-iscsi_gfs2-core1-1", "checksum": "/root/checksum",
                "host": {"id": "host-1", "lab_name": "core1"}}
        record = {"id": "vm-1", "pool_id": "pool-1", "host_id": "host-1", "name": item["name"],
                  "status": "succeeded", "state": "running", "freshness": "fresh", "spec": {"disk": "one", "nics": []}}
        item["canonical_spec"] = workloads.canonical_vm_spec(record["spec"])
        class API:
            def request(self, method, path): return [record]
        self.assertEqual(workloads.existing_vm_record(API(), ids, item, "running")["id"], "vm-1")
        record["freshness"] = "stale"
        with self.assertRaisesRegex(RuntimeError, "fresh unchanged"):
            workloads.existing_vm_record(API(), ids, item, "running")
        record["freshness"], record["host_id"] = "fresh", "other-host"
        with self.assertRaisesRegex(RuntimeError, "fresh unchanged"):
            workloads.existing_vm_record(API(), ids, item, "running")
        record["host_id"], record["pool_id"] = "host-1", "other-pool"
        with self.assertRaisesRegex(RuntimeError, "fresh unchanged"):
            workloads.existing_vm_record(API(), ids, item, "running")
        record["pool_id"], record["spec"] = "pool-1", {"memory": {"size_mib": 999}, "nics": []}
        with self.assertRaisesRegex(RuntimeError, "fresh unchanged"):
            workloads.existing_vm_record(API(), ids, item, "running")

    def test_existing_survivor_admission_keeps_target_off_and_checks_checksum(self):
        ids = self.ids(); ids["lab"] = Path("/tmp/lab")
        target, survivor = ids["hosts"][2], ids["hosts"][0]
        item = {"vm_id": "vm-1", "name": "cluster-iscsi_gfs2-core1-1", "checksum": "/root/checksum", "host": survivor}
        record = {"id": "vm-1", "pool_id": "pool-1", "host_id": survivor["id"], "name": item["name"],
                  "status": "succeeded", "state": "running", "freshness": "fresh", "spec": {"disk": "one", "nics": []}}
        item["canonical_spec"] = workloads.canonical_vm_spec(record["spec"])
        class API:
            def request(self, method, path): return [record]
        originals = {name: getattr(workloads, name) for name in ("wait_repository_host_ready", "fenced_outer_is_off", "wait_for_guest", "power", "wait_for_vm_state")}
        calls, boots = [], iter(("before-boot", "after-boot"))
        try:
            workloads.wait_repository_host_ready = lambda *args: calls.append(("ready", args[2]))
            workloads.fenced_outer_is_off = lambda *args: calls.append(("off", args[1]))
            def guest_wait(host, vm_id, config, checksum="", previous_boot_id="", timeout=180):
                calls.append(("guest", checksum, previous_boot_id)); return next(boots)
            workloads.wait_for_guest = guest_wait
            def power(api, vm_id, action):
                calls.append(("power", action)); record["state"] = "shut off" if action == "shutdown" else "running"
            workloads.power = power
            workloads.wait_for_vm_state = lambda api, vm_id, state: record
            evidence = workloads.admit_existing_survivor(API(), ids, {"storage_id": "store"}, target, [item], {})
            self.assertEqual(evidence["boot_id_before"], "before-boot")
            self.assertEqual(evidence["boot_id_after"], "after-boot")
            self.assertEqual(calls[:2], [("ready", "host-1"), ("ready", "host-2")])
            self.assertEqual([entry for entry in calls if entry[0] == "power"], [("power", "shutdown"), ("power", "start")])
            self.assertEqual([entry for entry in calls if entry[0] == "guest"], [("guest", "/root/checksum", ""), ("guest", "/root/checksum", "before-boot")])
            self.assertEqual(len([entry for entry in calls if entry[0] == "off"]), 3)
        finally:
            for name, value in originals.items(): setattr(workloads, name, value)

    def test_existing_survivor_admission_refuses_target_not_off_before_power(self):
        ids = self.ids(); ids["lab"] = Path("/tmp/lab")
        item = {"vm_id": "vm-1", "name": "cluster-iscsi_gfs2-core1-1", "checksum": "/root/checksum", "host": ids["hosts"][0],
                "canonical_spec": workloads.canonical_vm_spec({"disk": "one", "nics": []})}
        class API: pass
        originals = workloads.wait_repository_host_ready, workloads.fenced_outer_is_off, workloads.power
        calls = []
        try:
            workloads.wait_repository_host_ready = lambda *args: None
            workloads.fenced_outer_is_off = lambda *args: (_ for _ in ()).throw(RuntimeError("fenced outer Core was not observed off"))
            workloads.power = lambda *args: calls.append(args)
            with self.assertRaisesRegex(RuntimeError, "not observed off"):
                workloads.admit_existing_survivor(API(), ids, {"storage_id": "store"}, ids["hosts"][2], [item], {})
            self.assertEqual(calls, [])
        finally:
            workloads.wait_repository_host_ready, workloads.fenced_outer_is_off, workloads.power = originals

    def test_guest_uses_the_domain_mac_lease_not_domifaddr(self):
        original = workloads.shell
        commands = []
        def shell(host, command, check=True):
            commands.append(command)
            if "domiflist" in command:
                return "vnet0 network default virtio 52:54:00:aa:bb:cc\n"
            if "net-dhcp-leases" in command:
                return ("2026-09-12 09:00:00 52:54:00:aa:bb:cc ipv4 192.168.122.10/24 - ff:cloned-duid\n"
                        "2026-09-12 10:00:00 52:54:00:aa:bb:cc ipv4 192.168.122.50/24 - 01:52:54:00:aa:bb:cc\n")
            return "ok"
        workloads.shell = shell
        try:
            self.assertEqual(workloads.guest({}, "vm-1", {"ssh_key": "/private/key"}, "true"), "ok")
            self.assertIn("@192.168.122.50", commands[-1])
        finally:
            workloads.shell = original

    def test_capacity_uses_six_virtual_sizes_not_sparse_allocation(self):
        class API:
            def request(self, method, path):
                return [{"id": "store", "access": {"available_bytes": 600 + workloads.CAPACITY_HEADROOM_BYTES - 1}}]
        with self.assertRaises(RuntimeError):
            workloads.fixture_capacity(API(), "store", [{"virtual-size": 100, "actual-size": 1}] * 3)
        workloads.fixture_capacity(API(), "store", [{"virtual-size": 100, "actual-size": 1}] * 3, already_reserved=1)

    def test_requires_exact_three_core_private_topology(self):
        ids = self.ids()
        workloads.validate(ids)
        ids["hosts"].pop()
        with self.assertRaises(ValueError):
            workloads.validate(ids)

    def test_requires_the_observed_network_for_each_core(self):
        ids = self.ids()
        ids["hosts"][1].pop("network_id")
        with self.assertRaises(ValueError):
            workloads.validate(ids)

    def test_requires_outer_core_headroom_for_two_guests(self):
        ids = self.ids()
        ids["outer_core_memory_mib"] = 3072
        with self.assertRaises(ValueError):
            workloads.validate(ids)

    def test_refuses_result_outside_lab(self):
        lab = Path("/tmp/lab")
        with self.assertRaises(ValueError):
            workloads.inside_lab(lab, Path("/tmp/elsewhere/result.json"))

    def test_resume_requires_exact_stopped_core_vm_mapping_and_disk(self):
        ids = self.ids()
        ids["lab"] = Path("/tmp/lab")
        guests = []
        observed = []
        for host in ids["hosts"]:
            for ordinal in range(2):
                vm_id = f"{host['id']}-{ordinal}"
                disk = f"/mnt/nfs/nex-vms/{vm_id}/disk-0.qcow2"
                guests.append({"host": {**host, "lab": ids["lab"], "storage_path": "/mnt/nfs"}, "vm_id": vm_id, "disk": disk})
                observed.append({"id": vm_id, "name": f"cluster-nfs-{host['lab_name']}-{ordinal + 1}", "host_id": host["id"], "status": "succeeded", "state": "shut off"})
        class API:
            def request(self, method, path):
                return observed
        original = workloads.shell
        workloads.shell = lambda host, command: (next(item["disk"] for item in guests if item["vm_id"] in command)
                                                 if "domblklist" in command else json.dumps({"virtual-size": 3}))
        try:
            with tempfile.TemporaryDirectory() as directory:
                checkpoint = Path(directory) / "resume.json"
                workloads.save_resume(checkpoint, "nfs", "nfs-storage", guests)
                self.assertEqual(len(workloads.resume_guests(API(), ids, "nfs", checkpoint, 3)), 6)
                saved = json.loads(checkpoint.read_text())
                saved["vms"] = saved["vms"][:1]
                checkpoint.write_text(json.dumps(saved))
                self.assertEqual(len(workloads.resume_guests(API(), ids, "nfs", checkpoint, 3)), 1)
                saved["vms"] = [saved["vms"][0]] * 3
                checkpoint.write_text(json.dumps(saved))
                with self.assertRaises(RuntimeError):
                    workloads.resume_guests(API(), ids, "nfs", checkpoint, 3)
                saved = json.loads(checkpoint.read_text())
                saved["vms"][0]["network_id"] = "wrong-network"
                checkpoint.write_text(json.dumps(saved))
                with self.assertRaises(RuntimeError):
                    workloads.resume_guests(API(), ids, "nfs", checkpoint, 3)
        finally:
            workloads.shell = original

    def test_gfs_resume_persists_pool_and_canonical_spec(self):
        ids, host = self.ids(), self.ids()["hosts"][0]
        spec = workloads.expected_vm_spec(ids, "iscsi_gfs2", host, 1, 3, "cluster-iscsi_gfs2-core1-1")
        guest = {"host": host, "vm_id": "vm-1", "ordinal": 1, "name": "cluster-iscsi_gfs2-core1-1",
                 "disk": "/mnt/iscsi_gfs2/nex-vms/vm-1/disk-0.qcow2", "pool_id": "pool-1", "canonical_spec": spec}
        with tempfile.TemporaryDirectory() as directory:
            checkpoint = Path(directory) / "resume.json"
            workloads.save_resume(checkpoint, "iscsi_gfs2", "iscsi_gfs2-storage", [guest])
            saved = json.loads(checkpoint.read_text())["vms"][0]
        self.assertEqual(saved["pool_id"], "pool-1")
        self.assertEqual(saved["canonical_spec"], spec)

    def test_canonical_spec_treats_omitted_media_as_empty_but_rejects_media_change(self):
        ids, host = self.ids(), self.ids()["hosts"][0]
        expected = workloads.expected_vm_spec(ids, "iscsi_gfs2", host, 1, 3, "cluster-iscsi_gfs2-core1-1")
        actual = {**expected, "media_id": ""}
        self.assertEqual(workloads.canonical_vm_spec(actual), expected)
        self.assertNotEqual(workloads.canonical_vm_spec({**actual, "media_id": "fixture-media"}), expected)

    def test_stop_guests_refuses_unknown_or_stale_requested_vm_before_power(self):
        guests = [{"vm_id": "one"}, {"vm_id": "two"}]
        class API:
            def request(self, _method, _path):
                return [{"id": "one", "state": "running", "status": "succeeded", "freshness": "fresh"}]
        with self.assertRaisesRegex(RuntimeError, "fresh known"):
            workloads.stop_guests(API(), guests)

        class Paused:
            def request(self, _method, _path):
                return [{"id": "one", "state": "paused", "status": "succeeded", "freshness": "fresh"},
                        {"id": "two", "state": "shut off", "status": "succeeded", "freshness": "fresh"}]
        with self.assertRaisesRegex(RuntimeError, "fresh known"):
            workloads.stop_guests(Paused(), guests)

    def test_stop_guests_proves_all_already_off(self):
        guests = [{"vm_id": "one"}, {"vm_id": "two"}]
        class API:
            def request(self, _method, _path):
                return [{"id": vm, "state": "shut off", "status": "succeeded", "freshness": "fresh"} for vm in ("one", "two")]
        workloads.stop_guests(API(), guests)


if __name__ == "__main__":
    unittest.main()
