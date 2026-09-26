#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Probe stopped BIOS external qcow2 snapshots on one selected disposable Core candidate."""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import threading
import time
import uuid
from xml.sax.saxutils import quoteattr


URI = "qemu:///system"
DOMAIN_TYPE = "kvm"
ROOT = Path(__file__).resolve().parents[2]
REMOTE_CONFIG = None
SENTINEL = "NEX recovery S0 disposable lab"
IMAGE_ROOT = Path("/var/lib/libvirt/images/nex-recovery-s0")
REPORT = Path("/tmp/recovery-s0-native.json")
INCOMPLETE = False


def configured_domain_type(value):
    if value not in ("kvm", "qemu"):
        raise ValueError("native probe domain type must be kvm or qemu")
    return value


def require_enforcing_domain_profile(context, domain_uuid):
    expected = f"libvirt-{domain_uuid} (enforce)"
    observed = context.strip()
    if observed != expected:
        raise RuntimeError(f"QEMU is not under the expected enforcing AppArmor profile: {observed!r}")
    return observed


def snapshot_delete_flags(snapshot_xml):
    import xml.etree.ElementTree as ET

    root = ET.fromstring(snapshot_xml) if isinstance(snapshot_xml, str) else snapshot_xml
    active = []
    for node in root.iter():
        if node.tag.rsplit("}", 1)[-1] != "snapshotDeleteInProgress":
            continue
        value = (node.text or "").strip().lower()
        if value not in ("false", "no", "0"):
            active.append({"tag": node.tag, "value": node.text})
    return active


def delete_apparmor_context_observed(evidence):
    observation = evidence.get("qemu_process_context", {})
    return not observation.get("required") or bool(observation.get("observed"))


def delete_apparmor_context_status(evidence):
    observation = evidence.get("qemu_process_context", {})
    required = bool(observation.get("required"))
    observed = bool(observation.get("observed"))
    return {"required": required, "observed": observed,
            "verified": observed if required else None}


def enforcing_qemu_process(name, domain_uuid):
    expected = f"libvirt-{domain_uuid} (enforce)"
    pid_file = Path("/run/libvirt/qemu") / f"{name}.pid"
    try:
        pid = int(pid_file.read_text().strip())
        process = Path("/proc") / str(pid)
        context = (process / "attr/current").read_text().strip()
        executable = Path(os.readlink(process / "exe")).name
    except (OSError, ValueError):
        return []
    if context != expected or not executable.startswith("qemu"):
        return []
    now = datetime.now(timezone.utc).isoformat()
    return [{"pid": pid, "executable": executable, "context": context,
             "first_observed_at_utc": now, "last_observed_at_utc": now}]


class VirshError(Exception):
    pass


class IncompleteOperation(Exception):
    def __init__(self, message, stdout="", stderr=""):
        super().__init__(message)
        self.stdout = stdout.decode(errors="replace") if isinstance(stdout, bytes) else (stdout or "")
        self.stderr = stderr.decode(errors="replace") if isinstance(stderr, bytes) else (stderr or "")


