#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Run the six-guest shared-storage acceptance in the isolated cluster HA lab.

``ids.json`` is private lab output.  It supplies the enrolled pool, the three
host IDs, one already-registered repository per backend, a libvirt network,
and the minimal cloud guest image/key placed on each disposable Core.  This
runner creates every VM and changes every VM's power through Nex APIs.  The
one direct qemu-img conversion is only the disposable guest-image fixture:
it fills the stopped disk that Nex Core just created and assigned.

Example private input (paths and IDs are deliberately not public evidence)::

  {"controller": "https://127.0.0.1:44443", "password_file": "...",
   "pool_id": "...", "outer_core_memory_mib": 4096,
   "hosts": [{"id": "...", "lab_name": "core1", "network_id": "..."}, ...],
   "guest": {"image_path": "/opt/nex-lab/guest.qcow2",
             "ssh_key": "/root/.ssh/nex-guest", "user": "root"},
   "backends": {"iscsi_gfs2": {"storage_id": "...", "storage_path": "/mnt/nex-gfs2",
                                  "fence_host_id": "..."},
                "nfs": {"storage_id": "...", "storage_path": "/mnt/nex-nfs"},
                "smb": {"storage_id": "...", "storage_path": "/mnt/nex-smb"}}}
"""
import argparse
import base64
from concurrent.futures import ThreadPoolExecutor, TimeoutError as FutureTimeout
from datetime import datetime
import http.client
import json
from pathlib import Path
import re
import shlex
import ssl
import subprocess
import sys
import time
import urllib.parse
import uuid

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))
from cluster_ha_lab import CORE_NAMES, check_lab, ssh  # noqa: E402

BACKENDS = ("iscsi_gfs2", "nfs", "smb")
LOCK_PROBE_BACKENDS = BACKENDS
GUESTS_PER_HOST = 2
MEMORY_MIB = 512
CAPACITY_HEADROOM_BYTES = 4 * 1024**3
MASTER_AGENT_SERVICE = "nex-cluster-local.service"
WRITE_TIMEOUT = 600


class HTTPError(RuntimeError):
    """HTTP failure retaining the historical RuntimeError text plus status."""
    def __init__(self, status, message):
        super().__init__(message)
        self.status = status


class API:
    def __init__(self, url, password):
        parsed = urllib.parse.urlparse(url)
        if parsed.scheme != "https" or not parsed.hostname:
            raise ValueError("controller must be an https URL")
        self.url, self.host, self.port = url.rstrip("/"), parsed.hostname, parsed.port or 443
        self.context, self.cookie, self.csrf = ssl._create_unverified_context(), "", ""
        session = self.request("POST", "/api/v1/session", {"username": "root", "password": password}, auth=False)
        self.csrf = session["csrf_token"]

    def request(self, method, path, body=None, auth=True):
        headers = {"Origin": self.url}
        if auth and self.cookie:
            headers["Cookie"] = self.cookie
        if body is not None:
            headers.update({"Content-Type": "application/json", "X-CSRF-Token": self.csrf})
        connection = http.client.HTTPSConnection(self.host, self.port, context=self.context, timeout=60)
        connection.request(method, path, None if body is None else json.dumps(body), headers)
        response = connection.getresponse()
        raw = response.read()
        cookie = response.getheader("Set-Cookie")
        connection.close()
        data = json.loads(raw) if raw else {}
        if cookie:
            self.cookie = cookie.split(";", 1)[0]
        if not 200 <= response.status < 300:
            raise HTTPError(response.status, f"{method} {path}: {response.status} {data.get('error', data)}")
        return data

    def operation(self, operation_id, timeout=300):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            for operation in self.request("GET", "/api/v1/operations"):
                if operation["id"] == operation_id and operation["status"] not in ("queued", "dispatched", "running"):
                    if operation["status"] != "succeeded":
                        raise RuntimeError(f"operation {operation_id} {operation['status']}: {operation.get('error', '')}")
                    return operation
            time.sleep(1)
        raise RuntimeError(f"operation {operation_id} did not settle")


def inside_lab(lab, path):
    resolved = path.resolve()
    try:
        resolved.relative_to(lab)
    except ValueError as exc:
        raise ValueError("private result must stay below the selected lab") from exc
    return resolved


def validate(ids, selected=BACKENDS):
    if set(ids.get("backends", ())) != set(BACKENDS) or len(ids.get("hosts", ())) != len(CORE_NAMES):
        raise ValueError("ids requires exactly iscsi_gfs2, nfs, smb and three hosts")
    if (len({host.get("id") for host in ids["hosts"]}) != 3 or {host.get("lab_name") for host in ids["hosts"]} != set(CORE_NAMES)
            or any(not host.get("network_id") for host in ids["hosts"])):
        raise ValueError("hosts must be the three distinct Cores with their own verified network IDs")
    for name in selected:
        backend = ids["backends"][name]
        if not backend.get("storage_id") or not backend.get("storage_path", "").startswith("/"):
            raise ValueError(f"{name} needs a Core-reported storage ID and absolute mount path")
    guest = ids.get("guest", {})
    if not guest.get("image_path", "").startswith("/") or not guest.get("ssh_key", "").startswith("/"):
        raise ValueError("guest needs Core-local absolute image_path and ssh_key")
    if not all(ids.get(key) for key in ("controller", "password_file", "pool_id")):
        raise ValueError("ids needs controller, password_file and pool_id")
    if not isinstance(ids.get("outer_core_memory_mib"), int) or ids["outer_core_memory_mib"] < 4096:
        raise ValueError("lab needs the actual outer Core memory budget of at least 4096 MiB")


def shell(host, command, check=True):
    result = ssh(host["lab"], host["lab_name"], command, check=check)
    if check and result.returncode:
        raise RuntimeError(result.stderr.strip())
    return result.stdout


def shell_result(host, command):
    return ssh(host["lab"], host["lab_name"], command, check=False)


def guest(host, vm_id, config, script):
    # `domifaddr --source lease` is not reliable in the nested libvirt fixture,
    # despite dnsmasq holding a lease.  Bind through the domain's exact NIC MAC.
    interfaces = shell(host, f"virsh -c qemu:///system domiflist {shlex.quote(vm_id)}")
    macs = [line.split()[4].lower() for line in interfaces.splitlines()
            if len(line.split()) >= 5 and line.split()[4].count(":") == 5]
    if len(macs) != 1:
        raise RuntimeError(f"{vm_id} does not have exactly one guest NIC MAC")
    leases = shell(host, "virsh -c qemu:///system net-dhcp-leases default || true")
    client_id = "01:" + macs[0]
    address = next((fields[4].split("/", 1)[0] for fields in (line.split() for line in leases.splitlines())
                    if len(fields) >= 7 and fields[2].lower() == macs[0] and fields[3] == "ipv4" and fields[6].lower() == client_id), "")
    if not address:
        raise RuntimeError(f"{vm_id} has no guest DHCP lease")
    encoded = base64.b64encode(script.encode()).decode()
    command = (f"printf %s {shlex.quote(encoded)} | base64 -d | ssh -o BatchMode=yes "
               f"-o StrictHostKeyChecking=no -o ConnectTimeout=5 -i {shlex.quote(config['ssh_key'])} "
               f"{shlex.quote(config.get('user', 'root'))}@{shlex.quote(address)} sh -s")
    return shell(host, command)


def wait_for_guest(host, vm_id, config, checksum="", previous_boot_id="", timeout=180):
    deadline, last = time.monotonic() + timeout, ""
    while time.monotonic() < deadline:
        try:
            changed = "" if not previous_boot_id else f"test \"$(cat /proc/sys/kernel/random/boot_id)\" != {shlex.quote(previous_boot_id)}; "
            command = "set -eu; " + changed + ("cat /proc/sys/kernel/random/boot_id" if not checksum else
                                              f"sha256sum -c {shlex.quote(checksum)}; cat /proc/sys/kernel/random/boot_id")
            return guest(host, vm_id, config, command).strip()
        except Exception as exc:  # expected while a guest starts or reboots
            last = str(exc)
            time.sleep(2)
    raise RuntimeError(f"{vm_id} did not become reachable: {last}")


def vm_body(ids, host, backend, ordinal, disk_gib, name=None):
    return {"request_id": f"cluster-{backend}-{host['lab_name']}-{ordinal}-{uuid.uuid4().hex[:12]}",
            "pool_id": ids["pool_id"], "host_id": host["id"],
            "name": name or f"cluster-{backend}-{host['lab_name']}-{ordinal}", "guest_os": "linux",
            "cpu": {"count": 1, "sockets": 1, "cores": 1, "threads": 1, "model": "host-passthrough"},
            "memory": {"mode": "maximum", "size_mib": MEMORY_MIB},
            "disks": [{"storage_id": ids["backends"][backend]["storage_id"], "name": "system", "size_gib": disk_gib,
                       "bus": "virtio", "thin": True, "boot_order": 1}],
            "nics": [{"network_id": host["network_id"], "model": "virtio"}],
            "boot": {"firmware": "bios", "machine": "", "media_boot_order": 2},
            "start_after_create": False, "autostart": False}


def guest_image_info(host, guest_image):
    info = json.loads(shell(host, f"qemu-img info --output=json {shlex.quote(guest_image)}"))
    if info.get("virtual-size", 0) < 1 or info.get("actual-size", 0) < 1:
        raise RuntimeError("guest fixture has no usable virtual or allocated size")
    return info


def install_guest_image(host, vm_id, image, expected_virtual_size):
    path = shell(host, f"virsh -c qemu:///system domblklist {shlex.quote(vm_id)} | awk '$1==\"vda\" {{print $2}}'").strip()
    root = host["storage_path"] + "/"
    if not path.startswith(root):
        raise RuntimeError(f"{vm_id} disk is not assigned to {host['storage_path']}: {path}")
    # The domain and its storage assignment already came from Core.  This only supplies the
    # minimal disposable cloud image before the first API-directed start.
    shell(host, f"qemu-img convert -O qcow2 {shlex.quote(image)} {shlex.quote(path)}")
    info = json.loads(shell(host, f"qemu-img info --output=json {shlex.quote(path)}"))
    if info.get("virtual-size") != expected_virtual_size:
        raise RuntimeError(f"{vm_id} image virtual capacity differs after fixture placement: {info}")
    return path


def save_resume(path, backend, storage_id, guests):
    prior = json.loads(path.read_text()) if path.exists() else {}
    def saved(item):
        record = {"vm_id": item["vm_id"], "host_id": item["host"]["id"],
                  "lab_name": item["host"]["lab_name"], "network_id": item["host"]["network_id"],
                  "disk": item["disk"], "ordinal": item.get("ordinal"), "name": item.get("name")}
        if item.get("pool_id"):
            record["pool_id"] = item["pool_id"]
        if item.get("canonical_spec"):
            record["canonical_spec"] = item["canonical_spec"]
        return record
    path.write_text(json.dumps({"backend": backend, "storage_id": storage_id,
                                "failed_create_exclusions": prior.get("failed_create_exclusions", []),
                                "vms": [saved(item) for item in guests]}, indent=2) + "\n")


def replacement_vm_name(canonical, failed_vm_id):
    return f"{canonical}-replacement-{failed_vm_id.split('-', 1)[0]}"


def canonical_vm_spec(spec):
    if not isinstance(spec, dict):
        return None
    media_id = spec.get("media_id", "")
    if media_id is None:
        media_id = ""
    if not isinstance(media_id, str):
        return None
    result = {key: spec.get(key) for key in ("pool_id", "host_id", "name", "guest_os", "cpu", "memory", "disks", "boot", "start_after_create", "autostart")}
    result["media_id"] = media_id
    nics = spec.get("nics")
    if not isinstance(nics, list):
        return None
    result["nics"] = [{key: value for key, value in nic.items() if key != "mac"} for nic in nics if isinstance(nic, dict)]
    if len(result["nics"]) != len(nics):
        return None
    return result


def expected_vm_spec(ids, backend, host, ordinal, disk_gib, name):
    body = vm_body(ids, host, backend, ordinal, disk_gib, name)
    body.pop("request_id")
    return canonical_vm_spec(body)


def failed_create_exclusions(api, ids, backend, checkpoint, hosts, observed, slots):
    """Accept one explicitly named terminal failed lab create, never an uncertain VM."""
    saved = json.loads(checkpoint.read_text())
    entries = saved.get("failed_create_exclusions", [])
    if not isinstance(entries, list):
        raise RuntimeError("failed-create exclusions must be an explicit private list")
    if not entries:
        return {}
    if backend != "iscsi_gfs2":
        raise RuntimeError("failed-create exclusions are limited to the clustered iSCSI lab backend")
    by_id, slot_keys, result = {item.get("id"): item for item in observed}, {(h["id"], n) for h, n in slots}, {}
    operations = {item.get("id"): item for item in api.request("GET", "/api/v1/operations")}
    fresh = {item.get("id"): item for item in api.request("GET", "/api/v1/hosts")}
    repository = next((item for item in api.request("GET", "/api/v1/storage")
                       if item.get("id") == ids["backends"][backend]["storage_id"]), None)
    expected_uuid = (repository or {}).get("identity", {}).get("iscsi", {}).get("filesystem_uuid", "")
    for item in entries:
        vm_id, host_id, ordinal = item.get("vm_id"), item.get("host_id"), item.get("ordinal")
        host = next((candidate for candidate in hosts if candidate["id"] == host_id), None)
        canonical = f"cluster-{backend}-{host['lab_name']}-{ordinal}" if host and ordinal in (1, 2) else ""
        vm, operation = by_id.get(vm_id), operations.get(item.get("operation_id"))
        common = (isinstance(vm_id, str) and host and (host_id, ordinal) in slot_keys and
                  item.get("backend") == backend and item.get("name") == canonical and
                  isinstance(item.get("request_id"), str) and item["request_id"].startswith(canonical + "-") and
                  operation and operation.get("kind") == "vm-create" and operation.get("host_id") == host_id and
                  operation.get("status") == "failed" and fresh.get(host_id, {}).get("connection") == "fresh")
        if not common:
            raise RuntimeError("failed-create exclusion is not this exact terminal Core VM failure")
        live_failed_vm = (vm and vm.get("id") == vm_id and vm.get("host_id") == host_id and vm.get("name") == canonical and
                          vm.get("status") == "failed" and vm.get("state") == "unknown" and
                          vm.get("operation_id") == item.get("operation_id") and operation.get("request_id") == item.get("request_id"))
        adopted_absent_vm = (vm is None and operation.get("request_id") == "adopt-outcome:" + host_id + ":" + str(item.get("operation_id")))
        if not (live_failed_vm or adopted_absent_vm):
            raise RuntimeError("failed-create exclusion is not this exact terminal Core VM failure")
        for candidate in hosts:
            mount = shell_result(candidate, f"findmnt -n -o TARGET,FSTYPE,UUID --target {shlex.quote(candidate['storage_path'])}")
            fields = mount.stdout.split()
            if (mount.returncode != 0 or len(fields) < 2 or fields[0] != candidate["storage_path"] or
                    (fields[1] != "gfs2" or not expected_uuid or (len(fields) < 3 or fields[2] != expected_uuid))):
                raise RuntimeError("failed-create exclusion storage backing is not the trusted mounted repository")
            domain = shell_result(candidate, f"virsh -c qemu:///system domstate {shlex.quote(vm_id)}")
            if domain.returncode == 0 or "failed to get domain" not in domain.stderr:
                raise RuntimeError("failed-create exclusion has native execution evidence or an inaccessible Core")
            disk = candidate["storage_path"] + f"/nex-vms/{vm_id}/disk-0.qcow2"
            absent = shell_result(candidate, f"test ! -e {shlex.quote(disk)} && test ! -L {shlex.quote(disk)} && test ! -d {shlex.quote(disk.rsplit('/', 1)[0])}")
            if absent.returncode != 0:
                raise RuntimeError("failed-create exclusion retains an assigned disk")
        key = (host_id, ordinal)
        if key in result:
            raise RuntimeError("failed-create exclusions repeat one physical slot")
        result[key] = item
    return result


def resume_guests(api, ids, backend, checkpoint, expected_virtual_size):
    saved = json.loads(checkpoint.read_text())
    config = ids["backends"][backend]
    if saved.get("backend") != backend or saved.get("storage_id") != config["storage_id"]:
        raise RuntimeError("resume checkpoint does not match this backend storage")
    by_host = {host["id"]: {**host, "lab": ids["lab"], "storage_path": config["storage_path"]} for host in ids["hosts"]}
    observed = {item.get("id"): item for item in api.request("GET", "/api/v1/vms")}
    entries = saved.get("vms", [])
    if not entries or len(entries) > 6 or len({entry.get("vm_id") for entry in entries}) != len(entries):
        raise RuntimeError("resume checkpoint must contain one through six distinct VMs")
    guests = []
    for entry in entries:
        host = by_host.get(entry.get("host_id"))
        vm = observed.get(entry.get("vm_id"))
        if not host or entry.get("lab_name") != host["lab_name"] or entry.get("network_id") != host["network_id"]:
            raise RuntimeError("resume checkpoint host/network mapping is not this lab")
        if not vm or vm.get("host_id") != host["id"] or vm.get("status") != "succeeded" or vm.get("state") != "shut off":
            raise RuntimeError("resume VM is not an existing stopped Core-created VM on its recorded host")
        prefix = f"cluster-{backend}-{host['lab_name']}-"
        try:
            ordinal = int(vm.get("name", "").removeprefix(prefix).split("-replacement-", 1)[0])
        except ValueError as exc:
            raise RuntimeError("resume VM name is not this backend/Core ordinal") from exc
        canonical = prefix + str(ordinal)
        saved_name = entry.get("name") or canonical
        allowed_replacement = next((replacement_vm_name(canonical, excluded.get("vm_id", ""))
                                    for excluded in saved.get("failed_create_exclusions", [])
                                    if excluded.get("host_id") == host["id"] and excluded.get("ordinal") == ordinal), "")
        if (vm.get("name") != saved_name or ordinal not in (1, 2) or
                (saved_name != canonical and saved_name != allowed_replacement)):
            raise RuntimeError("resume VM name is not this backend/Core ordinal")
        if entry.get("ordinal") not in (None, ordinal):
            raise RuntimeError("resume checkpoint ordinal disagrees with the Core VM name")
        disk = shell(host, f"virsh -c qemu:///system domblklist {shlex.quote(entry['vm_id'])} | awk '$1==\"vda\" {{print $2}}'").strip()
        if disk != entry.get("disk") or not disk.startswith(host["storage_path"] + "/"):
            raise RuntimeError("resume VM disk is not the exact recorded selected-storage disk")
        if json.loads(shell(host, f"qemu-img info --output=json {shlex.quote(disk)}")).get("virtual-size") != expected_virtual_size:
            raise RuntimeError("resume VM disk no longer matches the guest fixture virtual capacity")
        item = {"host": host, "vm_id": entry["vm_id"], "disk": disk, "ordinal": ordinal, "name": saved_name}
        if backend == "iscsi_gfs2":
            disk_gib = (expected_virtual_size + 1024**3 - 1) // 1024**3
            expected = expected_vm_spec(ids, backend, host, ordinal, disk_gib, saved_name)
            if (vm.get("pool_id") != ids["pool_id"] or entry.get("pool_id") not in (None, ids["pool_id"]) or
                    canonical_vm_spec(vm.get("spec")) != expected or
                    entry.get("canonical_spec") not in (None, expected)):
                raise RuntimeError("resume VM pool or canonical spec is not this GFS2 workload")
            item.update({"pool_id": ids["pool_id"], "canonical_spec": expected})
        guests.append(item)
    if len({(item["host"]["id"], item["ordinal"]) for item in guests}) != len(guests):
        raise RuntimeError("resume checkpoint repeats a Core VM ordinal")
    if any(sum(item["host"]["id"] == host for item in guests) > 2 for host in by_host):
        raise RuntimeError("resume checkpoint may retain at most two VMs on each Core")
    return guests


def missing_slots(guests, hosts):
    present = {(item["host"]["id"], item["ordinal"]) for item in guests}
    if len(present) != len(guests):
        raise RuntimeError("resume checkpoint repeats a Core VM ordinal")
    return [(host, ordinal) for host in hosts for ordinal in range(1, GUESTS_PER_HOST + 1)
            if (host["id"], ordinal) not in present]


def power(api, vm_id, action):
    reply = api.request("POST", f"/api/v1/vms/{vm_id}/power", {"request_id": f"cluster-{action}-{uuid.uuid4().hex}", "action": action})
    return api.operation(reply["operation"]["id"])


def write_guest(item, backend, guest_config, start_epoch):
    checksum = f"/root/nex-cluster-{backend}-{item['vm_id']}.sha256"
    data = checksum.removesuffix(".sha256") + ".bin"
    tag = f"{backend}:{item['host']['lab_name']}:{item['vm_id']}"
    result = guest(item["host"], item["vm_id"], guest_config,
                   f"set -eu; umask 077; while test \"$(date +%s)\" -lt {start_epoch}; do sleep .05; done; "
                   f"started=$(date +%s.%N); printf '%s\\n' {shlex.quote(tag)} > {shlex.quote(data)}; i=0; "
                   f"while test $i -lt 32; do dd if=/dev/zero bs=1M count=1 conv=fsync oflag=append status=none >> {shlex.quote(data)}; i=$((i+1)); sleep .05; done; "
                   f"sha256sum {shlex.quote(data)} > {shlex.quote(checksum)}; sync; sha256sum -c {shlex.quote(checksum)}; printf '%s %s\\n' \"$started\" \"$(date +%s.%N)\"")
    return checksum, result


def interruption_write(item, backend, guest_config, start_epoch):
    path = f"/root/nex-cluster-{backend}-{item['vm_id']}.interruption.bin"
    marker = path + ".started"
    progress = path + ".progress"
    # This deliberately lasts long enough for the real fence request to overlap a guest fsync.
    # The prior checksum manifest remains the recovery oracle if this guest is the fence target.
    return guest(item["host"], item["vm_id"], guest_config,
                 f"set -eu; while test \"$(date +%s)\" -lt {start_epoch}; do sleep .05; done; started=$(date +%s.%N); printf '0\\n' > {shlex.quote(progress)}; sync -f {shlex.quote(progress)}; printf '%s\\n' \"$started\" > {shlex.quote(marker)}; sync -f {shlex.quote(marker)}; : > {shlex.quote(path)}; i=0; while test $i -lt 600; do "
                 f"printf '%s:%s\\n' {shlex.quote(backend)} \"$i\" >> {shlex.quote(path)}; "
                 f"dd if=/dev/zero bs=1M count=1 conv=fsync oflag=append status=none >> {shlex.quote(path)}; "
                 f"i=$((i+1)); printf '%s\\n' \"$i\" > {shlex.quote(progress)}; sync -f {shlex.quote(progress)}; sleep .1; done; sync; sha256sum {shlex.quote(path)} > {shlex.quote(path)}.sha256; sha256sum -c {shlex.quote(path)}.sha256; printf '%s %s\\n' \"$started\" \"$(date +%s.%N)\"")


def poll_interruption_markers(items, backend, guest_config, start_epoch, timeout=20):
    """Read the marker written inside each already-running interruption script."""
    deadline, markers = time.monotonic() + timeout, {}
    while time.monotonic() < deadline:
        for item in items:
            if item["vm_id"] in markers:
                continue
            path = f"/root/nex-cluster-{backend}-{item['vm_id']}.interruption.bin.started"
            try:
                raw = guest(item["host"], item["vm_id"], guest_config, f"cat {shlex.quote(path)}").strip()
            except Exception:
                pass
            else:
                marker = float(raw)
                if marker >= start_epoch:
                    markers[item["vm_id"]] = marker
        if len(markers) == len(items):
            return markers
        time.sleep(.2)
    raise RuntimeError("target interruption marker was not durably observed before fence dispatch")


def writer_progress(items, backend, guest_config, minimum=None, timeout=20):
    """Read each writer's fsynced monotonic byte counter."""
    deadline, values = time.monotonic() + timeout, {}
    while time.monotonic() < deadline:
        for item in items:
            path = f"/root/nex-cluster-{backend}-{item['vm_id']}.interruption.bin.progress"
            try:
                value = int(guest(item["host"], item["vm_id"], guest_config, f"cat {shlex.quote(path)}").strip())
            except Exception:
                continue
            if value < 1 or (minimum and value <= minimum.get(item["vm_id"], 0)):
                continue
            values[item["vm_id"]] = value
        if len(values) == len(items):
            return values
        time.sleep(.2)
    raise RuntimeError("guest writers did not report fsynced progress")


