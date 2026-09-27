#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Probe native full backup and independent restore in the selected Core S0 lab."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
from pathlib import PurePosixPath
import re
import shlex
import shutil
import subprocess
import sys
import threading
import time
import uuid

_SCRIPT_PATH = Path(__file__).resolve()
ROOT = _SCRIPT_PATH.parents[2] if len(_SCRIPT_PATH.parents) > 2 else Path.cwd()
BASE = ROOT / ".cache/recovery-s0-core/artifacts/debian-13-genericcloud-amd64-20260831-2587.qcow2"
BASE_SHA256 = "85a969b7e99d7c817414136033df18c58d5c45ac8d27bb36e8ccb67173d2d4e3"
REMOTE_CONFIG = None
SENTINEL = "NEX recovery S0 backup disposable lab"
SOURCE_PREFIX = "nex-recovery-s0-backup-"
SIZE = 16 * 1024**3
MARKERS = {"before-backup": "nex-recovery-s0-before-backup", "after-backup-begin": "nex-recovery-s0-after-backup-begin"}
NEX3_PACKAGE_VERSION = "11.3.0-3+deb13u3+nex3"
NEX3_LIBVIRT_PACKAGES = (
    "libvirt-clients", "libvirt-common", "libvirt-daemon", "libvirt-daemon-common",
    "libvirt-daemon-config-network", "libvirt-daemon-config-nwfilter",
    "libvirt-daemon-driver-network", "libvirt-daemon-driver-nodedev",
    "libvirt-daemon-driver-nwfilter", "libvirt-daemon-driver-qemu",
    "libvirt-daemon-driver-secret", "libvirt-daemon-driver-storage",
    "libvirt-daemon-log", "libvirt-daemon-system", "libvirt0",
)
NEX3_ARCHITECTURE_INDEPENDENT = frozenset(("libvirt-daemon-config-network", "libvirt-daemon-config-nwfilter"))


def run(args, *, check=True, input=None, timeout=180):
    result = subprocess.run(args, input=input, text=True, capture_output=True, timeout=timeout)
    if check and result.returncode:
        raise RuntimeError(f"{args[0]} failed: {result.stderr.strip() or result.stdout.strip()}")
    return result


def job_type(output):
    match = re.search(r"^Job type:\s*(\S+)\s*$", output, re.MULTILINE)
    if not match:
        raise RuntimeError(f"unrecognized domjobinfo result: {output[:600]}")
    return {"0": "none", "1": "bounded", "2": "unbounded", "3": "completed",
            "4": "failed", "5": "cancelled"}.get(match.group(1).lower(), match.group(1).lower())


def numeric_stats(output):
    return {key: int(value) for key, value in re.findall(r"^(\w+):\s*(\d+)\s*$", output, re.MULTILINE)}


def completed_backup_result(current, completed):
    if job_type(current) != "none":
        raise RuntimeError(f"native backup still appears active: {current[:600]}")
    if job_type(completed) != "completed":
        raise RuntimeError(f"native backup has no completed-job statistics: {completed[:600]}")
    stats = numeric_stats(completed)
    if stats.get("success") != 1 or stats.get("disk_processed", 0) <= 0 or stats.get("disk_remaining") != 0:
        raise RuntimeError(f"native backup completed without successful full-disk statistics: {completed[:600]}")
    return {"current": current.strip(), "completed": completed.strip()}


def require_candidate_libvirt_version(packages, expected_version):
    observed = dict(line.split("=", 1) for line in packages.splitlines() if "=" in line)
    actual = observed.get("libvirt-daemon-system")
    if actual != expected_version:
        raise RuntimeError(f"libvirt candidate version differs: expected {expected_version}, got {actual}")
    return actual


def restore_backup_paths(output, targets, checkpoint_backup):
    suffix = "checkpoint-present-" if checkpoint_backup else ""
    return {target: output / f"{suffix}{target}-private.qcow2" for target in targets}


def restore_marker(checkpoint_backup):
    return MARKERS["after-backup-begin"] if checkpoint_backup else MARKERS["before-backup"]


def require_checkpoint_listed(output, checkpoint_name):
    if not any(fields and fields[0] == checkpoint_name
               for fields in (line.split() for line in output.splitlines())):
        raise RuntimeError(f"expected checkpoint is absent: {checkpoint_name}")
    return output.strip()


def owner_only_metadata(path, expected_uid):
    metadata = Path(path).stat()
    observed = {"uid": metadata.st_uid, "gid": metadata.st_gid,
                "mode": oct(metadata.st_mode & 0o777), "size": metadata.st_size}
    if metadata.st_uid != expected_uid or metadata.st_mode & 0o777 != 0o600:
        raise RuntimeError(f"backup output is not QEMU-owned mode 0600: {path}")
    return observed


def require_file_digest(path, expected_digest, algorithm="sha256"):
    digest = hashlib.new(algorithm, Path(path).read_bytes()).hexdigest()
    if digest != expected_digest:
        raise RuntimeError(f"installed runtime file differs from selected package: {path}")
    return digest


def require_installed_versions(installed, package_names, expected_version):
    expected = {name: expected_version for name in package_names}
    if installed != expected:
        raise RuntimeError("installed libvirt package versions differ from the pinned candidate closure")
    return installed


def require_unclaimed_domains(existing_domains, generated_domains):
    collisions = sorted(set(existing_domains) & set(generated_domains))
    if collisions:
        raise RuntimeError(f"generated probe domain names are already in use: {collisions}")


def require_nfs_mount(payload, target, source):
    try:
        filesystems = json.loads(payload)["filesystems"]
    except (json.JSONDecodeError, KeyError, TypeError) as exc:
        raise RuntimeError("cannot parse exact NFS mount observation") from exc
    if len(filesystems) != 1:
        raise RuntimeError("expected one exact NFS mount observation")
    mount = filesystems[0]
    options = set(mount.get("options", "").split(","))
    if (mount.get("target") != target or (source is not None and mount.get("source") != source) or
            mount.get("fstype") != "nfs4" or "vers=4.2" not in options or
            "rw" not in options or "ro" in options):
        raise RuntimeError("selected recovery export mount differs from the exact writable NFSv4.2 target")
    return mount


def require_nfs_destination(destination, target):
    destination = PurePosixPath(destination)
    target = PurePosixPath(target)
    if (not target.is_absolute() or target.parent != PurePosixPath("/mnt") or
            not target.name.startswith("recovery-s0-nfs") or destination.parent != target or
            destination.name in ("", ".", "..")):
        raise ValueError("destination must be one unique child of a selected recovery S0 NFS mount")
    return destination, target


def ensure_nfs_mount(cfg):
    target = Path(cfg["destination_mount"])
    source = cfg["destination_source"]
    if target.parent != Path("/mnt") or not target.name.startswith("recovery-s0-nfs"):
        raise RuntimeError("NFS mount path is outside the dedicated recovery mount namespace")
    if target.is_symlink():
        raise RuntimeError("NFS mount path is a symlink")
    query = ["findmnt", "-J", "-M", str(target), "-o", "TARGET,SOURCE,FSTYPE,OPTIONS"]
    before = run(query, check=False)
    created = False
    if before.returncode:
        if target.exists():
            if not target.is_dir() or any(target.iterdir()):
                raise RuntimeError("unmounted NFS target exists and is not an empty directory")
        else:
            target.mkdir(mode=0o755)
        run(["mount", "-t", "nfs4", "-o", "rw,vers=4.2,hard,proto=tcp", source, str(target)])
        created = True
    observed = run(query).stdout
    mount = require_nfs_mount(observed, str(target), source)
    return {"mount": mount, "created_by_probe": created}


def parse_sha256sums(data):
    entries = {}
    for line in data.splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  (.+)", line)
        if not match or match.group(2) in entries:
            raise ValueError("invalid or duplicate SHA256SUMS entry")
        entries[match.group(2)] = match.group(1)
    return entries


def local_candidate_package_pin(package_dir, source_manifest, expected_manifest_sha256,
                                expected_version, staged_package_dir):
    package_dir = Path(package_dir)
    source_manifest = Path(source_manifest)
    selected_sums = package_dir / "SHA256SUMS.selected"
    for path in (package_dir, source_manifest, selected_sums):
        if path.is_symlink() or not path.exists():
            raise RuntimeError(f"candidate input is missing or a symlink: {path}")
    manifest_data = source_manifest.read_bytes()
    if hashlib.sha256(manifest_data).hexdigest() != expected_manifest_sha256:
        raise RuntimeError("full candidate manifest digest differs from the selected +nex3 manifest")
    full_entries = parse_sha256sums(manifest_data.decode())
    selected_data = selected_sums.read_bytes()
    selected_entries = parse_sha256sums(selected_data.decode())
    if set(selected_entries.values()) == set():
        raise RuntimeError("selected candidate package manifest is empty")
    by_basename = {}
    for filename, digest in full_entries.items():
        basename = Path(filename).name
        if basename in by_basename:
            raise RuntimeError(f"candidate manifest has duplicate package filename: {basename}")
        by_basename[basename] = digest
    if set(selected_entries) != {path.name for path in package_dir.glob("*.deb")}:
        raise RuntimeError("selected package list does not cover exactly the staged local debs")
    packages = []
    seen_names = set()
    for filename, digest in selected_entries.items():
        path = package_dir / filename
        if path.is_symlink() or not path.is_file():
            raise RuntimeError(f"selected candidate package is missing or a symlink: {filename}")
        if hashlib.sha256(path.read_bytes()).hexdigest() != digest or by_basename.get(filename) != digest:
            raise RuntimeError(f"selected package differs from the full candidate manifest: {filename}")
        metadata = run(["dpkg-deb", "-f", str(path), "Package", "Version", "Architecture"])
        values = [line.split(": ", 1)[1] for line in metadata.stdout.splitlines() if ": " in line]
        if len(values) != 3 or values[1] != expected_version or values[2] not in ("amd64", "all"):
            raise RuntimeError(f"selected package metadata differs from the candidate: {filename}")
        package_name = values[0]
        if package_name in seen_names:
            raise RuntimeError(f"duplicate selected package: {package_name}")
        seen_names.add(package_name)
        packages.append({"package": package_name, "version": values[1], "architecture": values[2],
                         "file": filename, "sha256": digest})
    if seen_names != set(NEX3_LIBVIRT_PACKAGES):
        raise RuntimeError("selected libvirt package set differs from the +nex3 runtime closure")
    return {"version": expected_version, "manifest_sha256": expected_manifest_sha256,
            "selected_manifest_sha256": hashlib.sha256(selected_data).hexdigest(),
            "staged_package_dir": str(staged_package_dir), "packages": packages}


