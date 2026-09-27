#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Interrupt one disposable native snapshot consolidation and reconcile after daemon restart.

This probe is intentionally separate from recovery_s0.py. It embeds that probe's
native helpers on the selected Core and adds one bounded daemon-crash campaign.
An interrupted or ambiguous result keeps the domain and every disk image intact.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shlex
import subprocess
import sys
import uuid

ROOT = Path(__file__).resolve().parents[2]
REMOTE_CONFIG = None
SENTINEL = "NEX recovery S0 disposable lab"
PACKAGE_SET = (
    "libvirt-clients", "libvirt-common", "libvirt-daemon", "libvirt-daemon-common",
    "libvirt-daemon-config-network", "libvirt-daemon-config-nwfilter",
    "libvirt-daemon-driver-network", "libvirt-daemon-driver-nodedev",
    "libvirt-daemon-driver-nwfilter", "libvirt-daemon-driver-qemu",
    "libvirt-daemon-driver-secret", "libvirt-daemon-driver-storage",
    "libvirt-daemon-log", "libvirt-daemon-system", "libvirt0",
)
PACKAGE_VERSION = "11.3.0-3+deb13u3+nex2"
PACKAGE_MANIFEST_SHA256 = "926cf76145b10555bc535d68d2df38581dace88efc154ae70d3a37990d1a4f60"
STAGED_PACKAGE_DIR = "/var/tmp/recovery-s0-final-six-patch"
CORE_ROOT = Path("/var/lib/libvirt/images/nex-recovery-s0-interrupt")


def run(args, *, check=True, input=None, timeout=120):
    result = subprocess.run(args, input=input, text=True, capture_output=True, timeout=timeout)
    if check and result.returncode:
        raise RuntimeError(f"{args[0]} failed: {result.stderr.strip() or result.stdout.strip()}")
    return result


def validate_lab(path):
    path = Path(path)
    assert not path.is_symlink(), "candidate lab must not be a symlink"
    lab = path.resolve(strict=True)
    assert lab.parent == (ROOT / ".cache").resolve() and lab.name.startswith("recovery-s0-core."), "select the disposable recovery S0 lab"
    assert json.loads((lab / "lab.json").read_text()) == {"disposable": True, "fixture": "cluster-ha-v1"}, "candidate lab marker differs"
    for name in ("id_ed25519", "known_hosts", "ports.json"):
        item = lab / name
        assert item.is_file() and not item.is_symlink(), f"candidate lab is missing {name}"
    assert (lab / "id_ed25519").stat().st_mode & 0o077 == 0, "candidate SSH key permissions are too broad"
    return lab


def ssh_args(lab, node):
    ports = json.loads((lab / "ports.json").read_text())
    assert node in ports, "selected Core guest is not in the disposable lab"
    return ["ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
            "-o", "StrictHostKeyChecking=yes", "-o", f"UserKnownHostsFile={lab / 'known_hosts'}",
            "-i", str(lab / "id_ed25519"), "-p", str(ports[node]), "root@127.0.0.1"]