def require_pending_target_writes(futures):
    if any(future.done() for future in futures):
        raise RuntimeError("target write failed before fence dispatch")


def clear_interruption_markers(items, backend, guest_config):
    for item in items:
        path = f"/root/nex-cluster-{backend}-{item['vm_id']}.interruption.bin.started"
        guest(item["host"], item["vm_id"], guest_config, f"rm -f {shlex.quote(path)}; sync")


def healthy_cluster(result, repository_id, members=3):
    """Decode the actual Core relay shape: operations, each with member status reports."""
    operation = next((item for item in result.get("operations", ())
                      if item.get("config", {}).get("repository_id") == repository_id), None)
    if not operation or result.get("status") not in ("accepted", "succeeded") or operation.get("status") != "healthy":
        raise RuntimeError(f"clustered GFS2 operation is not healthy: {result}")
    reports = operation.get("members", ())
    # `members` is an agent envelope; its observed readiness is nested at
    # `member.status`, not flattened into the envelope.
    if len(reports) != members or not all(isinstance(item.get("status"), dict) and item["status"].get("healthy") and
                                          item["status"].get("quorate") and item["status"].get("mounted") and
                                          item["status"].get("fencing_verified") and item["status"].get("status") == "healthy"
                                          for item in reports):
        raise RuntimeError(f"clustered GFS2 members are not all healthy: {result}")
    return operation