def verify_staged_runtime_packages(cfg):
    import io
    import tarfile

    package_dir = Path(cfg["candidate"]["staged_package_dir"])
    if package_dir.is_symlink() or not package_dir.is_dir():
        raise RuntimeError("selected candidate package staging directory is missing or a symlink")
    selected_path = package_dir / "SHA256SUMS.selected"
    if selected_path.is_symlink() or not selected_path.is_file():
        raise RuntimeError("selected candidate package checksum list is missing or a symlink")
    selected_data = selected_path.read_bytes()
    if hashlib.sha256(selected_data).hexdigest() != cfg["candidate"]["selected_manifest_sha256"]:
        raise RuntimeError("Core's staged selected-package manifest differs from the pinned candidate")
    selected_entries = parse_sha256sums(selected_data.decode())
    expected = {row["file"]: row for row in cfg["candidate"]["packages"]}
    if set(selected_entries) != set(expected):
        raise RuntimeError("Core's staged package list differs from the pinned candidate")
    if {path.name for path in package_dir.glob("*.deb")} != set(expected):
        raise RuntimeError("Core's staged directory contains an unpinned candidate package")

    package_names = tuple(sorted(row["package"] for row in expected.values()))
    installed_result = run(["dpkg-query", "-W", "-f=${binary:Package} ${Version}\\n", *package_names], check=False)
    installed = {}
    for line in installed_result.stdout.splitlines():
        fields = line.split()
        if len(fields) == 2:
            installed[fields[0].split(":", 1)[0]] = fields[1]
    if installed_result.returncode:
        raise RuntimeError("cannot read installed libvirt package versions")
    require_installed_versions(installed, package_names, cfg["candidate"]["version"])

    runtime_prefixes = ("usr/bin/", "usr/sbin/", "usr/lib/", "usr/libexec/", "lib/")
    checked_files = 0
    verified_packages = []
    for filename, row in expected.items():
        package_file = package_dir / filename
        if (package_file.is_symlink() or not package_file.is_file() or
                hashlib.sha256(package_file.read_bytes()).hexdigest() != row["sha256"] or
                selected_entries[filename] != row["sha256"]):
            raise RuntimeError(f"staged candidate package hash differs: {filename}")
        metadata = run(["dpkg-deb", "-f", str(package_file), "Package", "Version", "Architecture"])
        values = [line.split(": ", 1)[1] for line in metadata.stdout.splitlines() if ": " in line]
        if values != [row["package"], row["version"], row["architecture"]]:
            raise RuntimeError(f"staged candidate package metadata differs: {filename}")
        control = subprocess.run(["dpkg-deb", "--ctrl-tarfile", str(package_file)],
                                 capture_output=True, check=True)
        with tarfile.open(fileobj=io.BytesIO(control.stdout), mode="r:*") as archive:
            member = next((item for item in archive.getmembers()
                           if item.name.removeprefix("./") == "md5sums"), None)
            if member is None:
                raise RuntimeError(f"candidate package has no control md5sums: {filename}")
            manifest = archive.extractfile(member)
            if manifest is None:
                raise RuntimeError(f"candidate package md5sums could not be read: {filename}")
            for line in manifest.read().decode().splitlines():
                digest, relative = line.split(maxsplit=1)
                relative = relative.removeprefix("./")
                relative_path = PurePosixPath(relative)
                if relative_path.is_absolute() or ".." in relative_path.parts:
                    raise RuntimeError(f"candidate package contains an unsafe runtime path: {relative}")
                if not relative.startswith(runtime_prefixes):
                    continue
                installed_path = Path("/") / relative
                if installed_path.is_symlink() or not installed_path.is_file():
                    raise RuntimeError(f"installed candidate runtime file is missing or a symlink: /{relative}")
                require_file_digest(installed_path, digest, "md5")
                checked_files += 1
        verified_packages.append(filename)
    if checked_files == 0:
        raise RuntimeError("selected candidate packages exposed no installed runtime files to verify")
    return {"manifest_sha256": cfg["candidate"]["manifest_sha256"],
            "selected_manifest_sha256": cfg["candidate"]["selected_manifest_sha256"],
            "installed_versions": installed, "verified_package_files": verified_packages,
            "verified_package_hashes": {row["file"]: row["sha256"] for row in expected.values()},
            "installed_runtime_files_matched": checked_files}


def assigned_capacity_root(value):
    path = PurePosixPath(value)
    if (len(path.parts) != 4 or path.parts[:2] != ("/", "mnt")
            or path.parts[2] not in ("recovery-s0-capacity-final", "recovery-s0-capacity-sixpatch",
                                     "recovery-s0-capacity-hashverify", "recovery-s0-capacity-proof",
                                     "recovery-s0-capacity-corrected")
            or path.parts[3] in ("", ".", "..")):
        raise ValueError("ENOSPC root must be one direct child of an assigned capacity mount")
    return path


def enospc_recovery_action(state, block_error):
    state = state.strip().lower()
    block_error = block_error.strip().lower()
    if state == "paused (i/o error)" and "no space" in block_error:
        return "resume"
    if state == "running (booted)" and block_error == "no errors found":
        return "continue-running"
    raise RuntimeError(f"ENOSPC left an unrecognized source state: {state}; {block_error}")


def domain_is_running(state):
    return state.strip().lower() == "running"


def run_backup_failure_probes(cfg):
    return not cfg.get("restore_from_checkpoint_backup")


def finalize_successful_probe(domains, state, virsh, network_started, work, held, converted,
                              output, preserve_outputs):
    for domain in domains:
        if state(domain) != "absent":
            raise RuntimeError(f"successful probe left domain registered: {domain}")
    if network_started:
        virsh("net-destroy", "default", check=False)
    for path in (work, held, converted):
        shutil.rmtree(path, ignore_errors=True)
    if not preserve_outputs:
        shutil.rmtree(output, ignore_errors=True)