def run(*args, check=True, input=None, timeout=120):
    if len(args) == 1 and isinstance(args[0], (list, tuple)):
        args = tuple(args[0])
    try:
        result = subprocess.run([str(a) for a in args], input=input, text=True, timeout=timeout,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    except subprocess.TimeoutExpired as exc:
        raise IncompleteOperation(f"timed out after {timeout} seconds: {args[0]}",
                                  exc.stdout, exc.stderr) from exc
    if check and result.returncode:
        raise RuntimeError(f"{args[0]} failed: {result.stdout.strip()} {result.stderr.strip()}".strip())
    return result


def virsh(*args, check=True, timeout=120):
    result = run("virsh", "-c", URI, *args, check=False, timeout=timeout)
    if check and result.returncode:
        raise VirshError(f"virsh {' '.join(str(arg) for arg in args)}: "
                         f"{result.stderr.strip() or result.stdout.strip()}")
    return result


def qemu_io(path, operation, value):
    result = run("qemu-io", "-f", "qcow2", "-c",
                 f"{operation} -P 0x{value:02x} 4096 4096", path, check=False)
    if result.returncode:
        raise RuntimeError(f"qemu-io {operation} expected 0x{value:02x} at {path}: "
                           f"{result.stdout.strip()} {result.stderr.strip()}".strip())
    return result


def source_paths(name, targets):
    xml = virsh("dumpxml", name, "--inactive").stdout
    import xml.etree.ElementTree as ET
    domain = ET.fromstring(xml)
    paths = {}
    for disk in domain.findall("./devices/disk[@device='disk']"):
        target = disk.find("target").get("dev")
        source = disk.find("source")
        paths[target] = source.get("file")
    if set(paths) != set(targets) or any(not path for path in paths.values()):
        raise RuntimeError(f"unexpected inactive disk sources: {paths}")
    return paths


def domain_targets(name):
    xml = virsh("dumpxml", name, "--inactive").stdout
    import xml.etree.ElementTree as ET
    domain = ET.fromstring(xml)
    targets = [disk.find("target").get("dev")
               for disk in domain.findall("./devices/disk[@device='disk']")]
    if not targets or len(targets) != len(set(targets)):
        raise RuntimeError(f"unexpected inactive disk targets: {targets}")
    return targets


def snapshot_disk_paths(snapshot_xml):
    import xml.etree.ElementTree as ET
    if isinstance(snapshot_xml, str):
        snapshot_xml = ET.fromstring(snapshot_xml)
    saved = {}
    for disk in snapshot_xml.findall("./domain/devices/disk[@device='disk']"):
        target = disk.find("target").get("dev")
        source = disk.find("source")
        path = source.get("file") if source is not None else None
        if not path or target in saved:
            raise RuntimeError("snapshot saved domain has missing or duplicate disk sources")
        saved[target] = path
    post_point = {}
    for disk in snapshot_xml.findall("./disks/disk"):
        target = disk.get("name")
        source = disk.find("source")
        path = source.get("file") if source is not None else None
        if not path or target in post_point:
            raise RuntimeError("snapshot post-point disks have missing or duplicate sources")
        post_point[target] = path
    if not saved or set(saved) != set(post_point):
        raise RuntimeError("snapshot saved domain and external disk sources differ")
    return saved, post_point


def set_markers(name, targets, value):
    state = virsh("domstate", name).stdout.strip().lower()
    if state != "shut off":
        raise RuntimeError(f"refusing qemu-io while {name} is {state}")
    paths = source_paths(name, targets)
    for path in paths.values():
        qemu_io(path, "write", value)
        qemu_io(path, "read", value)


def check_markers(name, targets, value):
    state = virsh("domstate", name).stdout.strip().lower()
    if state != "shut off":
        raise RuntimeError(f"refusing qemu-io while {name} is {state}")
    paths = source_paths(name, targets)
    for path in paths.values():
        qemu_io(path, "read", value)
    return paths


def chain(path):
    return json.loads(run("qemu-img", "info", "--backing-chain", "--output=json", path).stdout)


def chain_observation(path):
    result = run("qemu-img", "info", "--backing-chain", "--output=json", path, check=False)
    if result.returncode:
        return {"head": path, "error": result.stderr.strip() or result.stdout.strip()}
    return {"head": path, "images": json.loads(result.stdout)}


def chain_usage(images):
    unique = {}
    for image in images:
        unique[image.get("filename")] = int(image.get("actual-size", 0))
    return {"image_count": len(unique), "actual_bytes": sum(unique.values())}


def qemu_storage_access_probe(path):
    """Match Core's disposable 0600 file ownership/access check for this mount."""
    path = Path(path)
    run("runuser", "-u", "libvirt-qemu", "--", "test", "-x", path)
    uid = int(run("id", "-u", "libvirt-qemu").stdout.strip())
    probe = path / f".nex-qemu-access-{uuid.uuid4().hex}"
    try:
        fd = os.open(probe, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        try:
            os.fchown(fd, uid, -1)
            stat = os.fstat(fd)
            if stat.st_uid != uid or stat.st_mode & 0o777 != 0o600:
                os.fchmod(fd, 0o600)
                stat = os.fstat(fd)
            if stat.st_uid != uid or stat.st_mode & 0o777 != 0o600:
                raise RuntimeError("storage cannot retain a QEMU-owned mode-0600 probe")
            os.fsync(fd)
        finally:
            os.close(fd)
        for option in ("-r", "-w"):
            run("runuser", "-u", "libvirt-qemu", "--", "test", option, probe)
    finally:
        probe.unlink(missing_ok=True)
    return {"directory_executable": True, "probe_owner_uid": uid,
            "probe_mode": "0600", "qemu_readable": True, "qemu_writable": True}


def require_exact_mount(output, target, fstype, source):
    fields = output.strip().split(None, 2)
    if len(fields) != 3 or fields != [str(target), fstype, source]:
        raise RuntimeError(f"selected mount differs from required exact target/type/source: {output.strip()!r}")
    return fields


def require_iscsi_attachment(output, target, device):
    lines = output.splitlines()
    for index, line in enumerate(lines):
        target_line = line.strip()
        if not (target_line == f"Target: {target}" or
                target_line.startswith(f"Target: {target} ")):
            continue
        session = []
        for candidate in lines[index + 1:]:
            if candidate.strip().startswith("Target:"):
                break
            session.append(candidate.strip())
        disk_name = Path(device).name
        if any(line.split()[:4] == ["Attached", "scsi", "disk", disk_name]
               for line in session):
            return
    raise RuntimeError("selected iSCSI target session is not attached to the mounted device")


def snapshot(name, label, targets):
    previous = source_paths(name, targets)
    previous_chains = {target: chain(path) for target, path in previous.items()}
    args = ["snapshot-create-as", name, label, "--disk-only", "--atomic"]
    expected = {}
    for index, target in enumerate(targets):
        head = ROOT / name / f"{label}-{index}.qcow2"
        expected[target] = str(head)
        args.extend(["--diskspec", f"{target},snapshot=external,file={head}"])
    virsh(*args)
    heads = source_paths(name, targets)
    observed = {}
    for target in targets:
        if heads[target] != expected[target]:
            raise RuntimeError(f"libvirt did not switch {target} to the requested external head")
        images = chain(heads[target])
        files = [image.get("filename") for image in images]
        expected_chain = [heads[target], *[image.get("filename") for image in previous_chains[target]]]
        if files != expected_chain:
            raise RuntimeError(f"{target} external backing chain differs from its exact prior chain")
        observed[target] = {"head": heads[target], "chain": images,
                            "usage": chain_usage(images)}
    return observed


def start_stop_for_libvirt_ownership(name, targets, require_qemu_access=False,
                                     require_apparmor=False):
    """Record libvirt's DAC ownership around a start/stop before offline snapshots."""
    before = {target: run("stat", "-c", "%U:%G %a", path).stdout.strip()
              for target, path in source_paths(name, targets).items()}
    qemu_access_before = {target: {
        "readable": run("runuser", "-u", "libvirt-qemu", "--", "test", "-r", path,
                         check=False).returncode == 0,
        "writable": run("runuser", "-u", "libvirt-qemu", "--", "test", "-w", path,
                         check=False).returncode == 0,
    } for target, path in source_paths(name, targets).items()}
    qemu_storage_probe = qemu_storage_access_probe(IMAGE_ROOT) if require_qemu_access else None
    virsh("start", name)
    state = virsh("domstate", name).stdout.strip().lower()
    if state not in ("running", "paused"):
        raise RuntimeError(f"domain did not start for ownership preparation: {state}")
    apparmor = None
    if require_apparmor:
        pid_file = Path("/run/libvirt/qemu") / f"{name}.pid"
        qemu_pid = int(pid_file.read_text().strip())
        context = Path(f"/proc/{qemu_pid}/attr/current").read_text()
        apparmor = {"qemu_pid": qemu_pid,
                    "context": require_enforcing_domain_profile(
                        context, virsh("domuuid", name).stdout.strip())}
    running = {target: run("stat", "-c", "%U:%G %a", path).stdout.strip()
               for target, path in source_paths(name, targets).items()}
    virsh("destroy", name)
    stopped = virsh("domstate", name).stdout.strip().lower()
    if stopped != "shut off":
        raise RuntimeError(f"domain did not stop after ownership preparation: {stopped}")
    inactive = {target: run("stat", "-c", "%U:%G %a", path).stdout.strip()
                for target, path in source_paths(name, targets).items()}
    return {"state_before_start": before, "state_while_running": running,
            "state_after_destroy": inactive,
            "qemu_access_before_start": qemu_access_before,
            "qemu_storage_access_probe": qemu_storage_probe,
            "apparmor": apparmor,
            "guest_os": False}


def revert(name, label):
    return virsh("snapshot-revert", name, label, "--force")


def revert_overlay_uuid(path):
    suffix = Path(path).name.rsplit(".", 1)[-1]
    try:
        parsed = str(uuid.UUID(suffix))
    except ValueError as exc:
        raise RuntimeError(f"revert overlay does not end in a UUID: {path}") from exc
    if suffix != parsed:
        raise RuntimeError(f"revert overlay UUID suffix is not canonical: {path}")
    return parsed


def revert_evidence(name, label, targets, expected_marker, verify_uuid=False, seen_paths=None):
    before = source_paths(name, targets)
    revert(name, label)
    after = check_markers(name, targets, expected_marker)
    chains = {target: chain(path) for target, path in after.items()}
    observed = {"snapshot": label, "before_heads": before,
                "heads": after, "chains": chains,
                "usage": {target: chain_usage(images) for target, images in chains.items()},
                "marker": f"0x{expected_marker:02x}"}
    if verify_uuid:
        overlays = {}
        for target, path in after.items():
            if path == before[target]:
                raise RuntimeError(f"revert did not create a new overlay for {target}")
            overlay_uuid = revert_overlay_uuid(path)
            if seen_paths is not None and path in seen_paths:
                raise RuntimeError(f"revert reused an overlay path: {path}")
            if seen_paths is not None:
                seen_paths.add(path)
            overlays[target] = {"path": path, "uuid": overlay_uuid}
        observed["uuid_overlays"] = overlays
    return observed


def delete_snapshot(name, label, require_apparmor=False):
    domain_uuid = virsh("domuuid", name).stdout.strip()
    profile = Path(f"/etc/apparmor.d/libvirt/libvirt-{domain_uuid}.files")
    captured = []
    qemu_processes = {}
    stop = threading.Event()

    def capture_profile():
        previous = None
        while not stop.wait(0.001):
            try:
                content = profile.read_text()
            except OSError:
                continue
            if content != previous:
                captured.append({"observed_at_utc": datetime.now(timezone.utc).isoformat(),
                                 "content": content})
                previous = content

    def capture_qemu_processes():
        for process in enforcing_qemu_process(name, domain_uuid):
            existing = qemu_processes.setdefault(process["pid"], process)
            existing["last_observed_at_utc"] = process["last_observed_at_utc"]

    def capture_qemu_context():
        while True:
            capture_qemu_processes()
            if stop.wait(0.005):
                return

    watcher = threading.Thread(target=capture_profile, daemon=True)
    qemu_watcher = threading.Thread(target=capture_qemu_context, daemon=True)
    started_at = datetime.now(timezone.utc).isoformat()
    watcher.start()
    qemu_watcher.start()
    try:
        result = virsh("snapshot-delete", name, label, check=False)
    finally:
        stop.set()
        watcher.join(timeout=1)
        qemu_watcher.join(timeout=1)
    process_observation = {
        "required": require_apparmor,
        "observed": bool(qemu_processes),
        "processes": list(qemu_processes.values()),
    }
    return result, {"path": str(profile), "started_at_utc": started_at,
                    "finished_at_utc": datetime.now(timezone.utc).isoformat(),
                    "transitions": captured,
                    "qemu_process_context": process_observation}


def snapshot_flags_for_domain(name):
    flags = {}
    labels = [label.strip() for label in virsh("snapshot-list", name, "--name").stdout.splitlines()
              if label.strip()]
    for label in labels:
        flags[label] = snapshot_delete_flags(virsh("snapshot-dumpxml", name, label).stdout)
    unresolved = {label: active for label, active in flags.items() if active}
    if unresolved:
        raise RuntimeError("snapshotDeleteInProgress remains after deletion: " +
                           json.dumps(unresolved, sort_keys=True))
    return flags


def inspect_failure(name, targets, expected_marker):
    observed = {"domain_state": "unavailable", "heads": {}, "snapshots": "unavailable"}
    state = virsh("domstate", name, check=False)
    if state.returncode == 0:
        observed["domain_state"] = state.stdout.strip()
    paths = source_paths(name, targets)
    observed["heads"] = paths
    observed["chains"] = {target: chain(path) for target, path in paths.items()}
    observed["files"] = {}
    observed["qemu_access"] = {}
    directories = {str(ROOT), str(ROOT / name)}
    for target, images in observed["chains"].items():
        observed["files"][target] = []
        observed["qemu_access"][target] = []
        for image in images:
            path = image["filename"]
            directories.add(str(Path(path).parent))
            stat = run("stat", "-c", "%U:%G %a", path, check=False)
            observed["files"][target].append({"path": path, "owner_mode": stat.stdout.strip(),
                                                "error": stat.stderr.strip() if stat.returncode else ""})
            observed["qemu_access"][target].append({
                "path": path,
                "readable": run("runuser", "-u", "libvirt-qemu", "--", "test", "-r", path,
                                check=False).returncode == 0,
                "writable": run("runuser", "-u", "libvirt-qemu", "--", "test", "-w", path,
                                check=False).returncode == 0,
            })
    observed["directories"] = {
        path: run("stat", "-c", "%U:%G %a", path, check=False).stdout.strip()
        for path in sorted(directories)
    }
    listing = virsh("snapshot-list", name, check=False)
    observed["snapshots"] = listing.stdout.strip() if listing.returncode == 0 else listing.stderr.strip()
    observed["snapshot_delete_in_progress"] = []
    if listing.returncode == 0:
        for label in virsh("snapshot-list", name, "--name").stdout.splitlines():
            label = label.strip()
            if not label:
                continue
            metadata = virsh("snapshot-dumpxml", name, label, check=False)
            if metadata.returncode == 0 and "<snapshotDeleteInProgress" in metadata.stdout:
                observed["snapshot_delete_in_progress"].append(label)
    observed["libvirt_logs"] = run("journalctl", "-u", "libvirtd", "-u", "virtqemud",
                                   "--since=5 minutes ago", "--no-pager", check=False).stdout[-12000:]
    kernel_log = run("journalctl", "-k", "--since=5 minutes ago", "--no-pager", check=False).stdout
    observed["kernel_denials"] = "\n".join(line for line in kernel_log.splitlines()
                                             if "apparmor=\"denied\"" in line.lower() or "denied" in line.lower())
    try:
        check_markers(name, targets, expected_marker)
        observed["expected_markers_intact"] = True
    except Exception as exc:
        observed["expected_markers_intact"] = False
        observed["marker_check_error"] = str(exc)
    return observed


def probe_atomic_create_failure():
    """Fail one disk target and prove a two-disk atomic create leaves no changed chain."""
    global INCOMPLETE
    name = f"nex-recovery-s0-atomic-{uuid.uuid4().hex[:8]}"
    work = ROOT / name
    work.mkdir(parents=True, mode=0o755)
    targets = ("vda", "vdb")
    disks = (work / "disk-0.qcow2", work / "disk-1.qcow2")
    expected = {"vda": 0x11, "vdb": 0x22}
    result = {"name": name, "case": "two-disk-atomic-create-failure",
              "status": "incomplete", "steps": []}
    defined = False
    native_attempted = False
    preserve = False
    try:
        domain = [f"<domain type='{DOMAIN_TYPE}'>", f"<name>{name}</name>",
                  "<memory unit='MiB'>256</memory><currentMemory unit='MiB'>256</currentMemory>",
                  "<vcpu>1</vcpu><os><type arch='x86_64' machine='pc'>hvm</type><boot dev='hd'/></os>",
                  "<features><acpi/><apic/></features><devices>",
                  "<emulator>/usr/bin/qemu-system-x86_64</emulator>"]
        for target, disk in zip(targets, disks):
            run("qemu-img", "create", "-f", "qcow2", disk, "64M")
            os.chmod(disk, 0o600)
            domain.append(f"<disk type='file' device='disk'><driver name='qemu' type='qcow2' cache='none'/><source file='{disk}'/><target dev='{target}' bus='virtio'/></disk>")
        domain.append("</devices></domain>")
        xml_path = work / "domain.xml"
        xml_path.write_text("".join(domain))
        virsh("define", xml_path)
        defined = True
        require_qemu_access = bool(REMOTE_CONFIG and REMOTE_CONFIG.get("require_qemu_access"))
        ownership = start_stop_for_libvirt_ownership(
            name, targets, require_qemu_access,
            bool(REMOTE_CONFIG and REMOTE_CONFIG.get("require_apparmor")))
        paths = source_paths(name, targets)
        for target, path in paths.items():
            qemu_io(path, "write", expected[target])
            qemu_io(path, "read", expected[target])
        before = {target: {"head": path, "chain": chain(path)} for target, path in paths.items()}
        result["steps"].append({"name": "baseline-and-libvirt-owner-observation",
                                "status": "passed", "ownership": ownership,
                                "markers": expected, "chains": before})

        args = ["snapshot-create-as", name, "injected-two-disk-failure",
                "--disk-only", "--atomic",
                "--diskspec", f"vda,snapshot=external,file={work / 'head-0.qcow2'}",
                "--diskspec", f"vdb,snapshot=external,file={work / 'missing' / 'head-1.qcow2'}"]
        native_attempted = True
        attempted = virsh(*args, check=False)
        result["native_command"] = {"returncode": attempted.returncode,
                                    "stdout": attempted.stdout.strip(),
                                    "stderr": attempted.stderr.strip()}
        after_paths = source_paths(name, targets)
        after_chains = {target: chain(path) for target, path in after_paths.items()}
        marker_results = {}
        state = virsh("domstate", name, check=False)
        if state.returncode == 0 and state.stdout.strip().lower() == "shut off":
            for target, path in after_paths.items():
                check = qemu_io(path, "read", expected[target])
                marker_results[target] = check.returncode == 0
        else:
            marker_results = {target: False for target in targets}
        snapshots = virsh("snapshot-list", name, "--name", check=False)
        files = sorted(str(path.relative_to(work)) for path in work.rglob("*") if path.is_file())
        unchanged = (attempted.returncode != 0 and after_paths == paths and
                     all(after_chains[target] == before[target]["chain"] for target in targets) and
                     all(marker_results.values()) and snapshots.returncode == 0 and
                     not snapshots.stdout.strip() and
                     not (work / "head-0.qcow2").exists() and not (work / "missing").exists())
        result["after_failure"] = {"domain_state": state.stdout.strip(),
                                   "heads": after_paths, "chains": after_chains,
                                   "markers_intact": marker_results,
                                   "snapshot_names": snapshots.stdout.strip(), "files": files}
        result["status"] = "passed" if unchanged else "failed"
        if not unchanged:
            preserve = True
            result["preserve"] = True
            result["error"] = "atomic create failure changed or incompletely exposed native state"
    except IncompleteOperation as exc:
        INCOMPLETE = True
        result["status"] = "incomplete"
        result["error"] = str(exc)
        result["partial_stdout"] = exc.stdout
        result["partial_stderr"] = exc.stderr
        preserve = True
    except Exception as exc:
        result["status"] = "failed"
        result["error"] = str(exc)
        if native_attempted:
            preserve = True
            result["preserve"] = True
            try:
                result["after_failure"] = inspect_failure(name, targets, expected["vda"])
            except Exception as observe_error:
                result["after_failure"] = {"observation_error": str(observe_error)}
    if preserve:
        REPORT.with_name(REPORT.stem + "-keep").touch()
        result["cleanup"] = {"status": "preserved-for-review", "domain": name,
                             "directory": str(work)}
        return result
    try:
        state = virsh("domstate", name, check=False, timeout=15)
        if state.returncode == 0 and state.stdout.strip().lower() != "shut off":
            virsh("destroy", name, timeout=15)
        if defined:
            virsh("undefine", name, "--snapshots-metadata", timeout=15)
    except (IncompleteOperation, VirshError) as exc:
        INCOMPLETE = True
        result["status"] = "incomplete"
        result["cleanup"] = {"status": "incomplete", "error": str(exc),
                             "domain": name, "directory": str(work)}
        REPORT.with_name(REPORT.stem + "-keep").touch()
    else:
        shutil.rmtree(work, ignore_errors=True)
    return result


def retained_snapshot_evidence(name, expected_labels):
    state = virsh("domstate", name).stdout.strip().lower()
    if state != "shut off":
        raise RuntimeError(f"refusing reconciliation while {name} is {state}")
    labels = [label for label in virsh("snapshot-list", name, "--name").stdout.splitlines() if label]
    if set(labels) != set(expected_labels) or len(labels) != len(expected_labels):
        raise RuntimeError(f"snapshot metadata labels changed unexpectedly: {labels}")
    targets = domain_targets(name)
    current = source_paths(name, targets)
    current_snapshot = virsh("snapshot-current", name, "--name").stdout.strip()
    evidence = {"domain_state": state, "domain_uuid": virsh("domuuid", name).stdout.strip(),
                "current_snapshot": current_snapshot,
                "targets": targets,
                "current": {target: chain_observation(path)
                            for target, path in current.items()},
                "snapshots": {}, "integrity_errors": []}
    marker_errors = []
    for target, path in current.items():
        try:
            qemu_io(path, "read", 0x11)
        except Exception as exc:
            marker_errors.append(f"current/{target}: {exc}")
    for target, item in evidence["current"].items():
        if "error" in item:
            evidence["integrity_errors"].append(f"current/{target}: {item['error']}")
    for label in expected_labels:
        import xml.etree.ElementTree as ET
        snapshot_xml = ET.fromstring(virsh("snapshot-dumpxml", name, label).stdout)
        if snapshot_xml.findtext("state") != "shutoff":
            raise RuntimeError(f"snapshot {label} is not a stopped point")
        delete_flags = snapshot_delete_flags(snapshot_xml)
        if delete_flags:
            evidence["integrity_errors"].append(
                f"{label} has snapshotDeleteInProgress: {json.dumps(delete_flags, sort_keys=True)}")
        saved_paths, post_point_paths = snapshot_disk_paths(snapshot_xml)
        parent = snapshot_xml.findtext("./parent/name")
        points = {}
        for disk in snapshot_xml.findall("./disks/disk"):
            target = disk.get("name")
            if target not in targets:
                raise RuntimeError(f"snapshot {label} has an unexpected disk target")
            point_path = saved_paths[target]
            post_point_path = post_point_paths[target]
            expected_marker = 0x22 if label == "point-b" else 0x11
            marker_ok = True
            try:
                qemu_io(point_path, "read", expected_marker)
            except Exception as exc:
                marker_ok = False
                marker_errors.append(f"{label}/{target} saved domain source {point_path}: {exc}")
            saved_chain = chain_observation(point_path)
            post_point_chain = chain_observation(post_point_path)
            points[target] = {"saved_point_source": point_path,
                              "post_point_external_head": post_point_path,
                              "snapshot_parent": parent,
                              "marker": f"0x{expected_marker:02x}",
                              "marker_ok": marker_ok,
                              "saved_point_chain": saved_chain,
                              "saved_point_usage": (chain_usage(saved_chain["images"])
                                                     if "images" in saved_chain else None),
                              "post_point_chain": post_point_chain,
                              "post_point_usage": (chain_usage(post_point_chain["images"])
                                                    if "images" in post_point_chain else None),
                              "post_point_head_exists": Path(post_point_path).is_file(),
                              "delete_in_progress": bool(delete_flags)}
            observation = points[target]["saved_point_chain"]
            if "error" in observation:
                evidence["integrity_errors"].append(f"{label}/{target} saved_point_chain: {observation['error']}")
            post = points[target]["post_point_chain"]
            if "error" in post and points[target]["post_point_head_exists"]:
                evidence["integrity_errors"].append(f"{label}/{target} post_point_chain: {post['error']}")
            if "error" in post and not points[target]["post_point_head_exists"]:
                if label == current_snapshot:
                    evidence["integrity_errors"].append(f"current snapshot {label}/{target} post-point head is missing")
                else:
                    points[target]["post_point_head_status"] = "missing-noncurrent-revert-branch-source"
            else:
                points[target]["post_point_head_status"] = "readable"
        if set(points) != set(targets):
            raise RuntimeError(f"snapshot {label} does not describe every disk")
        evidence["snapshots"][label] = points
    linked_paths = set()
    for observation in evidence["current"].values():
        for image in observation.get("images", []):
            linked_paths.add(image.get("filename"))
    for snapshots in evidence["snapshots"].values():
        for point in snapshots.values():
            for image in point["saved_point_chain"].get("images", []):
                linked_paths.add(image.get("filename"))
    for label, snapshots in evidence["snapshots"].items():
        for target, point in snapshots.items():
            if (point.get("post_point_head_status") == "missing-noncurrent-revert-branch-source" and
                    point["post_point_external_head"] in linked_paths):
                evidence["integrity_errors"].append(
                    f"missing noncurrent post-point head is still referenced by an active or saved-point chain: {label}/{target}")
    evidence["integrity_errors"].extend(marker_errors)
    return evidence


def reconcile_and_retry_delete(name, label):
    global INCOMPLETE
    if not name.startswith("nex-recovery-s0-"):
        raise RuntimeError("refusing to inspect a domain outside the disposable recovery S0 namespace")
    expected_labels = ("point-a", "point-b", "point-c-after-revert")
    if label == "point-c":
        expected_labels = ("point-a", "point-b", "point-c")
    if label not in expected_labels:
        raise RuntimeError(f"retry target is not a known failed-delete point: {label}")
    result = {"name": name, "retry_target": label, "status": "reconciling"}
    result["before"] = retained_snapshot_evidence(name, expected_labels)
    if result["before"]["integrity_errors"]:
        result["status"] = "reconciled-before-delete-with-integrity-errors"
        result["preserve"] = True
        INCOMPLETE = True
        return result
    deleted, profile = delete_snapshot(name, label)
    result["native_delete"] = {"returncode": deleted.returncode,
                               "stdout": deleted.stdout.strip(), "stderr": deleted.stderr.strip(),
                               "apparmor_profile_during_delete": profile or None}
    try:
        result["after"] = retained_snapshot_evidence(
            name, [candidate for candidate in expected_labels if candidate != label])
        if result["after"]["integrity_errors"]:
            raise RuntimeError("post-delete reconciliation found integrity errors")
    except Exception as exc:
        result["status"] = "uncertain-after-native-delete"
        result["observation_error"] = str(exc)
        result["preserve"] = True
        INCOMPLETE = True
        return result
    if deleted.returncode:
        result["status"] = "native-delete-failed-state-reconciled"
        result["preserve"] = True
        INCOMPLETE = True
    elif not delete_apparmor_context_observed(profile):
        result["status"] = "incomplete-apparmor-context-unobserved"
        result["preserve"] = True
        INCOMPLETE = True
    else:
        result["status"] = "passed"
    return result


def probe_disks(count, paced=False):
    global INCOMPLETE
    mode = "paced" if paced else "rapid"
    name = f"nex-recovery-s0-{mode}-{uuid.uuid4().hex[:8]}"
    work = ROOT / name
    work.mkdir(parents=True, mode=0o755)
    targets = [f"vd{chr(ord('a') + i)}" for i in range(count)]
    disks = [work / f"disk-{i}.qcow2" for i in range(count)]
    domain = [f"<domain type='{DOMAIN_TYPE}'>", f"<name>{name}</name>",
              "<memory unit='MiB'>256</memory><currentMemory unit='MiB'>256</currentMemory>",
              "<vcpu>1</vcpu><os><type arch='x86_64' machine='pc'>hvm</type><boot dev='hd'/></os>",
              "<features><acpi/><apic/></features><devices>",
              "<emulator>/usr/bin/qemu-system-x86_64</emulator>"]
    for target, disk in zip(targets, disks):
        run("qemu-img", "create", "-f", "qcow2", disk, "64M")
        os.chmod(disk, 0o600)
        domain.append(f"<disk type='file' device='disk'><driver name='qemu' type='qcow2' cache='none'/><source file={quoteattr(str(disk))}/><target dev='{target}' bus='virtio'/></disk>")
    domain.append("</devices></domain>")
    xml_path = work / "domain.xml"
    xml_path.write_text("".join(domain))
    result = {
        "name": name,
        "disk_count": count,
        "mode": mode,
        "targets": targets,
        "steps": [],
        "status": "running",
    }
    expected_marker = 0x11
    defined = False
    try:
        virsh("define", xml_path)
        defined = True
        require_qemu_access = bool(REMOTE_CONFIG and REMOTE_CONFIG.get("require_qemu_access"))
        ownership = start_stop_for_libvirt_ownership(
            name, targets, require_qemu_access,
            bool(REMOTE_CONFIG and REMOTE_CONFIG.get("require_apparmor")))
        result["steps"].append({"name": "libvirt-owner-start-stop-observed", "status": "observed",
                                **ownership})
        set_markers(name, targets, 0x11)
        result["steps"].append({"name": "baseline-marker", "status": "passed"})

        # A stopped guest is the phase-5 baseline. Refusal is evidence; no alternate
        # hand-built qemu-img chain is substituted for libvirt's operation.
        for label, value in (("point-a", 0x22), ("point-b", 0x33)):
            chain_evidence = snapshot(name, label, targets)
            set_markers(name, targets, value)
            expected_marker = value
            result["steps"].append({"name": f"create-{label}-and-write", "status": "passed",
                                    "heads": source_paths(name, targets), "chain": chain_evidence})

        before = source_paths(name, targets)
        result["steps"].append({"name": "backing-chain-observed", "status": "passed",
                                "heads": before, "chain": {target: chain(path) for target, path in before.items()}})

        revert_records = []
        seen_revert_paths = set()
        verify_uuid = bool(REMOTE_CONFIG and REMOTE_CONFIG.get("expect_uuid_revert_overlays"))
        revert_records.append(revert_evidence(name, "point-a", targets, 0x11,
                                              verify_uuid, seen_revert_paths))
        expected_marker = 0x11
        result["steps"].append({"name": "revert-older-point", "status": "passed",
                                **revert_records[-1]})

        if paced:
            time.sleep(1.1)
        revert_records.append(revert_evidence(name, "point-b", targets, 0x22,
                                              verify_uuid, seen_revert_paths))
        expected_marker = 0x22
        if paced:
            time.sleep(1.1)
        revert_records.append(revert_evidence(name, "point-a", targets, 0x11,
                                              verify_uuid, seen_revert_paths))
        expected_marker = 0x11
        result["steps"].append({"name": "revert-newer-then-repeat-older", "status": "passed",
                                "reverts": revert_records[1:]})

        # A new point after reverting exercises the branch that makes snapshot deletion
        # unsafe to infer from a simple parent/child list.
        chain_evidence = snapshot(name, "point-c-after-revert", targets)
        set_markers(name, targets, 0x44)
        expected_marker = 0x44
        if paced:
            time.sleep(1.1)
        revert_records.append(revert_evidence(name, "point-c-after-revert", targets, 0x11,
                                              verify_uuid, seen_revert_paths))
        expected_marker = 0x11
        result["steps"].append({"name": "repeated-revert-after-new-point", "status": "passed",
                                "chain": chain_evidence, "revert": revert_records[-1]})

        for label in ("point-c-after-revert", "point-b", "point-a"):
            deleted, apparmor_profile = delete_snapshot(name, label)
            step = {"name": f"delete-{label}",
                    "status": "passed" if deleted.returncode == 0 else "failed",
                    "stdout": deleted.stdout.strip(), "stderr": deleted.stderr.strip(),
                    "apparmor_context": delete_apparmor_context_status(apparmor_profile)}
            if deleted.returncode:
                step["classification"] = "unsupported" if "unsupported configuration:" in deleted.stderr.lower() else "native-command-error"
            if apparmor_profile:
                step["apparmor_profile_during_delete"] = apparmor_profile
            result["steps"].append(step)
            if deleted.returncode:
                step["classification"] = "unsupported" if "unsupported configuration:" in deleted.stderr.lower() else "native-command-error"
                result["status"] = "partial"
                try:
                    step["after_failure"] = inspect_failure(name, targets, 0x11)
                except Exception as exc:
                    step["after_failure"] = {"observation_error": str(exc)}
                # A failed commit can leave a chain partially advanced. Keep the disposable
                # domain and every image intact until the captured observations are reviewed.
                result["preserve"] = True
                INCOMPLETE = True
                break
            if not delete_apparmor_context_observed(apparmor_profile):
                step["status"] = "incomplete"
                step["classification"] = "apparmor-context-unobserved-during-delete"
                result["status"] = "partial"
                result["preserve"] = True
                INCOMPLETE = True
                try:
                    step["after_context_failure"] = inspect_failure(name, targets, 0x11)
                except Exception as exc:
                    step["after_context_failure"] = {"observation_error": str(exc)}
                break
            current = check_markers(name, targets, 0x11)
            for path in current.values():
                chain(path)
            step["heads"] = current
            step["snapshot_delete_in_progress"] = snapshot_flags_for_domain(name)
        if result["status"] != "partial":
            # This synthetic BIOS domain has no guest OS; this proves libvirt/QEMU can
            # reopen the consolidated chain, not that a guest OS boots successfully.
            virsh("start", name)
            observed = virsh("domstate", name).stdout.strip().lower()
            result["steps"].append({"name": "start-after-delete", "status": "passed",
                                    "observed_state": observed, "guest_os": False})
            if observed not in ("running", "paused"):
                raise RuntimeError(f"domain did not become active after snapshot deletion: {observed}")
            virsh("destroy", name)
            result["status"] = "passed"
    except VirshError as exc:
        result["status"] = "failed"
        result["error"] = str(exc)
        result["classification"] = "unsupported" if "unsupported configuration:" in str(exc).lower() else "native-command-error"
        if defined:
            result["preserve"] = True
        try:
            result["after_failure"] = inspect_failure(name, targets, expected_marker)
            message = str(exc).lower()
            if ("snapshot-revert" in message and "external snapshot file" in message and
                    "already exists" in message and result["after_failure"].get("expected_markers_intact") is True):
                result["status"] = "observed-refusal-current-marker"
                result["classification"] = "rapid-revert-path-collision"
        except Exception as observe_error:
            result["after_failure"] = {"observation_error": str(observe_error)}
    except IncompleteOperation as exc:
        INCOMPLETE = True
        result["status"] = "incomplete"
        result["error"] = str(exc)
    except Exception as exc:
        result["status"] = "failed"
        result["error"] = str(exc)
        if defined:
            result["preserve"] = True
            try:
                result["after_failure"] = inspect_failure(name, targets, expected_marker)
            except Exception as observe_error:
                result["after_failure"] = {"observation_error": str(observe_error)}
    if result["status"] != "incomplete":
        if result.get("preserve"):
            REPORT.with_name(REPORT.stem + "-keep").touch()
            result["cleanup"] = {"status": "preserved-for-review", "domain": name,
                                 "directory": str(work)}
            return result
        try:
            state = virsh("domstate", name, check=False, timeout=15)
            if state.returncode == 0 and state.stdout.strip().lower() != "shut off":
                virsh("destroy", name, timeout=15)
            if defined:
                virsh("undefine", name, "--snapshots-metadata", timeout=15)
        except (IncompleteOperation, VirshError) as exc:
            INCOMPLETE = True
            result["cleanup"] = {"status": "incomplete", "error": str(exc),
                                 "domain": name, "directory": str(work)}
            Path("/tmp/recovery-s0-native-keep").touch()
            result["status"] = "incomplete"
        else:
            shutil.rmtree(work, ignore_errors=True)
    return result


def verify_retained_markers(name, targets, expected_points, current_marker):
    labels = [label for label in virsh("snapshot-list", name, "--name").stdout.splitlines() if label]
    if set(labels) != set(expected_points) or len(labels) != len(expected_points):
        raise RuntimeError(f"retained snapshot labels differ: {labels}")
    points = {}
    snapshot_flags = {}
    for label, expected in expected_points.items():
        import xml.etree.ElementTree as ET
        snapshot_xml = ET.fromstring(virsh("snapshot-dumpxml", name, label).stdout)
        saved, _ = snapshot_disk_paths(snapshot_xml)
        snapshot_flags[label] = snapshot_delete_flags(snapshot_xml)
        if set(saved) != set(targets):
            raise RuntimeError(f"snapshot {label} does not retain every disk")
        chains = {}
        for target, path in saved.items():
            qemu_io(path, "read", expected)
            images = chain(path)
            chains[target] = {"saved_source": path, "image_count": len(images),
                              "usage": chain_usage(images)}
        points[label] = {"marker": f"0x{expected:02x}", "disks": chains}
    current = check_markers(name, targets, current_marker)
    unresolved_flags = {label: flags for label, flags in snapshot_flags.items() if flags}
    evidence = {"snapshot_count": len(labels), "points": points,
            "snapshot_delete_in_progress": snapshot_flags,
            "snapshot_delete_flags_clear": not unresolved_flags,
            "unresolved_snapshot_delete_flags": unresolved_flags,
            "current_marker": f"0x{current_marker:02x}",
            "current_heads": current,
            "current_chains": {target: {"image_count": len(images := chain(path)),
                                          "usage": chain_usage(images)}
                               for target, path in current.items()}}
    if unresolved_flags:
        raise RuntimeError("snapshotDeleteInProgress remains after reconciliation: " +
                           json.dumps(unresolved_flags, sort_keys=True))
    return evidence


def reconcile_all_snapshot_markers(name, window_minutes=10):
    """Read every saved point and current head independently, retaining individual failures."""
    import xml.etree.ElementTree as ET
    state = virsh("domstate", name, check=False)
    labels_result = virsh("snapshot-list", name, "--name", check=False)
    labels = [label.strip() for label in labels_result.stdout.splitlines() if label.strip()]
    targets = domain_targets(name)
    current = source_paths(name, targets)
    evidence = {"domain": name, "domain_state": state.stdout.strip(),
                "snapshot_labels": labels, "targets": targets,
                "current": {}, "snapshots": {}, "libvirt_log": "",
                "qemu_log": "", "block_jobs": ""}
    for target, path in current.items():
        marker = run("qemu-io", "-f", "qcow2", "-c", "read -P 0x13 4096 4096", path,
                     check=False)
        marker_candidates = [f"0x{candidate:02x}" for candidate in range(0x10, 0x14)
                             if run("qemu-io", "-f", "qcow2", "-c",
                                    f"read -P 0x{candidate:02x} 4096 4096", path,
                                    check=False).returncode == 0]
        evidence["current"][target] = {
            "head": path, "marker_expected": "0x13", "marker_ok": marker.returncode == 0,
            "matching_marker_candidates": marker_candidates,
            "marker_output": marker.stdout.strip(), "marker_error": marker.stderr.strip(),
            "chain": chain_observation(path)}
    for label in labels:
        snapshot_result = virsh("snapshot-dumpxml", name, label, check=False)
        item = {"metadata_error": snapshot_result.stderr.strip(), "disks": {}}
        if snapshot_result.returncode == 0:
            snapshot_xml = ET.fromstring(snapshot_result.stdout)
            saved, post_point = snapshot_disk_paths(snapshot_xml)
            item["parent"] = snapshot_xml.findtext("./parent/name")
            item["state"] = snapshot_xml.findtext("state")
            item["snapshot_delete_in_progress"] = snapshot_delete_flags(snapshot_xml)
            try:
                expected = 0x10 + int(label.rsplit("-", 1)[1])
            except (ValueError, IndexError):
                expected = None
            for target in targets:
                path = saved.get(target)
                post_path = post_point.get(target)
                if path is None:
                    item["disks"][target] = {"error": "saved domain source missing"}
                    continue
                marker = (run("qemu-io", "-f", "qcow2", "-c",
                              f"read -P 0x{expected:02x} 4096 4096", path,
                              check=False) if expected is not None else None)
                marker_candidates = ([f"0x{candidate:02x}" for candidate in range(0x10, 0x14)
                                      if run("qemu-io", "-f", "qcow2", "-c",
                                             f"read -P 0x{candidate:02x} 4096 4096", path,
                                             check=False).returncode == 0]
                                     if expected is not None else [])
                item["disks"][target] = {
                    "saved_domain_source": path,
                    "expected_marker": f"0x{expected:02x}" if expected is not None else None,
                    "marker_ok": marker is not None and marker.returncode == 0,
                    "matching_marker_candidates": marker_candidates,
                    "marker_output": marker.stdout.strip() if marker else "",
                    "marker_error": marker.stderr.strip() if marker else "",
                    "saved_chain": chain_observation(path),
                    "post_point_source": post_path,
                    "post_point_chain": chain_observation(post_path) if post_path else None,
                }
        evidence["snapshots"][label] = item
    evidence["block_jobs"] = virsh("blockjob", name, "--info", check=False).stdout.strip()
    since = f"{window_minutes} minutes ago"
    evidence["libvirt_log"] = run("journalctl", "-u", "libvirtd", "-u", "virtqemud",
                                   f"--since={since}", "--no-pager", check=False).stdout[-20000:]
    log_path = Path(f"/var/log/libvirt/qemu/{name}.log")
    qemu_log = run("cat", log_path, check=False)
    evidence["qemu_log"] = qemu_log.stdout[-20000:] if qemu_log.returncode == 0 else qemu_log.stderr.strip()
    return evidence


def probe_point_chain_stress(count, disk_count=2):
    """Stress a bounded two-disk chain and validate every retained marker."""
    global INCOMPLETE
    if count not in (3, 16, 32) or disk_count not in (1, 2):
        raise ValueError("chain probe supports three-point controls and 16/32-point two-disk stress")
    if count != 3 and disk_count != 2:
        raise ValueError("16/32-point stress requires two disks")
    name = f"nex-recovery-s0-chain{count}x{disk_count}-{uuid.uuid4().hex[:8]}"
    work = ROOT / name
    work.mkdir(parents=True, mode=0o755)
    targets = ["vda", "vdb"][:disk_count]
    disks = [work / f"disk-{index}.qcow2" for index in range(disk_count)]
    result = {"name": name, "case": f"{disk_count}-disk-{count}-point-chain-control",
              "point_limit_tested": count, "status": "running", "steps": []}
    defined = False
    preserve = False
    started_at = time.monotonic()
    expected_points = {}
    try:
        domain = [f"<domain type='{DOMAIN_TYPE}'>", f"<name>{name}</name>",
                  "<memory unit='MiB'>256</memory><currentMemory unit='MiB'>256</currentMemory>",
                  "<vcpu>1</vcpu><os><type arch='x86_64' machine='pc'>hvm</type><boot dev='hd'/></os>",
                  "<features><acpi/><apic/></features><devices>",
                  "<emulator>/usr/bin/qemu-system-x86_64</emulator>"]
        for target, disk in zip(targets, disks):
            run("qemu-img", "create", "-f", "qcow2", disk, "64M")
            os.chmod(disk, 0o600)
            domain.append(f"<disk type='file' device='disk'><driver name='qemu' type='qcow2' cache='none'/><source file={quoteattr(str(disk))}/><target dev='{target}' bus='virtio'/></disk>")
        domain.append("</devices></domain>")
        xml_path = work / "domain.xml"
        xml_path.write_text("".join(domain))
        virsh("define", xml_path)
        defined = True
        ownership = start_stop_for_libvirt_ownership(
            name, targets, require_apparmor=bool(REMOTE_CONFIG and
                                                 REMOTE_CONFIG.get("require_apparmor")))
        set_markers(name, targets, 0x10)
        result["ownership"] = ownership
        result["capacity_before"] = {"free_bytes": shutil.disk_usage(IMAGE_ROOT).free,
                                     "total_bytes": shutil.disk_usage(IMAGE_ROOT).total}
        result["points"] = []
        for index in range(count):
            label = f"stress-{index:02d}"
            marker = 0x10 + index
            before = time.monotonic()
            observed = snapshot(name, label, targets)
            elapsed = time.monotonic() - before
            expected_points[label] = marker
            next_marker = marker + 1
            set_markers(name, targets, next_marker)
            result["points"].append({"label": label, "retained_marker": f"0x{marker:02x}",
                                     "create_seconds": elapsed, "chain": observed,
                                     "next_current_marker": f"0x{next_marker:02x}"})
        current_marker = 0x10 + count
        result["after_create"] = verify_retained_markers(
            name, targets, expected_points, current_marker)
        parent_label = "stress-00" if count == 3 else f"stress-{count // 4:02d}"
        result["parent_delete"] = {"target": parent_label, "had_descendants": True}
        before_parent_delete = result["after_create"]
        deleted, profile = delete_snapshot(
            name, parent_label, require_apparmor=bool(REMOTE_CONFIG and
                                                      REMOTE_CONFIG.get("require_apparmor")))
        result["parent_delete"].update({"returncode": deleted.returncode,
                                        "stdout": deleted.stdout.strip(),
                                        "stderr": deleted.stderr.strip(),
                                        "apparmor_profile_during_delete": profile or None,
                                        "apparmor_context": delete_apparmor_context_status(profile)})
        if deleted.returncode == 0:
            expected_points.pop(parent_label)
            result["parent_delete"]["status"] = "passed"
        else:
            result["parent_delete"]["status"] = "native-command-error"
        try:
            result["parent_delete"]["after"] = verify_retained_markers(
                name, targets, expected_points, current_marker)
        except Exception as exc:
            result["parent_delete"]["reconciliation_error"] = str(exc)
            result["preserve"] = True
            preserve = True
            INCOMPLETE = True
            result["status"] = "uncertain-after-parent-delete"
            return result
        if deleted.returncode:
            result["parent_delete"]["refusal_safe"] = True
            result["parent_delete"]["before_snapshot_count"] = before_parent_delete["snapshot_count"]
            result["status"] = "native-command-error-reconciled"
            result["preserve"] = True
            preserve = True
            INCOMPLETE = True
            result["elapsed_seconds"] = time.monotonic() - started_at
            REPORT.with_name(REPORT.stem + "-keep").touch()
            result["cleanup"] = {"status": "preserved-for-review", "domain": name,
                                 "directory": str(work)}
            return result
        elif not delete_apparmor_context_observed(profile):
            result["parent_delete"]["status"] = "incomplete-apparmor-context-unobserved"
            result["status"] = "incomplete-apparmor-context-unobserved"
            result["preserve"] = True
            preserve = True
            INCOMPLETE = True
            result["elapsed_seconds"] = time.monotonic() - started_at
            REPORT.with_name(REPORT.stem + "-keep").touch()
            result["cleanup"] = {"status": "preserved-for-review", "domain": name,
                                 "directory": str(work)}
            return result

        # Remove remaining points from newest to oldest, reconciling every retained
        # marker and the current marker after each native deletion.
        deletion_steps = []
        for index in reversed(range(count)):
            label = f"stress-{index:02d}"
            if label not in expected_points:
                continue
            deleted, profile = delete_snapshot(
                name, label)
            step = {"label": label, "returncode": deleted.returncode,
                    "stdout": deleted.stdout.strip(), "stderr": deleted.stderr.strip(),
                    "apparmor_profile_during_delete": profile or None,
                    "apparmor_context": delete_apparmor_context_status(profile)}
            if deleted.returncode == 0:
                expected_points.pop(label)
            try:
                step["after"] = verify_retained_markers(
                    name, targets, expected_points, current_marker)
            except Exception as exc:
                step["status"] = "uncertain-after-delete"
                step["reconciliation_error"] = str(exc)
                deletion_steps.append(step)
                result["deletions"] = deletion_steps
                result["preserve"] = True
                preserve = True
                INCOMPLETE = True
                result["status"] = "uncertain-after-delete"
                return result
            step["status"] = "passed" if deleted.returncode == 0 else "native-command-error-reconciled"
            deletion_steps.append(step)
            if deleted.returncode:
                # Do not retry an unchanged failure. Reconciled state permits this
                # stress run to stop safely with its remaining metadata retained.
                result["deletions"] = deletion_steps
                result["status"] = "partial"
                result["preserve"] = True
                preserve = True
                INCOMPLETE = True
                return result
            if not delete_apparmor_context_observed(profile):
                step["status"] = "incomplete-apparmor-context-unobserved"
                result["deletions"] = deletion_steps
                result["status"] = "incomplete-apparmor-context-unobserved"
                result["preserve"] = True
                preserve = True
                INCOMPLETE = True
                return result
        result["deletions"] = deletion_steps
        if expected_points:
            raise RuntimeError(f"snapshot deletion left points behind: {sorted(expected_points)}")
        virsh("start", name)
        state = virsh("domstate", name).stdout.strip().lower()
        if state not in ("running", "paused"):
            raise RuntimeError(f"domain did not reopen after {count}-point deletion: {state}")
        result["final_reopen"] = {"status": "passed", "observed_state": state,
                                  "guest_os": False}
        virsh("destroy", name)
        result["capacity_after"] = {"free_bytes": shutil.disk_usage(IMAGE_ROOT).free,
                                    "total_bytes": shutil.disk_usage(IMAGE_ROOT).total}
        result["elapsed_seconds"] = time.monotonic() - started_at
        result["status"] = "passed"
    except IncompleteOperation as exc:
        INCOMPLETE = True
        result["status"] = "incomplete"
        result["error"] = str(exc)
        result["partial_stdout"] = exc.stdout
        result["partial_stderr"] = exc.stderr
        preserve = True
    except Exception as exc:
        result["status"] = "failed"
        result["error"] = str(exc)
        preserve = defined
        if preserve:
            result["preserve"] = True
    if preserve:
        REPORT.with_name(REPORT.stem + "-keep").touch()
        result["cleanup"] = {"status": "preserved-for-review", "domain": name,
                             "directory": str(work)}
        return result
    try:
        state = virsh("domstate", name, check=False, timeout=15)
        if state.returncode == 0 and state.stdout.strip().lower() != "shut off":
            virsh("destroy", name, timeout=15)
        if defined:
            virsh("undefine", name, "--snapshots-metadata", timeout=15)
    except (IncompleteOperation, VirshError) as exc:
        INCOMPLETE = True
        result["cleanup"] = {"status": "incomplete", "error": str(exc),
                             "domain": name, "directory": str(work)}
        result["status"] = "incomplete"
    else:
        shutil.rmtree(work, ignore_errors=True)
    return result


def inner_probe(config):
    global ROOT, IMAGE_ROOT, REPORT, INCOMPLETE, DOMAIN_TYPE
    if config.get("domain_type") is not None:
        DOMAIN_TYPE = configured_domain_type(config["domain_type"])
    ROOT = Path(config["work"])
    IMAGE_ROOT = Path(config["storage_root"])
    REPORT = ROOT / "result.json"
    assert ROOT.parent == IMAGE_ROOT and not ROOT.is_symlink(), "probe work path escaped the private image root"
    assert (ROOT / "sentinel").read_text() == SENTINEL, "private disposable-lab sentinel differs"
    assert not any(path != ROOT / "sentinel" for path in ROOT.iterdir()), "probe work path is not empty"
    run("virsh", "-c", URI, "list")
    versions = {
        "os_release": Path("/etc/os-release").read_text(),
        "packages": run("dpkg-query", "-W", "-f=${binary:Package} ${Version}\\n",
                         "libvirt-daemon-system", "libvirt-clients", "qemu-system-x86",
                         "qemu-utils", check=False).stdout,
        "libvirt": run("virsh", "-c", URI, "version").stdout,
        "snapshot_revert_help": run("virsh", "help", "snapshot-revert").stdout,
        "snapshot_delete_help": run("virsh", "help", "snapshot-delete").stdout,
        "checkpoint_create_help": run("virsh", "help", "checkpoint-create", check=False).stdout,
        "backup_begin_help": run("virsh", "help", "backup-begin", check=False).stdout,
        "qemu_img": run("qemu-img", "--version").stdout,
        "qemu_io": run("qemu-io", "--version").stdout,
        "kvm_device": Path("/dev/kvm").exists(),
        "storage": config["storage_observation"],
    }
    if config.get("retry_preserved_delete"):
        retry = config["retry_preserved_delete"]
        cases = [reconcile_and_retry_delete(retry["domain"], retry["label"])]
    elif config.get("atomic_create_failure_only"):
        cases = [probe_atomic_create_failure()]
    elif config.get("stress_point_chain"):
        cases = [probe_point_chain_stress(config["stress_point_chain"])]
    elif config.get("parent_delete_control"):
        cases = [probe_point_chain_stress(3, 1), probe_point_chain_stress(3, 2)]
    elif config.get("reconcile_chain_domain"):
        cases = [{**reconcile_all_snapshot_markers(config["reconcile_chain_domain"]),
                  "status": "passed"}]
    else:
        cases = [probe_disks(1), probe_disks(2)]
        if not INCOMPLETE:
            cases.extend([probe_disks(1, paced=True), probe_disks(2, paced=True)])
    report = {"versions": versions, "scope": "stopped BIOS external qcow2 snapshots only",
              "cases": cases,
              "limitations": ["running capture, interruption/failure recovery, firmware variants, and backup/checkpoint APIs untested"]}
    if INCOMPLETE:
        REPORT.with_name(REPORT.stem + "-keep").touch()
    if INCOMPLETE or any(case["status"] != "passed" for case in cases):
        report["exit_code"] = 1
    else:
        report["exit_code"] = 0
    REPORT.write_text(json.dumps(report, indent=2) + "\n")
    return report


def validate_lab(path):
    path = Path(path)
    assert not path.is_symlink(), "candidate lab must not be a symlink"
    lab = path.resolve(strict=True)
    cache = (ROOT / ".cache").resolve()
    assert lab.parent == cache and lab.name.startswith("recovery-s0-core."), "candidate must be selected recovery S0 lab"
    assert json.loads((lab / "lab.json").read_text()) == {"disposable": True, "fixture": "cluster-ha-v1"}, "candidate lab is not marked disposable"
    for name in ("id_ed25519", "known_hosts", "ports.json"):
        item = lab / name
        assert item.is_file() and not item.is_symlink(), f"candidate lab is missing {name}"
    assert (lab / "id_ed25519").stat().st_mode & 0o077 == 0, "candidate SSH key permissions are too broad"
    return lab


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lab-dir", type=Path, required=True)
    parser.add_argument("--node", choices=("core1", "core2", "core3"), required=True)
    parser.add_argument("--domain-type", choices=("kvm", "qemu"),
                        help="override the domain type for a selected no-KVM TCG lab")
    parser.add_argument("--require-apparmor", action="store_true",
                        help="require the QEMU process to run in its enforcing per-domain AppArmor profile")
    parser.add_argument("--evidence", type=Path, required=True, help="new ignored .cache directory")
    parser.add_argument("--storage-root", type=Path,
                        help="selected disposable storage mountpoint; defaults to the Core local image directory")
    parser.add_argument("--expected-fstype", help="require this exact filesystem type at --storage-root")
    parser.add_argument("--expected-source", help="require this exact findmnt SOURCE at --storage-root")
    parser.add_argument("--iscsi-device-size", help="for an exclusive iSCSI mount, require this lsblk SIZE (for example 2G)")
    parser.add_argument("--iscsi-target", help="require this exact target name in the active iSCSI session output")
    parser.add_argument("--atomic-create-failure", action="store_true",
                        help="run only the bounded two-disk partial-create failure case")
    parser.add_argument("--stress-point-chain", type=int, choices=(16, 32),
                        help="run only a bounded local two-disk chain stress at 16 or 32 points")
    parser.add_argument("--parent-delete-control", action="store_true",
                        help="run fresh three-point oldest-parent deletion controls, one disk then two")
    parser.add_argument("--reconcile-chain-domain",
                        help="read-only exhaustive reconciliation of a preserved nex-recovery-s0-chain3x domain")
    parser.add_argument("--retry-preserved-delete", metavar="DOMAIN=SNAPSHOT",
                        help="reconcile and retry one exact previously failed disposable S0 deletion")
    parser.add_argument("--expect-uuid-revert-overlays", action="store_true",
                        help="require unique UUID-suffixed heads for every immediate native revert")
    args = parser.parse_args()
    lab = validate_lab(args.lab_dir)
    storage_root = args.storage_root or IMAGE_ROOT
    if bool(args.expected_fstype) != bool(args.expected_source):
        parser.error("--expected-fstype and --expected-source must be provided together")
    if bool(args.iscsi_device_size) != bool(args.iscsi_target):
        parser.error("--iscsi-device-size and --iscsi-target must be provided together")
    if args.iscsi_device_size and args.expected_fstype != "ext4":
        parser.error("iSCSI validation requires --expected-fstype ext4")
    if args.expected_fstype and not args.storage_root:
        parser.error("explicit backend validation requires --storage-root")
    if args.storage_root and not args.expected_fstype:
        parser.error("--storage-root requires exact --expected-fstype and --expected-source")
    special_cases = sum((bool(args.atomic_create_failure), bool(args.retry_preserved_delete),
                         bool(args.stress_point_chain), bool(args.parent_delete_control),
                         bool(args.reconcile_chain_domain)))
    if special_cases > 1:
        parser.error("atomic-create, preserved-delete, and chain-stress modes are exclusive")
    if args.require_apparmor and not args.parent_delete_control:
        parser.error("--require-apparmor applies only to the parent merge in --parent-delete-control")
    if args.expect_uuid_revert_overlays and (args.atomic_create_failure or args.retry_preserved_delete or args.stress_point_chain or args.parent_delete_control or args.reconcile_chain_domain):
        parser.error("UUID revert assertions apply only to the full snapshot lifecycle probe")
    if args.stress_point_chain and args.storage_root:
        parser.error("chain stress is limited to the selected Core local image filesystem")
    assert not args.evidence.exists(), "evidence directory must be new"
    evidence = args.evidence.resolve()
    assert evidence.is_relative_to((ROOT / ".cache").resolve()), "raw evidence must remain in ignored .cache"
    ports = json.loads((lab / "ports.json").read_text())
    ssh = ["ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
           "-o", "StrictHostKeyChecking=yes", "-o", f"UserKnownHostsFile={lab / 'known_hosts'}",
           "-i", str(lab / "id_ed25519"), "-p", str(ports[args.node]), "root@127.0.0.1"]
    run_id = uuid.uuid4().hex
    work = str(storage_root / f"nex-recovery-s0-{run_id}")
    storage_observation = {"requested_root": str(storage_root), "validation": "local-default"}
    if args.expected_fstype:
        inspect = ssh + ["findmnt", "-n", "-M", shlex.quote(str(storage_root)),
                         "-o", "TARGET,FSTYPE,SOURCE"]
        observed = run(inspect, check=False, timeout=30)
        if observed.returncode:
            raise RuntimeError(f"selected storage root is not an exact mountpoint: {observed.stderr.strip()}")
        fields = require_exact_mount(observed.stdout, storage_root,
                                     args.expected_fstype, args.expected_source)
        storage_observation = {"requested_root": str(storage_root), "target": fields[0],
                               "fstype": fields[1], "source": fields[2], "validation": "exact-findmnt"}
        if args.iscsi_device_size:
            device = fields[2]
            if not device.startswith("/dev/"):
                raise RuntimeError("exclusive iSCSI SOURCE must be a block device")
            device_arg = shlex.quote(device)
            size = run(ssh + ["lsblk", "-dn", "-o", "SIZE", device_arg], check=False, timeout=30)
            device_type = run(ssh + ["lsblk", "-dn", "-o", "TYPE", device_arg], check=False, timeout=30)
            if (size.returncode or size.stdout.strip() != args.iscsi_device_size or
                    device_type.returncode or device_type.stdout.strip() != "disk"):
                raise RuntimeError(f"exclusive iSCSI device size differs: {size.stdout.strip()!r}")
            session = run(ssh + ["iscsiadm", "-m", "session", "-P", "3"], check=False, timeout=30)
            if session.returncode:
                raise RuntimeError("cannot inspect active iSCSI target-to-device mapping")
            require_iscsi_attachment(session.stdout, args.iscsi_target, device)
            storage_observation["device_size"] = size.stdout.strip()
            storage_observation["iscsi_target_session"] = args.iscsi_target
    storage_root_q = shlex.quote(str(storage_root))
    work_q = shlex.quote(work)
    local_setup = ""
    if not args.storage_root:
        local_setup = f"install -d -m 0755 {storage_root_q}; "
    setup = (f"set -eu; {local_setup} test -d {storage_root_q}; test ! -e {work_q}; "
             f"install -d -m 0711 {work_q}; "
             f"printf %s {shlex.quote(SENTINEL)} > {shlex.quote(work + '/sentinel')}")
    run(ssh + [setup])
    retry = None
    if args.retry_preserved_delete:
        if "=" not in args.retry_preserved_delete:
            parser.error("--retry-preserved-delete must be DOMAIN=SNAPSHOT")
        domain, label = args.retry_preserved_delete.split("=", 1)
        if label not in ("point-c-after-revert", "point-c"):
            parser.error("retry label must match an observed failed S0 deletion")
        retry = {"domain": domain, "label": label}
    reconcile_chain = args.reconcile_chain_domain
    if reconcile_chain and (not reconcile_chain.startswith("nex-recovery-s0-chain3x") or
                            not all(character.isalnum() or character == "-" for character in reconcile_chain)):
        parser.error("read-only chain reconciliation accepts only a generated three-point S0 domain name")
    config = {"work": work, "storage_root": str(storage_root),
              "storage_observation": storage_observation,
              "require_qemu_access": bool(args.storage_root),
              "atomic_create_failure_only": args.atomic_create_failure,
              "stress_point_chain": args.stress_point_chain,
              "parent_delete_control": args.parent_delete_control,
              "require_apparmor": args.require_apparmor,
              "reconcile_chain_domain": reconcile_chain,
              "expect_uuid_revert_overlays": args.expect_uuid_revert_overlays,
              "retry_preserved_delete": retry}
    if args.domain_type:
        config["domain_type"] = args.domain_type
    source = (Path(__file__).read_text()
              .replace("ROOT = Path(__file__).resolve().parents[2]", "ROOT = Path('/')")
              .replace("REMOTE_CONFIG = None", f"REMOTE_CONFIG = {config!r}"))
    evidence.mkdir(parents=True, mode=0o700)
    (evidence / "candidate.json").write_text(json.dumps({"node": args.node, "work": work}, indent=2) + "\n")
    try:
        reply = run(ssh + ["python3", "-"], input=source, check=False, timeout=1800)
    except IncompleteOperation as exc:
        (evidence / "remote.stdout.partial").write_text(exc.stdout)
        (evidence / "remote.stderr.partial").write_text(exc.stderr)
        (evidence / "timeout.txt").write_text(str(exc) + "\nRemote scratch was left in place.\n")
        raise RuntimeError(f"native probe transport timed out; candidate scratch retained: {evidence}") from exc
    (evidence / "remote.stdout").write_text(reply.stdout)
    (evidence / "remote.stderr").write_text(reply.stderr)
    if not reply.stdout.strip():
        raise RuntimeError(f"native probe produced no result (exit {reply.returncode}); evidence: {evidence}")
    report = json.loads(reply.stdout)
    (evidence / "result.json").write_text(json.dumps(report, indent=2) + "\n")
    if reply.returncode == 0 and report["exit_code"] == 0:
        run(ssh + ["set -eu; test \"$(cat " + shlex.quote(work + "/sentinel") + ")\" = " +
                   shlex.quote(SENTINEL) + "; rm -rf -- " + shlex.quote(work)])
    print(evidence)
    if reply.returncode or report["exit_code"]:
        raise RuntimeError(f"native probe incomplete or failed; candidate scratch retained: {evidence}")


if REMOTE_CONFIG is None:
    if __name__ == "__main__":
        main()
else:
    print(json.dumps(inner_probe(REMOTE_CONFIG)))