def wait_for_cluster(api, pool_id, repository_id, timeout=300):
    deadline, last = time.monotonic() + timeout, None
    while time.monotonic() < deadline:
        try:
            last = api.request("GET", "/api/v1/storage/cluster?" + urllib.parse.urlencode({"pool_id": pool_id}))
        except HTTPError as exc:
            if exc.status != 503:
                raise
            last = str(exc)
            time.sleep(2)
            continue
        try:
            return healthy_cluster(last, repository_id)
        except RuntimeError:
            time.sleep(2)
    raise RuntimeError(f"clustered GFS2 did not become healthy: {last}")


def require_member_fence_target(hosts, pool_id, target):
    pool_hosts = [item for item in hosts if item.get("pool_id") == pool_id]
    masters = {item.get("inventory", {}).get("managed", {}).get("membership", {}).get("master_host_id") for item in pool_hosts}
    masters.discard(None)
    masters.discard("")
    if len(masters) != 1:
        raise RuntimeError("cannot determine one observed pool master for public fence polling")
    ready_views = []
    for item in pool_hosts:
        members = item.get("inventory", {}).get("managed", {}).get("membership", {}).get("members", ())
        ready = [member.get("host_id") for member in members if member.get("status") == "ready" and member.get("host_id")]
        if len(ready) != len(set(ready)):
            raise RuntimeError("observed pool membership repeats a ready member")
        ready_views.append(frozenset(ready))
    if not ready_views or len(set(ready_views)) != 1:
        raise RuntimeError("observed pool membership is inconsistent for public fence polling")
    if target not in ready_views[0] or sum(item.get("id") == target for item in pool_hosts) != 1:
        raise RuntimeError("public-poll fence target is not one current ready pool member")
    if target == next(iter(masters)):
        raise RuntimeError("public-poll fence scenario must target a pool member, not the master")