def inner_probe(cfg):
    """This function is copied to Core C and runs only inside the disposable lab."""
    import xml.etree.ElementTree as ET

    work = Path(cfg["work"])
    destination = Path(cfg["destination"])
    output = Path(cfg["output"])
    name = cfg["domain"]
    restored_name = cfg["restored_domain"]
    converted_name = restored_name + "-convert"
    targets = ("vda", "vdb")
    expected = MARKERS["before-backup"]
    result = {"status": "incomplete", "candidate_alias": cfg.get("candidate_alias", "Core C"), "steps": []}

    assert work.parent == Path("/var/lib/libvirt/images/nex-recovery-s0-backup"), "source path escaped the private probe root"
    assert (work / "sentinel").read_text() == SENTINEL, "private disposable-lab sentinel differs"
    assert not work.is_symlink() and not destination.is_symlink(), "probe paths must not be symlinks"
    assert destination.resolve(strict=True) == destination, "probe destination must not contain symlink or traversal components"
    if cfg.get("enospc_only"):
        assert destination.parent == work and output.parent == destination, "ENOSPC-only scratch output must stay inside the private source directory"
    else:
        destination_mount = Path(cfg.get("destination_mount", "/mnt/recovery-s0-nfs"))
        assert destination.parent.resolve(strict=True) == destination_mount.resolve(strict=True), "NFS destination must be a direct child of the assigned mount"
        assert output.parent == destination, "backup output must be a direct child of the assigned destination"
        mount_observation = run(["findmnt", "-J", "-M", str(destination_mount),
                                 "-o", "TARGET,SOURCE,FSTYPE,OPTIONS"]).stdout
        result["destination_mount"] = require_nfs_mount(
            mount_observation, str(destination_mount), cfg["destination_source"])
    assert output.is_dir() and not output.is_symlink() and not any(output.iterdir()), "backup output must be a fresh direct child of the assigned destination"
    if cfg.get("destination_source"):
        sentinel_path = destination / ".nex-recovery-s0-export-sentinel"
        with sentinel_path.open("x") as sentinel_file:
            sentinel_file.write(SENTINEL + "\n")
            sentinel_file.flush()
            os.fsync(sentinel_file.fileno())
        if sentinel_path.read_text() != SENTINEL + "\n":
            raise RuntimeError("selected recovery export sentinel did not survive a write/read round trip")
        result["destination_export_sentinel"] = {"path": str(sentinel_path), "status": "passed",
                                                 "sha256": hashlib.sha256(sentinel_path.read_bytes()).hexdigest()}
    candidate_receipt = verify_staged_runtime_packages(cfg) if cfg.get("candidate") else None

    def virsh(*args, check=True, timeout=180):
        return run(["virsh", "-c", "qemu:///system", *map(str, args)], check=check, timeout=timeout)

    def state(domain):
        observed = virsh("domstate", domain, check=False)
        return observed.stdout.strip().lower() if observed.returncode == 0 else "absent"

    def wait_stopped(domain):
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            if state(domain) == "shut off":
                return
            time.sleep(1)
        raise TimeoutError(f"{domain} did not stop")

    def guest(domain, command, timeout=300):
        request = {"execute": "guest-exec", "arguments": {"path": "/bin/sh", "arg": ["-ec", command], "capture-output": True}}
        reply = json.loads(virsh("qemu-agent-command", domain, json.dumps(request)).stdout)["return"]
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            status = json.loads(virsh("qemu-agent-command", domain, json.dumps({
                "execute": "guest-exec-status", "arguments": {"pid": reply["pid"]}})).stdout)["return"]
            if status.get("exited"):
                stdout = base64.b64decode(status.get("out-data", "")).decode(errors="replace")
                stderr = base64.b64decode(status.get("err-data", "")).decode(errors="replace")
                if status.get("exitcode") != 0:
                    raise RuntimeError(f"guest command failed: {stderr.strip()}")
                return stdout
            time.sleep(0.25)
        raise TimeoutError("guest-agent command did not complete")

    def wait_agent(domain):
        deadline = time.monotonic() + 300
        while time.monotonic() < deadline:
            try:
                guest(domain, "systemctl is-active --quiet qemu-guest-agent")
                return
            except Exception:
                time.sleep(2)
        raise TimeoutError("Debian guest agent did not become ready")

    def disk_paths(domain):
        root = ET.fromstring(virsh("dumpxml", domain, "--inactive").stdout)
        found = {}
        for disk in root.findall("./devices/disk[@device='disk']"):
            source = disk.find("source")
            found[disk.find("target").get("dev")] = source.get("file")
        if set(found) != set(targets) or any(not path for path in found.values()):
            raise RuntimeError(f"unexpected disk set: {sorted(found)}")
        return found

    def chain(path):
        return json.loads(run(["qemu-img", "info", "--backing-chain", "--output=json", path]).stdout)

    def qemu_user(*args, check=True):
        return run(["runuser", "-u", "libvirt-qemu", "--", *map(str, args)], check=check)

    def create_reusable_target(domain, target, path):
        info = virsh("domblkinfo", domain, target).stdout
        capacity = int(next(line.split(":", 1)[1].strip().split()[0]
                            for line in info.splitlines() if line.startswith("Capacity:")))
        qemu_user("sh", "-c", "umask 077; exec qemu-img create -f qcow2 \"$1\" \"$2\"",
                  "sh", path, str(capacity))
        metadata = path.stat()
        if metadata.st_uid != 64055 or metadata.st_mode & 0o777 != 0o600:
            raise RuntimeError(f"reusable target does not have owner-only QEMU permissions: {path}")
        return {"path": str(path), "capacity": capacity, "mode": oct(metadata.st_mode & 0o777),
                "uid": metadata.st_uid, "gid": metadata.st_gid}

    def create_vm(domain, root_disk, data_disk, seed=None, network=False):
        cdrom = (f"<disk type='file' device='cdrom'><driver name='qemu' type='raw'/><source file='{seed}'/>"
                 "<target dev='sda' bus='sata'/><readonly/></disk>") if seed else ""
        interface = "<interface type='network'><source network='default'/><model type='virtio'/></interface>" if network else ""
        xml = (f"<domain type='kvm'><name>{domain}</name><memory unit='MiB'>1024</memory>"
               "<currentMemory unit='MiB'>1024</currentMemory><vcpu>2</vcpu>"
               "<os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os>"
               "<features><acpi/><apic/></features><cpu mode='host-model'/><clock offset='utc'/><devices>"
               "<emulator>/usr/bin/qemu-system-x86_64</emulator>"
               f"<disk type='file' device='disk'><driver name='qemu' type='qcow2' cache='none'/><source file='{root_disk}'/><target dev='vda' bus='virtio'/></disk>"
               f"<disk type='file' device='disk'><driver name='qemu' type='qcow2' cache='none'/><source file='{data_disk}'/><target dev='vdb' bus='virtio'/></disk>"
               f"{cdrom}{interface}<channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0'/></channel>"
               "<serial type='pty'><target type='isa-serial' port='0'/></serial>"
               "<graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1'/>"
               "<video><model type='vga' heads='1' primary='yes'/></video></devices></domain>")
        xml_path = root_disk.parent / f"{domain}.xml"
        xml_path.write_text(xml)
        virsh("define", xml_path)

    def wait_backup(domain, timeout=600):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            job = virsh("domjobinfo", domain, "--rawstats", check=False)
            if job.returncode:
                raise RuntimeError(f"cannot determine native backup job state: {job.stderr.strip()}")
            if job_type(job.stdout) == "none":
                completed = virsh("domjobinfo", domain, "--completed", "--keep-completed", "--anystats", "--rawstats", check=False)
                if completed.returncode:
                    raise RuntimeError(f"cannot determine completed native backup result: {completed.stderr.strip()}")
                return completed_backup_result(job.stdout, completed.stdout)
            time.sleep(0.5)
        raise TimeoutError(f"native full backup did not finish within {timeout} seconds")

    def wait_terminal_job(domain, timeout=120):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            current = virsh("domjobinfo", domain, "--rawstats", check=False)
            if current.returncode:
                raise RuntimeError(f"cannot determine native backup job state: {current.stderr.strip()}")
            current_type = job_type(current.stdout)
            if current_type == "none":
                completed = virsh("domjobinfo", domain, "--completed", "--keep-completed",
                                  "--anystats", "--rawstats", check=False)
                if completed.returncode:
                    raise RuntimeError(f"cannot determine terminal native backup result: {completed.stderr.strip()}")
                kind = job_type(completed.stdout)
                if kind not in ("completed", "failed", "cancelled"):
                    raise RuntimeError(f"native job disappeared without terminal statistics: {completed.stdout.strip()}")
                return {"current": current.stdout.strip(), "terminal_type": kind,
                        "completed": completed.stdout.strip(), "stats": numeric_stats(completed.stdout)}
            time.sleep(0.25)
        raise TimeoutError(f"native job did not reach a known terminal state within {timeout} seconds")

    def clear_completed_job(domain):
        virsh("domjobinfo", domain, "--completed", "--anystats", "--rawstats", check=False)

    def backup_targets(label, capacity=None, root=None):
        root = output if root is None else Path(root)
        paths = {}
        disks = []
        for target in targets:
            path = root / f"{label}-{target}.qcow2"
            if capacity is None:
                create_reusable_target(name, target, path)
            else:
                qemu_user("sh", "-c", "umask 077; exec qemu-img create -f qcow2 \"$1\" \"$2\"",
                          "sh", path, str(capacity))
            paths[target] = path
            disks.append(f"<disk name='{target}' type='file'><target file='{path}'/>"
                         "<driver type='qcow2'/></disk>")
        xml = root / f"{label}-backup.xml"
        xml.write_text(f"<domainbackup><disks>{''.join(disks)}</disks></domainbackup>")
        return paths, xml

    def free_bytes(path):
        fields = run(["df", "-B1", "--output=avail", str(path)]).stdout.splitlines()
        return int(fields[-1].strip())

    def image_sizes(paths):
        details = {}
        for target, path in paths.items():
            info = json.loads(qemu_user("qemu-img", "info", "--output=json", path).stdout)
            details[target] = {"virtual_size": info["virtual-size"], "actual_size": info.get("actual-size")}
        return details

    def image_hashes(paths):
        return {target: qemu_user("sha256sum", path).stdout.split()[0]
                for target, path in paths.items()}

    def job_active(domain):
        job = virsh("domjobinfo", domain, "--rawstats", check=False)
        return job.returncode == 0 and job_type(job.stdout) != "none"

    network_started = False
    preserve_outputs = False

    def output_paths(domainbackup_xml):
        root = ET.fromstring(domainbackup_xml)
        return {disk.get("name"): disk.find("target").get("file")
                for disk in root.findall("./disks/disk") if disk.find("target") is not None}

    held = work.with_name(work.name + ".held")
    restored = work.with_name(work.name + "-restore")
    converted = restored.with_name(restored.name + "-converted")
    completed = False
    try:
        package_names = ("nex-core-local", "libvirt-clients", "libvirt-daemon-system",
                         "qemu-system-x86", "qemu-utils", "genisoimage")
        versions = run(["dpkg-query", "-W", "-f=${binary:Package}=${Version}\n", *package_names]).stdout
        result["candidate"] = {
            "os_release": Path("/etc/os-release").read_text(), "packages": versions,
            "virsh_version": run(["virsh", "--version"]).stdout.strip(),
            "qemu_version": run(["qemu-system-x86_64", "--version"]).stdout.splitlines()[0],
            "backup_begin_help": virsh("help", "backup-begin").stdout,
            "checkpoint_create_help": virsh("help", "checkpoint-create").stdout,
        }
        expected_libvirt_version = cfg.get("expected_libvirt_version")
        if expected_libvirt_version:
            result["candidate"]["verified_libvirt_version"] = require_candidate_libvirt_version(
                versions, expected_libvirt_version)
            result["candidate"]["expected_manifest_sha256"] = cfg["candidate"]["manifest_sha256"]
            result["candidate"]["selected_manifest_sha256"] = cfg["candidate"]["selected_manifest_sha256"]
            result["candidate"]["installed_runtime_file_verification"] = candidate_receipt
        existing_domains = [line.strip() for line in virsh("list", "--all", "--name").stdout.splitlines() if line.strip()]
        require_unclaimed_domains(existing_domains, (name, restored_name, converted_name))
        foreign_domains = [domain for domain in existing_domains if not domain.startswith(SOURCE_PREFIX)]
        if foreign_domains and not cfg.get("restore_from_checkpoint_backup"):
            raise RuntimeError(f"candidate has foreign pre-existing domains: {foreign_domains}")
        result["preexisting_probe_domains"] = existing_domains
        for domain in existing_domains:
            domain_state = state(domain)
            if domain_state not in ("shut off", "running", "paused"):
                raise RuntimeError(f"candidate has an uncertain probe domain state: {domain}={domain_state}")
            job = virsh("domjobinfo", domain, "--rawstats", check=False)
            if domain_state == "shut off" and job.returncode and "domain is not running" in job.stderr.lower():
                continue
            if job.returncode or job_type(job.stdout) != "none":
                raise RuntimeError(f"candidate has an uncertain native job on {domain}: {job.stdout or job.stderr}")
        default_network = ET.fromstring(virsh("net-dumpxml", "default").stdout)
        if default_network.find("forward").get("mode") != "nat" or default_network.find("bridge").get("name") != "virbr0":
            raise RuntimeError("candidate default network differs from the isolated NAT lab network")
        network_state = virsh("net-info", "default").stdout
        active_line = next(line.strip() for line in network_state.splitlines() if line.strip().startswith("Active:"))
        if active_line.split() != ["Active:", "yes"]:
            if cfg.get("restore_from_checkpoint_backup"):
                raise RuntimeError("checkpoint restore probe requires the existing default lab network to be active")
            virsh("net-start", "default")
            network_started = True
        result["default_network"] = {"started_by_probe": network_started, "xml": ET.tostring(default_network, encoding="unicode")}
        import grp
        import pwd
        os.chown(work, pwd.getpwnam("libvirt-qemu").pw_uid, grp.getgrnam("kvm").gr_gid)
        os.chmod(work, 0o750)
        os.chown(work / "base.qcow2", pwd.getpwnam("libvirt-qemu").pw_uid, grp.getgrnam("kvm").gr_gid)
        result["steps"].append({"name": "private-qemu-path-access-prepared", "status": "passed"})

        run(["qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", work / "base.qcow2", work / "root.qcow2", str(SIZE)])
        run(["qemu-img", "create", "-f", "qcow2", work / "data.qcow2", "1G"])
        seed_dir = work / "seed"
        seed_dir.mkdir()
        (seed_dir / "user-data").write_text(
            "#cloud-config\ndisable_root: false\nssh_pwauth: false\npackages:\n  - qemu-guest-agent\n"
            "runcmd:\n  - [ systemctl, enable, --now, qemu-guest-agent ]\n")
        (seed_dir / "meta-data").write_text(f"instance-id: {name}\nlocal-hostname: {name}\n")
        seed = work / "seed.iso"
        run(["genisoimage", "-quiet", "-output", seed, "-volid", "cidata", "-joliet", "-rock", seed_dir])
        create_vm(name, work / "root.qcow2", work / "data.qcow2", seed, network=True)
        virsh("start", name)
        wait_agent(name)
        guest(name, "mkfs.ext4 -F /dev/vdb >/dev/null; mkdir -p /mnt/recovery; mount /dev/vdb /mnt/recovery; "
                     f"printf %s {shlex.quote(expected)} > /root/recovery-marker; "
                     f"printf %s {shlex.quote(expected)} > /mnt/recovery/recovery-marker; sync")
        result["steps"].append({"name": "guest-wrote-baseline-markers-to-both-disks", "status": "passed"})

        if not cfg.get("enospc_only"):
            # Keep the external snapshot in place while the live full push runs.
            virsh("snapshot-create-as", name, f"{name}-retained", "--disk-only", "--atomic",
                  "--diskspec", f"vda,snapshot=external,file={work / 'root-retained.qcow2'}",
                  "--diskspec", f"vdb,snapshot=external,file={work / 'data-retained.qcow2'}")
            guest(name, f"printf %s {shlex.quote(MARKERS['before-backup'])} > /root/recovery-marker; "
                         f"printf %s {shlex.quote(MARKERS['before-backup'])} > /mnt/recovery/recovery-marker; sync")
            before_heads = disk_paths(name)
            snapshot_xml = virsh("snapshot-dumpxml", name, f"{name}-retained").stdout
            result["steps"].append({"name": "retained-external-snapshot-chain", "status": "passed",
                                    "snapshot_xml": snapshot_xml, "active_heads": before_heads})

            backup_xml_path = output / "full-backup.xml"
            expected_paths = {}
            reusable_targets = {}
            entries = []
            for target in targets:
                target_file = output / f"{target}-private.qcow2"
                reusable_targets[target] = create_reusable_target(name, target, target_file)
                expected_paths[target] = str(target_file)
                entries.append(f"<disk name='{target}' type='file'><target file='{target_file}'/>"
                               "<driver type='qcow2'/></disk>")
            backup_xml_path.write_text(f"<domainbackup><disks>{''.join(entries)}</disks></domainbackup>")
            result["reusable_targets"] = reusable_targets
            vm_uuid = virsh("domuuid", name).stdout.strip()
            profile_file = Path("/etc/apparmor.d/libvirt") / f"libvirt-{vm_uuid}.files"
            capture = {"profile_file": str(profile_file), "profile_versions": [], "target_metadata": []}

            def capture_backup_access():
                end = time.monotonic() + 3
                last_profile = None
                while time.monotonic() < end:
                    try:
                        current = profile_file.read_text()
                    except OSError as exc:
                        current = f"unavailable: {exc}"
                    if current != last_profile:
                        capture["profile_versions"].append({"monotonic": time.monotonic(), "content": current})
                        last_profile = current
                    for path in expected_paths.values():
                        try:
                            observed_stat = Path(path).stat()
                            metadata = {"path": path, "mode": oct(observed_stat.st_mode & 0o777),
                                        "uid": observed_stat.st_uid, "gid": observed_stat.st_gid,
                                        "size": observed_stat.st_size}
                        except OSError as exc:
                            metadata = {"path": path, "error": str(exc)}
                        capture["target_metadata"].append(metadata)
                    time.sleep(0.01)

            watcher = threading.Thread(target=capture_backup_access, daemon=True)
            watcher.start()
            backup_begin = virsh("backup-begin", name, backup_xml_path, "--reuse-external", check=False)
            watcher.join()
            result["backup_access_capture"] = capture
            if backup_begin.returncode:
                raise RuntimeError(f"backup-begin failed: {backup_begin.stderr.strip() or backup_begin.stdout.strip()}")
            dump = virsh("backup-dumpxml", name, check=False)
            observed = output_paths(dump.stdout) if dump.returncode == 0 else {}
            guest(name, f"printf %s {shlex.quote(MARKERS['after-backup-begin'])} > /root/recovery-marker; "
                         f"printf %s {shlex.quote(MARKERS['after-backup-begin'])} > /mnt/recovery/recovery-marker; "
                         "dd if=/dev/urandom of=/root/backup-load bs=1M count=24 status=none; sync", timeout=180)
            job = wait_backup(name)
            if state(name) not in ("running", "paused"):
                raise RuntimeError("running domain stopped during native full backup")
            if observed and observed != expected_paths:
                raise RuntimeError(f"backup-dumpxml target set/path differs from request: {observed}")
            after_heads = disk_paths(name)
            if after_heads != before_heads:
                raise RuntimeError("native full backup changed the retained external disk heads")
            result["steps"].append({"name": "native-full-push", "status": "passed",
                                    "declared_disk_targets": expected_paths,
                                    "observed_disk_targets": sorted(observed) if observed else "job completed before dumpxml",
                                    "backup_job_after_completion": job,
                                    "external_heads_unchanged": True})
            result["reusable_targets_after_backup"] = {
                target: owner_only_metadata(path, pwd.getpwnam("libvirt-qemu").pw_uid)
                for target, path in expected_paths.items()}

            checkpoint_path = output / "checkpoint.xml"
            checkpoint_path.write_text(f"<domaincheckpoint><name>{name}-checkpoint</name></domaincheckpoint>")
            cp_outputs = []
            checkpoint_paths = {}
            for target in targets:
                target_file = output / f"checkpoint-{target}-private.qcow2"
                create_reusable_target(name, target, target_file)
                checkpoint_paths[target] = target_file
                cp_outputs.append(f"<disk name='{target}' type='file'><target file='{target_file}'/>"
                                  "<driver type='qcow2'/></disk>")
            cp_backup = output / "checkpoint-backup.xml"
            cp_backup.write_text(f"<domainbackup><disks>{''.join(cp_outputs)}</disks></domainbackup>")
            checkpoint = virsh("backup-begin", name, cp_backup, "--checkpointxml", checkpoint_path,
                               "--reuse-external", check=False)
            result["checkpoint_with_external_chain"] = {
                "status": "accepted" if checkpoint.returncode == 0 else "refused",
                "message": (checkpoint.stderr.strip() or checkpoint.stdout.strip())[:1200]}
            if checkpoint.returncode == 0:
                result["checkpoint_with_external_chain"]["backup_job"] = wait_backup(name)
                result["checkpoint_with_external_chain"]["checkpoint_list"] = virsh("checkpoint-list", name).stdout.strip()
                result["checkpoint_with_external_chain"]["output_sha256"] = image_hashes(checkpoint_paths)
                result["checkpoint_with_external_chain"]["output_chains"] = {
                    target: json.loads(qemu_user("qemu-img", "info", "--backing-chain", "--output=json", path).stdout)
                    for target, path in checkpoint_paths.items()}
                for target, entries in result["checkpoint_with_external_chain"]["output_chains"].items():
                    if len(entries) != 1 or entries[0].get("backing-filename"):
                        raise RuntimeError(f"checkpoint full-backup output for {target} retains a source-chain dependency")
                result["checkpoint_with_external_chain"]["checkpoint_list"] = require_checkpoint_listed(
                    result["checkpoint_with_external_chain"]["checkpoint_list"], f"{name}-checkpoint")
                result["checkpoint_with_external_chain"]["output_metadata"] = {
                    target: owner_only_metadata(path, pwd.getpwnam("libvirt-qemu").pw_uid)
                    for target, path in checkpoint_paths.items()}

            if cfg.get("restore_from_checkpoint_backup"):
                checkpoint_list_before = virsh("checkpoint-list", name).stdout.strip()
                checkpoint_list_before = require_checkpoint_listed(
                    checkpoint_list_before, f"{name}-checkpoint")
                checkpoint_present_paths = {}
                checkpoint_present_targets = []
                for target in targets:
                    target_file = output / f"checkpoint-present-{target}-private.qcow2"
                    checkpoint_present_paths[target] = target_file
                    create_reusable_target(name, target, target_file)
                    checkpoint_present_targets.append(
                        f"<disk name='{target}' type='file'><target file='{target_file}'/>"
                        "<driver type='qcow2'/></disk>")
                checkpoint_present_xml = output / "checkpoint-present-backup.xml"
                checkpoint_present_xml.write_text(
                    f"<domainbackup><disks>{''.join(checkpoint_present_targets)}</disks></domainbackup>")
                checkpoint_present_begin = virsh(
                    "backup-begin", name, checkpoint_present_xml, "--reuse-external", check=False)
                if checkpoint_present_begin.returncode:
                    raise RuntimeError("full backup while checkpoint exists failed: " +
                                       (checkpoint_present_begin.stderr.strip() or checkpoint_present_begin.stdout.strip()))
                checkpoint_present_job = wait_backup(name)
                checkpoint_list_after = virsh("checkpoint-list", name).stdout.strip()
                checkpoint_list_after = require_checkpoint_listed(
                    checkpoint_list_after, f"{name}-checkpoint")
                if checkpoint_list_after != checkpoint_list_before:
                    raise RuntimeError("full backup changed checkpoint metadata")
                checkpoint_present_chains = {
                    target: json.loads(qemu_user("qemu-img", "info", "--backing-chain", "--output=json", path).stdout)
                    for target, path in checkpoint_present_paths.items()}
                for target, entries in checkpoint_present_chains.items():
                    if len(entries) != 1 or entries[0].get("backing-filename"):
                        raise RuntimeError(f"checkpoint-present full-backup output for {target} retains a source-chain dependency")
                checkpoint_present_metadata = {
                    target: owner_only_metadata(path, pwd.getpwnam("libvirt-qemu").pw_uid)
                    for target, path in checkpoint_present_paths.items()}
                result["full_backup_while_checkpoint_present"] = {
                    "status": "passed", "checkpoint_list_before": checkpoint_list_before,
                    "checkpoint_list_after": checkpoint_list_after, "backup_job": checkpoint_present_job,
                    "output_sha256": image_hashes(checkpoint_present_paths),
                    "output_metadata": checkpoint_present_metadata,
                    "output_chains": checkpoint_present_chains}

            original_markers = guest(name, "mount /dev/vdb /mnt/recovery; "
                                    "printf 'root=%s data=%s\\n' \"$(cat /root/recovery-marker)\" \"$(cat /mnt/recovery/recovery-marker)\"").strip()
            if original_markers != f"root={MARKERS['after-backup-begin']} data={MARKERS['after-backup-begin']}":
                raise RuntimeError(f"source guest did not retain post-backup writes: {original_markers}")
            result["steps"].append({"name": "source-guest-post-backup-markers", "status": "passed",
                                    "guest_markers": original_markers})

            clear_completed_job(name)
            preserve_outputs = True

        if cfg.get("enospc_destination"):
            enospc_output = Path(cfg["enospc_destination"])
            capacity_root = Path(cfg["enospc_root"])
            assert not enospc_output.exists() and not enospc_output.is_symlink(), "ENOSPC output must be a new path"
            assert enospc_output.parent.resolve(strict=True) == capacity_root.resolve(strict=True), "ENOSPC output must be a direct child of the assigned capacity directory"
            assert not capacity_root.is_symlink() and capacity_root.is_dir(), "assigned capacity directory must be a real directory"
            capacity_mount = run(["findmnt", "-n", "-o", "TARGET", "-T", str(capacity_root)]).stdout.strip()
            assert Path(capacity_mount).resolve(strict=True) == capacity_root.parent.resolve(strict=True), "capacity directory must be a direct child of its dedicated mount"
            assert run(["findmnt", "-n", "-o", "TARGET", "-T", str(enospc_output.parent)]).stdout.strip() == capacity_mount, "capacity destination is not on the assigned file-backed NFS mount"
            assert run(["findmnt", "-n", "-o", "FSTYPE", "-T", str(enospc_output.parent)]).stdout.strip() in ("nfs", "nfs4"), "capacity destination is not NFS"
            enospc_output.mkdir(mode=0o700)
            enospc_before = free_bytes(enospc_output)
            capacity_paths, capacity_xml = backup_targets("enospc-probe", root=enospc_output)
            enospc_started = time.time()
            enospc_begin = virsh("backup-begin", name, capacity_xml, "--reuse-external", check=False)
            if enospc_begin.returncode:
                raise RuntimeError(f"ENOSPC native backup did not begin: {enospc_begin.stderr.strip()}")
            enospc_terminal = wait_terminal_job(name, timeout=300)
            enospc_files = {}
            for target, image in capacity_paths.items():
                info = qemu_user("qemu-img", "info", "--output=json", image, check=False)
                check = qemu_user("qemu-img", "check", "-f", "qcow2", image, check=False)
                metadata = image.stat()
                enospc_files[target] = {
                    "mode": oct(metadata.st_mode & 0o777), "uid": metadata.st_uid,
                    "gid": metadata.st_gid, "file_size_bytes": metadata.st_size,
                    "info_exit_code": info.returncode, "check_exit_code": check.returncode,
                    "check_output": (check.stdout + check.stderr)[:1600],
                    "sha256": qemu_user("sha256sum", image).stdout.split()[0],
                }
            failed_stats = enospc_terminal["stats"]
            enospc_result = {
                "output": str(enospc_output), "available_bytes_before": enospc_before,
                "available_bytes_after": free_bytes(enospc_output),
                "backup_begin_message": (enospc_begin.stdout + enospc_begin.stderr).strip(),
                "elapsed_seconds": time.time() - enospc_started,
                "terminal_job": enospc_terminal,
                "domain_state": virsh("domstate", name, "--reason").stdout.strip(),
                "block_error": virsh("domblkerror", name).stdout.strip(),
                "source_filesystem_available_bytes": free_bytes(work),
                "partial_outputs": enospc_files,
            }
            result["enospc_probe"] = enospc_result
            recovery_action = enospc_recovery_action(
                enospc_result["domain_state"], enospc_result["block_error"])
            if (enospc_terminal["terminal_type"] != "failed" or failed_stats.get("success") != 0
                    or failed_stats.get("disk_processed", 0) >= failed_stats.get("disk_total", 0)
                    or failed_stats.get("disk_remaining", 0) <= 0
                    or "no space left on device" not in enospc_terminal["completed"].lower()
                    or len(enospc_files) != len(targets)
                    or any(item["info_exit_code"] != 0 for item in enospc_files.values())):
                raise RuntimeError("destination-full probe did not produce a failed incomplete native job and inspectable outputs")
            current = virsh("domjobinfo", name, "--rawstats", check=False)
            if current.returncode or job_type(current.stdout) != "none" or enospc_result["source_filesystem_available_bytes"] < 1024**3:
                raise RuntimeError("refusing source write verification without a detached job and healthy source filesystem space")
            if recovery_action == "resume":
                virsh("resume", name)
                deadline = time.monotonic() + 60
                while time.monotonic() < deadline and state(name) != "running":
                    time.sleep(0.5)
                if state(name) != "running":
                    raise RuntimeError("source guest did not resume after the verified destination-full failure")
            elif not domain_is_running(state(name)):
                raise RuntimeError("source guest left the running state after the verified destination-full failure")
            resumed_markers = guest(name, "mount /dev/vdb /mnt/recovery; "
                                    "printf 'nex-recovery-s0-after-enospc-resume' > /root/resume-marker; "
                                    "printf 'nex-recovery-s0-after-enospc-resume' > /mnt/recovery/resume-marker; sync; "
                                    "printf 'root=%s data=%s\\n' \"$(cat /root/resume-marker)\" \"$(cat /mnt/recovery/resume-marker)\"")
            if resumed_markers.strip() != "root=nex-recovery-s0-after-enospc-resume data=nex-recovery-s0-after-enospc-resume":
                raise RuntimeError(f"source guest did not verify writes after ENOSPC resume: {resumed_markers.strip()}")
            enospc_result["resume"] = {
                "action": recovery_action,
                "domain_state": virsh("domstate", name, "--reason").stdout.strip(),
                "current_job": virsh("domjobinfo", name, "--rawstats").stdout.strip(),
                "block_error_after_resume": virsh("domblkerror", name).stdout.strip(),
                "guest_markers_after_new_writes": resumed_markers.strip(),
            }
            enospc_hashes_after_resume = image_hashes(capacity_paths)
            enospc_result["resume"]["output_sha256_after_resume"] = enospc_hashes_after_resume
            if enospc_hashes_after_resume != {target: item["sha256"] for target, item in enospc_files.items()}:
                raise RuntimeError("failed ENOSPC backup outputs changed during source resume")
            enospc_result["resume"]["failed_outputs_preserved"] = True
            result["steps"].append({"name": "real-enospc-failure-source-usability-and-output-preservation", "status": "passed",
                                    "terminal_type": enospc_terminal["terminal_type"],
                                    "source_recovery_action": recovery_action,
                                    "guest_markers": resumed_markers.strip(),
                                    "failed_outputs_preserved": True})

        if cfg.get("enospc_only"):
            if not cfg.get("enospc_destination"):
                raise RuntimeError("ENOSPC-only probe requires an assigned capacity destination")
            virsh("shutdown", name)
            wait_stopped(name)
            virsh("undefine", name)
            result["status"] = "passed"
            completed = True
            return result

        if run_backup_failure_probes(cfg):
            capacity_paths, capacity_xml = backup_targets("capacity-probe", capacity=1024 * 1024)
            capacity_started = time.time()
            capacity_begin = virsh("backup-begin", name, capacity_xml, "--reuse-external", check=False)
            capacity_state = virsh("domjobinfo", name, "--rawstats", check=False)
            capacity_result = {"started_at_unix": capacity_started,
                               "backup_begin_returncode": capacity_begin.returncode,
                               "message": (capacity_begin.stderr.strip() or capacity_begin.stdout.strip())[:1200],
                               "current_job": capacity_state.stdout.strip(),
                               "current_job_query_error": capacity_state.stderr.strip() if capacity_state.returncode else None}
            result["bounded_capacity_probe"] = capacity_result
            if capacity_begin.returncode:
                if capacity_state.returncode or job_type(capacity_state.stdout) != "none":
                    raise RuntimeError("capacity refusal left native job state unknown or active")
                capacity_result["outcome"] = "rejected-before-job"
            else:
                capacity_result["outcome"] = "accepted"
                capacity_result["abort"] = virsh("domjobabort", name, check=False).stderr.strip()
                capacity_result["terminal_job"] = wait_terminal_job(name)
                capacity_result["output_sizes"] = image_sizes(capacity_paths)
                clear_completed_job(name)
            result["steps"].append({"name": "bounded-capacity-probe-observed", "status": "passed",
                                    "outcome": capacity_result["outcome"]})

        if run_backup_failure_probes(cfg):
            clear_completed_job(name)
            stalled_paths, stalled_xml = backup_targets("stalled-cancel-probe")
            mount_source = run(["findmnt", "-T", str(destination), "-no", "SOURCE"]).stdout.strip()
            server = mount_source.split(":", 1)[0]
            route = run(["ip", "route", "get", server]).stdout
            match = re.search(r"\bdev\s+(\S+)", route)
            if not match:
                raise RuntimeError(f"cannot identify the assigned NFS client interface: {route.strip()}")
            interface = match.group(1)
            qdiscs = run(["tc", "qdisc", "show", "dev", interface]).stdout.strip()
            filters = run(["tc", "filter", "show", "dev", interface]).stdout.strip()
            expected_qdisc = ("qdisc fq_codel 0: root refcnt 2 limit 10240p flows 1024 quantum 1514 "
                              "target 5ms interval 100ms memory_limit 32Mb ecn drop_batch 64")
            if qdiscs != expected_qdisc or filters:
                raise RuntimeError(f"refusing to replace an unexpected Core C qdisc: {qdiscs}")
            stall_begin = time.time()
            stalled_begin = virsh("backup-begin", name, stalled_xml, "--reuse-external", check=False)
            stall_result = {"started_at_unix": stall_begin,
                            "backup_begin_returncode": stalled_begin.returncode,
                            "backup_begin_message": (stalled_begin.stderr.strip() or stalled_begin.stdout.strip())[:1200],
                            "interface": interface, "destination_host": server,
                            "original_qdisc": qdiscs, "original_filters": filters}
            result["stalled_cancel_probe"] = stall_result
            if stalled_begin.returncode:
                raise RuntimeError(f"stalled-job probe could not begin: {stalled_begin.stderr.strip()}")
            qdisc_installed = False
            try:
                run(["tc", "qdisc", "replace", "dev", interface, "root", "handle", "1:", "prio"])
                qdisc_installed = True
                run(["tc", "filter", "add", "dev", interface, "protocol", "ip", "parent", "1:",
                     "prio", "1", "u32", "match", "ip", "dst", f"{server}/32", "action", "drop"])
                stall_result["traffic_filter_installed_at_unix"] = time.time()
                time.sleep(2)
                first = virsh("domjobinfo", name, "--rawstats", check=False)
                time.sleep(2)
                second = virsh("domjobinfo", name, "--rawstats", check=False)
                if first.returncode or second.returncode:
                    raise RuntimeError("cannot observe native job while assigned NFS client traffic is filtered")
                first_stats = numeric_stats(first.stdout)
                second_stats = numeric_stats(second.stdout)
                stall_result["job_samples"] = [first.stdout.strip(), second.stdout.strip()]
                stall_result["filter_counters"] = run(["tc", "-s", "filter", "show", "dev", interface, "parent", "1:"]).stdout.strip()
                drop_match = re.search(r"\(dropped\s+(\d+),", stall_result["filter_counters"])
                dropped = int(drop_match.group(1)) if drop_match else 0
                stall_result["stalled"] = (job_type(first.stdout) in ("bounded", "unbounded") and
                                            job_type(second.stdout) in ("bounded", "unbounded") and
                                            first_stats.get("disk_total", 0) > 0 and
                                            first_stats.get("disk_processed") == second_stats.get("disk_processed") and
                                            second_stats.get("disk_remaining") == first_stats.get("disk_remaining") and
                                            dropped > 0)
                if not stall_result["stalled"]:
                    raise RuntimeError("assigned NFS traffic filter did not demonstrate an active stalled job and dropped packets")
                abort = virsh("domjobabort", name, check=False)
                stall_result["domjobabort"] = {"returncode": abort.returncode,
                                                "stdout": abort.stdout.strip(),
                                                "stderr": abort.stderr.strip()}
                if abort.returncode:
                    raise RuntimeError(f"native backup cancellation request failed: {abort.stderr.strip()}")
            finally:
                if qdisc_installed:
                    run(["tc", "qdisc", "del", "dev", interface, "root"])
                    stall_result["traffic_restored_at_unix"] = time.time()
                    restored_qdisc = run(["tc", "qdisc", "show", "dev", interface]).stdout.strip()
                    restored_filters = run(["tc", "filter", "show", "dev", interface]).stdout.strip()
                    stall_result["restored_qdisc"] = restored_qdisc
                    stall_result["restored_filters"] = restored_filters
                    if restored_qdisc != qdiscs or restored_filters != filters:
                        raise RuntimeError("Core C traffic control state differs from the captured baseline after restoration")
            stall_result["terminal_job"] = wait_terminal_job(name)
            stall_result["output_sizes"] = image_sizes(stalled_paths)
            stall_result["output_sha256"] = image_hashes(stalled_paths)
            if (stall_result["terminal_job"]["terminal_type"] != "cancelled"
                    or stall_result["terminal_job"]["stats"].get("success") != 0):
                raise RuntimeError("stalled native backup did not end as a failed cancellation")
            clear_completed_job(name)
            result["steps"].append({"name": "targeted-nfs-stall-and-domjobabort-observed", "status": "passed",
                                    "stalled": stall_result["stalled"],
                                    "terminal_type": stall_result["terminal_job"]["terminal_type"]})

        if run_backup_failure_probes(cfg):
            clear_completed_job(name)
            shutdown_paths, shutdown_xml = backup_targets("shutdown-probe")
            shutdown_started = time.time()
            shutdown_begin = virsh("backup-begin", name, shutdown_xml, "--reuse-external", check=False)
            shutdown_result = {"started_at_unix": shutdown_started,
                               "backup_begin_returncode": shutdown_begin.returncode,
                               "backup_begin_message": (shutdown_begin.stderr.strip() or shutdown_begin.stdout.strip())[:1200]}
            result["shutdown_during_backup_probe"] = shutdown_result
            if shutdown_begin.returncode:
                raise RuntimeError(f"shutdown-interruption probe could not begin: {shutdown_begin.stderr.strip()}")
            shutdown_result["job_before_shutdown"] = virsh("domjobinfo", name, "--rawstats", check=False).stdout.strip()
            shutdown_result["shutdown_requested_at_unix"] = time.time()
            virsh("shutdown", name)
            wait_stopped(name)
            shutdown_result["guest_state_after_shutdown"] = state(name)
            shutdown_result["job_after_shutdown"] = virsh("domjobinfo", name, "--rawstats", check=False).stdout.strip()
            shutdown_result["terminal_query"] = virsh("domjobinfo", name, "--completed", "--keep-completed",
                                                        "--anystats", "--rawstats", check=False).__dict__
            if shutdown_result["terminal_query"]["returncode"] != 0:
                raise RuntimeError("shutdown during backup left terminal native job outcome unknown")
            shutdown_terminal = shutdown_result["terminal_query"]["stdout"]
            shutdown_type = job_type(shutdown_terminal)
            if shutdown_type not in ("completed", "failed", "cancelled"):
                raise RuntimeError(f"shutdown during backup has no terminal job statistics: {shutdown_terminal.strip()}")
            shutdown_result["terminal_type"] = shutdown_type
            shutdown_result["stats"] = numeric_stats(shutdown_terminal)
            shutdown_result["output_sizes"] = image_sizes(shutdown_paths)
            shutdown_result["output_sha256"] = image_hashes(shutdown_paths)
            clear_completed_job(name)

        if state(name) != "shut off":
            virsh("shutdown", name)
        wait_stopped(name)
        snapshot_chains = {target: chain(path) for target, path in before_heads.items()}
        result["steps"].append({"name": "retained-external-snapshot-chain-stopped-inspection", "status": "passed",
                                "chains": snapshot_chains})
        checkpoint_restore = cfg.get("restore_from_checkpoint_backup", False)
        backup_paths = restore_backup_paths(output, targets, checkpoint_restore)
        expected_restore_marker = restore_marker(checkpoint_restore)
        output_info = {target: json.loads(qemu_user("qemu-img", "info", "--backing-chain", "--output=json", path).stdout)
                       for target, path in backup_paths.items()}
        for target, entries in output_info.items():
            if len(entries) != 1 or entries[0].get("backing-filename"):
                raise RuntimeError(f"{target} full export retains a source-chain dependency")
        result["steps"].append({"name": "native-outputs-have-no-backing-files", "status": "passed",
                                "output_chains": output_info})

        converted.mkdir(mode=0o750)
        import grp
        import pwd
        os.chown(converted, pwd.getpwnam("libvirt-qemu").pw_uid, grp.getgrnam("kvm").gr_gid)
        for target, source in zip(targets, (work / "root-retained.qcow2", work / "data-retained.qcow2")):
            destination_path = converted / f"{target}.qcow2"
            run(["qemu-img", "convert", "-p", "-f", "qcow2", "-O", "qcow2", source, destination_path])
            os.chown(destination_path, pwd.getpwnam("libvirt-qemu").pw_uid, grp.getgrnam("kvm").gr_gid)
            os.chmod(destination_path, 0o600)
        result["steps"].append({"name": "stopped-qemu-img-convert-completed-before-source-removal", "status": "passed"})

        os.rename(work, held)
        result["steps"].append({"name": "original-images-and-backing-chain-unavailable", "status": "passed"})
        restored.mkdir(mode=0o750)
        os.chown(restored, pwd.getpwnam("libvirt-qemu").pw_uid, grp.getgrnam("kvm").gr_gid)
        qemu_uid = pwd.getpwnam("libvirt-qemu").pw_uid
        result["export_metadata_before_restore"] = {
            target: owner_only_metadata(path, qemu_uid) for target, path in backup_paths.items()}
        for target, path in backup_paths.items():
            result.setdefault("export_sha256_before_restore", {})[target] = qemu_user("sha256sum", path).stdout.split()[0]
            qemu_user("cp", "--reflink=auto", path, restored / f"{target}-export.qcow2")
        local_exports = {target: restored / f"{target}-export.qcow2" for target in targets}
        qemu_user("qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", local_exports["vda"], restored / "root.qcow2")
        qemu_user("qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", local_exports["vdb"], restored / "data.qcow2")
        create_vm(restored_name, restored / "root.qcow2", restored / "data.qcow2")
        virsh("start", restored_name)
        wait_agent(restored_name)
        markers = guest(restored_name, "mount /dev/vdb /mnt/recovery; "
                        "printf 'root=%s data=%s\\n' \"$(cat /root/recovery-marker)\" \"$(cat /mnt/recovery/recovery-marker)\"").strip()
        if markers != f"root={expected_restore_marker} data={expected_restore_marker}":
            raise RuntimeError(f"independent restore markers differ from the backup start point: {markers}")
        result["export_sha256_after_restore"] = {
            target: qemu_user("sha256sum", path).stdout.split()[0] for target, path in backup_paths.items()}
        if result["export_sha256_after_restore"] != result["export_sha256_before_restore"]:
            raise RuntimeError("backup export changed during restore from writable overlays")
        result["export_metadata_after_restore"] = {
            target: owner_only_metadata(path, qemu_uid) for target, path in backup_paths.items()}
        if result["export_metadata_after_restore"] != result["export_metadata_before_restore"]:
            raise RuntimeError("backup output ownership, permissions, or sizes changed during local restore")
        result["steps"].append({"name": "independent-restore-booted-from-local-export-copies-while-original-unavailable", "status": "passed",
                                "guest_markers": markers})
        virsh("destroy", restored_name)
        virsh("undefine", restored_name)
        shutil.rmtree(restored, ignore_errors=True)

        # A damaged disposable copy must fail structural validation; the complete NFS outputs stay untouched.
        damaged = output / "truncated.qcow2"
        qemu_user("cp", "--reflink=auto", backup_paths["vda"], damaged)
        qemu_user("truncate", "-s", "512", damaged)
        damage_check = qemu_user("qemu-img", "check", "-f", "qcow2", damaged, check=False)
        if damage_check.returncode == 0:
            raise RuntimeError("qemu-img accepted the deliberately truncated backup copy")
        result["steps"].append({"name": "truncated-copy-rejected", "status": "passed",
                                "validator": "qemu-img check", "exit_code": damage_check.returncode})

        corrupted = output / "corrupt-header.qcow2"
        qemu_user("cp", "--reflink=auto", backup_paths["vda"], corrupted)
        qemu_user("python3", "-c", "import sys; f=open(sys.argv[1], 'r+b'); f.write(b'CORRUPT!'); f.close()", corrupted)
        corruption_check = qemu_user("qemu-img", "check", "-f", "qcow2", corrupted, check=False)
        if corruption_check.returncode == 0:
            raise RuntimeError("qemu-img accepted the deliberately corrupted backup copy")
        result["steps"].append({"name": "corrupt-copy-rejected", "status": "passed",
                                "validator": "qemu-img check", "exit_code": corruption_check.returncode})

        create_vm(converted_name, converted / "vda.qcow2", converted / "vdb.qcow2")
        virsh("start", converted_name)
        wait_agent(converted_name)
        converted_markers = guest(converted_name, "mount /dev/vdb /mnt/recovery; "
                                  "printf 'root=%s data=%s\\n' \"$(cat /root/recovery-marker)\" \"$(cat /mnt/recovery/recovery-marker)\"").strip()
        if converted_markers != f"root={MARKERS['after-backup-begin']} data={MARKERS['after-backup-begin']}":
            raise RuntimeError(f"stopped qemu-img convert restore markers differ from the shutoff source: {converted_markers}")
        virsh("destroy", converted_name)
        virsh("undefine", converted_name)
        shutil.rmtree(converted, ignore_errors=True)
        result["steps"].append({"name": "stopped-qemu-img-convert-independent-restore", "status": "passed",
                                "guest_markers": converted_markers})

        virsh("undefine", name, "--snapshots-metadata", "--checkpoints-metadata")
        os.rename(held, work)
        shutil.rmtree(work, ignore_errors=True)
        result["status"] = "passed"
        completed = True
    except Exception as exc:
        result["status"] = "failed"
        result["error"] = str(exc)
        result["preserved_paths"] = [str(path) for path in (work, held, output) if path.exists()]
        for domain in (converted_name, restored_name, name):
            observed = virsh("domstate", domain, check=False)
            if observed.returncode == 0:
                result.setdefault("failure_observation", {})[domain] = {
                    "state": observed.stdout.strip(),
                    "snapshots": virsh("snapshot-list", domain, check=False).stdout.strip(),
                    "job": virsh("domjobinfo", domain, check=False).stdout.strip(),
                }
    finally:
        if completed:
            finalize_successful_probe((converted_name, restored_name, name), state, virsh,
                                      network_started, work, held, converted, output,
                                      preserve_outputs)
    return result