REMOTE_CAMPAIGN = r'''
import hashlib
import re
import signal
import sys
import xml.etree.ElementTree as ET

INTERRUPT_CONFIG = __INTERRUPT_CONFIG__
PACKAGE_SET = tuple(INTERRUPT_CONFIG["packages"])
PACKAGE_VERSION = INTERRUPT_CONFIG["package_version"]
PACKAGE_MANIFEST_SHA256 = INTERRUPT_CONFIG["package_manifest_sha256"]
SENTINEL = INTERRUPT_CONFIG["sentinel"]

def active_blockjob(output):
    return bool(re.search(r"(?im)^\s*block commit:\s*\[\s*\d+(?:\.\d+)?\s*%\s*\]", output))


def blockjob_state(result):
    message = (result.stdout + " " + result.stderr).lower()
    if result.returncode == 0 and active_blockjob(result.stdout + "\n" + result.stderr):
        return "active"
    if any(known in message for known in ("no current block job", "domain is not running")):
        return "inactive"
    return "unknown"

def verify_candidate():
    packages = run("dpkg-query", "-W", "-f=${binary:Package} ${Version}\n", *PACKAGE_SET)
    rows = [line.split() for line in packages.stdout.splitlines()]
    installed = {row[0].split(":", 1)[0]: row[-1] for row in rows if len(row) == 2}
    if packages.returncode or installed != {name: PACKAGE_VERSION for name in PACKAGE_SET}:
        raise RuntimeError("Core does not have the exact matched libvirt package set")
    artifact = Path(INTERRUPT_CONFIG["staged_package_dir"]) / "selected-packages.json"
    if not artifact.is_file():
        raise RuntimeError("matched libvirt package handoff is missing on Core")
    manifest = json.loads(artifact.read_text())
    entries = manifest.get("packages", {})
    if (manifest.get("artifact_manifest_sha256") != PACKAGE_MANIFEST_SHA256 or
            len(entries) != len(PACKAGE_SET) or
            {row.get("package") for row in entries.values()} != set(PACKAGE_SET)):
        raise RuntimeError("matched four-patch package manifest differs")
    for filename, row in entries.items():
        package_file = artifact.parent / filename
        if (package_file.is_symlink() or not package_file.is_file() or
                hashlib.sha256(package_file.read_bytes()).hexdigest() != row.get("sha256")):
            raise RuntimeError(f"staged package hash differs: {filename}")
        metadata = run("dpkg-deb", "-f", package_file, "Package", "Version", "Architecture")
        values = [field.split(": ", 1)[1] for field in metadata.stdout.splitlines()]
        if values != [row.get("package"), PACKAGE_VERSION, row.get("architecture")]:
            raise RuntimeError(f"staged package metadata differs: {filename}")
    return {"packages": packages.stdout, "artifact_manifest_sha256": PACKAGE_MANIFEST_SHA256,
            "staged_package_hashes_verified": len(entries),
            "virsh_version": run("virsh", "-c", URI, "version").stdout}


def active_daemon():
    candidates = []
    for unit in ("libvirtd.service", "virtqemud.service"):
        result = run("systemctl", "show", unit, "-p", "ActiveState", "-p", "MainPID", check=False)
        values = dict(line.split("=", 1) for line in result.stdout.splitlines() if "=" in line)
        pid = int(values.get("MainPID", "0"))
        if values.get("ActiveState") == "active" and pid > 1:
            candidates.append({"unit": unit, "pid": pid, "properties": result.stdout.strip()})
    if len(candidates) != 1:
        raise RuntimeError(f"cannot identify exactly one active libvirt daemon: {candidates}")
    return candidates[0]


def write_point(name, targets, values):
    paths = source_paths(name, targets)
    for target in targets:
        value = values[target]
        qemu_io(paths[target], "write", value["marker"])
        qemu_io(paths[target], "read", value["marker"])
        write = run("qemu-io", "-f", "qcow2", "-c",
                    f"write -P 0x{value['payload']:02x} {value['offset']} {value['length']}",
                    paths[target], check=False, timeout=90)
        if write.returncode:
            raise RuntimeError(f"payload write failed for {target}: {write.stderr.strip()}")
    return paths


def read_value(path, value):
    result = run("qemu-io", "-f", "qcow2", "-c",
                 f"read -P 0x{value['payload']:02x} {value['offset']} {value['length']}",
                 path, check=False, timeout=90)
    return {"ok": result.returncode == 0, "output": result.stdout.strip(),
            "error": result.stderr.strip()}


def read_markers(path, value):
    result = run("qemu-io", "-f", "qcow2", "-c",
                 f"read -P 0x{value['marker']:02x} 4096 4096", path,
                 check=False, timeout=30)
    return {"ok": result.returncode == 0, "output": result.stdout.strip(),
            "error": result.stderr.strip()}


def disk_read_completed(result, length, offset):
    expected = f"read {length}/{length} bytes at offset {offset}"
    return not result.get("error") and expected in result.get("output", "")


def point_values(index, target_index):
    return {"marker": 0x10 + index * 0x10 + target_index,
            "payload": 0x50 + index * 0x10 + target_index,
            "offset": (8, 16, 96)[index] << 20,
            "length": (80 if index == 1 else 8) << 20}


def current_values(targets):
    return {target: {"marker": 0x40 + index,
                     "payload": 0x80 + index,
                     "offset": 104 << 20, "length": 8 << 20}
            for index, target in enumerate(targets)}


def persisted_domain_status(name):
    path = Path("/run/libvirt/qemu") / f"{name}.xml"
    try:
        xml = path.read_text()
    except OSError as exc:
        return {"path": str(path), "xml": None, "jobs": [],
                "snapshot_delete_marker": False, "error": str(exc)}
    root = ET.fromstring(xml)
    blockjobs = root.find("./blockjobs")
    jobs = [] if blockjobs is None else [
        {"name": job.get("name"), "state": job.get("state"),
         "type": job.get("type"),
         "disks": [disk.get("dst") for disk in job.findall("./disk")]}
        for job in blockjobs.findall("./blockjob")]
    marker = snapshot_delete_marker(xml)
    return {"path": str(path), "xml": xml, "jobs": jobs,
            "blockjobs_active": blockjobs is not None and blockjobs.get("active") == "yes",
            "snapshot_delete_marker": marker}


def snapshot_delete_marker(status_xml):
    root = ET.fromstring(status_xml)
    return any(node.tag.rsplit("}", 1)[-1] == "snapshotDelete"
               for node in root.iter())


def monitor_jobs(name, query):
    request = json.dumps({"execute": query})
    result = virsh("qemu-monitor-command", name, "--pretty", request, check=False)
    try:
        response = json.loads(result.stdout)
    except json.JSONDecodeError:
        return {"ok": False, "returncode": result.returncode,
                "stdout": result.stdout.strip(), "stderr": result.stderr.strip()}
    return {"ok": result.returncode == 0 and "return" in response,
            "return": response.get("return"), "stdout": result.stdout.strip(),
            "stderr": result.stderr.strip()}


def restart_job_observation(name, targets):
    state = virsh("domstate", name, check=False)
    state_name = state.stdout.strip().lower()
    disks = {target: virsh("blockjob", name, target, "--info", check=False)
             for target in targets}
    disk_states = {target: blockjob_state(result) for target, result in disks.items()}
    generic = virsh("domjobinfo", name, check=False)
    generic_text = (generic.stdout + " " + generic.stderr).lower()
    generic_none = ((generic.returncode == 0 and re.search(
        r"(?im)^\s*job type:\s*none\s*$", generic.stdout)) or
        (state_name == "shut off" and generic.returncode != 0 and
         "domain is not running" in generic_text))
    persisted = persisted_domain_status(name)
    qmp_block = qmp_jobs = None
    if state_name in ("running", "paused"):
        qmp_block = monitor_jobs(name, "query-block-jobs")
        qmp_jobs = monitor_jobs(name, "query-jobs")
    qmp_empty = (state_name == "shut off" or
                 (qmp_block is not None and qmp_block.get("ok") and qmp_block.get("return") == [] and
                  qmp_jobs is not None and qmp_jobs.get("ok") and qmp_jobs.get("return") == []))
    status_clear = ((state_name == "shut off" and "error" in persisted) or
                    ("error" not in persisted and not persisted["jobs"] and
                     not persisted.get("blockjobs_active", False)))
    terminal = (state.returncode == 0 and state_name in ("paused", "shut off") and
                all(value == "inactive" for value in disk_states.values()) and
                generic_none and status_clear and qmp_empty)
    return {"domain_state": state_name, "domain_state_returncode": state.returncode,
            "disk_jobs": {target: {"state": disk_states[target],
                                   "returncode": result.returncode,
                                   "stdout": result.stdout.strip(),
                                   "stderr": result.stderr.strip()}
                          for target, result in disks.items()},
            "generic_job": {"none": bool(generic_none), "returncode": generic.returncode,
                            "stdout": generic.stdout.strip(), "stderr": generic.stderr.strip()},
            "qmp_block_jobs": qmp_block, "qmp_jobs": qmp_jobs,
            "persisted_status": persisted, "terminal": terminal}


def restart_recovery_ready(snapshot_delete_marker, observation):
    return bool(snapshot_delete_marker and observation.get("terminal") and
                observation.get("domain_state") in ("paused", "shut off"))


def oldest_delete_retry_ready(snapshot_delete_marker, terminal_jobs, labels, current,
                              parents, flags, point_a_readable, integrity_errors):
    return bool(snapshot_delete_marker and terminal_jobs and
                set(labels) == {"point-a", "point-b", "point-c"} and
                current == "point-c" and
                parents == {"point-a": None, "point-b": "point-a", "point-c": "point-b"} and
                not any(flags.values()) and point_a_readable and not integrity_errors)


def stop_delete_client(process):
    if process is None:
        return None
    was_running = process.poll() is None
    action = "terminate" if was_running else "already-exited"
    if was_running:
        process.terminate()
        try:
            stdout, stderr = process.communicate(timeout=3)
        except subprocess.TimeoutExpired:
            process.kill()
            action = "kill-after-terminate-timeout"
            stdout, stderr = process.communicate()
    else:
        stdout, stderr = process.communicate()
    return {"was_running": was_running, "client_action": action,
            "returncode": process.returncode, "stdout": stdout.strip(), "stderr": stderr.strip()}


def inspect_domain(name, targets, expected_labels, expected_points, current_expected):
    state = virsh("domstate", name, check=False)
    labels_result = virsh("snapshot-list", name, "--name", check=False)
    labels = [label.strip() for label in labels_result.stdout.splitlines() if label.strip()]
    current_snapshot = virsh("snapshot-current", name, "--name", check=False)
    evidence = {"domain_state": state.stdout.strip().lower(), "labels": labels,
                "current_snapshot": current_snapshot.stdout.strip(),
                "current": {}, "snapshots": {}, "jobs": {}, "integrity_errors": []}
    if state.returncode or evidence["domain_state"] != "shut off":
        evidence["integrity_errors"].append("domain is not observably shut off")
    if labels_result.returncode:
        evidence["integrity_errors"].append("snapshot list is unavailable")
    if current_snapshot.returncode or evidence["current_snapshot"] != "point-c":
        evidence["integrity_errors"].append("current recovery-point identity is unavailable or changed")
    for target in targets:
        job = virsh("blockjob", name, target, "--info", check=False)
        evidence["jobs"][target] = {"returncode": job.returncode,
                                    "stdout": job.stdout.strip(), "stderr": job.stderr.strip()}
        state = blockjob_state(job)
        evidence["jobs"][target]["state"] = state
        if state == "unknown":
            evidence["integrity_errors"].append(f"blockjob observation failed for {target}")
    active_jobs = [target for target, job in evidence["jobs"].items()
                   if job["state"] == "active"]
    if (evidence["domain_state"] != "shut off" or active_jobs or
            any(error.startswith("blockjob observation failed") for error in evidence["integrity_errors"])):
        evidence["integrity_errors"].append("domain or disk block job is still active; image reads were skipped")
        for label in labels:
            result = virsh("snapshot-dumpxml", name, label, check=False)
            if result.returncode:
                evidence["integrity_errors"].append(f"snapshot XML unavailable: {label}")
                continue
            root = ET.fromstring(result.stdout)
            evidence["snapshots"][label] = {
                "xml": result.stdout,
                "parent": root.findtext("./parent/name"),
                "delete_flags": {node.get("name"): bool(snapshot_delete_flags(node))
                                 for node in root.findall("./disks/disk")},
                "disks": {},
            }
        if set(labels) != set(expected_labels):
            evidence["integrity_errors"].append(f"snapshot labels differ from expected set: {labels}")
        return evidence
    active = source_paths(name, targets)
    for target, path in active.items():
        expected = current_expected[target]
        marker = read_markers(path, expected)
        payload = read_value(path, expected)
        chain = chain_observation(path)
        evidence["current"][target] = {"head": path, "marker": marker, "payload": payload, "chain": chain}
        images = chain.get("images", [])
        if (not marker["ok"] or not payload["ok"] or "error" in chain or not images or
                images[0].get("filename") != path or any(
                    not Path(image.get("filename", "")).is_file() for image in images)):
            evidence["integrity_errors"].append(f"current {target} marker, payload or chain did not verify")
    for label in labels:
        result = virsh("snapshot-dumpxml", name, label, check=False)
        if result.returncode:
            evidence["integrity_errors"].append(f"snapshot XML unavailable: {label}")
            continue
        root = ET.fromstring(result.stdout)
        saved, post = snapshot_disk_paths(root)
        parent = root.findtext("./parent/name")
        flags = {node.get("name"): bool(snapshot_delete_flags(node))
                 for node in root.findall("./disks/disk")}
        evidence["snapshots"][label] = {"xml": result.stdout, "parent": parent,
                                         "saved_sources": saved, "post_point_sources": post,
                                         "delete_flags": flags, "disks": {}}
        expected_for_label = expected_points.get(label)
        for target in targets:
            path = saved.get(target)
            if not path:
                evidence["integrity_errors"].append(f"snapshot {label} lacks saved disk {target}")
                continue
            expected = expected_for_label[target] if expected_for_label and target in expected_for_label else None
            disk = {"saved_source": path, "chain": chain_observation(path)}
            if expected:
                disk["marker"] = read_markers(path, expected)
                disk["payload"] = read_value(path, expected)
                if not disk["marker"]["ok"] or not disk["payload"]["ok"]:
                    evidence["integrity_errors"].append(f"snapshot {label}/{target} marker or payload did not verify")
            images = disk["chain"].get("images", [])
            if ("error" in disk["chain"] or not images or
                    images[0].get("filename") != path or any(
                        not Path(image.get("filename", "")).is_file() for image in images)):
                evidence["integrity_errors"].append(f"snapshot {label}/{target} chain is unreadable")
            evidence["snapshots"][label]["disks"][target] = disk
    referenced = set()
    for current in evidence["current"].values():
        referenced.update(image.get("filename") for image in current["chain"].get("images", []))
    for snapshot in evidence["snapshots"].values():
        for disk in snapshot["disks"].values():
            referenced.update(image.get("filename") for image in disk["chain"].get("images", []))
    for label, snapshot in evidence["snapshots"].items():
        snapshot["post_point_head_status"] = {}
        for target, path in snapshot["post_point_sources"].items():
            if Path(path).is_file():
                snapshot["post_point_head_status"][target] = "present"
            elif path in referenced:
                snapshot["post_point_head_status"][target] = "missing-but-referenced"
                evidence["integrity_errors"].append(f"snapshot {label}/{target} post-point head is missing but referenced")
            else:
                snapshot["post_point_head_status"][target] = "missing-unreferenced-branch-head"
    if set(labels) != set(expected_labels):
        evidence["integrity_errors"].append(f"snapshot labels differ from expected set: {labels}")
    return evidence


def wait_for_blockjob(name, targets, process, timeout):
    started = time.monotonic()
    last = {}
    while time.monotonic() - started < timeout:
        for target in targets:
            job = virsh("blockjob", name, target, "--info", check=False)
            last[target] = {"returncode": job.returncode,
                            "stdout": job.stdout.strip(), "stderr": job.stderr.strip()}
            state = blockjob_state(job)
            last[target]["state"] = state
            if state == "unknown":
                return {"observation_error": target, "last": last,
                        "elapsed_seconds": time.monotonic() - started}
            if state == "active" and process.poll() is None:
                return {"target": target, "job": last[target], "elapsed_seconds": time.monotonic() - started}
        if process.poll() is not None:
            return {"completed_before_boundary": True, "returncode": process.returncode,
                    "last": last, "elapsed_seconds": time.monotonic() - started}
        time.sleep(0.05)
    return {"timeout": True, "last": last, "elapsed_seconds": time.monotonic() - started}


def interrupt_campaign(cfg):
    global ROOT, IMAGE_ROOT, REPORT
    ROOT = Path(cfg["root"])
    IMAGE_ROOT = ROOT
    REPORT = ROOT / "result.json"
    name = cfg["domain"]
    work = ROOT / name
    if work.parent != ROOT or work.is_symlink() or (work / "sentinel").read_text() != SENTINEL:
        raise RuntimeError("interruption work directory marker or boundary differs")
    if any(item.name != "sentinel" for item in work.iterdir()):
        raise RuntimeError("interruption work directory is not empty")
    packages = verify_candidate()
    existing = [item for item in virsh("list", "--all", "--name").stdout.splitlines() if item]
    preflight = []
    targets = ["vda", "vdb"]
    for item in existing:
        prior_interruption = bool(re.fullmatch(r"nex-recovery-s0-interrupt-[0-9a-f]{12}", item))
        if not item.startswith("nex-recovery-s0-fw-") and not prior_interruption:
            raise RuntimeError(f"unexpected domain on assigned Core candidate: {item}")
        state = virsh("domstate", item).stdout.strip().lower()
        if state != "shut off":
            raise RuntimeError(f"assigned Core candidate is not idle: {item} is {state}")
        labels = [label for label in virsh("snapshot-list", item, "--name").stdout.splitlines() if label]
        flags = []
        for label in labels:
            xml = ET.fromstring(virsh("snapshot-dumpxml", item, label).stdout)
            flags.extend(f"{label}/{node.get('name')}" for node in xml.findall("./disks/disk")
                         if snapshot_delete_flags(node))
        if flags:
            raise RuntimeError(f"assigned Core candidate has unresolved snapshot deletion flags: {item}")
        record = {"domain": item, "state": state, "snapshots": labels}
        if prior_interruption:
            prior_work = ROOT / item
            if (prior_work.parent != ROOT or prior_work.is_symlink() or
                    not (prior_work / "sentinel").is_file() or
                    (prior_work / "sentinel").read_text() != SENTINEL):
                raise RuntimeError(f"prior interruption fixture sentinel differs: {item}")
            if domain_targets(item) != targets:
                raise RuntimeError(f"prior interruption fixture disk targets differ: {item}")
            if set(labels) != {"point-b", "point-c"} or virsh("snapshot-current", item, "--name").stdout.strip() != "point-c":
                raise RuntimeError(f"prior interruption fixture snapshot set/current point differs: {item}")
            for target in targets:
                job = virsh("blockjob", item, target, "--info", check=False)
                if blockjob_state(job) != "inactive":
                    raise RuntimeError(f"prior interruption fixture job status is not observably idle: {item}/{target}")
            record["prior_interruption_preserved"] = True
        preflight.append(record)
    result = {"status": "incomplete", "domain": name, "work": str(work),
              "packages": packages, "candidate_preflight": preflight,
              "targets": targets, "steps": [], "preserve": True}
    if name in existing:
        raise RuntimeError("generated interruption domain already exists")
    disks = [work / "disk-0.qcow2", work / "disk-1.qcow2"]
    domain = ["<domain type='kvm'>", f"<name>{name}</name>",
              "<memory unit='MiB'>256</memory><currentMemory unit='MiB'>256</currentMemory>",
              "<vcpu>1</vcpu><os><type arch='x86_64' machine='pc'>hvm</type><boot dev='hd'/></os>",
              "<features><acpi/><apic/></features><devices>",
              "<emulator>/usr/bin/qemu-system-x86_64</emulator>"]
    for target, disk in zip(targets, disks):
        run("qemu-img", "create", "-f", "qcow2", disk, "128M")
        os.chmod(disk, 0o600)
        domain.append(f"<disk type='file' device='disk'><driver name='qemu' type='qcow2' cache='none'/><source file={quoteattr(str(disk))}/><target dev='{target}' bus='virtio'/></disk>")
    domain.append("</devices></domain>")
    (work / "domain.xml").write_text("".join(domain))
    defined = False
    expected = {}
    points = {}
    delete = None
    try:
        virsh("define", work / "domain.xml")
        defined = True
        result["domain_uuid"] = virsh("domuuid", name).stdout.strip()
        virsh("start", name)
        if virsh("domstate", name).stdout.strip().lower() not in ("running", "paused"):
            raise RuntimeError("test domain did not start for ownership preparation")
        virsh("destroy", name)
        if virsh("domstate", name).stdout.strip().lower() != "shut off":
            raise RuntimeError("test domain did not stop before snapshot creation")
        for disk_index, target in enumerate(targets):
            expected[target] = point_values(0, disk_index)
        write_point(name, targets, expected)
        snapshot(name, "point-a", targets)
        expected = {target: point_values(1, index) for index, target in enumerate(targets)}
        write_point(name, targets, expected)
        snapshot(name, "point-b", targets)
        expected = {target: point_values(2, index) for index, target in enumerate(targets)}
        write_point(name, targets, expected)
        snapshot(name, "point-c", targets)
        current = current_values(targets)
        write_point(name, targets, current)
        points = {label: {target: point_values(index, disk_index)
                          for disk_index, target in enumerate(targets)}
                  for index, label in enumerate(("point-a", "point-b", "point-c"))}
        before = inspect_domain(name, targets, ("point-a", "point-b", "point-c"), points, current)
        result["before"] = before
        if before["integrity_errors"]:
            result["status"] = "preflight-failed-preserved"
            result["error"] = "snapshot chain did not pass pre-interruption validation"
            return result
        old_daemon = active_daemon()
        result["daemon_before"] = old_daemon
        delete = subprocess.Popen(["virsh", "-c", URI, "snapshot-delete", name, "point-a"],
                                  text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                  start_new_session=True)
        boundary = wait_for_blockjob(name, targets, delete, cfg["boundary_timeout_seconds"])
        result["blockjob_boundary"] = boundary
        if not boundary.get("target"):
            result["delete_command"] = stop_delete_client(delete)
            result["status"] = "boundary-not-observed-preserved"
            result["preserve"] = True
            return result
        target = boundary["target"]
        result["persisted_status_at_boundary"] = persisted_domain_status(name)
        actual_daemon = active_daemon()
        if actual_daemon != old_daemon:
            raise RuntimeError("libvirt daemon identity changed before the planned interruption")
        last_job = virsh("blockjob", name, target, "--info", check=False)
        last_state = blockjob_state(last_job)
        if delete.poll() is not None or last_state != "active":
            result["stale_blockjob_observation"] = {"target": target,
                                                    "returncode": last_job.returncode,
                                                    "stdout": last_job.stdout.strip(),
                                                    "stderr": last_job.stderr.strip(),
                                                    "state": last_state}
            result["delete_command"] = stop_delete_client(delete)
            result["status"] = "blockjob-boundary-ended-preserved"
            result["preserve"] = True
            return result
        result["final_blockjob_observation"] = {
            "target": target,
            "observed_at_utc": datetime.now(timezone.utc).isoformat(),
            "returncode": last_job.returncode,
            "stdout": last_job.stdout.strip(),
            "stderr": last_job.stderr.strip(),
            "state": last_state,
        }
        os.kill(old_daemon["pid"], signal.SIGKILL)
        result["daemon_kill"] = {"unit": old_daemon["unit"], "pid": old_daemon["pid"], "signal": "SIGKILL"}
        time.sleep(0.25)
        start = run("systemctl", "start", old_daemon["unit"], check=False, timeout=30)
        result["daemon_start"] = {"returncode": start.returncode,
                                  "stdout": start.stdout.strip(), "stderr": start.stderr.strip()}
        new_daemon = None
        for _ in range(100):
            try:
                new_daemon = active_daemon()
                if new_daemon["pid"] != old_daemon["pid"]:
                    break
            except Exception:
                pass
            time.sleep(0.1)
        result["daemon_after"] = new_daemon
        if start.returncode or not new_daemon or new_daemon["pid"] == old_daemon["pid"]:
            result["delete_command"] = stop_delete_client(delete)
            result["status"] = "daemon-restart-unresolved-preserved"
            result["error"] = "libvirt daemon did not return with a new process identity"
            return result
        result["delete_command"] = stop_delete_client(delete)
        observations = []
        restarted = None
        for _ in range(100):
            restarted = restart_job_observation(name, targets)
            observations.append(restarted)
            if restarted["terminal"]:
                break
            time.sleep(0.1)
        result["restart_reconciliation"] = {
            "snapshot_delete_marker_at_boundary": result["persisted_status_at_boundary"].get(
                "snapshot_delete_marker", False),
            "observations": observations,
            "terminal": bool(restarted and restarted["terminal"]),
            "recovery_ready": bool(restarted and restart_recovery_ready(
                result["persisted_status_at_boundary"].get("snapshot_delete_marker", False),
                restarted)),
        }
        if not restarted or not restarted["terminal"]:
            result["status"] = "interrupted-state-preserved-unresolved"
            result["retry"] = "not attempted; restarted native jobs are not terminal"
            return result

        domain_uuid = virsh("domuuid", name, check=False)
        sentinel = run("cat", work / "sentinel", check=False)
        if (domain_uuid.returncode or domain_uuid.stdout.strip() != result["domain_uuid"] or
                sentinel.returncode or sentinel.stdout.strip() != SENTINEL):
            result["status"] = "restart-fixture-identity-unresolved-preserved"
            result["retry"] = "not attempted; selected fixture identity changed"
            return result
        if restarted["domain_state"] == "paused":
            stopped = virsh("destroy", name, check=False)
            stopped_state = virsh("domstate", name, check=False)
            result["stop_paused_guest_after_terminal_jobs"] = {
                "returncode": stopped.returncode,
                "stdout": stopped.stdout.strip(), "stderr": stopped.stderr.strip(),
                "domain_state": stopped_state.stdout.strip().lower(),
            }
            if (stopped.returncode or stopped_state.returncode or
                    stopped_state.stdout.strip().lower() != "shut off"):
                result["status"] = "paused-guest-stop-unresolved-preserved"
                result["retry"] = "not attempted; synthetic guest did not stop cleanly"
                return result
        elif restarted["domain_state"] != "shut off":
            result["status"] = "restart-domain-state-unresolved-preserved"
            result["retry"] = "not attempted; only paused or shutoff test domains are accepted"
            return result

        if not result["restart_reconciliation"]["recovery_ready"]:
            result["status"] = "snapshot-delete-marker-missing-preserved"
            result["retry"] = "not attempted; persisted native status lacked snapshotDelete"
            return result

        graph = [label for label in virsh("snapshot-list", name, "--name").stdout.splitlines() if label]
        snapshot_flags = snapshot_flags_for_domain(name)
        expected_points = {"point-b": points["point-b"], "point-c": points["point-c"]}
        reconciled = inspect_domain(name, targets, ("point-a", "point-b", "point-c"),
                                    expected_points, current)
        parents = {label: snap.get("parent") for label, snap in reconciled["snapshots"].items()}
        result["after_native_cancellation"] = {
            "graph": graph, "parents": parents,
            "flags": snapshot_flags, "domain": reconciled,
        }
        point_a = {}
        for target, disk in reconciled["snapshots"]["point-a"]["disks"].items():
            value = points["point-a"][target]
            path = disk["saved_source"]
            marker = read_markers(path, value)
            payload = read_value(path, value)
            point_a[target] = {"saved_source": path, "marker": marker,
                               "marker_read_completed": disk_read_completed(marker, 4096, 4096),
                               "payload": payload,
                               "payload_read_completed": disk_read_completed(
                                   payload, value["length"], value["offset"])}
        result["point_a_after_restart_cancel"] = point_a
        point_a_readable = (set(point_a) == set(targets) and all(
            item["marker_read_completed"] and item["payload_read_completed"]
            for item in point_a.values()))
        retry_ready = oldest_delete_retry_ready(
            result["restart_reconciliation"]["snapshot_delete_marker_at_boundary"],
            restarted["terminal"], graph, virsh("snapshot-current", name, "--name").stdout.strip(),
            parents, snapshot_flags, point_a_readable, reconciled["integrity_errors"])
        result["reconciled_retry_ready"] = retry_ready
        if not retry_ready:
            result["status"] = "post-restart-reconciliation-unresolved-preserved"
            result["retry"] = "not attempted; graph, flags, or disk reads did not reconcile"
            return result
        retry = virsh("snapshot-delete", name, "point-a", check=False, timeout=300)
        result["reconciled_point_a_delete"] = {
            "attempted_once": True, "returncode": retry.returncode,
            "stdout": retry.stdout.strip(), "stderr": retry.stderr.strip(),
        }
        if retry.returncode:
            result["status"] = "reconciled-delete-failed-preserved"
            result["retry"] = "not repeated; native delete command failed"
            return result

        after_labels = [label for label in virsh("snapshot-list", name, "--name").stdout.splitlines() if label]
        after_flags = snapshot_flags_for_domain(name)
        expected_after = inspect_domain(
            name, targets, ("point-b", "point-c"), expected_points, current)
        after_parents = {label: snap.get("parent")
                         for label, snap in expected_after["snapshots"].items()}
        after_state = restart_job_observation(name, targets)
        if (set(after_labels) != {"point-b", "point-c"} or
                after_parents != {"point-b": None, "point-c": "point-b"} or
                virsh("snapshot-current", name, "--name").stdout.strip() != "point-c" or
                any(after_flags.values()) or expected_after["integrity_errors"] or
                not after_state["terminal"]):
            result["status"] = "post-delete-retained-data-unresolved-preserved"
            result["post_delete"] = {"labels": after_labels, "parents": after_parents,
                                     "flags": after_flags, "domain": expected_after,
                                     "jobs": after_state}
            return result

        reopened = virsh("start", name, check=False)
        reopen_state = virsh("domstate", name, check=False)
        reopen_jobs = restart_job_observation(name, targets)
        result["qemu_reopen"] = {"returncode": reopened.returncode,
                                 "stdout": reopened.stdout.strip(),
                                 "stderr": reopened.stderr.strip(),
                                 "state": reopen_state.stdout.strip().lower(),
                                 "jobs": reopen_jobs}
        if (reopened.returncode or reopen_state.returncode or
                reopen_state.stdout.strip().lower() not in ("running", "paused") or
                any(job["state"] != "inactive" for job in reopen_jobs["disk_jobs"].values())):
            result["status"] = "qemu-reopen-unresolved-preserved"
            return result
        stopped = virsh("destroy", name, check=False)
        final_state = virsh("domstate", name, check=False)
        final_jobs = restart_job_observation(name, targets)
        final_domain = inspect_domain(name, targets, ("point-b", "point-c"),
                                      expected_points, current)
        result["final"] = {"destroy": {"returncode": stopped.returncode,
                                        "stdout": stopped.stdout.strip(),
                                        "stderr": stopped.stderr.strip()},
                           "domain_state": final_state.stdout.strip().lower(),
                           "jobs": final_jobs, "domain": final_domain}
        if (stopped.returncode or final_state.returncode or
                final_state.stdout.strip().lower() != "shut off" or
                not final_jobs["terminal"] or final_domain["integrity_errors"]):
            result["status"] = "final-reconciliation-unresolved-preserved"
            return result
        virsh("undefine", name, "--snapshots-metadata")
        if virsh("dominfo", name, check=False).returncode == 0:
            raise RuntimeError("verified disposable domain remained defined after cleanup")
        shutil.rmtree(work)
        result["cleanup"] = "removed-after-native-cancel-retry-and-reopen"
        result["status"] = "passed"
        result["preserve"] = False
        return result
    except Exception as exc:
        result["error"] = str(exc)
        result["status"] = "failed-preserved" if defined else "failed-before-domain"
        result["preserve"] = defined
        if delete is not None:
            result["delete_client_after_exception"] = stop_delete_client(delete)
        try:
            if defined:
                result["final_observation"] = inspect_domain(
                    name, targets, ("point-a", "point-b", "point-c"), points if expected else {},
                    current_values(targets))
        except Exception as observe_error:
            result["observation_error"] = str(observe_error)
        return result

'''
compile(REMOTE_CAMPAIGN.replace("__INTERRUPT_CONFIG__", "{}"),
        "recovery_s0_interrupt_remote.py", "exec")