def require_master_agent_target(ids, hosts):
    """Bind the disruptive service stop to this lab's observed current master."""
    master = next((host for host in ids["hosts"] if host.get("lab_name") == "core1"), None)
    if not master:
        raise RuntimeError("selected lab has no core1 master target")
    pool_hosts = [host for host in hosts if host.get("pool_id") == ids["pool_id"]]
    memberships = [host.get("inventory", {}).get("managed", {}).get("membership", {}) for host in pool_hosts]
    expected_members = frozenset(host["id"] for host in ids["hosts"])
    if len(pool_hosts) != 3 or any(frozenset(member.get("host_id") for member in membership.get("members", ())
                                             if member.get("status") == "ready") != expected_members
                                  for membership in memberships):
        raise RuntimeError("observed pool membership does not exactly match the selected lab")
    observed = {membership.get("master_host_id") for membership in memberships}
    observed.discard("")
    observed.discard(None)
    if observed != {master["id"]} or sum(host.get("id") == master["id"] for host in pool_hosts) != 1:
        raise RuntimeError("selected Core is not the one observed current pool master")
    return master


def master_agent_outage_command(ids, master, unit):
    if master.get("lab_name") != "core1" or not re.fullmatch(r"nex-master-agent-[a-f0-9]{16}", unit):
        raise ValueError("unsafe master-agent outage target")
    service = shlex.quote(MASTER_AGENT_SERVICE)
    watchdog = shlex.quote(unit)
    return ("set -eu; "
            f"test \"$(systemctl show --property=Id --value {service})\" = {service}; "
            f"systemctl is-active --quiet {service}; "
            f"systemd-run --quiet --collect --unit {watchdog} --on-active=180s /usr/bin/systemctl start {service}; "
            f"systemctl is-active --quiet {watchdog}.timer; systemctl stop {service}; "
            f"! systemctl is-active --quiet {service}")


def restore_master_agent(ids, master):
    if master.get("lab_name") != "core1":
        raise ValueError("unsafe master-agent restore target")
    service = shlex.quote(MASTER_AGENT_SERVICE)
    ssh(ids["lab"], master["lab_name"],
        f"set -eu; test \"$(systemctl show --property=Id --value {service})\" = {service}; systemctl start {service}; systemctl is-active --quiet {service}")


def require_master_agent_inactive(ids, master):
    if master.get("lab_name") != "core1":
        raise ValueError("unsafe master-agent status target")
    service = shlex.quote(MASTER_AGENT_SERVICE)
    ssh(ids["lab"], master["lab_name"],
        f"set -eu; test \"$(systemctl show --property=Id --value {service})\" = {service}; ! systemctl is-active --quiet {service}")


def member_reconciliation_observations(hosts, master_id):
    members = [host for host in hosts if host.get("id") != master_id]
    if len(members) != 2:
        raise RuntimeError("master-agent outage requires exactly two member observations")
    result = []
    for host in members:
        checks = host.get("inventory", {}).get("checks", ())
        check = next((check for check in checks if check.get("name") == "pool-storage-reconciliation"), {})
        result.append({"host_id": host.get("id"), "connection": host.get("connection"), "check": check})
    return result


def check_observed_after(check, after):
    try:
        return datetime.fromisoformat(check.get("observed_at", "").replace("Z", "+00:00")).timestamp() >= after
    except (TypeError, ValueError):
        return False


def wait_for_member_reconciliation(api, ids, master_id, healthy, after, timeout=90):
    deadline, last = time.monotonic() + timeout, []
    while time.monotonic() < deadline:
        last = member_reconciliation_observations(api.request("GET", "/api/v1/hosts"), master_id)
        if all(item["connection"] == "fresh" and item["check"].get("freshness") == "fresh" and
               (item["check"].get("status") == "healthy") == healthy and
               check_observed_after(item["check"], after) and
               (healthy or "master-list" in item["check"].get("error", "")) for item in last):
            return last
        time.sleep(1)
    state = "healthy" if healthy else "failed"
    raise RuntimeError(f"members did not report fresh {state} reconciliation during master-agent outage: {last}")


def refused_start_during_master_outage(api, item, master_origin):
    """A stopped member VM must not gain a writable start permit without its master."""
    vm_id = item.get("id")
    if not isinstance(vm_id, str) or not vm_id:
        raise RuntimeError("admission VM has no API identity")
    before = {key: item.get(key) for key in ("id", "pool_id", "host_id", "name", "spec")}
    reply = api.request("POST", f"/api/v1/vms/{vm_id}/power",
                        {"request_id": f"cluster-master-outage-start-{uuid.uuid4().hex}", "action": "start"})
    operation_id = reply["operation"]["id"]
    deadline, outcome = time.monotonic() + 45, None
    while time.monotonic() < deadline:
        outcome = next((operation for operation in api.request("GET", "/api/v1/operations")
                        if operation.get("id") == operation_id), None)
        if outcome and outcome.get("status") not in ("queued", "dispatched", "running"):
            break
        time.sleep(1)
    exact_inventory_refusal = isinstance(outcome, dict) and outcome.get("error") == (
        f'Post "{master_origin}/api/v1/pool/vms/inventory": dial tcp '
        f'{urllib.parse.urlparse(master_origin).hostname}:9444: connect: connection refused')
    if not outcome or outcome.get("status") != "failed" or not ("pool master" in outcome.get("error", "") or exact_inventory_refusal):
        raise RuntimeError(f"stopped VM start was not safely refused during master-agent outage: {outcome}")
    after = wait_for_vm_state(api, vm_id, "shut off")
    if after.get("state") != "shut off" or {key: after.get(key) for key in before} != before:
        raise RuntimeError("refused start changed the stopped VM state")
    return {"vm_id": vm_id, "operation": outcome, "identity": before}


def guest_boot_ids(guests, guest_config):
    return {item["vm_id"]: guest(item["host"], item["vm_id"], guest_config, "cat /proc/sys/kernel/random/boot_id").strip()
            for item in guests}


def bind_stopped_admission_vm(api, ids, vm_id, master_id, repository_id):
    record = next((vm for vm in api.request("GET", "/api/v1/vms") if vm.get("id") == vm_id), None)
    host_ids = {host["id"] for host in ids["hosts"]}
    disks = (record or {}).get("spec", {}).get("disks", ())
    if (not record or record.get("pool_id") != ids["pool_id"] or record.get("host_id") not in host_ids - {master_id} or
            record.get("status") != "succeeded" or record.get("freshness") != "fresh" or record.get("state") != "shut off" or
            not any(disk.get("storage_id") == repository_id for disk in disks)):
        raise RuntimeError("admission VM is not a fresh stopped member-owned clustered-iSCSI fixture")
    return record


def await_writer_outputs(writes, timeout=WRITE_TIMEOUT):
    """Use one post-recovery deadline for all six fsynced guest writers."""
    deadline, outputs = time.monotonic() + timeout, []
    try:
        for future in writes:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise FutureTimeout()
            outputs.append(future.result(timeout=remaining))
    except FutureTimeout as exc:
        raise RuntimeError("guest writer did not finish within the post-recovery global budget") from exc
    return outputs


