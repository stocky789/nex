#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise public libvirt snapshot consolidation on one selected disposable Core."""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import shlex
import subprocess
import sys
import uuid

ROOT = Path(__file__).resolve().parents[2]
SENTINEL = "NEX recovery S0 disposable lab"
PACKAGE_DIR = "/var/tmp/recovery-s0-final-six-patch"
IMAGE_ROOT = "/var/lib/libvirt/images/nex-recovery-s0-consolidate"
STORAGE_ROOTS = {
    IMAGE_ROOT: None,
    "/mnt/recovery-s0-vm-nfs": "nfs4",
    "/mnt/recovery-s0-vm-smb": "cifs",
    "/mnt/recovery-s0-iscsi-exclusive": "ext4",
}


REMOTE_CAMPAIGN = r'''
import json
import os
import re
import signal
import subprocess
import sys
import time
import xml.etree.ElementTree as ET
from xml.sax.saxutils import quoteattr

CONFIG = CONSOLIDATE_CONFIG
ROOT = Path(CONFIG["image_root"])
IMAGE_ROOT = ROOT
SENTINEL = CONFIG["sentinel"]
REPORT = Path(CONFIG["work"]) / "result.json"
targets = ["vda", "vdb"]
name = CONFIG["domain"]
work = Path(CONFIG["work"])
D = {target: str(work / f"disk-{i}.qcow2") for i, target in enumerate(targets)}
A = {target: str(work / f"point-a-{i}.qcow2") for i, target in enumerate(targets)}
B = {target: str(work / f"point-b-{i}.qcow2") for i, target in enumerate(targets)}
C = {target: str(work / f"point-c-{i}.qcow2") for i, target in enumerate(targets)}
result = {"domain": name, "work": str(work), "mode": CONFIG["mode"], "steps": []}
ACTIVE_CLIENT = None


def checkpoint(step):
    result["steps"].append(step)
    REPORT.write_text(json.dumps(result, indent=2) + "\n")


def active_blockjob(output):
    return bool(re.search(r"(?im)^\s*(?:active\s+)?block commit:\s*\[\s*\d+(?:\.\d+)?\s*%\s*\]", output))


def read_extent(path, marker, payload, offset, length):
    reads = {}
    for kind, pattern, start, size in (("marker", marker, 4096, 4096),
                                       ("payload", payload, offset, length)):
        observed = run("qemu-io", "-f", "qcow2", "-c",
                       f"read -P 0x{pattern:02x} {start} {size}", path,
                       check=False, timeout=180)
        reads[kind] = {"returncode": observed.returncode,
                       "stdout": observed.stdout, "stderr": observed.stderr}
    return {"ok": all(value["returncode"] == 0 for value in reads.values()), **reads}


def read_rows_ok(rows):
    return all(row.get("ok") is True for row in rows.values())


def expected_marker(label, values):
    key = {"point-b-saved": "point-a", "point-b-post": "point-b",
           "point-c-saved": "point-b", "point-c-post": "current",
           "current": "current"}[label]
    return values[key]["marker"]


def chain_filenames(images):
    if not isinstance(images, list):
        raise RuntimeError("qemu-img backing chain is not a list")
    return [image.get("filename") for image in images]


def observed_chain_filenames(observation):
    if not isinstance(observation, dict) or not isinstance(observation.get("images"), list):
        raise RuntimeError("qemu-img chain observation is missing its image list")
    return chain_filenames(observation["images"])


def jobs_are_clear(state, generic_none, block, jobs, persisted):
    return (state == "none" and generic_none and block.get("ok") and block.get("return") == [] and
            jobs.get("ok") and jobs.get("return") == [] and persisted.get("exists") and
            not persisted.get("active") and persisted.get("count") == 0)


def write_extent(path, marker, payload, offset, length):
    qemu_io(path, "write", marker)
    observed = run("qemu-io", "-f", "qcow2", "-c",
                   f"write -P 0x{payload:02x} {offset} {length}", path,
                   check=False, timeout=180)
    if observed.returncode:
        raise RuntimeError(f"payload write failed for {path}: {observed.stderr.strip()}")


def require_fixture_file(path):
    candidate = Path(path)
    if (not candidate.is_absolute() or candidate.parent != work or
            candidate.is_symlink() or not candidate.is_file() or
            candidate.resolve(strict=True) != candidate or candidate.stat().st_nlink != 1):
        raise RuntimeError(f"image is not a unique regular file in the exact fixture directory: {path}")
    return candidate


def disk_file_sources(xml):
    root = ET.fromstring(xml)
    sources = []
    for disk in root.iter():
        if disk.tag.rsplit("}", 1)[-1].lower() != "disk":
            continue
        # File-backed media are references too. Inspect all device kinds rather
        # than assuming an unfamiliar type cannot hold a writable/path alias.
        target_node = disk.find("target")
        target = target_node.get("dev") if target_node is not None else disk.get("name")
        for source in disk.findall(".//source"):
            path = source.get("file")
            if path:
                sources.append((target, path))
    return sources


def scan_xml_chains(xml, description, invalid_paths, allow_missing=False):
    sources = disk_file_sources(xml)
    observed = []
    for target, path in sources:
        if path in invalid_paths:
            raise RuntimeError(f"{description} directly references a selected A top")
        if not Path(path).exists():
            if allow_missing:
                observed.append({"target": target, "source": path, "exists": False})
                continue
            raise RuntimeError(f"{description} has an uninspectable disk source: {path}")
        images = chain(path)
        if any(image.get("filename") in invalid_paths for image in images):
            raise RuntimeError(f"{description} backing chain references a selected A top")
        observed.append({"target": target, "source": path, "images": images})
    return observed


def require_stopped_foreign_domain(domain, state):
    normalized = state.strip().lower()
    if normalized != "shut off":
        raise RuntimeError(f"foreign domain {domain} is not shut off; refusing A-top unlink")
    return normalized


def qmp_domain(domain, command):
    observed = virsh("qemu-monitor-command", domain, "--pretty",
                     json.dumps({"execute": command}), check=False)
    try:
        data = json.loads(observed.stdout)
    except json.JSONDecodeError:
        return {"ok": False, "returncode": observed.returncode,
                "stdout": observed.stdout, "stderr": observed.stderr}
    return {"ok": observed.returncode == 0 and "return" in data,
            "return": data.get("return"), "stdout": observed.stdout,
            "stderr": observed.stderr}


def qmp(command):
    return qmp_domain(name, command)


def qemu_apparmor_context():
    domain_uuid = virsh("domuuid", name).stdout.strip()
    pid_file = Path("/run/libvirt/qemu") / f"{name}.pid"
    pid = int(pid_file.read_text().strip())
    process = Path("/proc") / str(pid)
    executable = Path(os.readlink(process / "exe")).name
    context = (process / "attr/current").read_text().strip()
    if not executable.startswith("qemu"):
        raise RuntimeError("public blockcommit QEMU process has an unexpected executable")
    return {"pid": pid, "domain_uuid": domain_uuid,
            "context": require_enforcing_domain_profile(context, domain_uuid)}


def domain_jobs(target):
    per_disk = virsh("blockjob", name, target, "--info", check=False)
    text = (per_disk.stdout + "\n" + per_disk.stderr).lower()
    if per_disk.returncode == 0 and "no current block job" in text:
        state = "none"
    elif per_disk.returncode == 0 and active_blockjob(text):
        state = "active"
    else:
        state = "unknown"
    generic = virsh("domjobinfo", name, check=False)
    block = qmp("query-block-jobs")
    jobs = qmp("query-jobs")
    status_path = Path("/run/libvirt/qemu") / f"{name}.xml"
    persisted = {"exists": status_path.exists(), "active": None, "count": None}
    if status_path.exists():
        status = ET.fromstring(status_path.read_text())
        job_node = status.find("./blockjobs")
        persisted["active"] = job_node is not None and job_node.get("active") == "yes"
        persisted["count"] = 0 if job_node is None else len(job_node.findall("./blockjob"))
    return {"disk": {"state": state, "stdout": per_disk.stdout,
                      "stderr": per_disk.stderr, "returncode": per_disk.returncode},
            "generic": {"returncode": generic.returncode, "stdout": generic.stdout,
                        "stderr": generic.stderr,
                        "none": generic.returncode == 0 and "job type:" in generic.stdout.lower()
                        and "none" in generic.stdout.lower()},
            "qmp_block_jobs": block, "qmp_jobs": jobs, "persisted": persisted,
            "clear": jobs_are_clear(
                state, generic.returncode == 0 and "job type:" in generic.stdout.lower() and
                "none" in generic.stdout.lower(), block, jobs, persisted)}


def daemon_pid():
    observed = run("systemctl", "show", "libvirtd.service", "-p", "ActiveState", "-p", "MainPID")
    values = dict(row.split("=", 1) for row in observed.stdout.splitlines() if "=" in row)
    pid = int(values.get("MainPID", "0"))
    if values.get("ActiveState") != "active" or pid < 2:
        raise RuntimeError(f"libvirtd identity unavailable: {observed.stdout}")
    return {"pid": pid, "properties": observed.stdout}


def stop_client(client):
    if client.poll() is None:
        client.terminate()
        try:
            client.wait(timeout=5)
        except subprocess.TimeoutExpired:
            client.kill()
            client.wait(timeout=5)
    return {"pid": client.pid, "returncode": client.returncode}


def wait_new_daemon(old_pid):
    deadline = time.monotonic() + 45
    while time.monotonic() < deadline:
        try:
            observed = daemon_pid()
            if observed["pid"] != old_pid:
                return observed
        except Exception:
            pass
        time.sleep(0.25)
    raise RuntimeError("libvirtd did not restart with a new process identity")


def qemu_chains():
    docs = snapshot_docs()
    heads = source_paths(name, targets)
    parsed = {label: ET.fromstring(raw) for label, raw in docs["xml"].items()}
    snapshot_paths = {label: snapshot_disk_paths(root) for label, root in parsed.items()}
    references = {path for path in heads.values()}
    for saved, _post in snapshot_paths.values():
        references.update(saved.values())
    revert_paths = {label: {} for label in parsed}
    for root in parsed.values():
        label = root.findtext("./name")
        revert = root.find("./revertdisks")
        if revert is None:
            continue
        for disk in revert.findall(".//disk"):
            source = disk.find("source")
            path = source.get("file") if source is not None else None
            if path:
                references.add(path)
                target_node = disk.find("target")
                target = target_node.get("dev") if target_node is not None else disk.get("name")
                if target in targets:
                    if target in revert_paths[label]:
                        raise RuntimeError(f"snapshot {label} has duplicate revert sources for {target}")
                    revert_paths[label][target] = path

    def observe(path, *, required):
        command = ["qemu-img", "info", "--force-share", "--backing-chain", "--output=json", path]
        if not Path(path).exists():
            if required:
                raise RuntimeError(f"referenced qemu-img chain head is missing: {path}")
            return {"head": path, "state": "absent-unreferenced-postpoint"}
        reply = run(*command, check=False, timeout=120)
        if reply.returncode:
            raise RuntimeError(f"qemu-img chain read failed for {path}: {reply.stderr.strip()}")
        return {"head": path, "command": command, "images": json.loads(reply.stdout)}

    observed = {}
    for target in targets:
        points = {}
        for label, (saved, post) in snapshot_paths.items():
            if target not in saved or target not in post:
                raise RuntimeError(f"snapshot {label} omits target {target}")
            reverted = revert_paths[label].get(target)
            points[label] = {
                "saved": observe(saved[target], required=True),
                # Libvirt may unlink an abandoned post-point head on revert.
                # Its absence is safe to record only when no current, saved, or
                # revert source refers to it and it is not the current point.
                "post": observe(post[target], required=(
                    post[target] in references or label == docs["current"])),
                "revert": observe(reverted, required=True) if reverted else None,
            }
        observed[target] = {
            "base": observe(D[target], required=True),
            "current": observe(heads[target], required=True),
            "snapshots": points,
        }
    return observed


def snapshot_postpoint_state(observation):
    if observation.get("state") == "absent-unreferenced-postpoint":
        return "absent-unreferenced-postpoint"
    if isinstance(observation.get("images"), list) and observation["images"]:
        return "present"
    raise RuntimeError("post-point chain observation is incomplete")


def record_branch_commit_reply(target, command, reply):
    record = {"argv": command, "returncode": reply.returncode,
              "stdout": reply.stdout, "stderr": reply.stderr}
    result.setdefault("branch_commits", {})[target] = record
    checkpoint(f"branch-blockcommit-{target}-command-returned")
    return record


def current_chains():
    heads = source_paths(name, targets)
    return {target: {"head": heads[target], "images": chain(heads[target])}
            for target in targets}


def snapshot_docs():
    labels = [row for row in virsh("snapshot-list", name, "--name").stdout.splitlines() if row]
    documents = {label: virsh("snapshot-dumpxml", name, label).stdout for label in labels}
    return {"labels": labels, "current": virsh("snapshot-current", name, "--name").stdout.strip(),
            "xml": documents}


def rewrite_point_b(raw):
    root = ET.fromstring(raw)
    domain = root.find("domain")
    replaced = []
    for disk in domain.findall("./devices/disk[@device='disk']"):
        target = disk.find("target").get("dev")
        source = disk.find("source")
        if source is None or source.get("file") != A[target]:
            raise RuntimeError(f"point-b saved source did not match A for {target}")
        source.set("file", D[target])
        replaced.append(target)
        if disk.find("backingStore") is not None:
            raise RuntimeError("unexpected saved backingStore in point-b XML")
    parent = root.find("parent")
    if parent is None or parent.findtext("name") != "point-a":
        raise RuntimeError("point-b parent differs from point-a")
    root.remove(parent)
    return ET.tostring(root, encoding="unicode") + "\n", replaced


def create_fixture():
    if (work / "sentinel").read_text() != SENTINEL:
        raise RuntimeError("selected disposable sentinel differs")
    if (str(ROOT) not in CONFIG["allowed_storage_roots"] or ROOT.is_symlink() or ROOT.resolve() != ROOT or
            work.parent != ROOT or work.is_symlink()):
        raise RuntimeError("selected scratch path is not an exact child of the configured image root")
    if list(work.iterdir()) != [work / "sentinel"]:
        raise RuntimeError("selected scratch directory is not empty")
    storage = CONFIG["storage_observation"]
    if storage["validation"] == "exact-findmnt":
        mounted = run("findmnt", "-n", "-M", ROOT, "-o", "TARGET,FSTYPE,SOURCE")
        require_exact_mount(mounted.stdout, ROOT, storage["fstype"], storage["source"])
        if storage.get("iscsi_target"):
            device = storage["source"]
            size = run("lsblk", "-dn", "-o", "SIZE", device).stdout.strip()
            device_type = run("lsblk", "-dn", "-o", "TYPE", device).stdout.strip()
            if size != storage["device_size"] or device_type != "disk":
                raise RuntimeError("selected iSCSI device size or type changed before fixture creation")
            session = run("iscsiadm", "-m", "session", "-P", "3")
            require_iscsi_attachment(session.stdout, storage["iscsi_target"], device)
        result["qemu_storage_access"] = qemu_storage_access_probe(ROOT)
        result["storage"] = storage
    candidate = verify_candidate()
    result["candidate"] = candidate
    for i, target in enumerate(targets):
        run("qemu-img", "create", "-f", "qcow2", D[target], "128M")
        os.chmod(D[target], 0o600)
    xml = [f"<domain type='{CONFIG.get('domain_type', 'kvm')}'>", f"<name>{name}</name>",
           "<memory unit='MiB'>256</memory><currentMemory unit='MiB'>256</currentMemory>",
           "<vcpu>1</vcpu><os><type arch='x86_64' machine='pc'>hvm</type><boot dev='hd'/></os>",
           "<features><acpi/><apic/></features><devices>",
           "<emulator>/usr/bin/qemu-system-x86_64</emulator>"]
    for target in targets:
        xml.append(f"<disk type='file' device='disk'><driver name='qemu' type='qcow2' cache='none'/><source file={quoteattr(D[target])}/><target dev='{target}' bus='virtio'/></disk>")
    xml.append("</devices></domain>")
    definition = work / "domain.xml"
    definition.write_text("".join(xml))
    virsh("define", definition)
    result["uuid"] = virsh("domuuid", name).stdout.strip()
    if not result["uuid"]:
        raise RuntimeError("defined domain has no UUID")
    result["libvirt_file_ownership"] = start_stop_for_libvirt_ownership(
        name, targets, require_apparmor=CONFIG.get("require_apparmor", False))

    values = {
        "base": {"marker": 0x10, "payload": 0x50, "offset": 16 << 20, "length": 4 << 20},
        "point-a": {"marker": 0x20, "payload": 0x60, "offset": 32 << 20, "length": 80 << 20},
        "point-b": {"marker": 0x30, "payload": 0x70, "offset": 8 << 20, "length": 4 << 20},
        "point-c": {"marker": 0x40, "payload": 0x80, "offset": 112 << 20, "length": 4 << 20},
        "current": {"marker": 0x50, "payload": 0x90, "offset": 120 << 20, "length": 4 << 20},
    }
    result["values"] = values

    def write_state(key):
        heads = source_paths(name, targets)
        for target in targets:
            item = values[key]
            write_extent(heads[target], item["marker"], item["payload"], item["offset"], item["length"])

    write_state("base")
    for label in ("point-a", "point-b", "point-c"):
        snapshot(name, label, targets)
        write_state(label)
    write_state("current")
    docs = snapshot_docs()
    if docs["labels"] != ["point-a", "point-b", "point-c"] or docs["current"] != "point-c":
        raise RuntimeError("initial snapshot history differs")
    result["baseline"] = {target: {
        "point-a-saved": read_extent(D[target], **values["base"]),
        "point-a-post": read_extent(A[target], **values["point-a"]),
        "point-b-saved": read_extent(A[target], **values["point-a"]),
        "point-b-post": read_extent(B[target], **values["point-b"]),
        "point-c-saved": read_extent(B[target], **values["point-b"]),
        "current": read_extent(C[target], **values["current"]),
        "point-c-payload": read_extent(C[target], values["current"]["marker"],
                                        values["point-c"]["payload"],
                                        values["point-c"]["offset"], values["point-c"]["length"]),
    } for target in targets}
    if any(not read_rows_ok(disk) for disk in result["baseline"].values()):
        raise RuntimeError("pre-commit marker/payload baseline failed")
    result["before"] = {"snapshots": docs, "inactive_xml": virsh("dumpxml", name, "--inactive").stdout,
                        "chains": qemu_chains()}
    checkpoint("fixture-and-baseline-verified")


def commit_command(target, bandwidth=None):
    command = ["blockcommit", name, target, "--top", A[target], "--base", D[target], "--wait", "--verbose"]
    if bandwidth is not None:
        command[5:5] = ["--bandwidth", str(bandwidth)]
    return command


def commit(target):
    command = commit_command(target)
    apparmor_before = qemu_apparmor_context() if CONFIG.get("require_apparmor") else None
    observed = virsh(*command, check=False, timeout=900)
    record = {"argv": command, "returncode": observed.returncode,
              "stdout": observed.stdout, "stderr": observed.stderr}
    if observed.returncode or "commit complete" not in observed.stdout.lower():
        raise RuntimeError(f"native blockcommit did not complete for {target}")
    jobs = domain_jobs(target)
    if not jobs["clear"]:
        raise RuntimeError(f"{target} jobs did not reconcile empty after native commit")
    if apparmor_before is not None:
        apparmor_after = qemu_apparmor_context()
        if (apparmor_after["pid"] != apparmor_before["pid"] or
                apparmor_after["domain_uuid"] != apparmor_before["domain_uuid"]):
            raise RuntimeError(f"QEMU identity changed during public {target} blockcommit")
        record["apparmor_during_commit"] = {
            "before": apparmor_before, "after": apparmor_after,
        }
    record["jobs_after"] = jobs
    record["chains_after"] = qemu_chains()
    result.setdefault("commits", {})[target] = record
    checkpoint(f"blockcommit-{target}-complete")


def commit_midpoint_restart(target):
    global ACTIVE_CLIENT
    command = commit_command(target, CONFIG["bandwidth_mib"])
    stdout_path = work / f"{target}-blockcommit.stdout"
    stderr_path = work / f"{target}-blockcommit.stderr"
    with stdout_path.open("w") as stdout_file, stderr_path.open("w") as stderr_file:
        client = subprocess.Popen(["virsh", "-c", URI, *command],
                                  stdout=stdout_file, stderr=stderr_file, text=True)
        ACTIVE_CLIENT = client
        deadline = time.monotonic() + CONFIG["boundary_timeout_seconds"]
        boundary = None
        while time.monotonic() < deadline:
            query = virsh("blockjob", name, target, "--info", check=False)
            text = (query.stdout + "\n" + query.stderr).lower()
            if query.returncode == 0 and active_blockjob(text):
                boundary = {"timestamp": datetime.now(timezone.utc).isoformat(),
                            "stdout": query.stdout, "stderr": query.stderr,
                            "returncode": query.returncode}
                break
            if client.poll() is not None:
                break
            if query.returncode and "no current block job" not in text:
                raise RuntimeError(f"unrecognized blockjob response: {query.stdout} {query.stderr}")
            time.sleep(0.1)
        if boundary is None:
            client.wait(timeout=30)
            raise RuntimeError("no active Block Commit boundary observed before timeout")
        qpid = int((Path("/run/libvirt/qemu") / f"{name}.pid").read_text().strip())
        if virsh("domstate", name).stdout.strip().lower() != "paused":
            raise RuntimeError("guest left paused state at blockcommit boundary")
        last = virsh("blockjob", name, target, "--info", check=False)
        last_text = (last.stdout + "\n" + last.stderr).lower()
        if last.returncode or not active_blockjob(last_text) or client.poll() is not None:
            raise RuntimeError("active job/client ended before daemon kill")
        qmp_active = qmp("query-block-jobs")
        qmp_jobs = qmp_active.get("return")
        if (not qmp_active.get("ok") or not isinstance(qmp_jobs, list) or len(qmp_jobs) != 1):
            raise RuntimeError("QMP did not report exactly one active block job")
        job = qmp_jobs[0]
        if (job.get("type") != "commit" or target not in job.get("device", "") or
                job.get("ready") is not False or not 0 < job.get("offset", 0) < job.get("len", 0)):
            raise RuntimeError("QMP commit job is not an active nonterminal target commit")
        daemon_before = daemon_pid()
        apparmor_before = qemu_apparmor_context() if CONFIG.get("require_apparmor") else None
        if apparmor_before is not None and (apparmor_before["pid"] != qpid or
                                             apparmor_before["domain_uuid"] != result["uuid"]):
            raise RuntimeError("enforcing QEMU identity changed before daemon kill")
        qpid_final = int((Path("/run/libvirt/qemu") / f"{name}.pid").read_text().strip())
        final_state = virsh("domstate", name).stdout.strip().lower()
        final_client = client.poll()
        final_job = qmp("query-block-jobs")
        final_jobs = final_job.get("return")
        daemon_final = daemon_pid()
        if (qpid_final != qpid or final_state != "paused" or final_client is not None or
                daemon_final["pid"] != daemon_before["pid"] or not final_job.get("ok") or
                not isinstance(final_jobs, list) or len(final_jobs) != 1):
            raise RuntimeError("job, client, daemon, or QEMU identity changed before daemon kill")
        final_qmp_job = final_jobs[0]
        if (final_qmp_job.get("device") != job.get("device") or
                final_qmp_job.get("type") != "commit" or final_qmp_job.get("ready") is not False or
                not 0 < final_qmp_job.get("offset", 0) < final_qmp_job.get("len", 0)):
            raise RuntimeError("target commit completed or changed before daemon kill")
        if apparmor_before is not None:
            apparmor_final = qemu_apparmor_context()
            if apparmor_final != apparmor_before:
                raise RuntimeError("QEMU confinement or process identity changed before daemon kill")
        boundary["final_pre_kill"] = {"timestamp": datetime.now(timezone.utc).isoformat(),
                                      "stdout": last.stdout, "stderr": last.stderr,
                                      "qmp_block_jobs": final_job,
                                      "daemon": daemon_final, "qemu_pid": qpid_final,
                                      "domain_state": final_state}
        result["midcommit_boundary"] = {"command": command, "boundary": boundary,
                                        "qemu_pid": qpid, "daemon_before": daemon_before,
                                        "domain_uuid": result["uuid"],
                                        "qmp_block_jobs": final_job, "client_pid": client.pid,
                                        "apparmor_before_restart": apparmor_before}
        checkpoint("midcommit-active-boundary")
        os.kill(daemon_before["pid"], signal.SIGKILL)
        daemon_after = wait_new_daemon(daemon_before["pid"])
        if virsh("domuuid", name).stdout.strip() != result["uuid"]:
            raise RuntimeError("domain UUID changed after daemon restart")
        if virsh("domstate", name).stdout.strip().lower() != "paused":
            raise RuntimeError("guest did not remain paused after daemon restart")
        if int((Path("/run/libvirt/qemu") / f"{name}.pid").read_text().strip()) != qpid:
            raise RuntimeError("QEMU PID changed after daemon restart")
        apparmor_after = qemu_apparmor_context() if CONFIG.get("require_apparmor") else None
        if apparmor_before is not None:
            if (apparmor_after["pid"] != qpid or
                    apparmor_after["domain_uuid"] != result["uuid"] or
                    apparmor_after["context"] != apparmor_before["context"]):
                raise RuntimeError("QEMU identity or enforcing profile changed after daemon restart")
        result["midcommit_boundary"].update({"daemon_after": daemon_after,
                                              "apparmor_after_restart": apparmor_after})
        observations = []
        deadline = time.monotonic() + 240
        while time.monotonic() < deadline:
            query = virsh("blockjob", name, target, "--info", check=False)
            text = (query.stdout + "\n" + query.stderr).lower()
            observations.append({"stdout": query.stdout, "stderr": query.stderr,
                                 "returncode": query.returncode,
                                 "timestamp": datetime.now(timezone.utc).isoformat()})
            if query.returncode == 0 and "no current block job" in text:
                jobs = domain_jobs(target)
                if jobs["clear"]:
                    break
            elif query.returncode and "domain is not running" not in text:
                raise RuntimeError(f"blockjob query failed after restart: {query.stderr.strip()}")
            time.sleep(0.5)
        else:
            raise RuntimeError("native commit job did not reach terminal empty state")
        if client.poll() is None:
            try:
                client.wait(timeout=30)
            except subprocess.TimeoutExpired:
                client.terminate()
                client.wait(timeout=5)
        else:
            client.wait(timeout=5)
        record = {"argv": command, "returncode": client.returncode,
                  "stdout": stdout_path.read_text(), "stderr": stderr_path.read_text(),
                  "jobs_after": domain_jobs(target), "observations": observations,
                  "daemon_before": daemon_before, "daemon_after": daemon_after,
                  "qemu_pid_before": qpid,
                  "qemu_pid_after": int((Path("/run/libvirt/qemu") / f"{name}.pid").read_text().strip()),
                  "chains_after": qemu_chains()}
        if apparmor_before is not None:
            record["apparmor_during_commit"] = {"before": apparmor_before,
                                                "after": apparmor_after}
        if not record["jobs_after"]["clear"]:
            raise RuntimeError("libvirt/QMP disagree that the native job is terminal")
        result.setdefault("commits", {})[target] = record
        result["midcommit_observations"] = observations
        ACTIVE_CLIENT = None
        checkpoint("midcommit-job-terminal-after-restart")


def verify_final_data():
    expected = {
        "point-b-saved": (D, "point-a"),
        "point-b-post": (B, "point-b"),
        "point-c-saved": (B, "point-b"),
        "point-c-post": (C, "point-c"),
        "current": (C, "current"),
    }
    verified = {}
    for target in targets:
        verified[target] = {}
        for label, (paths, state) in expected.items():
            values = result["values"][state]
            marker = expected_marker(label, result["values"])
            payload_values = values
            verified[target][label] = read_extent(paths[target], marker,
                                                   payload_values["payload"],
                                                   payload_values["offset"],
                                                   payload_values["length"])
            if not verified[target][label]["ok"]:
                raise RuntimeError(f"final retained data failed for {target}/{label}")
    return verified


def verify_reopen(name, expected_uuid, invalidated_paths):
    domain_uuid = virsh("domuuid", name).stdout.strip()
    if domain_uuid != expected_uuid:
        raise RuntimeError("domain UUID changed on reopen")
    domain_xml = virsh("dumpxml", name).stdout
    qmp_observations = {command: qmp(command) for command in
                        ("query-block", "query-named-block-nodes", "query-block-jobs", "query-jobs")}
    if any(not item["ok"] for item in qmp_observations.values()):
        raise RuntimeError("QMP graph/job query failed after reopen")
    if any(path in json.dumps({"xml": domain_xml, "qmp": qmp_observations})
           for path in invalidated_paths):
        raise RuntimeError("reopened QEMU XML/QMP still refers to invalidated A")
    state = virsh("domstate", name).stdout.strip().lower()
    if state not in ("running", "paused"):
        raise RuntimeError("QEMU did not reopen")
    return {"state": state, "uuid": domain_uuid, "xml": domain_xml, "qmp": qmp_observations,
            "chains": qemu_chains()}


def run_branch_fixture():
    """Exercise source-derived off-branch repair with public native commands."""
    create_fixture()
    # The common setup leaves C current and the domain shut off. Create an
    # unsnapshotted write, revert B, then make a second child from B.
    point_values = {"point-a": 0x20, "point-b": 0x20, "point-c": 0x30,
                    "current": 0x50, "branch-write": 0x61, "point-d": 0x70}
    virsh("snapshot-revert", name, "point-b", "--force")
    if virsh("domstate", name).stdout.strip().lower() != "shut off":
        raise RuntimeError("point-B revert did not leave the fixture stopped")
    b_heads = source_paths(name, targets)
    b_reads = {}
    for target, path in b_heads.items():
        try:
            qemu_io(path, "read", point_values["point-b"])
            b_reads[target] = {"ok": True, "marker": f"0x{point_values['point-b']:02x}"}
        except Exception as error:
            b_reads[target] = {"ok": False, "error": str(error)}
    if any(not item["ok"] for item in b_reads.values()):
        raise RuntimeError("point-B revert did not restore its saved markers")
    before_d = snapshot_docs()
    result["branch_revert_b"] = {"heads": b_heads, "reads": b_reads,
                                 "snapshot_docs": before_d}
    for target, path in b_heads.items():
        value = result["values"]["point-c"]
        write_extent(path, point_values["point-d"], value["payload"],
                     value["offset"], value["length"])
    snapshot(name, "point-d", targets)
    docs = snapshot_docs()
    if docs["labels"] != ["point-a", "point-b", "point-c", "point-d"] or docs["current"] != "point-d":
        raise RuntimeError("branched snapshot labels/current differ")
    parents = {label: ET.fromstring(raw).findtext("./parent/name")
               for label, raw in docs["xml"].items()}
    expected_parents = {"point-a": None, "point-b": "point-a",
                        "point-c": "point-b", "point-d": "point-b"}
    if parents != expected_parents:
        raise RuntimeError(f"branch snapshot tree differs: {parents}")
    snapshots = {}
    for label, raw in docs["xml"].items():
        root = ET.fromstring(raw)
        saved, post = snapshot_disk_paths(root)
        snapshots[label] = {"xml": raw, "parent": parents[label],
                            "saved_sources": saved, "post_sources": post}
    # C is off the current B->D branch. Libvirt resolves A's deleted source
    # from its first child B; identify direct C references from actual XML.
    a_parent = {target: D[target] for target in targets}
    a_tops = snapshots["point-a"]["post_sources"]
    offbranch = []
    for target in targets:
        child = snapshots["point-c"]["saved_sources"][target]
        images = chain(child)
        if len(images) < 2:
            raise RuntimeError(f"off-branch point-C chain is unexpectedly shallow for {target}")
        backing = images[0].get("full-backing-filename") or images[0].get("backing-filename")
        if backing and not Path(backing).is_absolute():
            backing = str(Path(child).parent / backing)
        if backing != a_tops[target]:
            raise RuntimeError(f"point-C immediate backing does not identify A for {target}")
        offbranch.append({"snapshot": "point-c", "target": target,
                          "child": child, "old_backing": backing,
                          "new_backing": a_parent[target], "chain": images})
    if len(offbranch) != len(targets):
        raise RuntimeError("not all off-branch direct children were identified")
    saved_markers = {}
    marker_by_point = {"point-a": 0x10, "point-b": 0x20,
                       "point-c": 0x30, "point-d": point_values["point-d"]}
    for label, details in snapshots.items():
        expected_marker = marker_by_point[label]
        saved_markers[label] = {}
        for target, path in details["saved_sources"].items():
            try:
                qemu_io(path, "read", expected_marker)
                saved_markers[label][target] = {"ok": True,
                                                "marker": f"0x{expected_marker:02x}"}
            except Exception as error:
                saved_markers[label][target] = {"ok": False, "error": str(error)}
        if any(not item["ok"] for item in saved_markers[label].values()):
            raise RuntimeError(f"saved marker failed for retained point {label}")
    result["branch_before_commit"] = {
        "snapshot_docs": docs, "parents": parents, "snapshots": snapshots,
        "saved_markers": saved_markers, "offbranch_direct_children": offbranch,
        "current_heads": source_paths(name, targets), "current_chains": current_chains(),
        "chain_observations": qemu_chains(),
        "snapshot_delete_flags": {label: snapshot_delete_flags(raw)
                                  for label, raw in docs["xml"].items()},
    }
    if any(result["branch_before_commit"]["snapshot_delete_flags"].values()):
        raise RuntimeError("branch fixture already has snapshot deletion flags")
    snapshot_roots = []
    abandoned_postpoints = []
    for label, detail in snapshots.items():
        for target, path in detail["saved_sources"].items():
            snapshot_roots.append((label + ":saved", target, path))
        point_observations = result["branch_before_commit"]["chain_observations"]
        for target, path in detail["post_sources"].items():
            observed = point_observations[target]["snapshots"][label]["post"]
            if snapshot_postpoint_state(observed) == "absent-unreferenced-postpoint":
                abandoned_postpoints.append({"snapshot": label, "target": target,
                                             "path": path, "observation": observed})
            else:
                snapshot_roots.append((label + ":post", target, path))
        root = ET.fromstring(detail["xml"])
        for node in root.iter():
            if node.tag.rsplit("}", 1)[-1].lower() != "revertdisks":
                continue
            for disk in node.iter():
                if disk.tag.rsplit("}", 1)[-1].lower() != "disk":
                    continue
                source = disk.find("source")
                target_node = disk.find("target")
                target = target_node.get("dev") if target_node is not None else disk.get("name")
                path = None if source is None else source.get("file")
                if target in targets and path:
                    snapshot_roots.append((label + ":revertdisks", target, path))
    result["branch_before_commit"]["all_snapshot_chain_roots"] = snapshot_roots
    result["branch_before_commit"]["abandoned_postpoint_sources"] = abandoned_postpoints
    # Capture standalone logical copies of each A top before public commit;
    # do not rely on an invalidated overlay as the comparison source.
    references = {}
    for target in targets:
        path = work / f"precommit-a-logical-{target}.qcow2"
        run("qemu-img", "convert", "-f", "qcow2", "-O", "qcow2", a_tops[target], path)
        os.chmod(path, 0o600)
        info = json.loads(run("qemu-img", "info", "--output=json", path).stdout)
        if info.get("backing-filename") or info.get("full-backing-filename"):
            raise RuntimeError("standalone precommit A reference unexpectedly has a backing file")
        run("qemu-img", "compare", "-f", "qcow2", "-F", "qcow2", a_tops[target], path)
        references[target] = str(path)
    result["precommit_a_references"] = references
    # Start paused for public blockcommit, matching the already-tested native
    # lifecycle; no --delete is passed.
    virsh("start", name)
    if virsh("domstate", name).stdout.strip().lower() == "running":
        virsh("suspend", name)
    if virsh("domstate", name).stdout.strip().lower() != "paused":
        raise RuntimeError("branch fixture QEMU did not remain paused")
    result["branch_qemu"] = {"paused": True,
                             "apparmor": qemu_apparmor_context()
                             if CONFIG.get("require_apparmor") else None}
    for target in targets:
        command = ["blockcommit", name, target, "--top", a_tops[target],
                   "--base", D[target], "--wait", "--verbose"]
        apparmor_before = qemu_apparmor_context() if CONFIG.get("require_apparmor") else None
        reply = virsh(*command, check=False, timeout=900)
        record = record_branch_commit_reply(target, command, reply)
        record["jobs_after"] = domain_jobs(target)
        checkpoint(f"branch-blockcommit-{target}-jobs-observed")
        record["chains_after"] = qemu_chains()
        if apparmor_before is not None:
            apparmor_after = qemu_apparmor_context()
            if apparmor_after != apparmor_before:
                raise RuntimeError(f"QEMU confinement/process identity changed during {target} blockcommit")
            record["apparmor_during_commit"] = {"before": apparmor_before,
                                                "after": apparmor_after}
        if reply.returncode or "commit complete" not in reply.stdout.lower() or not record["jobs_after"]["clear"]:
            raise RuntimeError(f"public no-delete blockcommit failed for {target}")
        checkpoint(f"branch-blockcommit-{target}-complete")
    if virsh("domstate", name).stdout.strip().lower() != "paused":
        raise RuntimeError("branch fixture left paused state before stopped inspection")
    virsh("destroy", name)
    if virsh("domstate", name).stdout.strip().lower() != "shut off":
        raise RuntimeError("branch fixture did not stop after public commits")
    for target in targets:
        run("qemu-img", "compare", "-f", "qcow2", "-F", "qcow2", D[target], references[target])
    # Identify every direct child of A from retained saved sources and current
    # heads. Revert can create an additional source on the current branch.
    candidate_files = {}
    for label, target, source in snapshot_roots:
        for image in chain(source):
            image_path = image.get("filename")
            backing = image.get("full-backing-filename") or image.get("backing-filename")
            if backing and not Path(backing).is_absolute():
                backing = str(Path(image_path).parent / backing)
            if backing == a_tops[target]:
                candidate_files[image_path] = {"snapshot": label, "target": target,
                                               "path": image_path, "old_backing": backing,
                                               "new_backing": D[target]}
    for target, source in source_paths(name, targets).items():
        for image in chain(source):
            image_path = image.get("filename")
            backing = image.get("full-backing-filename") or image.get("backing-filename")
            if backing and not Path(backing).is_absolute():
                backing = str(Path(image_path).parent / backing)
            if backing == a_tops[target]:
                candidate_files[image_path] = {"snapshot": "current", "target": target,
                                               "path": image_path, "old_backing": backing,
                                               "new_backing": D[target]}
    if not candidate_files:
        raise RuntimeError("no source-derived direct children of A were found")
    result["all_a_direct_children"] = list(candidate_files.values())
    # Rebase only those stopped files after checking their immediate backing
    # headers and the completed base equivalence above.
    rebased = []
    for child in candidate_files.values():
        candidate = require_fixture_file(child["path"])
        base = require_fixture_file(child["new_backing"])
        info = json.loads(run("qemu-img", "info", "--output=json", child["path"]).stdout)
        observed = info.get("full-backing-filename") or info.get("backing-filename")
        if observed and not Path(observed).is_absolute():
            observed = str(Path(child["path"]).parent / observed)
        if observed != child["old_backing"]:
            raise RuntimeError("selected direct-child backing changed before controlled rebase")
        reference = work / f"pre-rebase-{len(rebased)}-{candidate.name}.qcow2"
        run("qemu-img", "convert", "-f", "qcow2", "-O", "qcow2", candidate, reference)
        os.chmod(reference, 0o600)
        reference_info = json.loads(run("qemu-img", "info", "--output=json", reference).stdout)
        if reference_info.get("backing-filename") or reference_info.get("full-backing-filename"):
            raise RuntimeError("pre-rebase child reference is unexpectedly backed")
        command = ["qemu-img", "rebase", "-u", "-F", "qcow2", "-f", "qcow2",
                   "-b", str(base), str(candidate)]
        reply = run(*command, check=False, timeout=180)
        if reply.returncode:
            raise RuntimeError(f"off-branch qemu-img rebase failed: {reply.stderr.strip()}")
        after = json.loads(run("qemu-img", "info", "--output=json", child["path"]).stdout)
        backing = after.get("full-backing-filename") or after.get("backing-filename")
        if backing and not Path(backing).is_absolute():
            backing = str(Path(child["path"]).parent / backing)
        if backing != child["new_backing"]:
            raise RuntimeError("off-branch rebase did not persist the selected base")
        run("qemu-img", "check", child["path"])
        run("qemu-img", "compare", "-f", "qcow2", "-F", "qcow2", child["path"], reference)
        rebased.append({**child, "argv": command, "before": info, "after": after,
                        "standalone_before": str(reference),
                        "logical_contents_preserved": True})
    # Rewrite every retained snapshot source that literally names A's top,
    # including saved-domain, disk, and revert-disk XML. Retain C/D parents.
    rewritten = {}
    for label, raw in docs["xml"].items():
        if label == "point-a":
            continue
        root = ET.fromstring(raw)
        if label == "point-b":
            parent = root.find("parent")
            if parent is None or parent.findtext("name") != "point-a":
                raise RuntimeError("point-B parent is not point-A before explicit redefine")
            root.remove(parent)
        changed = []
        for disk in root.iter():
            if disk.tag.rsplit("}", 1)[-1] != "disk":
                continue
            target_node = disk.find("target")
            target = target_node.get("dev") if target_node is not None else disk.get("name")
            if target not in targets:
                continue
            for source in disk.findall(".//source"):
                if source.get("file") == a_tops[target]:
                    source.set("file", D[target])
                    changed.append(target)
        if label == "point-b" and set(changed) != set(targets):
            raise RuntimeError("point-B did not expose both A sources for explicit rewrite")
        if changed:
            redefine_path = work / f"{label}-redefined.xml"
            redefine_path.write_text(ET.tostring(root, encoding="unicode") + "\n")
            reply = virsh("snapshot-create", name, redefine_path, "--redefine", check=False)
            if reply.returncode:
                raise RuntimeError(f"snapshot metadata redefine failed for {label}: {reply.stderr.strip()}")
            rewritten[label] = {"targets": changed,
                                "xml": ET.tostring(root, encoding="unicode")}
    after_redefine = snapshot_docs()
    if after_redefine["labels"] != ["point-a", "point-b", "point-c", "point-d"]:
        raise RuntimeError("snapshot redefine changed the branch labels")
    new_parents = {label: ET.fromstring(raw).findtext("./parent/name")
                   for label, raw in after_redefine["xml"].items()}
    if new_parents != {"point-a": None, "point-b": None,
                       "point-c": "point-b", "point-d": "point-b"}:
        raise RuntimeError(f"explicit branch redefine produced unexpected parents: {new_parents}")
    # Assert no retained definition refers to A, including saved and revert
    # disk sections, then use the public metadata-only operation.
    for label, raw in after_redefine["xml"].items():
        if label == "point-a":
            continue
        if any(path in raw for path in a_tops.values()):
            raise RuntimeError(f"retained snapshot {label} still refers to an A top")
        if snapshot_delete_flags(raw):
            raise RuntimeError(f"retained snapshot {label} has a deletion-in-progress flag")
    expected_saved_marker = {"point-b": 0x20, "point-c": 0x30,
                             "point-d": point_values["point-d"]}
    retained_after_rebase = {}
    for label, marker in expected_saved_marker.items():
        saved, _ = snapshot_disk_paths(after_redefine["xml"][label])
        retained_after_rebase[label] = {}
        for target, path in saved.items():
            try:
                qemu_io(path, "read", marker)
                retained_after_rebase[label][target] = {"ok": True,
                                                       "marker": f"0x{marker:02x}"}
            except Exception as error:
                retained_after_rebase[label][target] = {"ok": False, "error": str(error)}
        if any(not row["ok"] for row in retained_after_rebase[label].values()):
            raise RuntimeError(f"retained {label} saved markers changed during rebase")
    current_after_rebase = source_paths(name, targets)
    for path in current_after_rebase.values():
        qemu_io(path, "read", point_values["point-d"])
    delete = virsh("snapshot-delete", name, "point-a", "--metadata", check=False)
    if delete.returncode:
        raise RuntimeError(f"metadata-only A delete failed: {delete.stderr.strip()}")
    result["branch_rebase"] = rebased
    result["branch_redefine"] = {"parents": new_parents, "snapshot_docs": after_redefine,
                                 "rewritten": rewritten,
                                 "retained_markers_after_rebase": retained_after_rebase,
                                 "current_heads_after_rebase": current_after_rebase}
    result["branch_metadata_delete"] = {"argv": ["snapshot-delete", name, "point-a", "--metadata"],
                                        "stdout": delete.stdout}
    checkpoint("branch-offcurrent-child-rebased-and-a-metadata-deleted")
    post_delete = snapshot_docs()
    if post_delete["labels"] != ["point-b", "point-c", "point-d"]:
        raise RuntimeError("metadata-only A delete did not retain B/C/D")
    invalid_paths = set(a_tops.values())
    selected_snapshot_sources = {
        label: scan_xml_chains(raw, f"fixture snapshot {label}", invalid_paths,
                               allow_missing=True)
        for label, raw in post_delete["xml"].items()}
    # Audit every remaining image/header/chain and the selected domain XML
    # before unlinking exact A top files.
    retained_saved_chains = {}
    for label, raw in post_delete["xml"].items():
        saved, _ = snapshot_disk_paths(raw)
        retained_saved_chains[label] = {target: chain(path) for target, path in saved.items()}
    inventory = {"snapshot_docs": post_delete,
                 "inactive_xml": virsh("dumpxml", name, "--inactive").stdout,
                 "current_chains": current_chains(), "all_chains": qemu_chains(),
                 "snapshot_source_chains": selected_snapshot_sources,
                 "retained_saved_chains": retained_saved_chains}
    if any(path in json.dumps(inventory) for path in a_tops.values()):
        raise RuntimeError("A top remains referenced after snapshot metadata deletion")
    # Scan both active/default and inactive definitions, plus every saved
    # snapshot disk/backing source for all other domains. Any unreadable XML
    # or missing referenced disk fails closed before unlink.
    foreign_inventory = []
    foreign = [row for row in virsh("list", "--all", "--name").stdout.splitlines() if row]
    for domain_name in foreign:
        if domain_name == name:
            continue
        definitions = {}
        for mode, suffix in (("default", []), ("inactive", ["--inactive"])):
            xml_reply = virsh("dumpxml", domain_name, *suffix, check=False)
            if xml_reply.returncode:
                raise RuntimeError(f"cannot inspect {mode} XML for another domain")
            definitions[mode] = scan_xml_chains(xml_reply.stdout,
                f"{domain_name} {mode} domain XML", invalid_paths)
        state = virsh("domstate", domain_name, check=False)
        if state.returncode:
            raise RuntimeError(f"cannot inspect live state for another domain {domain_name}")
        domain_state = require_stopped_foreign_domain(domain_name, state.stdout)
        labels_reply = virsh("snapshot-list", domain_name, "--name", check=False)
        if labels_reply.returncode:
            raise RuntimeError(f"cannot enumerate snapshots for another domain {domain_name}")
        snapshots = {}
        for label in [row.strip() for row in labels_reply.stdout.splitlines() if row.strip()]:
            snapshot_reply = virsh("snapshot-dumpxml", domain_name, label, check=False)
            if snapshot_reply.returncode:
                raise RuntimeError(f"cannot inspect snapshot {label} on another domain")
            snapshots[label] = scan_xml_chains(snapshot_reply.stdout,
                f"{domain_name} snapshot {label}", invalid_paths)
        foreign_inventory.append({"domain": domain_name, "state": domain_state,
                                  "definitions": definitions,
                                  "snapshots": snapshots})
    unlinked = []
    for path in a_tops.values():
        candidate = Path(path)
        if (candidate.parent != work or candidate.is_symlink() or not candidate.is_file() or
                candidate.stat().st_nlink != 1):
            raise RuntimeError("A top unlink target failed exact file checks")
        candidate.unlink()
        unlinked.append(path)
    result["branch_post_delete"] = {"snapshot_docs": post_delete,
                                    "references": "zero in fixture metadata/XML/chains and foreign domain XML/chains",
                                    "foreign_domains": foreign_inventory,
                                    "unlinked_a_tops": unlinked,
                                    "rebased_children": rebased}
    # Revert both saved branches after the A layer is gone. The markers prove
    # that the retained off-current point and current-branch point reopen.
    for label, marker in (("point-c", 0x30), ("point-d", point_values["point-d"])):
        virsh("snapshot-revert", name, label, "--force")
        if virsh("domstate", name).stdout.strip().lower() != "shut off":
            raise RuntimeError(f"{label} revert did not leave the fixture stopped")
        heads = source_paths(name, targets)
        reads = {}
        for target, path in heads.items():
            try:
                qemu_io(path, "read", marker)
                reads[target] = {"ok": True, "marker": f"0x{marker:02x}"}
            except Exception as error:
                reads[target] = {"ok": False, "error": str(error)}
        if any(not row["ok"] for row in reads.values()):
            raise RuntimeError(f"{label} retained marker failed after A removal")
        virsh("start", name)
        if virsh("domstate", name).stdout.strip().lower() == "running":
            virsh("suspend", name)
        if virsh("domstate", name).stdout.strip().lower() != "paused":
            raise RuntimeError(f"{label} QEMU did not reopen paused")
        reopened = {"uuid": virsh("domuuid", name).stdout.strip(),
                    "chains": qemu_chains(), "qmp": qmp("query-block"),
                    "apparmor": qemu_apparmor_context()
                    if CONFIG.get("require_apparmor") else None}
        if reopened["uuid"] != result["uuid"] or not reopened["qmp"]["ok"]:
            raise RuntimeError(f"{label} QEMU reopen evidence failed")
        if any(path in json.dumps(reopened) for path in a_tops.values()):
            raise RuntimeError(f"{label} reopened QEMU still references an A top")
        virsh("destroy", name)
        if virsh("domstate", name).stdout.strip().lower() != "shut off":
            raise RuntimeError(f"{label} QEMU failed to stop after reopen")
        result.setdefault("branch_reopens", []).append({"label": label,
            "marker": f"0x{marker:02x}", "heads": heads, "reads": reads,
            "reopen": reopened})
        checkpoint(f"branch-revert-reopen-{label}-passed")
    result["status"] = "passed"
    checkpoint("passed")
    return result


def run_fixture():
    if CONFIG["mode"] == "branch":
        return run_branch_fixture()
    create_fixture()
    virsh("start", name)
    if virsh("domstate", name).stdout.strip().lower() == "running":
        virsh("suspend", name)
    if virsh("domstate", name).stdout.strip().lower() != "paused":
        raise RuntimeError("synthetic QEMU is not paused")
    qemu_pid = int((Path("/run/libvirt/qemu") / f"{name}.pid").read_text().strip())
    result["qemu_pid_before"] = qemu_pid
    checkpoint("synthetic-domain-paused")
    if CONFIG["mode"] == "midcommit":
        commit_midpoint_restart("vda")
    else:
        commit("vda")
    if CONFIG["mode"] == "between-disk":
        old = daemon_pid()
        os.kill(old["pid"], signal.SIGKILL)
        new = wait_new_daemon(old["pid"])
        if virsh("domstate", name).stdout.strip().lower() != "paused":
            raise RuntimeError("guest did not remain paused across between-disk restart")
        if int((Path("/run/libvirt/qemu") / f"{name}.pid").read_text().strip()) != qemu_pid:
            raise RuntimeError("QEMU PID changed across between-disk restart")
        result["between_disk_restart"] = {"daemon_before": old, "daemon_after": new,
                                          "qemu_pid": qemu_pid,
                                          "jobs": domain_jobs("vda"),
                                          "chains": qemu_chains()}
        checkpoint("between-disk-restart-reconciled")
    commit("vdb")
    if not all(result["commits"][target]["jobs_after"]["clear"] for target in targets):
        raise RuntimeError("final block jobs are not empty")
    if virsh("domstate", name).stdout.strip().lower() != "paused":
        raise RuntimeError("guest is not paused before stopped chain verification")
    virsh("destroy", name)
    if virsh("domstate", name).stdout.strip().lower() != "shut off":
        raise RuntimeError("synthetic QEMU failed to stop")
    result["post_shutdown_before_metadata"] = {
        "domain_uuid": virsh("domuuid", name).stdout.strip(),
        "snapshot_docs": snapshot_docs(),
        "inactive_xml": virsh("dumpxml", name, "--inactive").stdout,
        "chains": qemu_chains(),
    }
    checkpoint("stopped-backing-chains-before-metadata")

    docs = snapshot_docs()
    if docs["labels"] != ["point-a", "point-b", "point-c"] or docs["current"] != "point-c":
        raise RuntimeError("snapshot metadata changed before point-A redefinition")
    b_xml, replaced = rewrite_point_b(docs["xml"]["point-b"])
    if set(replaced) != set(targets):
        raise RuntimeError("point-b saved domain sources were not all rewritten")
    b_path = work / "point-b-redefined.xml"
    b_path.write_text(b_xml)
    redefine = virsh("snapshot-create", name, b_path, "--redefine")
    docs = snapshot_docs()
    if docs["labels"] != ["point-a", "point-b", "point-c"] or docs["current"] != "point-c":
        raise RuntimeError("point-B redefine changed labels/current")
    b_after = ET.fromstring(docs["xml"]["point-b"])
    b_saved, b_post = snapshot_disk_paths(b_after)
    if b_after.findtext("./parent/name") is not None or b_saved != D or b_post != B:
        raise RuntimeError("point-B redefine did not persist the required parent/sources")
    c_after = ET.fromstring(docs["xml"]["point-c"])
    c_saved, c_post = snapshot_disk_paths(c_after)
    if c_after.findtext("./parent/name") != "point-b" or c_saved != B or c_post != C:
        raise RuntimeError("point-C saved/post-point source graph changed unexpectedly")
    redefine_flags = {label: snapshot_delete_flags(raw) for label, raw in docs["xml"].items()}
    if any(redefine_flags.values()):
        raise RuntimeError(f"snapshot redefine left active deletion flags: {redefine_flags}")
    if any(path in docs["xml"]["point-b"] + docs["xml"]["point-c"] for path in A.values()):
        raise RuntimeError("retained snapshot XML still names A")
    result["point_b_redefine"] = {"output": redefine.stdout, "xml": b_xml,
                                  "snapshot_docs": docs}
    checkpoint("point-b-redefined-and-no-a-xml-refs")

    delete = virsh("snapshot-delete", name, "point-a", "--metadata", check=False)
    result["metadata_delete"] = {"argv": ["snapshot-delete", name, "point-a", "--metadata"],
                                 "returncode": delete.returncode,
                                 "stdout": delete.stdout, "stderr": delete.stderr}
    if delete.returncode:
        raise RuntimeError(f"metadata-only point-A delete failed: {delete.stderr.strip()}")
    docs = snapshot_docs()
    if docs["labels"] != ["point-b", "point-c"] or docs["current"] != "point-c":
        raise RuntimeError("metadata-only delete produced an unexpected snapshot graph")
    parents = {label: ET.fromstring(raw).findtext("./parent/name")
               for label, raw in docs["xml"].items()}
    if parents != {"point-b": None, "point-c": "point-b"}:
        raise RuntimeError(f"retained snapshot parent graph differs: {parents}")
    flags = {label: snapshot_delete_flags(raw) for label, raw in docs["xml"].items()}
    if any(flags.values()):
        raise RuntimeError(f"retained snapshots have active deletion flags: {flags}")
    for target in targets:
        for key, path, expected, observation in (
                ("D", D[target], [D[target]],
                 result["post_shutdown_before_metadata"]["chains"][target]["base"]),
                ("B", B[target], [B[target], D[target]],
                 result["post_shutdown_before_metadata"]["chains"][target]["snapshots"]["point-b"]["post"]),
                ("C", C[target], [C[target], B[target], D[target]],
                 result["post_shutdown_before_metadata"]["chains"][target]["snapshots"]["point-c"]["post"])):
            before = observed_chain_filenames(observation)
            after = chain_filenames(chain(path))
            if before != expected or after != expected:
                raise RuntimeError(f"stopped {target}/{key} backing chain differs before/after metadata delete")
    result["post_delete"] = {"snapshot_docs": docs,
                             "inactive_xml": virsh("dumpxml", name, "--inactive").stdout,
                             "chains": qemu_chains()}
    result["final_data"] = verify_final_data()
    checkpoint("point-a-metadata-deleted-retained-data-and-chains-passed")

    virsh("start", name)
    result["reopen"] = verify_reopen(name, result["uuid"], A.values())
    result["reopen"]["qemu_pid"] = (Path("/run/libvirt/qemu") / f"{name}.pid").read_text().strip()
    virsh("destroy", name)
    if virsh("domstate", name).stdout.strip().lower() != "shut off":
        raise RuntimeError("reopened synthetic QEMU failed to stop")
    final_docs = snapshot_docs()
    final_xml = virsh("dumpxml", name, "--inactive").stdout
    final_chains = qemu_chains()
    final_current_chains = current_chains()
    inventory = json.dumps({"docs": final_docs, "xml": final_xml,
                            "chains": final_chains, "current_chains": final_current_chains,
                            "qmp": result["reopen"]["qmp"]})
    if any(path in inventory for path in A.values()):
        raise RuntimeError("A remains referenced after metadata deletion")
    result["pre_unlink_reference_scan"] = {
        "passed": True,
        "paths": list(A.values()),
        "sources_scanned": ["snapshot XML", "inactive domain XML", "QMP block and job graphs",
                            "current and saved-point backing chains"],
    }
    unlinked = []
    for path in A.values():
        candidate = Path(path)
        if candidate.parent != work or candidate.is_symlink() or not candidate.is_file():
            raise RuntimeError(f"exact point-A unlink precondition failed: {path}")
        candidate.unlink()
        if candidate.exists():
            raise RuntimeError(f"point-A file remains after unlink: {path}")
        unlinked.append({"path": path, "absent_after_unlink": True})
    result["final_stopped"] = {"snapshot_docs": final_docs, "inactive_xml": final_xml,
                               "chains": final_chains, "current_chains": final_current_chains,
                               "unlinked_a": unlinked}
    checkpoint("point-a-images-unlinked-after-reference-scan")

    revert_checks = []
    for label, state_key in (("point-c", "point-b"), ("point-b", "point-a")):
        reverted = virsh("snapshot-revert", name, label, "--force")
        if virsh("domstate", name).stdout.strip().lower() != "shut off":
            raise RuntimeError(f"{label} revert did not leave the synthetic domain shut off")
        if virsh("domuuid", name).stdout.strip() != result["uuid"]:
            raise RuntimeError(f"{label} revert changed the domain UUID")
        heads = source_paths(name, targets)
        values = result["values"][state_key]
        reads = {target: read_extent(heads[target], values["marker"], values["payload"],
                                     values["offset"], values["length"])
                 for target in targets}
        if any(not item["ok"] for item in reads.values()):
            raise RuntimeError(f"{label} native revert marker/payload verification failed")
        revert_checks.append({"label": label, "output": reverted.stdout, "heads": heads,
                              "expected_state": state_key, "reads": reads,
                              "chains": current_chains(),
                              "current": snapshot_docs()["current"]})
        result["reverts"] = revert_checks
        checkpoint(f"native-revert-{label}-passed")
    result["status"] = "passed"
    checkpoint("passed")
    return result


if CONFIG.get("run_campaign", True):
    try:
        report = run_fixture()
    except Exception as error:
        result["status"] = "failed-preserved"
        result["error"] = str(error)
        if ACTIVE_CLIENT is not None:
            try:
                result["midcommit_client_stopped"] = stop_client(ACTIVE_CLIENT)
            except Exception as client_error:
                result["midcommit_client_stop_error"] = str(client_error)
        try:
            result["last_domain_state"] = virsh("domstate", name, check=False).stdout.strip()
        except Exception as observe_error:
            result["observation_error"] = str(observe_error)
        try:
            all_snapshot_chains = qemu_chains()
            result["failure_reconciliation"] = {
                "domain_state": virsh("domstate", name, check=False).stdout.strip(),
                "snapshot_docs": snapshot_docs(),
                "inactive_xml": virsh("dumpxml", name, "--inactive", check=False).stdout,
                "current_heads": source_paths(name, targets),
                "current_chains": {target: all_snapshot_chains[target]["current"]
                                   for target in targets},
                "jobs": {target: domain_jobs(target) for target in targets},
                "all_snapshot_chains": all_snapshot_chains,
            }
        except Exception as reconcile_error:
            result["failure_reconciliation_error"] = str(reconcile_error)
        REPORT.write_text(json.dumps(result, indent=2) + "\n")
        print(json.dumps(result))
        sys.exit(1)
    print(json.dumps(report))
'''