def main():
    from recovery_s0_firmware import ssh_args, validate_lab

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lab-dir", type=Path, required=True)
    parser.add_argument("--evidence", type=Path, required=True, help="new ignored .cache directory")
    parser.add_argument("--destination", help="unique writable child of the assigned NFS mount")
    parser.add_argument("--destination-mount", default="/mnt/recovery-s0-nfs",
                        help="dedicated existing NFS mount path on the selected Core guest")
    parser.add_argument("--destination-source", help="require and, if needed, mount this exact NFS export source")
    parser.add_argument("--enospc-root", help="optional empty assigned child on a separate small file-backed NFS mount")
    parser.add_argument("--enospc-only", action="store_true", help="run only the isolated real-capacity exhaustion probe")
    parser.add_argument("--node", choices=("core1", "core2", "core3"), default="core3",
                        help="selected disposable Core guest in the recovery lab")
    parser.add_argument("--restore-from-checkpoint-backup", action="store_true",
                        help="restore from the checkpointed full-backup outputs")
    parser.add_argument("--expected-libvirt-version", help="require the selected installed libvirt package version")
    parser.add_argument("--candidate-manifest-sha256", help="record the independently verified selected-package manifest digest")
    parser.add_argument("--candidate-package-dir", type=Path,
                        help="local staged +nex3 15-package closure with SHA256SUMS.selected")
    parser.add_argument("--candidate-source-manifest", type=Path,
                        help="local full +nex3 SHA256SUMS matched to the source recipe")
    parser.add_argument("--staged-package-dir", default="/var/tmp/recovery-s0-nex3-branch-116615ecd128",
                        help="read-only selected .deb staging directory on the Core guest")
    args = parser.parse_args()
    lab = validate_lab(args.lab_dir)
    if args.enospc_only:
        assert not args.destination, "ENOSPC-only mode does not use the normal backup destination"
        assert args.enospc_root, "ENOSPC-only mode requires a fresh assigned capacity child"
    else:
        assert args.destination, "normal backup mode requires a unique NFS destination"
        destination, destination_mount = require_nfs_destination(args.destination, args.destination_mount)
    if args.restore_from_checkpoint_backup:
        assert args.expected_libvirt_version and args.candidate_manifest_sha256, "checkpoint restore runs require exact candidate version and package manifest pins"
        assert args.expected_libvirt_version == NEX3_PACKAGE_VERSION, "checkpoint restore mode is pinned to the reviewed +nex3 candidate"
        assert re.fullmatch(r"[0-9a-f]{64}", args.candidate_manifest_sha256), "candidate manifest digest must be lowercase SHA256"
        assert args.candidate_package_dir and args.candidate_source_manifest, "checkpoint restore runs require local candidate package and source manifest paths"
        assert args.destination_source, "checkpoint restore mode requires the exact selected NFS export source"
    enospc_root = assigned_capacity_root(args.enospc_root) if args.enospc_root else None
    if args.enospc_only:
        assert enospc_root is not None, "ENOSPC-only mode requires an assigned capacity child"
    assert not args.evidence.exists(), "evidence directory must be new"
    assert args.evidence.resolve().is_relative_to((ROOT / ".cache").resolve()), "raw evidence must remain in ignored .cache"
    assert BASE.is_file() and not BASE.is_symlink(), "verified Debian cloud image is missing"
    if hashlib.file_digest(BASE.open("rb"), "sha256").hexdigest() != BASE_SHA256:
        raise RuntimeError("cached Debian cloud image digest differs from the verified fixture")

    run_id = uuid.uuid4().hex
    work = f"/var/lib/libvirt/images/nex-recovery-s0-backup/{run_id}"
    destination = f"{work}/scratch-output" if args.enospc_only else args.destination
    cfg = {"work": work, "destination": destination, "output": f"{destination}/{run_id}",
           "domain": SOURCE_PREFIX + run_id[:12], "restored_domain": SOURCE_PREFIX + "restore-" + run_id[:8],
           "candidate_alias": f"Core {args.node[-1].upper()}",
           "restore_from_checkpoint_backup": args.restore_from_checkpoint_backup,
           "destination_mount": str(destination_mount) if not args.enospc_only else None,
           "destination_source": args.destination_source}
    if args.expected_libvirt_version:
        cfg["expected_libvirt_version"] = args.expected_libvirt_version
    if args.candidate_manifest_sha256:
        cfg["candidate_manifest_sha256"] = args.candidate_manifest_sha256
    if args.restore_from_checkpoint_backup:
        cfg["candidate"] = local_candidate_package_pin(
            args.candidate_package_dir, args.candidate_source_manifest,
            args.candidate_manifest_sha256, args.expected_libvirt_version,
            args.staged_package_dir)
    if args.enospc_only:
        cfg["enospc_only"] = True
    if enospc_root is not None:
        cfg["enospc_root"] = str(enospc_root)
        cfg["enospc_destination"] = str(enospc_root / run_id)
    evidence = args.evidence.resolve()
    evidence.mkdir(parents=True, mode=0o700)
    (evidence / "config.json").write_text(json.dumps(cfg, indent=2) + "\n")
    ssh = ssh_args(lab, args.node)
    if args.destination_source:
        ports = json.loads((lab / "ports.json").read_text())
        storage_ssh = ["ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
                       "-o", "StrictHostKeyChecking=yes", "-o", f"UserKnownHostsFile={lab / 'known_hosts'}",
                       "-i", str(lab / "id_ed25519"), "-p", str(ports["storage"]), "root@127.0.0.1"]
        export_command = "printf '%s\\n' '--- /etc/exports ---'; cat /etc/exports; printf '%s\\n' '--- exportfs -v ---'; exportfs -v"
        before = run(storage_ssh + [export_command], check=False)
        if before.returncode:
            raise RuntimeError(f"could not capture selected lab export configuration before mount: {evidence}")
        (evidence / "storage-exports-before.txt").write_text(before.stdout)
    if cfg.get("candidate"):
        preflight_cfg = {"candidate": cfg["candidate"], "candidate_preflight_only": True}
        preflight_source = Path(__file__).read_text().replace("REMOTE_CONFIG = None",
                                                             f"REMOTE_CONFIG = {preflight_cfg!r}")
        preflight = run(ssh + ["python3", "-"], input=preflight_source, check=False, timeout=600)
        (evidence / "candidate-preflight.stdout").write_text(preflight.stdout)
        (evidence / "candidate-preflight.stderr").write_text(preflight.stderr)
        if preflight.returncode:
            raise RuntimeError(f"read-only candidate preflight failed before Core scratch setup; private evidence: {evidence}")
        try:
            receipt = json.loads(preflight.stdout)
        except json.JSONDecodeError as exc:
            raise RuntimeError(f"candidate preflight returned invalid JSON; private evidence: {evidence}") from exc
        if receipt.get("manifest_sha256") != cfg["candidate"]["manifest_sha256"]:
            raise RuntimeError("Core runtime file preflight did not match the selected full candidate manifest")
        (evidence / "candidate-preflight.json").write_text(json.dumps(receipt, indent=2) + "\n")
    if args.destination_source:
        mount_cfg = {"destination_mount": cfg["destination_mount"], "destination_source": args.destination_source}
        mount_source = Path(__file__).read_text().replace("REMOTE_CONFIG = None",
                                                         f"REMOTE_CONFIG = {dict(mount_cfg, mount_setup_only=True)!r}")
        mounted = run(ssh + ["python3", "-"], input=mount_source, check=False, timeout=300)
        (evidence / "mount-setup.stdout").write_text(mounted.stdout)
        (evidence / "mount-setup.stderr").write_text(mounted.stderr)
        if mounted.returncode:
            raise RuntimeError(f"exact writable NFS mount setup failed before Core scratch setup: {evidence}")
        try:
            mount_receipt = json.loads(mounted.stdout)
        except json.JSONDecodeError as exc:
            raise RuntimeError(f"NFS mount setup returned invalid JSON: {evidence}") from exc
        require_nfs_mount(json.dumps({"filesystems": [mount_receipt["mount"]]}),
                          cfg["destination_mount"], args.destination_source)
        (evidence / "mount-setup.json").write_text(json.dumps(mount_receipt, indent=2) + "\n")
    remote_parent = "/var/lib/libvirt/images/nex-recovery-s0-backup"
    setup = (f"set -eu; install -d -m 0755 {shlex.quote(remote_parent)}; "
             f"test ! -e {shlex.quote(work)}; install -d -m 0700 {shlex.quote(work)}; "
             f"printf %s {shlex.quote(SENTINEL)} > {shlex.quote(work + '/sentinel')}; "
             f"test ! -e {shlex.quote(destination)}; "
             f"install -d -m 0777 {shlex.quote(destination)}; "
             f"test ! -e {shlex.quote(cfg['output'])}; install -d -m 0777 {shlex.quote(cfg['output'])}")
    run(ssh + [setup])
    scp = ["scp", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
           "-o", f"UserKnownHostsFile={lab / 'known_hosts'}", "-i", str(lab / "id_ed25519"),
           "-P", str(json.loads((lab / "ports.json").read_text())[args.node]), str(BASE),
           f"root@127.0.0.1:{work}/base.qcow2"]
    run(scp, timeout=600)
    source = Path(__file__).read_text().replace("REMOTE_CONFIG = None", f"REMOTE_CONFIG = {cfg!r}")
    reply = run(ssh + ["python3", "-"], input=source, check=False, timeout=2400)
    (evidence / "remote.stdout").write_text(reply.stdout)
    (evidence / "remote.stderr").write_text(reply.stderr)
    if args.destination_source:
        after = run(storage_ssh + [export_command], check=False)
        (evidence / "storage-exports-after.txt").write_text(after.stdout)
        if after.returncode or after.stdout != before.stdout:
            raise RuntimeError(f"lab storage export configuration changed during the backup probe: {evidence}")
    if reply.returncode:
        raise RuntimeError(f"Core C backup probe exited {reply.returncode}; private evidence: {evidence}")
    result = json.loads(reply.stdout)
    (evidence / "result.json").write_text(json.dumps(result, indent=2) + "\n")
    print(evidence)
    if result["status"] != "passed":
        raise RuntimeError(f"Core C backup probe did not pass; private evidence: {evidence}")


if REMOTE_CONFIG is not None:
    if REMOTE_CONFIG.get("candidate_preflight_only"):
        print(json.dumps(verify_staged_runtime_packages(REMOTE_CONFIG)))
    elif REMOTE_CONFIG.get("mount_setup_only"):
        print(json.dumps(ensure_nfs_mount(REMOTE_CONFIG)))
    else:
        print(json.dumps(inner_probe(REMOTE_CONFIG)))
else:
    if __name__ == "__main__":
        sys.exit(main())