def master_agent_outage(api, ids, guests, backend, guest_config, admission_vm_id):
    """Stop only the observed master agent while six existing guest writers run."""
    hosts = api.request("GET", "/api/v1/hosts")
    master = require_master_agent_target(ids, hosts)
    admission = bind_stopped_admission_vm(api, ids, admission_vm_id, master["id"], ids["backends"][backend]["storage_id"])
    unit = "nex-master-agent-" + uuid.uuid4().hex[:16]
    started = int(time.time()) + 3
    with ThreadPoolExecutor(max_workers=6) as pool:
        writes = {pool.submit(interruption_write, guest, backend, guest_config, started): guest for guest in guests}
        try:
            markers = poll_interruption_markers(guests, backend, guest_config, started)
            progress_before = writer_progress(guests, backend, guest_config)
            boots_before = guest_boot_ids(guests, guest_config)
            ssh(ids["lab"], master["lab_name"], master_agent_outage_command(ids, master, unit))
            stopped_at = time.time()
            failed = wait_for_member_reconciliation(api, ids, master["id"], False, stopped_at, timeout=45)
            require_master_agent_inactive(ids, master)
            progress_after = writer_progress(guests, backend, guest_config, progress_before)
            boots_after = guest_boot_ids(guests, guest_config)
            if boots_after != boots_before:
                raise RuntimeError("a guest rebooted during the master-agent outage")
            if any(future.done() for future in writes):
                raise RuntimeError("a guest writer ended during the master-agent outage")
            time.sleep(3)
            if any(future.done() for future in writes):
                raise RuntimeError("a guest writer did not continue through the master-agent outage")
            require_master_agent_inactive(ids, master)
            refusal = refused_start_during_master_outage(api, admission, ids["peer_origins"][master["lab_name"]])
            require_master_agent_inactive(ids, master)
        finally:
            restore_error = None
            try:
                restore_master_agent(ids, master)
            except Exception as exc:
                restore_error = exc
            if restore_error:
                raise RuntimeError(f"master-agent restore failed: {restore_error}") from restore_error
        restored_at = time.time()
        outputs = await_writer_outputs(writes)
        recovered = wait_for_member_reconciliation(api, ids, master["id"], True, restored_at)
    return {"master_host_id": master["id"], "service": MASTER_AGENT_SERVICE, "watchdog_unit": unit,
            "writer_markers": markers, "writer_progress_before": progress_before, "writer_progress_after": progress_after,
            "guest_boot_ids_before": boots_before, "guest_boot_ids_after": boots_after, "failed_member_reconciliation": failed,
            "recovered_member_reconciliation": recovered, "write_windows": windows(outputs), "start_refusal": refusal}


def wait_for_host(api, host_id, timeout=300):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        host = next((item for item in api.request("GET", "/api/v1/hosts") if item["id"] == host_id), None)
        if host and host.get("connection") == "fresh":
            return host
        time.sleep(2)
    raise RuntimeError(f"fenced host {host_id} did not reconnect")


def fenced_outer_domain(lab, lab_name):
    profile = json.loads((lab / "fence-profile.json").read_text())
    domain_id, container = profile.get("domains", {}).get(lab_name), profile.get("container")
    try:
        uuid.UUID(domain_id)
    except (TypeError, ValueError) as exc:
        raise RuntimeError("fence profile has no UUID for the exact fenced Core") from exc
    if not profile.get("disposable") or not isinstance(container, str) or not container.startswith("nex-cluster-ha-libvirt-"):
        raise RuntimeError("fence profile is not this disposable lab")
    return container, domain_id


def restore_fenced_outer(lab, lab_name):
    """Return only the proven-off outer Core domain that the fence targeted."""
    container, domain_id = fenced_outer_domain(lab, lab_name)
    base = ["sudo", "-n", "docker", "exec", container, "virsh", "-c", "qemu:///system"]
    name = subprocess.run([*base, "domname", domain_id], check=True, capture_output=True, text=True).stdout.strip()
    if name != f"nex-cluster-{lab_name}":
        raise RuntimeError("fence profile UUID resolves to an unexpected outer domain")
    state = subprocess.run([*base, "domstate", domain_id], check=True, capture_output=True, text=True).stdout.strip()
    if state != "shut off":
        raise RuntimeError(f"fenced outer Core was not observed off before return: {state}")
    subprocess.run([*base, "start", domain_id], check=True)
    state = subprocess.run([*base, "domstate", domain_id], check=True, capture_output=True, text=True).stdout.strip()
    if state != "running":
        raise RuntimeError(f"fenced outer Core did not return running: {state}")


def fenced_outer_is_off(lab, lab_name):
    container, domain_id = fenced_outer_domain(lab, lab_name)
    command = ["sudo", "-n", "docker", "exec", container, "virsh", "-c", "qemu:///system", "domstate", domain_id]
    state = subprocess.run(command, check=True, capture_output=True, text=True).stdout.strip()
    if state != "shut off":
        raise RuntimeError(f"fenced outer Core was not observed off: {state}")


def fixture_capacity(api, repository_id, image_infos, already_reserved=0):
    storage = next((item for item in api.request("GET", "/api/v1/storage") if item.get("id") == repository_id), None)
    if not storage:
        raise RuntimeError(f"Core does not report selected storage {repository_id}")
    access = storage.get("access", {})
    available = access.get("available_bytes")
    if not isinstance(available, int):
        raise RuntimeError(f"Core has no usable available-capacity observation for {repository_id}")
    # Thin qcow2 allocation is not enough evidence: every one of the six virtual disks must
    # fit with room for metadata and guest writes, even when the base image is sparse.
    needed = GUESTS_PER_HOST * len(CORE_NAMES) * max(info["virtual-size"] for info in image_infos) + CAPACITY_HEADROOM_BYTES
    quota = access.get("effective_quota_bytes")
    # A resume reuses six already-reserved Core disks.  Include only their observed
    # allocation in free capacity, while retaining the full virtual-capacity test.
    usable = available + already_reserved
    if usable < needed or (isinstance(quota, int) and quota < needed):
        raise RuntimeError(f"Core capacity/quota cannot hold six virtual guest disks plus headroom on {repository_id}")


def observed_within(when, now=None, maximum_age=30):
    """Require a real, current Core observation, never a future timestamp."""
    if not isinstance(when, str):
        return False
    try:
        observed = datetime.fromisoformat(when.replace("Z", "+00:00")).timestamp()
    except ValueError:
        return False
    age = (time.time() if now is None else now) - observed
    return 0 <= age <= maximum_age


def repository_host_ready(storage, repository_id, host_id, backend):
    """Check the current admission evidence for one VM's selected Core."""
    access, capabilities = storage.get("access", {}), storage.get("capabilities", {})
    if (storage.get("id") != repository_id or storage.get("status") != "succeeded" or
            access.get("status") != "healthy" or not access.get("readable") or not access.get("writable") or
            not capabilities.get("vm_disk") or storage.get("error") or
            not observed_within(access.get("observed_at"))):
        return False
    if backend == "iscsi_gfs2" and host_id not in capabilities.get("eligible_host_ids", ()):
        return False
    # The registration host supplies the master access observation.  Every other
    # Core must report the same repository generation and identity itself.
    if host_id == storage.get("host_id"):
        return True
    member = next((item for item in storage.get("member_access", ())
                   if item.get("host_id") == host_id), None)
    generation = storage.get("generation")
    if (not isinstance(generation, int) or not member or member.get("repository_id") != repository_id or
            member.get("generation") != generation):
        return False
    try:
        identity_matches = storage_identity_matches(storage.get("identity"), json.loads(member.get("identity", "")))
    except (TypeError, json.JSONDecodeError):
        return False
    return (identity_matches and not member.get("error") and member.get("readable") and member.get("writable") and
            member.get("qemu_writable") and member.get("locking") and observed_within(member.get("observed_at")))


def storage_identity_matches(expected, observed):
    """Mirror Core's durable SMB marker comparison for member admission."""
    if not isinstance(expected, dict) or not isinstance(observed, dict):
        return False
    wanted_smb, seen_smb = expected.get("smb"), observed.get("smb")
    if wanted_smb is not None or seen_smb is not None:
        if not isinstance(wanted_smb, dict) or not isinstance(seen_smb, dict):
            return False
        server, share = wanted_smb.get("server"), wanted_smb.get("share")
        if (not isinstance(server, str) or not server or not isinstance(share, str) or not share or
                server != seen_smb.get("server") or share != seen_smb.get("share")):
            return False
        marker, member_marker = wanted_smb.get("marker_id"), seen_smb.get("marker_id")
        if marker or member_marker:
            return isinstance(marker, str) and bool(marker) and marker == member_marker
        if marker not in (None, "") or member_marker not in (None, ""):
            return False
        filesystem_id, member_filesystem_id = wanted_smb.get("filesystem_id"), seen_smb.get("filesystem_id")
        return isinstance(filesystem_id, str) and bool(filesystem_id) and filesystem_id == member_filesystem_id
    return bool(expected) and bool(observed) and observed == expected