def build_remote_payload(helper_source, config, *, invoke=False):
    payload = helper_source + "\n" + REMOTE_CAMPAIGN.replace("__INTERRUPT_CONFIG__", repr(config))
    if invoke:
        payload += "\nresult = interrupt_campaign(INTERRUPT_CONFIG)\n"
        payload += "print(json.dumps(result, indent=2))\n"
        payload += "if result.get('status') != 'passed': sys.exit(1)\n"
    return payload


def native_helper_source():
    helper_path = ROOT / "tests/integration/recovery_s0.py"
    source = helper_path.read_text()
    footer = "\nif REMOTE_CONFIG is None:\n"
    if footer not in source:
        raise RuntimeError("native S0 helper boundary changed; review before running interruption probe")
    return source.split(footer, 1)[0].replace(
        "ROOT = Path(__file__).resolve().parents[2]", "ROOT = Path('/')")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lab-dir", type=Path, required=True)
    parser.add_argument("--evidence", type=Path, required=True, help="new ignored .cache evidence directory")
    parser.add_argument("--boundary-timeout", type=float, default=15.0)
    args = parser.parse_args()
    if args.boundary_timeout <= 0 or args.boundary_timeout > 60:
        parser.error("--boundary-timeout must be between 0 and 60 seconds")
    lab = validate_lab(args.lab_dir)
    evidence = args.evidence.resolve()
    assert not args.evidence.exists(), "evidence directory must be new"
    assert evidence.is_relative_to((ROOT / ".cache").resolve()), "raw evidence must stay in ignored .cache"
    helper_source = native_helper_source()
    run_id = uuid.uuid4().hex[:12]
    domain = f"nex-recovery-s0-interrupt-{run_id}"
    work = CORE_ROOT / domain
    config = {"root": str(CORE_ROOT), "domain": domain,
              "boundary_timeout_seconds": args.boundary_timeout,
              "packages": list(PACKAGE_SET), "package_version": PACKAGE_VERSION,
              "package_manifest_sha256": PACKAGE_MANIFEST_SHA256,
              "sentinel": SENTINEL,
              "staged_package_dir": STAGED_PACKAGE_DIR}
    payload = build_remote_payload(helper_source, config, invoke=True)
    evidence.mkdir(parents=True, mode=0o700)
    (evidence / "candidate.json").write_text(json.dumps({"candidate": "Core B", "domain": domain,
                                                          "work": str(work), "status": "not-run"}, indent=2) + "\n")
    ssh = ssh_args(lab, "core2")
    setup = ("set -eu; install -d -m 0755 " + shlex.quote(str(CORE_ROOT)) + "; test ! -e " +
             shlex.quote(str(work)) + "; install -d -m 0711 " + shlex.quote(str(work)) + "; printf %s " +
             shlex.quote(SENTINEL) + " > " + shlex.quote(str(work / "sentinel")))
    prepared = run(ssh + [setup], check=False, timeout=30)
    if prepared.returncode:
        raise RuntimeError("Core B scratch precondition failed: " + prepared.stderr.strip())
    try:
        reply = run(ssh + ["python3", "-"], input=payload, check=False, timeout=900)
    except subprocess.TimeoutExpired as exc:
        (evidence / "timeout.txt").write_text("Remote interruption campaign timed out; Core B scratch was retained.\n")
        if exc.stdout:
            (evidence / "remote.stdout.partial").write_text(exc.stdout if isinstance(exc.stdout, str) else exc.stdout.decode(errors="replace"))
        if exc.stderr:
            (evidence / "remote.stderr.partial").write_text(exc.stderr if isinstance(exc.stderr, str) else exc.stderr.decode(errors="replace"))
        raise RuntimeError(f"interruption campaign timed out; scratch retained: {work}") from exc
    (evidence / "remote.stdout").write_text(reply.stdout)
    (evidence / "remote.stderr").write_text(reply.stderr)
    try:
        report = json.loads(reply.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"Core B interruption probe returned invalid JSON; scratch retained: {work}") from exc
    report["campaign"] = {"domain": domain, "work": str(work), "candidate": "Core B",
                          "package_manifest_sha256": PACKAGE_MANIFEST_SHA256,
                          "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest()}
    (evidence / "result.json").write_text(json.dumps(report, indent=2) + "\n")
    if reply.returncode or report.get("status") != "passed":
        raise RuntimeError(f"interruption probe {report.get('status')}; native files preserved at {work}; evidence: {evidence}")
    print(evidence)


if __name__ == "__main__" and REMOTE_CONFIG is None:
    main()