def load_module(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def native_helper_source():
    helper = (ROOT / "tests/integration/recovery_s0.py").read_text()
    footer = "\nif REMOTE_CONFIG is None:\n"
    if footer not in helper:
        raise RuntimeError("native S0 helper boundary changed; review before running consolidation probe")
    return helper.split(footer, 1)[0].replace(
        "ROOT = Path(__file__).resolve().parents[2]", "ROOT = Path('/')")


def build_remote_payload(helper_source: str, config: dict, package_campaign: str) -> str:
    payload = helper_source + "\n" + package_campaign + "\n"
    payload += "CONSOLIDATE_CONFIG = " + repr(config) + "\n" + REMOTE_CAMPAIGN
    compile(payload, "recovery_s0_consolidate_remote.py", "exec")
    return payload


def validate_storage_selection(root, fstype, source, iscsi_target, iscsi_device_size):
    root = Path(root)
    if not root.is_absolute() or str(root) not in STORAGE_ROOTS:
        raise ValueError("image root must be the dedicated local or previously selected S0 storage path")
    expected_type = STORAGE_ROOTS[str(root)]
    if expected_type is None:
        if any((fstype, source, iscsi_target, iscsi_device_size)):
            raise ValueError("local image storage does not accept external mount selectors")
        return {"requested_root": str(root), "validation": "local-default"}
    if fstype != expected_type or not source:
        raise ValueError(f"{root} requires an exact {expected_type} mount source")
    if expected_type == "ext4":
        if not iscsi_target or iscsi_device_size != "2G":
            raise ValueError("the selected exclusive iSCSI mount requires its exact target and 2G device")
    elif iscsi_target or iscsi_device_size:
        raise ValueError("iSCSI selectors apply only to the selected ext4 mount")
    return {"requested_root": str(root), "validation": "exact-findmnt",
            "target": str(root), "fstype": fstype, "source": source,
            "iscsi_target": iscsi_target, "device_size": iscsi_device_size}


def candidate_identity(default_version, default_manifest, version, manifest):
    selected_version = version or default_version
    selected_manifest = manifest or default_manifest
    if not selected_version or len(selected_manifest) != 64 or any(
            character not in "0123456789abcdef" for character in selected_manifest):
        raise ValueError("candidate package version and lowercase SHA256 manifest are required")
    if bool(version) != bool(manifest):
        raise ValueError("explicit candidate version and manifest must be supplied together")
    return selected_version, selected_manifest


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lab-dir", type=Path, required=True)
    parser.add_argument("--node", required=True, help="selected guest in the disposable Core lab")
    parser.add_argument("--evidence", type=Path, required=True, help="new ignored .cache evidence directory")
    parser.add_argument("--mode", choices=("normal", "between-disk", "midcommit", "branch"), required=True)
    parser.add_argument("--image-root", type=Path, default=Path(IMAGE_ROOT))
    parser.add_argument("--expected-fstype", help="require this exact mounted filesystem type")
    parser.add_argument("--expected-source", help="require this exact mount source")
    parser.add_argument("--iscsi-target", help="require this exact active iSCSI target for the selected LUN")
    parser.add_argument("--iscsi-device-size", help="require this exact iSCSI device size (only 2G is accepted)")
    parser.add_argument("--package-dir", type=Path, default=Path(PACKAGE_DIR))
    parser.add_argument("--package-version", help="require an explicitly selected candidate package version")
    parser.add_argument("--package-manifest-sha256", help="bind candidate verification to the selected manifest")
    parser.add_argument("--boundary-timeout", type=float, default=45)
    parser.add_argument("--bandwidth-mib", type=int, default=1)
    parser.add_argument("--require-apparmor", action="store_true",
                        help="require the selected domain's exact enforcing QEMU AppArmor profile")
    args = parser.parse_args(argv)
    if args.boundary_timeout <= 0 or args.boundary_timeout > 120:
        parser.error("--boundary-timeout must be greater than 0 and at most 120 seconds")
    if args.bandwidth_mib < 1 or args.bandwidth_mib > 64:
        parser.error("--bandwidth-mib must be between 1 and 64 MiB/s")
    try:
        storage_observation = validate_storage_selection(
            args.image_root, args.expected_fstype, args.expected_source,
            args.iscsi_target, args.iscsi_device_size)
    except ValueError as error:
        parser.error(str(error))
    lab = load_module(ROOT / "tests/integration/recovery_s0_interrupt.py", "recovery_s0_interrupt")
    try:
        package_version, package_manifest_sha256 = candidate_identity(
            lab.PACKAGE_VERSION, lab.PACKAGE_MANIFEST_SHA256,
            args.package_version, args.package_manifest_sha256)
    except ValueError as error:
        parser.error(str(error))
    native_helpers = {"__name__": "recovery_s0_storage_helpers"}
    exec(compile(native_helper_source(), "recovery_s0_storage_helpers.py", "exec"), native_helpers)
    candidate = lab.validate_lab(args.lab_dir)
    ssh = lab.ssh_args(candidate, args.node)
    if storage_observation["validation"] == "exact-findmnt":
        inspect = "findmnt -n -M " + shlex.quote(str(args.image_root)) + " -o TARGET,FSTYPE,SOURCE"
        mounted = subprocess.run(ssh + [inspect], text=True, capture_output=True, timeout=30)
        if mounted.returncode:
            parser.error("selected storage root is not an exact mountpoint: " + mounted.stderr.strip())
        fields = native_helpers["require_exact_mount"](
            mounted.stdout, args.image_root, storage_observation["fstype"],
            storage_observation["source"])
        storage_observation.update({"target": fields[0], "fstype": fields[1],
                                    "source": fields[2]})
        if storage_observation.get("iscsi_target"):
            device = fields[2]
            if not device.startswith("/dev/"):
                parser.error("selected iSCSI source is not a block device")
            device_q = shlex.quote(device)
            size = subprocess.run(ssh + ["lsblk -dn -o SIZE " + device_q],
                                  text=True, capture_output=True, timeout=30)
            kind = subprocess.run(ssh + ["lsblk -dn -o TYPE " + device_q],
                                  text=True, capture_output=True, timeout=30)
            if (size.returncode or size.stdout.strip() != storage_observation["device_size"] or
                    kind.returncode or kind.stdout.strip() != "disk"):
                parser.error("selected iSCSI block device identity or size differs")
            session = subprocess.run(ssh + ["iscsiadm -m session -P 3"],
                                     text=True, capture_output=True, timeout=30)
            if session.returncode:
                parser.error("cannot inspect active iSCSI sessions")
            native_helpers["require_iscsi_attachment"](
                session.stdout, storage_observation["iscsi_target"], device)
    evidence = args.evidence.resolve()
    if args.evidence.exists() or not evidence.is_relative_to((ROOT / ".cache").resolve()):
        parser.error("--evidence must be a new directory under ignored .cache")
    run_id = uuid.uuid4().hex[:12]
    domain = f"nex-recovery-s0-consolidate-{run_id}"
    work = args.image_root / domain
    config = {"root": str(args.image_root), "image_root": str(args.image_root),
              "work": str(work), "domain": domain, "mode": args.mode,
              "sentinel": SENTINEL,
              "storage_observation": storage_observation,
              "allowed_storage_roots": list(STORAGE_ROOTS),
              "boundary_timeout_seconds": args.boundary_timeout,
              "bandwidth_mib": args.bandwidth_mib,
              "require_apparmor": args.require_apparmor}
    package_config = {"packages": list(lab.PACKAGE_SET),
                      "package_version": package_version,
                      "package_manifest_sha256": package_manifest_sha256,
                      "sentinel": SENTINEL,
                      "staged_package_dir": str(args.package_dir)}
    payload = build_remote_payload(native_helper_source(), config,
                                   lab.REMOTE_CAMPAIGN.replace("__INTERRUPT_CONFIG__", repr(package_config)))
    evidence.mkdir(parents=True, mode=0o700)
    (evidence / "candidate.json").write_text(json.dumps({"node": args.node, "domain": domain,
                                                          "work": str(work), "mode": args.mode}, indent=2) + "\n")
    root_q = shlex.quote(str(args.image_root))
    if storage_observation["validation"] == "exact-findmnt":
        expected_mount = " ".join((storage_observation["target"], storage_observation["fstype"], storage_observation["source"]))
        mount_check = ("test \"$(findmnt -n -M " + root_q +
                       " -o TARGET,FSTYPE,SOURCE)\" = " + shlex.quote(expected_mount) + "; ")
        root_setup = "test -d " + root_q + "; "
    else:
        mount_check = ""
        root_setup = "install -d -m 0755 " + root_q + "; "
    setup = ("set -eu; test ! -L " + root_q + "; " + mount_check + root_setup +
             "test -d " + root_q +
             "; test ! -e " +
             shlex.quote(str(work)) + "; install -d -m 0711 " + shlex.quote(str(work)) + "; printf %s " +
             shlex.quote(SENTINEL) + " > " + shlex.quote(str(work / "sentinel")))
    prepared = subprocess.run(ssh + [setup], text=True, capture_output=True, timeout=30)
    if prepared.returncode:
        raise RuntimeError("selected Core scratch preflight failed: " + prepared.stderr.strip())
    try:
        reply = subprocess.run(ssh + ["python3", "-"], input=payload, text=True,
                               capture_output=True, timeout=1800)
    except subprocess.TimeoutExpired as error:
        (evidence / "timeout.txt").write_text("Remote probe timed out; preserve the selected Core fixture.\n")
        if error.stdout:
            partial = error.stdout.decode(errors="replace") if isinstance(error.stdout, bytes) else error.stdout
            (evidence / "remote.stdout.partial").write_text(partial)
        if error.stderr:
            partial = error.stderr.decode(errors="replace") if isinstance(error.stderr, bytes) else error.stderr
            (evidence / "remote.stderr.partial").write_text(partial)
        raise RuntimeError(f"probe timed out; fixture retained at {work}") from error
    (evidence / "remote.stdout").write_text(reply.stdout)
    (evidence / "remote.stderr").write_text(reply.stderr)
    try:
        report = json.loads(reply.stdout)
    except json.JSONDecodeError as error:
        raise RuntimeError(f"Core returned invalid JSON; fixture retained at {work}") from error
    report["campaign"] = {"node": args.node, "domain": domain, "work": str(work),
                          "mode": args.mode, "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
                          "package_manifest_sha256": package_manifest_sha256}
    (evidence / "result.json").write_text(json.dumps(report, indent=2) + "\n")
    if reply.returncode or report.get("status") != "passed":
        raise RuntimeError(f"consolidation probe {report.get('status')}; fixture retained at {work}; evidence: {evidence}")
    print(evidence)


if __name__ == "__main__":
    main()