def wait_repository_host_ready(api, repository_id, host_id, backend, timeout=120):
    """Wait briefly for fresh admission evidence; never POST against stale storage."""
    deadline, last = time.monotonic() + timeout, None
    while True:
        repositories = api.request("GET", "/api/v1/storage")
        last = next((item for item in repositories if item.get("id") == repository_id), None)
        if last and repository_host_ready(last, repository_id, host_id, backend):
            return last
        if time.monotonic() >= deadline:
            raise RuntimeError(f"selected storage {repository_id} is not currently admitted on host {host_id}: {last}")
        time.sleep(2)


def fence_proof(operation, target):
    return next((proof for proof in operation.get("config", {}).get("fence_proofs", ()) if proof.get("host_id") == target), {})


def proof_overlaps_survivors(observed_at, outputs):
    at = datetime.fromisoformat(observed_at.replace("Z", "+00:00")).timestamp()
    ranges = windows(outputs)
    if not max(item["started"] for item in ranges) <= at <= min(item["ended"] for item in ranges):
        raise RuntimeError(f"fence exclusion did not occur during all surviving guest writes: {observed_at} {ranges}")
    return ranges


def wait_for_fence_proof(api, pool_id, repository_id, target, previous_observed_at, timeout=120):
    deadline, last = time.monotonic() + timeout, None
    while time.monotonic() < deadline:
        last = api.request("GET", "/api/v1/storage/cluster?" + urllib.parse.urlencode({"pool_id": pool_id}))
        operation = next((item for item in last.get("operations", ())
                          if item.get("config", {}).get("repository_id") == repository_id), None)
        proof = fence_proof(operation or {}, target)
        if (proof.get("status") == "passed" and proof.get("exclusion_verified") and proof.get("observed_at") and
                proof.get("observed_at") != previous_observed_at):
            return operation
        time.sleep(2)
    raise RuntimeError(f"Core did not record an actual fence proof: {last}")


def windows(outputs):
    values = []
    for output in outputs:
        fields = output.strip().splitlines()[-1].split()
        if len(fields) != 2:
            raise RuntimeError(f"guest did not report a write window: {output}")
        values.append({"started": float(fields[0]), "ended": float(fields[1])})
    if max(item["started"] for item in values) >= min(item["ended"] for item in values):
        raise RuntimeError(f"guest fsync write windows did not overlap: {values}")
    return values


def require_distinct(values, label):
    if len(values) != len(set(values)) or any(not value for value in values):
        raise RuntimeError(f"{label} are not unique; a cloned guest may share an address or identity")


def stop_guests(api, guests):
    observed = {item["id"]: item for item in api.request("GET", "/api/v1/vms")}
    requested = {item["vm_id"] for item in guests}
    missing = requested - set(observed)
    unready = {vm_id: {key: observed[vm_id].get(key) for key in ("state", "status", "freshness")}
               for vm_id in requested & set(observed)
               if observed[vm_id].get("status") != "succeeded" or observed[vm_id].get("freshness") != "fresh" or
               observed[vm_id].get("state") not in ("running", "shut off")}
    if missing or unready:
        raise RuntimeError("Core did not provide fresh known state for every disposable workload VM")
    running = [item for item in guests if observed[item["vm_id"]].get("state") == "running"]
    with ThreadPoolExecutor(max_workers=6) as pool:
        list(pool.map(lambda item: power(api, item["vm_id"], "force-stop"), running))
    if running:
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            latest = {item["id"]: item for item in api.request("GET", "/api/v1/vms")}
            if all(latest.get(item["vm_id"], {}).get("state") == "shut off" and
                   latest.get(item["vm_id"], {}).get("status") == "succeeded" and
                   latest.get(item["vm_id"], {}).get("freshness") == "fresh" for item in guests):
                return
            time.sleep(1)
        raise RuntimeError("Core did not observe all disposable workload VMs shut off")
    # Even an initially-all-off set must be proven exact, fresh, and complete.
    latest = {item["id"]: item for item in api.request("GET", "/api/v1/vms")}
    if not all(latest.get(item["vm_id"], {}).get("state") == "shut off" and
               latest.get(item["vm_id"], {}).get("status") == "succeeded" and
               latest.get(item["vm_id"], {}).get("freshness") == "fresh" for item in guests):
        raise RuntimeError("Core did not observe all disposable workload VMs shut off")


def existing_vm_record(api, ids, item, state):
    record = next((candidate for candidate in api.request("GET", "/api/v1/vms")
                   if candidate.get("id") == item["vm_id"]), None)
    if (not record or record.get("id") != item["vm_id"] or record.get("pool_id") != ids["pool_id"] or
            record.get("host_id") != item["host"]["id"] or
            record.get("name") != item.get("name") or record.get("status") != "succeeded" or
            record.get("state") != state or record.get("freshness") != "fresh" or
            canonical_vm_spec(record.get("spec")) != item.get("canonical_spec")):
        raise RuntimeError("existing survivor VM is not a fresh unchanged Core-owned VM")
    return record


def wait_for_vm_state(api, vm_id, state, timeout=120):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        record = next((candidate for candidate in api.request("GET", "/api/v1/vms")
                       if candidate.get("id") == vm_id), None)
        if record and record.get("state") == state:
            return record
        time.sleep(1)
    raise RuntimeError(f"{vm_id} did not reach {state}")


def admit_existing_survivor(api, ids, config, target_host, guests, guest_config):
    """Restart one existing survivor only while the fenced target remains off."""
    survivors = [host for host in ids["hosts"] if host["id"] != target_host["id"]]
    if len(survivors) != 2:
        raise RuntimeError("fence admission requires the two surviving Cores")
    for host in survivors:
        wait_repository_host_ready(api, config["storage_id"], host["id"], "iscsi_gfs2")
    fenced_outer_is_off(ids["lab"], target_host["lab_name"])
    survivor = next(item for item in guests if item["host"]["id"] == survivors[0]["id"])
    before = existing_vm_record(api, ids, survivor, "running")
    boot_id = wait_for_guest(survivor["host"], survivor["vm_id"], guest_config, survivor["checksum"])
    power(api, survivor["vm_id"], "shutdown")
    wait_for_vm_state(api, survivor["vm_id"], "shut off")
    fenced_outer_is_off(ids["lab"], target_host["lab_name"])
    power(api, survivor["vm_id"], "start")
    new_boot_id = wait_for_guest(survivor["host"], survivor["vm_id"], guest_config, survivor["checksum"], boot_id)
    after = existing_vm_record(api, ids, survivor, "running")
    if {key: before.get(key) for key in ("id", "pool_id", "host_id", "name", "spec")} != {
            key: after.get(key) for key in ("id", "pool_id", "host_id", "name", "spec")}:
        raise RuntimeError("existing survivor VM changed identity or ownership during admission")
    fenced_outer_is_off(ids["lab"], target_host["lab_name"])
    return {"vm_id": survivor["vm_id"], "host_id": survivor["host"]["id"],
            "boot_id_before": boot_id, "boot_id_after": new_boot_id,
            "identity": {key: after.get(key) for key in ("id", "pool_id", "host_id", "name", "spec")}}


def competing_writer_probe(guests, backend):
    """Prove protocol locks with real QEMU and qemu-io on a fresh probe disk."""
    first, second = guests[0], next(item for item in guests if item["host"]["id"] != guests[0]["host"]["id"])
    probe = first["host"]["storage_path"] + "/.nex-lock-probe-" + uuid.uuid4().hex + ".qcow2"
    def qemu_identity(host):
        fields = shell(host, "id -u libvirt-qemu; id -g libvirt-qemu; id -G libvirt-qemu").splitlines()
        if len(fields) != 3 or not all(part.isdecimal() for part in fields[:2]):
            raise RuntimeError("cannot resolve numeric non-root libvirt-qemu identity")
        uid, gid = fields[:2]
        groups = fields[2].split()
        if (uid == "0" or gid == "0" or not groups or not all(group.isdecimal() and group != "0" for group in groups) or gid not in groups):
            raise RuntimeError("cannot resolve numeric non-root libvirt-qemu identity")
        return uid, gid, tuple(sorted(set(groups), key=int))
    uid, gid, groups = qemu_identity(first["host"])
    if qemu_identity(second["host"]) != (uid, gid, groups):
        raise RuntimeError("probe Cores do not have one consistent libvirt-qemu identity")
    credentials = f"--reuid={uid} --regid={gid} --groups={','.join(groups)}"
    runtime = shell(first["host"], f"set -eu; runtime=$(mktemp -d /run/nex-lock-probe.XXXXXX); chown {uid}:{gid} \"$runtime\"; "
                    f"test \"$(stat -c '%u:%g' \"$runtime\")\" = {shlex.quote(uid + ':' + gid)}; printf %s \"$runtime\"").strip()
    if not re.fullmatch(r"/run/nex-lock-probe\.[A-Za-z0-9]{6}", runtime):
        raise RuntimeError("QEMU lock holder runtime directory is not private host-local storage")
    holder_log, pid_file = runtime + "/holder.log", runtime + "/qemu.pid"
    def stop_holder(pid):
        if not pid.isdecimal() or int(pid) <= 1:
            raise RuntimeError("cannot identify the QEMU lock holder safely")
        # Linux comm is truncated; match the executable and this unique drive
        # argument before signalling anything, then wait for confirmed exit.
        drive = shlex.quote("file=" + probe + ",format=qcow2,if=none")
        shell(first["host"], f"set -eu; test -d /proc/{pid} || exit 0; "
              f"test \"$(readlink -f /proc/{pid}/exe)\" = \"$(readlink -f \"$(command -v qemu-system-x86_64)\")\"; "
              f"tr '\\000' '\\n' < /proc/{pid}/cmdline | grep -Fqx -- {drive}; "
              f"kill {pid}; i=0; while test -d /proc/{pid}; do test $i -lt 50; i=$((i+1)); sleep .1; done")
    holder = (f"set -eu; qemu-img create -f qcow2 {shlex.quote(probe)} 64M >/dev/null; chown {uid}:{gid} {shlex.quote(probe)}; "
              f"touch {shlex.quote(holder_log)}; chown {uid}:{gid} {shlex.quote(holder_log)}; "
              f"setpriv {credentials} -- qemu-system-x86_64 -nodefaults -display none -S -daemonize "
              f"-pidfile {shlex.quote(pid_file)} -drive file={shlex.quote(probe)},format=qcow2,if=none > {shlex.quote(holder_log)} 2>&1; "
              f"test -s {shlex.quote(pid_file)}; kill -0 \"$(cat {shlex.quote(pid_file)})\"; sleep 1")
    success, holder_pid = False, None
    try:
        shell(first["host"], holder)
        holder_pid = shell(first["host"], f"cat {shlex.quote(pid_file)}").strip()
        if not holder_pid.isdecimal() or int(holder_pid) <= 1:
            raise RuntimeError("QEMU lock holder did not expose a live numeric PID")
        try:
            if shell_result(second["host"], f"test -f {shlex.quote(probe)}").returncode != 0:
                raise RuntimeError("probe did not appear through the second Core mount")
            blocked = shell_result(second["host"], f"setpriv {credentials} -- qemu-io -f qcow2 -c 'write 0 512' {shlex.quote(probe)}")
            locks = shell(first["host"], "lslocks -o COMMAND,PID,TYPE,MODE,PATH --json")
            error = (blocked.stdout + blocked.stderr).lower()
            if blocked.returncode == 0 or "lock" not in error:
                raise RuntimeError(f"second Core did not receive a QEMU writable-lock refusal: {blocked.stdout}{blocked.stderr}")
            if probe not in locks:
                raise RuntimeError("lslocks did not show the exact held QEMU probe image")
        finally:
            stop_holder(holder_pid)
        released = shell_result(second["host"], f"setpriv {credentials} -- qemu-io -f qcow2 -c 'write 0 512' {shlex.quote(probe)}")
        if released.returncode != 0:
            raise RuntimeError(f"second Core could not acquire qemu-io after release: {released.stderr.strip()}")
        evidence = {"probe": probe, "holder_pid": holder_pid, "runtime": runtime,
                "qemu_identity": {"uid": uid, "gid": gid, "groups": list(groups)},
                "holder_log": shell(first["host"], f"cat {shlex.quote(holder_log)}"),
                "blocked_output": blocked.stdout + blocked.stderr, "released_output": released.stdout + released.stderr, "lslocks": locks}
        success = True
        return evidence
    finally:
        if holder_pid is None:
            # A daemon may exist even if its launch/SSH command failed afterward.
            # Retain failed artifacts; signal only a positively identified holder.
            candidate = shell(first["host"], f"cat {shlex.quote(pid_file)}", check=False).strip()
            if candidate.isdecimal() and int(candidate) > 1:
                stop_holder(candidate)
        if success:
            shell(first["host"], f"rm -f {shlex.quote(probe)} {shlex.quote(holder_log)} {shlex.quote(pid_file)}; rmdir {shlex.quote(runtime)} 2>/dev/null || true", check=False)


def save_workload_stage(lab, result):
    (lab / f"{result['backend']}-workload-stage.json").write_text(
        json.dumps({**result, "completed": False}, indent=2) + "\n")


def run_backend(api, ids, backend, running_window=0, resume=None, refresh_resume_image=False, master_agent_outage_gate=False, master_agent_outage_vm_id=""):
    config, guest_config = ids["backends"][backend], ids["guest"]
    if backend == "iscsi_gfs2":
        wait_for_cluster(api, ids["pool_id"], config["storage_id"])
        require_member_fence_target(api.request("GET", "/api/v1/hosts"), ids["pool_id"], config.get("fence_host_id"))
    guests = []
    try:
        prepared_hosts = [{**host, "lab": ids["lab"], "storage_path": config["storage_path"]} for host in ids["hosts"]]
        images = [guest_image_info(host, guest_config["image_path"]) for host in prepared_hosts]
        if len({info["virtual-size"] for info in images}) != 1:
            raise RuntimeError("the disposable guest fixture differs across Cores")
        if resume and resume.exists():
            guests = resume_guests(api, ids, backend, resume, images[0]["virtual-size"])
            if backend == "iscsi_gfs2":
                save_resume(resume, backend, config["storage_id"], guests)
            if refresh_resume_image:
                for item in guests:
                    item["disk"] = install_guest_image(item["host"], item["vm_id"], guest_config["image_path"], images[0]["virtual-size"])
            retained = sum(json.loads(shell(item["host"], f"qemu-img info --output=json {shlex.quote(item['disk'])}")).get("actual-size", 0)
                           for item in guests)
        else:
            retained = 0
        fixture_capacity(api, config["storage_id"], images, retained)
        disk_gib = (images[0]["virtual-size"] + 1024**3 - 1) // 1024**3
        slots = missing_slots(guests, prepared_hosts)
        checkpoint_ids = {item["vm_id"] for item in guests}
        observed = api.request("GET", "/api/v1/vms")
        all_slots = [(host, ordinal) for host in prepared_hosts for ordinal in range(1, GUESTS_PER_HOST + 1)]
        exclusions = failed_create_exclusions(api, ids, backend, resume, prepared_hosts, observed, all_slots) if resume and resume.exists() else {}
        for host, ordinal in slots:
            canonical = f"cluster-{backend}-{host['lab_name']}-{ordinal}"
            excluded = exclusions.get((host["id"], ordinal))
            name = replacement_vm_name(canonical, excluded["vm_id"]) if excluded else canonical
            if any(item.get("name") in (canonical, name) and item.get("id") not in checkpoint_ids and
                   item.get("id") != (excluded or {}).get("vm_id") for item in observed):
                raise RuntimeError("planned VM name already exists outside its checkpoint")
        for host, ordinal in slots:
            wait_repository_host_ready(api, config["storage_id"], host["id"], backend)
            canonical = f"cluster-{backend}-{host['lab_name']}-{ordinal}"
            excluded = exclusions.get((host["id"], ordinal))
            name = replacement_vm_name(canonical, excluded["vm_id"]) if excluded else canonical
            created = api.request("POST", "/api/v1/vms", vm_body(ids, host, backend, ordinal, disk_gib, name))
            api.operation(created["operation"]["id"])
            item = {"host": host, "vm_id": created["vm_id"], "ordinal": ordinal, "name": name,
                    "disk": install_guest_image(host, created["vm_id"], guest_config["image_path"], images[0]["virtual-size"])}
            if backend == "iscsi_gfs2":
                item.update({"pool_id": ids["pool_id"], "canonical_spec": expected_vm_spec(ids, backend, host, ordinal, disk_gib, name)})
            guests.append(item)
            if resume:
                save_resume(resume, backend, config["storage_id"], guests)
        if len({item["disk"] for item in guests}) != 6:
            raise RuntimeError("six VMs did not receive six distinct Core-assigned disks")
        for host_id in {item["host"]["id"] for item in guests}:
            wait_repository_host_ready(api, config["storage_id"], host_id, backend)
        with ThreadPoolExecutor(max_workers=6) as pool:
            list(pool.map(lambda item: power(api, item["vm_id"], "start"), guests))
        with ThreadPoolExecutor(max_workers=6) as pool:
            initial_boot_ids = list(pool.map(lambda item: wait_for_guest(item["host"], item["vm_id"], guest_config), guests))
        require_distinct(initial_boot_ids, "guest boot IDs")
        if running_window:
            marker = ids["lab"] / f"six-vms-running-{backend}.json"
            marker.write_text(json.dumps({"backend": backend, "vms": [item["vm_id"] for item in guests],
                                          "window_seconds": running_window, "observed_at": time.time()}, indent=2) + "\n")
            print(f"six {backend} VMs are running; screenshot window: {running_window}s", flush=True)
            time.sleep(running_window)
        lock_probe = competing_writer_probe(guests, backend) if backend in LOCK_PROBE_BACKENDS else None
        # Preserve raw native lock evidence even if a later guest stage fails.
        if lock_probe:
            save_workload_stage(ids["lab"], {"backend": backend, "competing_writer_probe": lock_probe})
        write_barrier = int(time.time()) + 3
        with ThreadPoolExecutor(max_workers=6) as pool:
            writes = list(pool.map(lambda item: write_guest(item, backend, guest_config, write_barrier), guests))
        for item, (checksum, _) in zip(guests, writes):
            item["checksum"] = checksum
        for item, boot_id in zip(guests, initial_boot_ids):
            item["boot_id"] = boot_id
        write_windows = windows([output for _, output in writes])
        with ThreadPoolExecutor(max_workers=6) as pool:
            list(pool.map(lambda item: power(api, item["vm_id"], "reboot"), guests))
        with ThreadPoolExecutor(max_workers=6) as pool:
            list(pool.map(lambda item: wait_for_guest(item["host"], item["vm_id"], guest_config, item["checksum"], item["boot_id"]), guests))
        result = {"backend": backend, "storage_id": config["storage_id"], "vms": [{"id": item["vm_id"], "host_id": item["host"]["id"], "disk": item["disk"]} for item in guests], "write_windows": write_windows,
                  "write_duration_seconds": [item["ended"] - item["started"] for item in write_windows], "reboot_hashes": "passed"}
        if lock_probe:
            result["competing_writer_probe"] = lock_probe
        # Keep successful pre-fence evidence if the subsequent exclusion gate fails.
        save_workload_stage(ids["lab"], result)
        if master_agent_outage_gate:
            if backend != "iscsi_gfs2":
                raise RuntimeError("master-agent outage requires the clustered iSCSI backend")
            result["master_agent_outage"] = master_agent_outage(api, ids, guests, backend, guest_config, master_agent_outage_vm_id)
            save_workload_stage(ids["lab"], result)
        if backend == "iscsi_gfs2" and not master_agent_outage_gate:
            target = config.get("fence_host_id")
            if target not in {host["id"] for host in ids["hosts"]}:
                raise RuntimeError("iscsi_gfs2 requires fence_host_id from this pool")
            previous_proof = fence_proof(wait_for_cluster(api, ids["pool_id"], config["storage_id"]), target).get("observed_at", "")
            target_items = [item for item in guests if item["host"]["id"] == target]
            fence_barrier = int(time.time()) + 3
            clear_interruption_markers(target_items, backend, guest_config)
            pool = ThreadPoolExecutor(max_workers=6)
            try:
                writes = {pool.submit(interruption_write, item, backend, guest_config, fence_barrier): item for item in guests}
                time.sleep(max(0, fence_barrier - time.time()) + .2)
                target_markers = poll_interruption_markers(target_items, backend, guest_config, fence_barrier)
                require_pending_target_writes([future for future, item in writes.items() if item["host"]["id"] == target])
                dispatch_at = time.time()
                if any(marker >= dispatch_at for marker in target_markers.values()):
                    raise RuntimeError("target interruption marker was not observed before fence dispatch")
                api.request("POST", "/api/v1/storage/cluster/verify-fence", {"request_id": f"cluster-fence-{uuid.uuid4().hex}",
                            "pool_id": ids["pool_id"], "repository_id": config["storage_id"], "host_id": target,
                            "fence_confirmation": f"FENCE {target}"})
                operation = wait_for_fence_proof(api, ids["pool_id"], config["storage_id"], target, previous_proof)
                survivor_outputs, target_interrupted = [], 0
                for future, item in writes.items():
                    if item["host"]["id"] == target:
                        try:
                            future.result()
                        except Exception:
                            target_interrupted += 1
                        continue
                    survivor_outputs.append(future.result())
            finally:
                pool.shutdown(wait=True)
            proof = fence_proof(operation, target)
            target_host = next(host for host in ids["hosts"] if host["id"] == target)
            fenced_outer_is_off(ids["lab"], target_host["lab_name"])
            if target_interrupted != len(target_items):
                raise RuntimeError("fenced target completed its 60-second write instead of losing connection after dispatch")
            survivor_windows = proof_overlaps_survivors(proof["observed_at"], survivor_outputs)
            survivor_admission = admit_existing_survivor(api, ids, config, target_host, guests, guest_config)
            restore_fenced_outer(ids["lab"], target_host["lab_name"])
            wait_for_host(api, target)
            wait_for_cluster(api, ids["pool_id"], config["storage_id"])
            for item in guests:
                if item["host"]["id"] == target:
                    power(api, item["vm_id"], "start")
            with ThreadPoolExecutor(max_workers=6) as pool:
                list(pool.map(lambda item: wait_for_guest(item["host"], item["vm_id"], guest_config, item["checksum"], timeout=300), guests))
            result["fence_hashes"] = "passed"
            result["fence_interruption"] = {"target_interrupted": target_interrupted, "target_start_markers": target_markers, "dispatch_at": dispatch_at, "proof_observed_at": proof["observed_at"],
                                             "survivor_write_windows": survivor_windows,
                                             "survivor_existing_vm_admission": survivor_admission}
        return result
    finally:
        primary = sys.exc_info()[0] is not None
        try:
            stop_guests(api, guests)
        except Exception:
            if not primary:
                raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lab-dir", type=Path, required=True, help="explicit cluster-ha disposable lab")
    parser.add_argument("--ids", type=Path, required=True, help="private provisioner output inside that lab")
    parser.add_argument("--backend", choices=BACKENDS, help="run one backend; default runs all three")
    parser.add_argument("--result", type=Path, help="private raw result below the lab")
    parser.add_argument("--resume-vms", type=Path, help="private six-VM checkpoint below the selected lab; creates it when absent")
    parser.add_argument("--refresh-resume-image", action="store_true", help="replace only checkpointed stopped disks from the current lab fixture before API starts")
    parser.add_argument("--master-agent-outage", action="store_true", help="stop and restore only the observed core1 Core agent while six clustered-iSCSI guest writers run")
    parser.add_argument("--master-agent-outage-vm-id", help="pre-existing fresh stopped clustered-iSCSI member VM used only for the refusal check")
    parser.add_argument("--running-window-seconds", type=int, default=30, help="bounded UI screenshot window after all six guests boot")
    parser.add_argument("--execute", action="store_true", help="permit VM creation, guest writes, reboots and a real GFS2 fence")
    args = parser.parse_args()
    if not args.execute:
        raise SystemExit("--execute is required; this runner creates six disposable VMs per backend and fences one Core")
    if args.running_window_seconds < 0 or args.running_window_seconds > 60:
        raise SystemExit("--running-window-seconds must be between 0 and 60")
    lab = check_lab(args.lab_dir)
    if args.ids.resolve().parent != lab or args.ids.is_symlink():
        raise SystemExit("--ids must be a regular private file directly inside the selected lab")
    ids = json.loads(args.ids.read_text())
    ids["lab"] = lab
    selected = (args.backend,) if args.backend else BACKENDS
    if args.master_agent_outage and selected != ("iscsi_gfs2",):
        raise SystemExit("--master-agent-outage requires --backend iscsi_gfs2")
    if args.master_agent_outage and not args.master_agent_outage_vm_id:
        raise SystemExit("--master-agent-outage requires --master-agent-outage-vm-id")
    validate(ids, selected)
    result_path = inside_lab(lab, args.result or lab / "cluster-storage-workloads.json")
    resume_path = inside_lab(lab, args.resume_vms) if args.resume_vms else None
    if args.refresh_resume_image and not resume_path:
        raise SystemExit("--refresh-resume-image requires --resume-vms")
    api = API(ids["controller"], Path(ids["password_file"]).read_text().strip())
    report = {"completed": False, "resource_budget": f"six guest VMs at 512 MiB each; three {ids['outer_core_memory_mib']} MiB outer Cores", "backends": []}
    try:
        for backend in selected:
            report["backends"].append(run_backend(api, ids, backend, args.running_window_seconds, resume_path, args.refresh_resume_image, args.master_agent_outage, args.master_agent_outage_vm_id or ""))
        report["completed"] = True
    except Exception as exc:
        # This is private, raw lab evidence: preserve the actual native blocker rather
        # than leaving an indistinguishable incomplete report after cleanup.
        report["error"] = str(exc)
        raise
    finally:
        result_path.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
