#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Build the matched libvirt candidate and run its unprivileged TCG regressions."""

import argparse
import hashlib
import importlib.util
import inspect
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import time
import uuid
from xml.etree import ElementTree
from xml.sax.saxutils import quoteattr

BUILDER_IMAGE = (
    "debian:trixie-slim@sha256:"
    "d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132"
)
SOURCE_VERSION = "11.3.0-3+deb13u3+nex4"
def system_probe_scope(mode: str) -> str:
    return f"paused BIOS external qcow2 {mode} public blockcommit; no integrated data-delete"
DEBIAN_SNAPSHOT = "20260920T000000Z"
PATCH_FILES = (
    "readonly-disk-apparmor.patch",
    "unique-revert-overlay.patch",
    "partial-backing-chain.patch",
    "qmp-json-depth.patch",
    "preserve-readonly-revert-media.patch",
)
CLOUD_IMAGE_URL = (
    "https://cloud.debian.org/images/cloud/trixie/20260831-2587/"
    "debian-13-genericcloud-amd64-20260831-2587.qcow2"
)
CLOUD_IMAGE_SHA256 = "85a969b7e99d7c817414136033df18c58d5c45ac8d27bb36e8ccb67173d2d4e3"
CANDIDATE_PACKAGES = (
    "libvirt-clients",
    "libvirt-common",
    "libvirt-daemon",
    "libvirt-daemon-common",
    "libvirt-daemon-driver-qemu",
    "libvirt-daemon-log",
    "libvirt0",
)
SYSTEM_CANDIDATE_PACKAGES = (
    "libvirt-clients", "libvirt-common", "libvirt-daemon", "libvirt-daemon-common",
    "libvirt-daemon-config-network", "libvirt-daemon-config-nwfilter",
    "libvirt-daemon-driver-network", "libvirt-daemon-driver-nodedev",
    "libvirt-daemon-driver-nwfilter", "libvirt-daemon-driver-qemu",
    "libvirt-daemon-driver-secret", "libvirt-daemon-driver-storage",
    "libvirt-daemon-log", "libvirt-daemon-system", "libvirt0",
)


def run(command: list[str], *, cwd: str | None = None, timeout: int | None = None) -> None:
    print("+", " ".join(command), flush=True)
    subprocess.run(command, check=True, cwd=cwd, timeout=timeout)


def check_packages(candidate: Path, packages=CANDIDATE_PACKAGES) -> list[Path]:
    debs = []
    for package in packages:
        matches = [path for path in candidate.glob(f"{package}_*_*.deb")
                   if path.name.endswith(("_amd64.deb", "_all.deb"))]
        if len(matches) != 1:
            raise RuntimeError(f"expected one amd64/all candidate package for {package}, got {matches}")
        metadata_lines = subprocess.run(
            ["dpkg-deb", "-f", str(matches[0]), "Package", "Version", "Architecture"],
            check=True,
            text=True,
            capture_output=True,
        ).stdout.splitlines()
        metadata = {}
        for line in metadata_lines:
            field, separator, value = line.partition(":")
            if not separator:
                raise RuntimeError(f"{matches[0].name} has malformed package metadata")
            metadata[field.strip()] = value.strip()
        if not {"Package", "Version", "Architecture"}.issubset(metadata):
            raise RuntimeError(f"{matches[0].name} has incomplete package metadata")
        observed_package = metadata["Package"]
        version = metadata["Version"]
        architecture = metadata["Architecture"]
        if (observed_package != package or version != SOURCE_VERSION or
                architecture not in {"amd64", "all"} or
                not matches[0].name.endswith(f"_{architecture}.deb")):
            raise RuntimeError(
                f"{matches[0].name} metadata is {observed_package} {version} {architecture}; "
                f"expected {package} {SOURCE_VERSION} with amd64 or all architecture"
            )
        debs.append(matches[0])
    return debs


def candidate_provenance(candidate: Path, debs: list[Path]) -> dict:
    manifest = candidate / "SHA256SUMS"
    inputs = candidate / "build-inputs.txt"
    if not manifest.is_file() or not inputs.is_file():
        raise RuntimeError("candidate lacks its checksum manifest or build-input record")
    input_fields = dict(line.split("=", 1) for line in inputs.read_text().splitlines()
                        if "=" in line)
    recipe = input_fields.get("recipe_sha256", "")
    if len(recipe) != 64:
        raise RuntimeError("candidate build-input record lacks a recipe SHA256")
    if input_fields.get("local_version") != SOURCE_VERSION:
        raise RuntimeError("candidate build-input record does not match the selected source version")
    return {
        "source_version": SOURCE_VERSION,
        "sha256sums_sha256": hashlib.sha256(manifest.read_bytes()).hexdigest(),
        "recipe_sha256": recipe,
        "packages": [
            {"filename": path.name, "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}
            for path in debs
        ],
    }


def create_container(name: str) -> str:
    command = ["docker", "create", "--init", "--platform", "linux/amd64", "--name", name,
               "-v", "nex-build-cache:/apt-cache", BUILDER_IMAGE, "sleep", "infinity"]
    print("+", " ".join(command), flush=True)
    result = subprocess.run(command, check=True, text=True, capture_output=True)
    container_id = result.stdout.strip()
    if not container_id:
        raise RuntimeError("docker create returned no container ID")
    print(f"Created container {container_id}", flush=True)
    try:
        run(["docker", "start", container_id])
    except Exception:
        subprocess.run(["docker", "rm", "-f", container_id], check=False)
        raise
    return container_id


def check_runtime_result(path: Path) -> None:
    if not path.is_file():
        raise RuntimeError("runtime container did not export evidence/result.json")
    report = json.loads(path.read_text())
    if (report.get("status") != "passed" or report.get("uri") != "qemu:///session" or
            report.get("domain_type") != "qemu"):
        raise RuntimeError("runtime evidence does not report a passed qemu:///session TCG probe")
    cases = report.get("cases", [])
    if len(cases) != 2 or {case.get("disk_count") for case in cases} != {1, 2}:
        raise RuntimeError("runtime evidence is missing its one- or two-disk case")
    expected_versions = {package: SOURCE_VERSION for package in CANDIDATE_PACKAGES}
    expected_versions.update({
        "qemu-system-x86": "1:10.0.13+ds-0+deb13u1",
        "qemu-utils": "1:10.0.13+ds-0+deb13u1",
    })
    observed_versions = {}
    for package_line in report.get("packages", []):
        package, version = package_line.split(maxsplit=1)
        observed_versions[package.split(":", 1)[0]] = version
    if any(observed_versions.get(package) != version for package, version in expected_versions.items()):
        raise RuntimeError("runtime evidence package versions differ from the pinned candidate/runtime")
    for case in cases:
        if case.get("status") != "passed" or case.get("mode") != "rapid":
            raise RuntimeError(f"runtime lifecycle case did not pass: {case.get('name')}")
        steps = {step.get("name") for step in case.get("steps", [])}
        required = {"revert-older-point", "revert-newer-then-repeat-older",
                    "repeated-revert-after-new-point", "delete-point-c-after-revert",
                    "delete-point-b", "delete-point-a", "start-after-delete"}
        if not required.issubset(steps):
            raise RuntimeError(f"runtime evidence is missing lifecycle steps: {sorted(required - steps)}")
        uuid_maps = []
        def find_uuid_maps(value):
            if isinstance(value, dict):
                if isinstance(value.get("uuid_overlays"), dict):
                    uuid_maps.append(value["uuid_overlays"])
                for child in value.values():
                    find_uuid_maps(child)
            elif isinstance(value, list):
                for child in value:
                    find_uuid_maps(child)
        find_uuid_maps(case)
        expected_overlays = 4
        if len(uuid_maps) != expected_overlays:
            raise RuntimeError(f"runtime evidence has {len(uuid_maps)} UUID overlay observations, expected {expected_overlays}")
    media = report.get("readonly_media_revert")
    if (not isinstance(media, dict) or media.get("status") != "passed" or
            media.get("snapshot_media_policy") != "no" or
            not media.get("restored_original_source") or
            media.get("media_overlay_created") is not False or
            not media.get("writable_disk_head_valid")):
        raise RuntimeError("runtime evidence lacks the read-only media revert regression")


def readonly_media_revert_probe(root: Path) -> dict:
    """Verify external revert restores read-only media without making an overlay."""
    uri = "qemu:///session"
    case_dir = root / f"readonly-media-{uuid.uuid4().hex}"
    case_dir.mkdir(mode=0o700)
    domain = f"nex-recovery-readonly-media-{uuid.uuid4().hex}"
    disk = case_dir / "root.qcow2"
    head = case_dir / "root-revert-head.qcow2"
    media_a = case_dir / "installer-a.iso"
    media_b = case_dir / "installer-b.iso"
    media_a.write_bytes(b"NEX-READONLY-MEDIA-A\n")
    media_b.write_bytes(b"NEX-READONLY-MEDIA-B\n")
    media_hash = hashlib.sha256(media_a.read_bytes()).hexdigest()
    snapshot = "readonly-media-point"

    def virsh(*args: str) -> str:
        result = subprocess.run(["virsh", "-c", uri, *args], text=True,
                                capture_output=True, timeout=120)
        if result.returncode:
            raise RuntimeError(
                f"virsh {' '.join(args)} failed ({result.returncode}): "
                f"{result.stderr.strip()} {result.stdout.strip()}"
            )
        return result.stdout

    def xml_text(command: str, *args: str) -> str:
        return virsh(command, domain, *args)

    def disk_source(xml: str, target: str) -> str | None:
        definition = ElementTree.fromstring(xml)
        for disk_node in definition.findall("./devices/disk"):
            target_node = disk_node.find("target")
            if target_node is not None and target_node.get("dev") == target:
                source_node = disk_node.find("source")
                if source_node is not None:
                    return source_node.get("file") or source_node.get("dev")
        return None

    snapshot_policy = None
    result = {"status": "failed-preserved", "domain": domain,
              "snapshot_media_policy": None, "restored_original_source": False,
              "media_overlay_created": None, "writable_disk_head_valid": False,
              "media_source_sha256_before": media_hash,
              "media_source_sha256_after": None}
    try:
        subprocess.run(["qemu-img", "create", "-q", "-f", "qcow2", str(disk), "64M"],
                       check=True, timeout=120, capture_output=True, text=True)
        domain_xml = f"""<domain type='qemu'>
  <name>{domain}</name>
  <memory unit='MiB'>256</memory><currentMemory unit='MiB'>256</currentMemory>
  <vcpu>1</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/><source file={quoteattr(str(disk))}/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/><source file={quoteattr(str(media_a))}/>
      <target dev='hdc' bus='ide'/><readonly/>
    </disk>
    <console type='pty'><target type='serial' port='0'/></console>
  </devices>
</domain>"""
        define = subprocess.run(["virsh", "-c", uri, "define", "/dev/stdin"],
                                input=domain_xml, text=True, capture_output=True, timeout=120)
        if define.returncode:
            raise RuntimeError(f"virsh define failed: {define.stderr.strip()}")
        virsh("snapshot-create-as", domain, snapshot, "--disk-only", "--atomic",
              "--diskspec", f"vda,snapshot=external,file={head}",
              "--diskspec", "hdc,snapshot=no")
        saved_xml = xml_text("snapshot-dumpxml", snapshot)
        saved = ElementTree.fromstring(saved_xml)
        for disk_node in saved.findall("./disks/disk"):
            if disk_node.get("name") == "hdc":
                snapshot_policy = disk_node.get("snapshot")
        result["snapshot_media_policy"] = snapshot_policy
        if snapshot_policy != "no":
            raise RuntimeError(f"snapshot metadata recorded hdc policy {snapshot_policy!r}, expected no")
        if disk_source(xml_text("dumpxml", "--inactive"), "hdc") != str(media_a):
            raise RuntimeError("initial inactive domain XML does not reference original read-only media")
        virsh("change-media", domain, "hdc", "--eject", "--config")
        virsh("change-media", domain, "hdc", str(media_b), "--insert", "--config")
        if disk_source(xml_text("dumpxml", "--inactive"), "hdc") != str(media_b):
            raise RuntimeError("test setup failed to replace the configured read-only media")
        virsh("snapshot-revert", domain, snapshot, "--force")
        inactive_xml = xml_text("dumpxml", "--inactive")
        reverted_snapshot_xml = xml_text("snapshot-dumpxml", snapshot)
        reverted_snapshot = ElementTree.fromstring(reverted_snapshot_xml)
        reverted_media_policy = None
        reverted_media_source = None
        for disk_node in reverted_snapshot.findall("./disks/disk"):
            if disk_node.get("name") == "hdc":
                reverted_media_policy = disk_node.get("snapshot")
                source_node = disk_node.find("source")
                if source_node is not None:
                    reverted_media_source = source_node.get("file") or source_node.get("dev")
        restored = disk_source(inactive_xml, "hdc") == str(media_a)
        readonly_node = ElementTree.fromstring(inactive_xml).find(
            "./devices/disk[target[@dev='hdc']]/readonly")
        media_overlay = (reverted_media_policy != "no" or reverted_media_source is not None or
                         any(case_dir.glob("installer-a.iso.*")))
        current_head = disk_source(inactive_xml, "vda")
        if not current_head:
            raise RuntimeError("reverted inactive domain has no writable vda source")
        image_info = json.loads(subprocess.run(
            ["qemu-img", "info", "--output=json", current_head], check=True,
            text=True, capture_output=True, timeout=120).stdout)
        checked_head = subprocess.run(["qemu-img", "check", current_head], text=True,
                                      capture_output=True, timeout=120)
        writable_valid = (image_info.get("format") == "qcow2" and
                          image_info.get("backing-filename") == str(head) and
                          checked_head.returncode == 0 and Path(current_head).is_file() and
                          head.is_file())
        media_hash_after = hashlib.sha256(media_a.read_bytes()).hexdigest()
        result.update({"restored_original_source": restored,
                       "readonly_restored": readonly_node is not None,
                       "reverted_snapshot_media_policy": reverted_media_policy,
                       "reverted_snapshot_media_source": reverted_media_source,
                       "media_overlay_created": media_overlay,
                       "writable_disk_head_valid": writable_valid,
                       "current_writable_disk_head": current_head,
                       "media_source_sha256_after": media_hash_after,
                       "snapshot_disk_targets": sorted(
                           item.get("name", "") for item in saved.findall("./disks/disk")),
                       "saved_snapshot_xml_sha256": hashlib.sha256(saved_xml.encode()).hexdigest(),
                       "restored_domain_xml_sha256": hashlib.sha256(inactive_xml.encode()).hexdigest()})
        if (not restored or readonly_node is None or media_overlay or not writable_valid or
                media_hash_after != media_hash):
            raise RuntimeError("readonly media or writable qcow2 chain verification failed")
        virsh("undefine", domain, "--snapshots-metadata")
        result["status"] = "passed"
        shutil.rmtree(case_dir)
    except Exception as error:
        result["error"] = str(error)
        try:
            result["current_domain_xml"] = xml_text("dumpxml", "--inactive")
            result["snapshot_list"] = virsh("snapshot-list", domain, "--tree")
        except Exception:
            pass
    return result


def check_apparmor_result(path: Path) -> None:
    if not path.is_file():
        raise RuntimeError("AppArmor guest did not export evidence/result.json")
    report = json.loads(path.read_text())
    cases = report.get("cases", [])
    if (report.get("exit_code") != 0 or report.get("uri") != "qemu:///system" or
            report.get("domain_type") != "qemu" or
            report.get("scope") != system_probe_scope("midcommit") or
            report.get("apparmor", {}).get("kernel_enabled") != "Y" or
            len(cases) != 1):
        raise RuntimeError("system-mode AppArmor public-consolidation evidence is incomplete or failed")
    observed_versions = {}
    for package_line in report.get("packages", []):
        package, version = package_line.split(maxsplit=1)
        observed_versions[package.split(":", 1)[0]] = version
    if any(observed_versions.get(package) != SOURCE_VERSION
           for package in SYSTEM_CANDIDATE_PACKAGES):
        raise RuntimeError("AppArmor guest evidence does not show the exact matched package set")
    case = cases[0]
    if (case.get("case") != "public-no-delete-2disk" or case.get("status") != "passed" or
            case.get("mode") != "midcommit"):
        raise RuntimeError("public two-disk consolidation did not pass")
    domain_uuid = case.get("uuid")
    if not isinstance(domain_uuid, str) or not domain_uuid:
        raise RuntimeError("public consolidation lacks a domain UUID")
    targets = {"vda", "vdb"}
    if set(case.get("commits", {})) != targets:
        raise RuntimeError("public consolidation did not commit both disk chains")
    for target, commit in case["commits"].items():
        argv = commit.get("argv", [])
        apparmor = commit.get("apparmor_during_commit", {})
        before = apparmor.get("before", {})
        after = apparmor.get("after", {})
        interrupted_vda = target == "vda"
        expected_client_result = (
            commit.get("returncode") == 1 and
            "disconnected from qemu:///system" in commit.get("stderr", "").lower()
        ) if interrupted_vda else (
            commit.get("returncode") == 0 and
            "commit complete" in commit.get("stdout", "").lower()
        )
        if (argv[:3] != ["blockcommit", case.get("domain"), target] or
                "--delete" in argv or "--active" in argv or
                "--top" not in argv or "--base" not in argv or
                not expected_client_result or
                commit.get("jobs_after", {}).get("clear") is not True):
            raise RuntimeError(f"{target} public commit did not reconcile as expected without data deletion")
        for observation in (before, after):
            if (observation.get("pid", 0) < 2 or observation.get("domain_uuid") != domain_uuid or
                    observation.get("context") != f"libvirt-{domain_uuid} (enforce)"):
                raise RuntimeError(f"{target} commit lacks bound enforcing QEMU identity evidence")
        if before.get("pid") != after.get("pid"):
            raise RuntimeError(f"QEMU identity changed across {target} public commit")

    boundary = case.get("midcommit_boundary", {})
    qemu_pid = boundary.get("qemu_pid")
    daemon_before = boundary.get("daemon_before", {}).get("pid")
    daemon_after = boundary.get("daemon_after", {}).get("pid")
    qmp_before = boundary.get("qmp_block_jobs", {})
    qmp_final = boundary.get("boundary", {}).get("final_pre_kill", {}).get("qmp_block_jobs", {})
    apparmor_before = boundary.get("apparmor_before_restart", {})
    apparmor_after = boundary.get("apparmor_after_restart", {})

    def is_midcommit_job(observation):
        jobs = observation.get("return")
        if not observation.get("ok") or not isinstance(jobs, list) or len(jobs) != 1:
            return False
        job = jobs[0]
        return (job.get("type") == "commit" and "vda" in job.get("device", "") and
                job.get("ready") is False and 0 < job.get("offset", 0) < job.get("len", 0))

    if (boundary.get("command") != case["commits"]["vda"].get("argv") or
            boundary.get("domain_uuid") != domain_uuid or
            not isinstance(qemu_pid, int) or qemu_pid < 2 or
            case.get("qemu_pid_before") != qemu_pid or daemon_before is None or
            daemon_after is None or daemon_before == daemon_after or
            not is_midcommit_job(qmp_before) or not is_midcommit_job(qmp_final) or
            qmp_before["return"][0].get("device") != qmp_final["return"][0].get("device") or
            qmp_final["return"][0].get("offset", 0) < qmp_before["return"][0].get("offset", 0) or
            boundary.get("boundary", {}).get("final_pre_kill", {}).get("qemu_pid") != qemu_pid or
            boundary.get("boundary", {}).get("final_pre_kill", {}).get("domain_state") != "paused" or
            case["commits"]["vda"].get("qemu_pid_before") != qemu_pid or
            case["commits"]["vda"].get("qemu_pid_after") != qemu_pid or
            case["commits"]["vda"].get("daemon_before", {}).get("pid") != daemon_before or
            case["commits"]["vda"].get("daemon_after", {}).get("pid") != daemon_after or
            apparmor_before.get("pid") != qemu_pid or apparmor_after.get("pid") != qemu_pid or
            apparmor_before.get("domain_uuid") != domain_uuid or
            apparmor_after.get("domain_uuid") != domain_uuid or
            apparmor_before.get("context") != f"libvirt-{domain_uuid} (enforce)" or
            apparmor_after.get("context") != apparmor_before.get("context")):
        raise RuntimeError("vda interruption lacks one nonterminal confined QEMU commit across restart")
    if not case["commits"]["vda"].get("observations"):
        raise RuntimeError("vda native job has no post-restart reconciliation observations")

    redefine = case.get("point_b_redefine", {})
    docs = redefine.get("snapshot_docs", {})
    if (not redefine.get("xml") or docs.get("labels") != ["point-a", "point-b", "point-c"] or
            docs.get("current") != "point-c"):
        raise RuntimeError("public consolidation lacks the retained point-B metadata redefine")
    metadata = case.get("metadata_delete", {})
    if (metadata.get("returncode") != 0 or metadata.get("argv", [])[-2:] != ["point-a", "--metadata"]):
        raise RuntimeError("oldest-point removal did not use metadata-only deletion")
    reference_scan = case.get("pre_unlink_reference_scan", {})
    if (reference_scan.get("passed") is not True or len(reference_scan.get("paths", [])) != 2 or
            len(reference_scan.get("sources_scanned", [])) < 4):
        raise RuntimeError("point-A files were not guarded by a complete zero-reference scan")
    final_stopped = case.get("final_stopped", {})
    unlinked_paths = {item.get("path") for item in final_stopped.get("unlinked_a", [])}
    reference_paths = set(reference_scan.get("paths", []))
    if (len(final_stopped.get("unlinked_a", [])) != 2 or
            len(reference_paths) != 2 or unlinked_paths != reference_paths or
            any(item.get("absent_after_unlink") is not True
                for item in final_stopped.get("unlinked_a", []))):
        raise RuntimeError("public consolidation did not unlink exactly the reference-checked point-A files")
    for field in ("inactive_xml", "snapshot_docs", "chains", "current_chains"):
        if not final_stopped.get(field):
            raise RuntimeError(f"final stopped-state evidence lacks {field}")
    scanned_state = {field: final_stopped[field]
                     for field in ("inactive_xml", "snapshot_docs", "chains", "current_chains")}
    if any(path in json.dumps(scanned_state) for path in reference_scan["paths"]):
        raise RuntimeError("final stopped domain or backing chains still name point A")

    targets = {"vda", "vdb"}
    data = case.get("final_data", {})
    expected_rows = {"point-b-saved", "point-b-post", "point-c-saved", "point-c-post", "current"}
    if set(data) != targets or any(set(rows) != expected_rows for rows in data.values()):
        raise RuntimeError("public consolidation did not record every retained data extent")
    if any(row.get("ok") is not True for rows in data.values() for row in rows.values()):
        raise RuntimeError("public consolidation lost a retained marker or payload")
    reopened = case.get("reopen", {})
    required_qmp = {"query-block", "query-named-block-nodes", "query-block-jobs", "query-jobs"}
    qmp = reopened.get("qmp", {})
    if (reopened.get("state") not in {"running", "paused"} or
            reopened.get("uuid") != domain_uuid or set(qmp) != required_qmp or
            any(item.get("ok") is not True for item in qmp.values())):
        raise RuntimeError("public consolidated domain did not reopen with readable QMP graphs")
    reverts = case.get("reverts", [])
    if [item.get("label") for item in reverts] != ["point-c", "point-b"]:
        raise RuntimeError("public consolidation lacks retained C/B native revert checks")
    if any(set(item.get("reads", {})) != targets or
           any(read.get("ok") is not True for read in item.get("reads", {}).values())
           for item in reverts):
        raise RuntimeError("a retained native revert lost marker/payload data")


DEEP32_SCOPE = "paused BIOS external qcow2 32-point two-disk public blockcommit; no --delete"
DEEP32_FOREIGN_GUARD = '''\
def require_stopped_foreign_domain(domain, state):
    normalized = state.strip().lower()
    if normalized != "shut off":
        raise RuntimeError(f"foreign domain {domain} is not shut off; refusing A-top unlink")
    return normalized
'''


def expected_redefined_parent_graph(labels: list[str]) -> dict[str, str | None]:
    return {label: (None if index < 2 else labels[index - 1])
            for index, label in enumerate(labels)}


def emit_deep32_status(payload: dict) -> None:
    print(json.dumps(payload), flush=True)


def deep32_data_by_target(targets: list[str], saved: dict, post: dict,
                          current: dict) -> dict:
    return {target: {"saved": saved[target], "post": post[target],
                     "current": current[target]} for target in targets}


def retained_point_reads(labels: list[str], targets: list[str], point_paths: dict,
                         point_payloads: dict) -> dict:
    return {
        target: {
            label: read_state(point_paths[label][target], 0x20 + index,
                              point_payloads[target][label])
            for index, label in enumerate(labels[1:], start=1)
        }
        for target in targets
    }


def snapshot_source_paths(snapshot_xml: dict, labels: list[str], targets: list[str]) -> dict:
    import xml.etree.ElementTree as ElementTree

    output = {}
    for label in labels:
        root = ElementTree.fromstring(snapshot_xml[label])
        paths = {}
        for disk in root.findall("./domain/devices/disk[@device='disk']"):
            target = disk.find("target").get("dev")
            source = disk.find("source")
            paths[target] = source.get("file") if source is not None else None
        if set(paths) != set(targets) or any(not path for path in paths.values()):
            raise RuntimeError(f"snapshot {label} does not expose both saved disk sources")
        output[label] = paths
    return output


def retained_saved_point_reads(labels: list[str], targets: list[str], saved_paths: dict,
                               point_payloads: dict) -> dict:
    return {
        target: {
            label: read_state(saved_paths[label][target], 0x20 + index - 1,
                              point_payloads[target][labels[index - 1]])
            for index, label in enumerate(labels[1:], start=1)
        }
        for target in targets
    }

DEEP32_REMOTE = r'''
import os
import xml.etree.ElementTree as ET

DEEP_CONFIG = CONFIG
ROOT = Path(DEEP_CONFIG["image_root"])
IMAGE_ROOT = ROOT
URI = "qemu:///system"
DOMAIN_TYPE = "qemu"
REMOTE_CONFIG = {"require_apparmor": True}
SENTINEL = DEEP_CONFIG["sentinel"]
name = DEEP_CONFIG["domain"]
work = Path(DEEP_CONFIG["work"])
targets = ["vda", "vdb"]
labels = [f"point-{index:02d}" for index in range(32)]
D = {target: str(work / f"disk-{index}.qcow2") for index, target in enumerate(targets)}
point_paths = {label: {target: str(work / f"{label}-{index}.qcow2")
                       for index, target in enumerate(targets)}
               for label in labels}
oldest_post_paths = point_paths[labels[0]]
REPORT = work / "result.json"
result = {"domain": name, "work": str(work), "case": "public-no-delete-deep32",
          "point_count": 32, "labels": labels, "status": "running",
          "base_paths": D, "point_paths": point_paths,
          "oldest_post_paths": oldest_post_paths}


def save():
    REPORT.write_text(json.dumps(result, indent=2) + "\n")


def image_sources(xml):
    root = ET.fromstring(xml)
    return [node.get("file") for node in root.iter()
            if node.tag.rsplit("}", 1)[-1] == "source" and node.get("file")]


def image_chain(path):
    images = chain(path)
    return [image.get("filename") for image in images]


def write_payload(path, value, index):
    offset = (8 + index) << 20
    run("qemu-io", "-f", "qcow2", "-c",
        f"write -P 0x{value:02x} {offset} {128 << 10}", path)
    return {"offset": offset, "length": 128 << 10, "pattern": value}


def read_state(path, marker, payload):
    reply = run("qemu-io", "-f", "qcow2",
                "-c", f"read -P 0x{marker:02x} 4096 4096",
                "-c", f"read -P 0x{payload['pattern']:02x} {payload['offset']} {payload['length']}",
                path, check=False, timeout=180)
    return {"marker_ok": reply.returncode == 0 and "read 4096/4096 bytes" in reply.stdout,
            "payload_ok": reply.returncode == 0 and "read 131072/131072 bytes" in reply.stdout,
            "returncode": reply.returncode, "stdout": reply.stdout, "stderr": reply.stderr,
            "marker": marker, "payload": payload}


def qmp(command):
    reply = virsh("qemu-monitor-command", name, "--pretty",
                  json.dumps({"execute": command}), check=False)
    try:
        parsed = json.loads(reply.stdout)
    except json.JSONDecodeError:
        return {"ok": False, "returncode": reply.returncode,
                "stdout": reply.stdout, "stderr": reply.stderr}
    return {"ok": reply.returncode == 0 and "return" in parsed,
            "return": parsed.get("return"), "returncode": reply.returncode,
            "stdout": reply.stdout, "stderr": reply.stderr}


def qmp_image_depths(observation, current_heads, minimum_depth=33):
    if not observation.get("ok") or not isinstance(observation.get("return"), list):
        raise RuntimeError("libvirt could not parse the deep QMP query-block reply")
    depths = {}
    for row in observation["return"]:
        image = row.get("inserted", {}).get("image")
        if not isinstance(image, dict):
            continue
        head = image.get("filename")
        if head not in current_heads.values():
            continue
        depth, cursor = 1, image
        while isinstance(cursor.get("backing-image"), dict):
            depth += 1
            cursor = cursor["backing-image"]
        for target, path in current_heads.items():
            if path == head:
                depths[target] = depth
    if set(depths) != set(targets) or any(depth < minimum_depth for depth in depths.values()):
        raise RuntimeError(f"QMP query-block did not return both {minimum_depth}-image chains: {depths}")
    return depths


def qemu_context(expected_uuid):
    pid = int((Path("/run/libvirt/qemu") / f"{name}.pid").read_text().strip())
    context = Path(f"/proc/{pid}/attr/current").read_text().strip()
    expected = f"libvirt-{expected_uuid} (enforce)"
    if context != expected or not os.path.basename(os.readlink(f"/proc/{pid}/exe")).startswith("qemu"):
        raise RuntimeError("deep32 QEMU process is outside its enforcing domain profile")
    return {"pid": pid, "domain_uuid": expected_uuid, "context": context}


def jobs_clear(target):
    blockjob = virsh("blockjob", name, target, "--info", check=False)
    text = (blockjob.stdout + "\n" + blockjob.stderr).lower()
    block = qmp("query-block-jobs")
    jobs = qmp("query-jobs")
    clear = (blockjob.returncode == 0 and "no current block job" in text and
             block.get("ok") and block.get("return") == [] and
             jobs.get("ok") and jobs.get("return") == [])
    return {"clear": clear, "blockjob": {"returncode": blockjob.returncode,
                                           "stdout": blockjob.stdout,
                                           "stderr": blockjob.stderr},
            "qmp_block_jobs": block, "qmp_jobs": jobs}


def snapshot_documents():
    found = [row.strip() for row in virsh("snapshot-list", name, "--name").stdout.splitlines()
             if row.strip()]
    docs = {label: virsh("snapshot-dumpxml", name, label).stdout for label in found}
    return {"labels": found, "current": virsh("snapshot-current", name, "--name").stdout.strip(),
            "xml": docs}


def qemu_profile_check():
    domain_uuid = virsh("domuuid", name).stdout.strip()
    return qemu_context(domain_uuid)


try:
    if (work.parent != ROOT or work.is_symlink() or ROOT.is_symlink() or
            ROOT.resolve() != ROOT or (work / "sentinel").read_text() != SENTINEL or
            list(work.iterdir()) != [work / "sentinel"]):
        raise RuntimeError("deep32 fixture path or sentinel did not match its exact disposable selection")
    result["package_check"] = verify_candidate()
    result["candidate"] = DEEP_CONFIG["candidate"]
    for target in targets:
        run("qemu-img", "create", "-f", "qcow2", D[target], "128M")
        os.chmod(D[target], 0o600)
    xml = ["<domain type='qemu'>", f"<name>{name}</name>",
           "<memory unit='MiB'>256</memory><currentMemory unit='MiB'>256</currentMemory>",
           "<vcpu>1</vcpu><os><type arch='x86_64' machine='pc'>hvm</type></os>",
           "<features><acpi/><apic/></features><devices>",
           "<emulator>/usr/bin/qemu-system-x86_64</emulator>"]
    for target in targets:
        xml.append(f"<disk type='file' device='disk'><driver name='qemu' type='qcow2' cache='none'/><source file={quoteattr(D[target])}/><target dev='{target}' bus='virtio'/></disk>")
    xml.append("</devices></domain>")
    definition = work / "domain.xml"
    definition.write_text("".join(xml))
    virsh("define", definition)
    domain_uuid = virsh("domuuid", name).stdout.strip()
    ownership = start_stop_for_libvirt_ownership(name, targets, require_apparmor=True)
    result["uuid"] = domain_uuid
    result["ownership"] = ownership
    base_payload = {}
    for target in targets:
        qemu_io(D[target], "write", 0x10)
        base_payload[target] = write_payload(D[target], 0x50, 0)
    point_payloads = {}
    for index, label in enumerate(labels):
        created = snapshot(name, label, targets)
        for target in targets:
            heads = source_paths(name, targets)
            marker = 0x20 + index
            qemu_io(heads[target], "write", marker)
            point_payloads.setdefault(target, {})[label] = write_payload(
                heads[target], 0x60 + index, index)
        if index in (0, 15, 31):
            result.setdefault("chain_depth_checkpoints", {})[label] = created
    docs = snapshot_documents()
    if docs["labels"] != labels or docs["current"] != labels[-1]:
        raise RuntimeError("deep32 snapshot labels/current differ from the intended sequential chain")
    parents = {label: ET.fromstring(raw).findtext("./parent/name") for label, raw in docs["xml"].items()}
    expected_parents = {label: (labels[index - 1] if index else None)
                        for index, label in enumerate(labels)}
    if parents != expected_parents:
        raise RuntimeError("deep32 snapshot parent graph is not a single 32-point chain")
    initial_heads = source_paths(name, targets)
    initial_chains = {target: {path: image_chain(path) for path in
                               [D[target], *[point_paths[label][target] for label in labels]]}
                      for target in targets}
    for target in targets:
        expected_path = [D[target]]
        if initial_chains[target][D[target]] != expected_path:
            raise RuntimeError("deep32 base disk is not standalone")
        for index, label in enumerate(labels):
            expected_path = [point_paths[label][target], *expected_path]
            if initial_chains[target][point_paths[label][target]] != expected_path:
                raise RuntimeError(f"initial backing chain differs at {target}/{label}")
    result["initial"] = {"uuid": domain_uuid, "snapshot_docs": docs,
                         "heads": initial_heads, "chains": initial_chains}
    virsh("start", name)
    if virsh("domstate", name).stdout.strip().lower() == "running":
        virsh("suspend", name)
    if virsh("domstate", name).stdout.strip().lower() != "paused":
        raise RuntimeError("deep32 QEMU did not pause before QMP and blockcommit")
    deep_qmp = qmp("query-block")
    depth = qmp_image_depths(deep_qmp, initial_heads)
    result["initial"]["qmp_query_block"] = deep_qmp
    result["initial"]["qmp_query_block_depth"] = depth
    result["paused_qemu"] = qemu_profile_check()
    result["commits"] = {}
    for target in targets:
        command = ["blockcommit", name, target, "--top", oldest_post_paths[target],
                   "--base", D[target], "--wait", "--verbose"]
        before = qemu_profile_check()
        reply = virsh(*command, check=False, timeout=1800)
        after = qemu_profile_check()
        jobs = jobs_clear(target)
        if (reply.returncode != 0 or "commit complete" not in reply.stdout.lower() or
                not jobs["clear"] or before != after or "--delete" in command):
            raise RuntimeError(f"public no-delete commit did not finish safely for {target}")
        result["commits"][target] = {"argv": command, "returncode": reply.returncode,
                                     "stdout": reply.stdout, "stderr": reply.stderr,
                                     "jobs_after": jobs,
                                     "apparmor_during_commit": {"before": before, "after": after}}
        save()
    live_qmp_after_commit = qmp("query-block")
    if not live_qmp_after_commit.get("ok") or any(path in json.dumps(live_qmp_after_commit)
                                                   for path in oldest_post_paths.values()):
        raise RuntimeError("live QMP block graph still references the oldest post-point")
    result["live_qmp_after_commits"] = live_qmp_after_commit
    if virsh("domstate", name).stdout.strip().lower() != "paused":
        raise RuntimeError("deep32 QEMU left paused state after public commits")
    virsh("destroy", name)
    if virsh("domstate", name).stdout.strip().lower() != "shut off":
        raise RuntimeError("deep32 QEMU did not stop before metadata work")
    docs = snapshot_documents()
    before_metadata = {"snapshot_docs": docs,
                       "inactive_xml": virsh("dumpxml", name, "--inactive").stdout,
                       "chains": {target: {path: image_chain(path) for path in
                                           [D[target], *[point_paths[label][target] for label in labels]]}
                                  for target in targets}}
    result["post_commit_stopped"] = before_metadata
    child = labels[1]
    root = ET.fromstring(docs["xml"][child])
    parent = root.find("parent")
    if parent is None or parent.findtext("name") != labels[0]:
        raise RuntimeError("oldest retained child metadata does not name point-00 parent")
    root.remove(parent)
    rewritten = {}
    for disk in root.findall("./domain/devices/disk[@device='disk']"):
        target = disk.find("target").get("dev")
        source = disk.find("source")
        if target not in targets or source is None or source.get("file") != oldest_post_paths[target]:
            raise RuntimeError("point-01 saved source does not exactly name the oldest point overlay")
        if disk.find("backingStore") is not None:
            raise RuntimeError("point-01 saved source contains unsupported explicit backing metadata")
        source.set("file", D[target])
        rewritten[target] = D[target]
    if set(rewritten) != set(targets):
        raise RuntimeError("point-01 metadata did not expose both oldest saved sources")
    redefine_path = work / "point-01-redefined.xml"
    redefine_path.write_text(ET.tostring(root, encoding="unicode") + "\n")
    redef = virsh("snapshot-create", name, redefine_path, "--redefine", check=False)
    if redef.returncode:
        raise RuntimeError(f"point-01 metadata redefine failed: {redef.stderr.strip()}")
    docs = snapshot_documents()
    for label in labels[1:]:
        if any(path in docs["xml"][label] for path in oldest_post_paths.values()):
            raise RuntimeError(f"retained metadata {label} still names an oldest point file")
    parent_after = {label: ET.fromstring(raw).findtext("./parent/name")
                    for label, raw in docs["xml"].items()}
    expected_after = expected_redefined_parent_graph(labels)
    if docs["labels"] != labels or parent_after != expected_after:
        raise RuntimeError("point-01 redefine did not preserve the remaining parent chain")
    result["point_01_redefine"] = {"xml": ET.tostring(root, encoding="unicode"),
                                   "rewritten_sources": rewritten, "base_paths": D,
                                   "removed_parent": labels[0], "snapshot_docs": docs,
                                   "parent_graph": parent_after}
    remove = virsh("snapshot-delete", name, labels[0], "--metadata", check=False)
    if remove.returncode:
        raise RuntimeError(f"metadata-only oldest point removal failed: {remove.stderr.strip()}")
    result["metadata_delete"] = {"argv": ["snapshot-delete", name, labels[0], "--metadata"],
                                 "returncode": remove.returncode, "stdout": remove.stdout,
                                 "stderr": remove.stderr}
    docs = snapshot_documents()
    expected_retained = labels[1:]
    if docs["labels"] != expected_retained or docs["current"] != labels[-1]:
        raise RuntimeError("metadata-only delete changed the retained 31-point graph")
    domain_xml = virsh("dumpxml", name, "--inactive").stdout
    domain_sources = image_sources(domain_xml)
    scanned = [{"owner": "inactive-domain", "source": source} for source in domain_sources]
    all_sources = list(domain_sources)
    for label, raw in docs["xml"].items():
        sources = image_sources(raw)
        scanned.extend({"owner": f"snapshot-{label}", "source": source} for source in sources)
        all_sources.extend(sources)
    names = [row.strip() for row in virsh("list", "--all", "--name").stdout.splitlines() if row.strip()]
    foreign = []
    for domain in names:
        if domain == name:
            continue
        state = virsh("domstate", domain).stdout.strip().lower()
        domain_state = require_stopped_foreign_domain(domain, state)
        definitions = {}
        foreign_sources = []
        for xml_kind, args in (("default", ("dumpxml", domain)),
                               ("inactive", ("dumpxml", domain, "--inactive"))):
            raw = virsh(*args).stdout
            sources = image_sources(raw)
            if any(path in oldest_post_paths.values() for path in sources):
                raise RuntimeError("a foreign domain XML refers to an oldest point file")
            definitions[xml_kind] = sources
            foreign_sources.extend(sources)
            all_sources.extend(sources)
        foreign_labels = [row.strip() for row in virsh("snapshot-list", domain, "--name").stdout.splitlines()
                          if row.strip()]
        snapshots = {}
        for label in foreign_labels:
            raw = virsh("snapshot-dumpxml", domain, label).stdout
            sources = image_sources(raw)
            if any(path in oldest_post_paths.values() for path in sources):
                raise RuntimeError("a foreign snapshot XML refers to an oldest point file")
            snapshots[label] = sources
            foreign_sources.extend(sources)
            all_sources.extend(sources)
        foreign.append({"domain": domain, "state": domain_state, "definitions": definitions,
                        "snapshots": snapshots, "sources": foreign_sources})
    chain_rows = []
    for source in sorted(set(all_sources)):
        if not Path(source).is_file():
            raise RuntimeError(f"reference scan cannot inspect source {source}")
        images = image_chain(source)
        chain_rows.append({"source": source, "images": images})
        if any(path in images for path in oldest_post_paths.values()):
            raise RuntimeError(f"a retained backing chain still refers to an oldest point file: {source}")
    reference_scan = {"passed": True,
                      "paths": sorted(oldest_post_paths.values()),
                      "sources_scanned": scanned,
                      "backing_chains_scanned": chain_rows,
                      "foreign_domains": foreign,
                      "live_qmp_after_commit": live_qmp_after_commit}
    result["pre_unlink_reference_scan"] = reference_scan
    unlinked = []
    saved_paths = snapshot_source_paths(docs["xml"], expected_retained, targets)
    saved_data = retained_saved_point_reads(labels, targets, saved_paths, point_payloads)
    post_data = retained_point_reads(labels, targets, point_paths, point_payloads)
    current_paths = source_paths(name, targets)
    current_data = {}
    for target in targets:
        if current_paths[target] != point_paths[labels[-1]][target]:
            raise RuntimeError(f"current source differs from the final post head for {target}")
        current_data[target] = read_state(current_paths[target], 0x3F,
                                          point_payloads[target][labels[-1]])
    final_data = deep32_data_by_target(targets, saved_data, post_data, current_data)
    result["current_heads_before_unlink"] = current_paths
    result["retained_data_before_unlink"] = final_data
    save()
    data_rows = [row for target in targets
                 for row in (*final_data[target]["saved"].values(),
                             *final_data[target]["post"].values(),
                             final_data[target]["current"])]
    if any(not row["marker_ok"] or not row["payload_ok"] for row in data_rows):
        raise RuntimeError("a retained 32-point marker or payload failed before oldest-file unlink")
    final_chains = {}
    for target in targets:
        paths = [D[target], *[point_paths[label][target] for label in expected_retained]]
        observed = {path: image_chain(path) for path in paths}
        expected = {D[target]: [D[target]]}
        prefix = []
        for label in expected_retained:
            prefix.insert(0, point_paths[label][target])
            expected[prefix[0]] = [*prefix, D[target]]
        if observed != expected:
            raise RuntimeError(f"retained 31-point chains differ before oldest unlink for {target}")
        final_chains[target] = observed
    result["retained_chains_before_unlink"] = final_chains
    save()
    for path in oldest_post_paths.values():
        candidate = Path(path)
        if (candidate.parent != work or candidate.is_symlink() or not candidate.is_file() or
                candidate.stat().st_nlink != 1):
            raise RuntimeError("oldest point file failed exact regular-file/inode checks")
        candidate.unlink()
        unlinked.append({"path": path, "absent_after_unlink": not candidate.exists()})
    if any(not item["absent_after_unlink"] for item in unlinked) or len(unlinked) != 2:
        raise RuntimeError("oldest point files were not both unlinked after zero-reference proof")
    result["final_stopped"] = {"state": virsh("domstate", name).stdout.strip().lower(),
                               "snapshot_docs": docs, "inactive_xml": domain_xml,
                               "chains": final_chains, "data": final_data,
                               "unlinked_oldest": unlinked}
    save()
    virsh("start", name)
    reopened_uuid = virsh("domuuid", name).stdout.strip()
    reopened_state = virsh("domstate", name).stdout.strip().lower()
    reopened_qmp = {command: qmp(command) for command in
                    ("query-block", "query-named-block-nodes", "query-block-jobs", "query-jobs")}
    reopened_depth = qmp_image_depths(reopened_qmp["query-block"], source_paths(name, targets),
                                      minimum_depth=32)
    reopened_profile = qemu_profile_check()
    if (reopened_uuid != domain_uuid or reopened_state not in ("running", "paused") or
            any(not observation.get("ok") for observation in reopened_qmp.values()) or
            reopened_profile["domain_uuid"] != domain_uuid):
        raise RuntimeError("same-UUID deep32 domain failed QMP/AppArmor reopen verification")
    result["reopen"] = {"uuid": reopened_uuid, "state": reopened_state,
                         "qmp": reopened_qmp, "qmp_query_block_depth": reopened_depth,
                         "apparmor": reopened_profile}
    virsh("destroy", name)
    if virsh("domstate", name).stdout.strip().lower() != "shut off":
        raise RuntimeError("deep32 reopened domain did not stop cleanly")
    virsh("undefine", name, "--snapshots-metadata")
    result["status"] = "passed"
    save()
    emit_deep32_status({"status": "passed", "case": result["case"],
                        "uuid": domain_uuid, "point_count": 32})
except Exception as exc:
    result["status"] = "failed-preserved"
    result["error"] = str(exc)
    try:
        if "domain_uuid" in locals():
            result["failure_state"] = {"uuid": virsh("domuuid", name, check=False).stdout.strip(),
                                       "state": virsh("domstate", name, check=False).stdout.strip(),
                                       "snapshot_docs": snapshot_documents()}
    except Exception as observe_error:
        result["failure_observation_error"] = str(observe_error)
    save()
    emit_deep32_status({"status": result["status"], "error": result["error"]})
    raise
'''


def check_deep32_result(path: Path, expected_candidate: dict) -> None:
    if not path.is_file():
        raise RuntimeError("system guest did not export 32-point evidence/result.json")
    report = json.loads(path.read_text())
    if (report.get("exit_code") != 0 or report.get("uri") != "qemu:///system" or
            report.get("domain_type") != "qemu" or report.get("scope") != DEEP32_SCOPE or
            report.get("apparmor", {}).get("kernel_enabled") != "Y" or
            report.get("candidate") != expected_candidate):
        raise RuntimeError("32-point system evidence is incomplete or not bound to the selected candidate")
    expected_versions = {package: SOURCE_VERSION for package in SYSTEM_CANDIDATE_PACKAGES}
    observed_versions = {}
    for package_line in report.get("packages", []):
        package, version = package_line.split(maxsplit=1)
        observed_versions[package.split(":", 1)[0]] = version
    if any(observed_versions.get(package) != version for package, version in expected_versions.items()):
        raise RuntimeError("32-point evidence does not show the exact candidate package closure")
    case = report.get("case", {})
    labels = [f"point-{index:02d}" for index in range(32)]
    domain_uuid = case.get("uuid")
    if (case.get("case") != "public-no-delete-deep32" or case.get("status") != "passed" or
            case.get("point_count") != 32 or case.get("labels") != labels or
            not isinstance(domain_uuid, str) or not domain_uuid):
        raise RuntimeError("32-point public-consolidation lifecycle did not pass its exact point count")
    targets = {"vda", "vdb"}
    initial = case.get("initial", {})
    if (initial.get("uuid") != domain_uuid or initial.get("snapshot_docs", {}).get("labels") != labels or
            initial.get("snapshot_docs", {}).get("current") != labels[-1]):
        raise RuntimeError("32-point fixture baseline is incomplete or has a different domain identity")
    depth = initial.get("qmp_query_block_depth", {})
    if set(depth) != targets or any(not isinstance(value, int) or value < 33 for value in depth.values()):
        raise RuntimeError("pre-commit QMP query-block did not expose both complete deep backing chains")
    _check_recorded_qmp_depths(initial.get("qmp_query_block", {}),
                               {target: case["point_paths"][labels[-1]][target]
                                for target in targets}, depth)
    if set(case.get("commits", {})) != targets:
        raise RuntimeError("32-point public path did not record both disk commits")
    for target, commit in case["commits"].items():
        argv = commit.get("argv", [])
        profile = f"libvirt-{domain_uuid} (enforce)"
        before = commit.get("apparmor_during_commit", {}).get("before", {})
        after = commit.get("apparmor_during_commit", {}).get("after", {})
        if (argv[:3] != ["blockcommit", case.get("domain"), target] or
                "--delete" in argv or "--active" in argv or "--top" not in argv or "--base" not in argv or
                commit.get("returncode") != 0 or "commit complete" not in commit.get("stdout", "").lower() or
                commit.get("jobs_after", {}).get("clear") is not True or
                before.get("domain_uuid") != domain_uuid or after.get("domain_uuid") != domain_uuid or
                before.get("context") != profile or after.get("context") != profile or
                before.get("pid", 0) < 2 or before.get("pid") != after.get("pid")):
            raise RuntimeError(f"{target} 32-point public commit lacks completion or enforcing-QEMU evidence")
    redefine = case.get("point_01_redefine", {})
    redefine_parents = redefine.get("parent_graph", {})
    expected_redefine_parents = expected_redefined_parent_graph(labels)
    if (redefine.get("rewritten_sources") != {target: redefine.get("base_paths", {}).get(target)
                                                for target in targets} or
            redefine.get("removed_parent") != labels[0] or
            redefine.get("snapshot_docs", {}).get("labels") != labels or
            redefine.get("snapshot_docs", {}).get("current") != labels[-1] or
            redefine_parents != expected_redefine_parents):
        raise RuntimeError("oldest-child snapshot metadata was not explicitly redefined to the base")
    metadata = case.get("metadata_delete", {})
    if (metadata.get("returncode") != 0 or
            metadata.get("argv", [])[-2:] != [labels[0], "--metadata"]):
        raise RuntimeError("oldest point was not removed with metadata-only deletion")
    reference_scan = case.get("pre_unlink_reference_scan", {})
    a_paths = case.get("oldest_post_paths", {})
    if (reference_scan.get("passed") is not True or set(a_paths) != targets or
            set(reference_scan.get("paths", [])) != set(a_paths.values()) or
            len(reference_scan.get("sources_scanned", [])) < 4):
        raise RuntimeError("oldest-point unlink lacks a complete zero-reference scan")
    expected_scan_owners = {"inactive-domain", *(f"snapshot-{label}" for label in labels[1:])}
    if not expected_scan_owners.issubset({row.get("owner") for row in reference_scan["sources_scanned"]}):
        raise RuntimeError("reference scan omitted an inactive or retained snapshot source")
    foreign = reference_scan.get("foreign_domains", [])
    for row in foreign:
        if row.get("state") != "shut off":
            raise RuntimeError("reference scan included a foreign domain that was not shut off")
        if set(row.get("definitions", {})) != {"default", "inactive"} or not isinstance(
                row.get("snapshots"), dict):
            raise RuntimeError("reference scan omitted a foreign domain definition or snapshot inventory")
        if any(old_path in row.get("sources", []) for old_path in a_paths.values()):
            raise RuntimeError("a foreign domain or snapshot XML still refers to an oldest point")
    final = case.get("final_stopped", {})
    expected_retained = labels[1:]
    if (final.get("state") != "shut off" or final.get("snapshot_docs", {}).get("labels") != expected_retained or
            final.get("snapshot_docs", {}).get("current") != labels[-1] or
            set(final.get("chains", {})) != targets or set(final.get("data", {})) != targets):
        raise RuntimeError("post-delete stopped state lacks all retained points, chains, and data")
    if (case.get("retained_data_before_unlink") != final.get("data") or
            case.get("retained_chains_before_unlink") != final.get("chains")):
        raise RuntimeError("retained data and chains were not persisted before oldest-file unlink")
    if any(path in json.dumps(final.get("snapshot_docs", {})) + json.dumps(final.get("chains", {}))
           for path in a_paths.values()):
        raise RuntimeError("oldest point remains in retained snapshot metadata or backing chains")
    expected_parents = {label: (None if index == 1 else labels[index - 1])
                        for index, label in enumerate(labels) if index > 0}
    observed_parents = {
        label: ElementTree.fromstring(raw).findtext("./parent/name")
        for label, raw in final["snapshot_docs"].get("xml", {}).items()
    }
    if observed_parents != expected_parents:
        raise RuntimeError("final metadata does not retain the exact 31-point parent chain")
    base_paths = case.get("base_paths", {})
    point_paths = case.get("point_paths", {})
    if set(base_paths) != targets or set(point_paths) != set(labels):
        raise RuntimeError("deep32 evidence omits the exact base or point path map")
    saved_paths = snapshot_source_paths(final["snapshot_docs"].get("xml", {}),
                                        expected_retained, sorted(targets))
    expected_heads = {target: point_paths[labels[-1]][target] for target in targets}
    if case.get("current_heads_before_unlink") != expected_heads:
        raise RuntimeError("current domain sources were not captured before oldest-file unlink")
    for target in targets:
        rows = final["data"][target]
        if (set(rows) != {"saved", "post", "current"} or
                set(rows["saved"]) != set(expected_retained) or
                set(rows["post"]) != set(expected_retained)):
            raise RuntimeError(f"retained marker or payload failed on {target}")
        if final["chains"][target].get(base_paths[target]) != [base_paths[target]]:
            raise RuntimeError(f"base chain changed on {target}")
        prefix = []
        for index, label in enumerate(expected_retained, start=1):
            point_path = point_paths[label][target]
            prefix.insert(0, point_path)
            if final["chains"][target].get(point_path) != [*prefix, base_paths[target]]:
                raise RuntimeError(f"retained backing chain differs at {target}/{label}")
            saved_source = (base_paths[target] if index == 1
                            else point_paths[labels[index - 1]][target])
            if saved_paths[label][target] != saved_source:
                raise RuntimeError(f"saved source mapping differs at {target}/{label}")
            saved = rows["saved"][label]
            saved_marker = 0x20 + index - 1
            saved_payload = {"pattern": 0x60 + index - 1,
                             "offset": (8 + index - 1) << 20, "length": 128 << 10}
            post = rows["post"][label]
            post_marker = 0x20 + index
            post_payload = {"pattern": 0x60 + index,
                            "offset": (8 + index) << 20, "length": 128 << 10}
            if (saved.get("marker_ok") is not True or saved.get("payload_ok") is not True or
                    saved.get("marker") != saved_marker or saved.get("payload") != saved_payload or
                    post.get("marker_ok") is not True or post.get("payload_ok") is not True or
                    post.get("marker") != post_marker or post.get("payload") != post_payload):
                raise RuntimeError(f"retained saved/post data expectation differs at {target}/{label}")
        current = rows["current"]
        if current.get("marker_ok") is not True or current.get("payload_ok") is not True:
            raise RuntimeError(f"current marker or payload failed on {target}")
        if current.get("marker") != 0x3F or current.get("payload") != {
                "pattern": 0x7F, "offset": 39 << 20, "length": 128 << 10}:
            raise RuntimeError(f"current point data expectation differs on {target}")
    unlinked = final.get("unlinked_oldest", [])
    if (len(unlinked) != 2 or {item.get("path") for item in unlinked} != set(a_paths.values()) or
            any(item.get("absent_after_unlink") is not True for item in unlinked)):
        raise RuntimeError("only the exact reference-checked oldest point files may be unlinked")
    reopened = case.get("reopen", {})
    required_qmp = {"query-block", "query-named-block-nodes", "query-block-jobs", "query-jobs"}
    if (reopened.get("uuid") != domain_uuid or reopened.get("state") not in {"running", "paused"} or
            set(reopened.get("qmp", {})) != required_qmp or
            any(not observation.get("ok") for observation in reopened["qmp"].values()) or
            reopened.get("apparmor", {}).get("context") != f"libvirt-{domain_uuid} (enforce)" or
            set(reopened.get("qmp_query_block_depth", {})) != targets or
            any(value < 32 for value in reopened["qmp_query_block_depth"].values())):
        raise RuntimeError("same-UUID 32-point consolidated domain did not reopen with QMP and AppArmor evidence")
    _check_recorded_qmp_depths(
        reopened["qmp"]["query-block"],
        {target: case["point_paths"][labels[-1]][target] for target in targets},
        reopened["qmp_query_block_depth"],
    )


def _check_recorded_qmp_depths(observation: dict, expected_heads: dict,
                               expected_depths: dict) -> None:
    if observation.get("ok") is not True or not isinstance(observation.get("return"), list):
        raise RuntimeError("raw QMP query-block observation is missing or invalid")
    found = {}
    for row in observation["return"]:
        image = row.get("inserted", {}).get("image")
        if not isinstance(image, dict):
            continue
        filename = image.get("filename")
        for target, expected_head in expected_heads.items():
            if filename != expected_head:
                continue
            depth, cursor = 1, image
            while isinstance(cursor.get("backing-image"), dict):
                depth += 1
                cursor = cursor["backing-image"]
            found[target] = depth
    if found != expected_depths:
        raise RuntimeError("raw QMP query-block chains do not match their recorded depths")


def check_branch_result(path: Path) -> None:
    if not path.is_file():
        raise RuntimeError("system guest did not export branch evidence/result.json")
    report = json.loads(path.read_text())
    cases = report.get("cases", [])
    if (report.get("exit_code") != 0 or report.get("uri") != "qemu:///system" or
            report.get("domain_type") != "qemu" or len(cases) != 1):
        raise RuntimeError("system-mode public branch evidence is incomplete or failed")
    case = cases[0]
    if (case.get("case") != "public-no-delete-branch" or case.get("status") != "passed" or
            case.get("mode") != "branch" or set(case.get("branch_commits", {})) != {"vda", "vdb"}):
        raise RuntimeError("public branch consolidation did not complete both disks")
    for target, commit in case["branch_commits"].items():
        argv = commit.get("argv", [])
        if (argv[:3] != ["blockcommit", case.get("domain"), target] or
                "--delete" in argv or "--active" in argv or commit.get("returncode") != 0 or
                "commit complete" not in commit.get("stdout", "").lower() or
                commit.get("jobs_after", {}).get("clear") is not True):
            raise RuntimeError(f"branch {target} commit did not finish through public no-delete blockcommit")
    deleted = case.get("branch_post_delete", {})
    if (deleted.get("references") != "zero in fixture metadata/XML/chains and foreign domain XML/chains" or
            len(deleted.get("unlinked_a_tops", [])) != 2 or
            [item.get("label") for item in case.get("branch_reopens", [])] != ["point-c", "point-d"]):
        raise RuntimeError("branch metadata deletion, reference scan, or reopen evidence is incomplete")


def host_run(candidate_dir: Path | None = None) -> int:
    workspace = Path.cwd().resolve()
    subprocess.run(["docker", "info"], check=True, stdout=subprocess.DEVNULL)
    print("Docker daemon is available", flush=True)
    run_root = workspace / ".cache" / "recovery-libvirt-ci" / uuid.uuid4().hex
    recipe = run_root / "recipe"
    output = run_root / "output"
    evidence = run_root / "evidence"
    for directory in (output, evidence):
        directory.mkdir(parents=True)
    if candidate_dir is None:
        recipe.mkdir()
        for name in ("build-matched-libvirt.sh", *PATCH_FILES):
            shutil.copy2(workspace / "packaging/libvirt" / name, recipe / name)
        shutil.copy2(workspace / "tests/integration/libvirt_readonly_policy.py",
                     recipe / "libvirt_readonly_policy.py")
        builder = f"nex-libvirt-ci-build-{uuid.uuid4().hex[:10]}"
        builder_id = None
        try:
            builder_id = create_container(builder)
            run(["docker", "exec", builder_id, "mkdir", "-p", "/recipe", "/output"])
            run(["docker", "cp", f"{recipe}/.", f"{builder_id}:/recipe/"])
            run(["docker", "exec", builder_id, "/recipe/build-matched-libvirt.sh"])
        finally:
            if builder_id:
                subprocess.run(["docker", "cp", f"{builder_id}:/output/.", str(output)], check=False)
                subprocess.run(["docker", "rm", "-f", builder_id], check=False)
        candidate = output
    else:
        candidate = candidate_dir.resolve()
        if not candidate.is_relative_to(workspace / ".cache"):
            raise RuntimeError("prebuilt candidate must be under the ignored workspace .cache directory")
    run(["sha256sum", "--check", "--quiet", "SHA256SUMS"], cwd=str(candidate))
    candidate_debs = check_packages(candidate, SYSTEM_CANDIDATE_PACKAGES)
    candidate_receipt = candidate_provenance(candidate, candidate_debs)
    print(f"Verified {len(candidate_debs)} matched native candidate packages at {SOURCE_VERSION}", flush=True)

    runtime = f"nex-libvirt-ci-runtime-{uuid.uuid4().hex[:10]}"
    runtime_id = None
    tests = workspace / "tests" / "integration"
    try:
        runtime_id = create_container(runtime)
        run(["docker", "exec", runtime_id, "mkdir", "-p", "/candidate", "/tests/integration", "/evidence"])
        for package in candidate_debs:
            run(["docker", "cp", str(package), f"{runtime_id}:/candidate/"])
        for name in ("libvirt_readonly_policy.py", "recovery_s0.py",
                     "recovery_s0_consolidate.py", "libvirt_recovery_ci.py"):
            run(["docker", "cp", str(tests / name), f"{runtime_id}:/tests/integration/"])
        try:
            run(["docker", "exec", runtime_id, "/bin/bash", "-euc", RUNTIME_INSTALL])
        finally:
            destination = evidence / "result.json"
            subprocess.run(["docker", "cp", f"{runtime_id}:/evidence/result.json", str(destination)],
                           check=False)
            apparmor_destination = evidence / "apparmor-system"
            apparmor_destination.mkdir(exist_ok=True)
            subprocess.run(["docker", "cp", f"{runtime_id}:/evidence/apparmor-system/.",
                            str(apparmor_destination)], check=False)
            if destination.is_file():
                outer_report = json.loads(destination.read_text())
                outer_report["candidate"] = candidate_receipt
                destination.write_text(json.dumps(outer_report, indent=2) + "\n")
            if sys.exc_info()[0] is None:
                check_runtime_result(destination)
                check_apparmor_result(apparmor_destination / "result.json")
    finally:
        if runtime_id:
            subprocess.run(["docker", "rm", "-f", runtime_id], check=False)
    print(f"CI evidence: {evidence / 'result.json'}", flush=True)
    return 0


RUNTIME_INSTALL = r"""cat >/etc/apt/sources.list <<'SOURCES'
deb http://snapshot.debian.org/archive/debian/20260920T000000Z/ trixie main
deb http://snapshot.debian.org/archive/debian/20260920T000000Z/ trixie-updates main
deb http://snapshot.debian.org/archive/debian-security/20260920T000000Z/ trixie-security main
SOURCES
rm -f /etc/apt/sources.list.d/*.sources /etc/apt/sources.list.d/*.list
cat >/etc/apt/apt.conf.d/99snapshot <<'APT'
Acquire::Check-Valid-Until "false";
APT
printf '#!/bin/sh\nexit 101\n' >/usr/sbin/policy-rc.d
chmod 0755 /usr/sbin/policy-rc.d
export DEBIAN_FRONTEND=noninteractive
install -d -m 0755 /apt-cache/partial
flock --exclusive --wait 5400 /apt-cache/recovery-libvirt-apt.lock \
  apt-get -o Dir::Cache::archives=/apt-cache -o Dir::Cache::archives::partial=/apt-cache/partial update
flock --exclusive --wait 5400 /apt-cache/recovery-libvirt-apt.lock \
  apt-get -o Dir::Cache::archives=/apt-cache -o Dir::Cache::archives::partial=/apt-cache/partial install -y --no-install-recommends \
  python3 qemu-system-x86 qemu-utils cloud-image-utils curl openssh-client \
  /candidate/libvirt-clients_11.3.0-3+deb13u3+nex4_amd64.deb \
  /candidate/libvirt-common_11.3.0-3+deb13u3+nex4_amd64.deb \
  /candidate/libvirt-daemon_11.3.0-3+deb13u3+nex4_amd64.deb \
  /candidate/libvirt-daemon-common_11.3.0-3+deb13u3+nex4_amd64.deb \
  /candidate/libvirt-daemon-driver-qemu_11.3.0-3+deb13u3+nex4_amd64.deb \
  /candidate/libvirt-daemon-log_11.3.0-3+deb13u3+nex4_amd64.deb \
  /candidate/libvirt0_11.3.0-3+deb13u3+nex4_amd64.deb
python3 /tests/integration/libvirt_readonly_policy.py --helper /usr/lib/libvirt/virt-aa-helper
python3 /tests/integration/libvirt_recovery_ci.py --runtime --evidence /evidence
"""


def runtime_run(evidence: Path) -> int:
    user = "nex-ci"
    if subprocess.run(["id", "-u", user], capture_output=True).returncode != 0:
        subprocess.run(["useradd", "--create-home", "--shell", "/bin/sh", user], check=True)
    uid = subprocess.run(["id", "-u", user], check=True, text=True,
                         capture_output=True).stdout.strip()
    runtime_dir = Path("/run/user") / uid
    runtime_dir.mkdir(parents=True, exist_ok=True)
    runtime_dir.chmod(0o700)
    subprocess.run(["chown", f"{user}:{user}", str(runtime_dir)], check=True)
    home = Path("/home") / user
    work = home / "recovery-s0-ci" / "work"
    work.mkdir(parents=True, exist_ok=True)
    evidence.mkdir(parents=True, exist_ok=True)
    subprocess.run(["chown", "-R", f"{user}:{user}", str(home / "recovery-s0-ci"), str(evidence)], check=True)
    subprocess.run(["runuser", "-u", user, "--", "env", f"HOME={home}",
                    f"XDG_RUNTIME_DIR={runtime_dir}", "libvirtd", "--daemon"], check=True)
    virsh = ["runuser", "-u", user, "--", "env", f"HOME={home}",
             f"XDG_RUNTIME_DIR={runtime_dir}", "virsh", "-c", "qemu:///session", "uri"]
    for attempt in range(30):
        probe = subprocess.run(virsh, text=True, capture_output=True)
        if probe.returncode == 0 and probe.stdout.strip() == "qemu:///session":
            break
        if attempt == 29:
            raise RuntimeError(f"qemu:///session daemon did not become ready: {probe.stderr.strip()}")
        time.sleep(1)
    run(["runuser", "-u", user, "--", "env", f"HOME={home}",
         f"XDG_RUNTIME_DIR={runtime_dir}", "python3", "/tests/integration/libvirt_recovery_ci.py",
         "--probe", "--evidence", str(evidence)])
    apparmor_evidence = evidence / "apparmor-system"
    apparmor_evidence.mkdir(parents=True, exist_ok=True)
    validate_system_guest_results(apparmor_evidence, Path("/candidate"))
    return 0


def validate_system_guest_results(evidence: Path, candidate_dir: Path) -> None:
    apparmor_guest_run(evidence, candidate_dir, system_mode="all")
    check_apparmor_result(evidence / "result.json")
    candidate_debs = check_packages(candidate_dir, SYSTEM_CANDIDATE_PACKAGES)
    candidate_receipt = candidate_provenance(candidate_dir, candidate_debs)
    check_deep32_result(evidence / "deep32-result.json", candidate_receipt)


def system_guest_source_files() -> tuple[Path, ...]:
    source_dir = Path(__file__).resolve().parent
    return tuple(source_dir / name for name in (
        "recovery_s0.py", "recovery_s0_consolidate.py", "libvirt_recovery_ci.py"))


def apparmor_guest_run(evidence: Path, candidate_dir: Path,
                       system_mode: str = "midcommit") -> None:
    """Run public consolidation in a fresh disposable AppArmor guest."""
    if system_mode not in {"midcommit", "branch", "deep32", "all"}:
        raise ValueError("unknown system guest probe mode")
    debs = check_packages(candidate_dir, SYSTEM_CANDIDATE_PACKAGES)
    provenance = candidate_provenance(candidate_dir, debs)

    guest = f"nexci{uuid.uuid4().hex[:10]}"
    work = evidence / f"guest-run-{guest}"
    work.mkdir(parents=True, mode=0o700)
    key = work / "id_ed25519"
    host_key = work / "ssh_host_ed25519_key"
    user_data = work / "user-data"
    meta_data = work / "meta-data"
    seed = work / "seed.iso"
    base = work / "debian.qcow2"
    disk = work / "guest.qcow2"
    console = work / "guest-console.log"
    evidence.mkdir(parents=True, exist_ok=True)
    process = None
    completed = False
    try:
        run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(key)])
        run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(host_key)])
        public_key = key.with_suffix(".pub").read_text().strip()
        host_public_key = host_key.with_suffix(".pub").read_text().split()
        host_private_text = host_key.read_text().rstrip().splitlines()
        host_public_text = host_key.with_suffix(".pub").read_text().rstrip()
        user_data.write_text(
            "#cloud-config\n"
            "users:\n"
            "  - default\n"
            f"  - name: {guest}\n"
            "    groups: [sudo]\n"
            "    shell: /bin/bash\n"
            "    sudo: ALL=(ALL) NOPASSWD:ALL\n"
            "    ssh_authorized_keys:\n"
            f"      - {public_key}\n"
            "ssh_pwauth: false\n"
            "ssh_deletekeys: false\n"
            "write_files:\n"
            "  - path: /etc/ssh/ssh_host_ed25519_key\n"
            "    owner: root:root\n"
            "    permissions: '0600'\n"
            "    content: |\n"
            + "".join(f"      {line}\n" for line in host_private_text)
            + "  - path: /etc/ssh/ssh_host_ed25519_key.pub\n"
            + "    owner: root:root\n"
            + "    permissions: '0644'\n"
            + "    content: |\n"
            + f"      {host_public_text}\n"
            + "  - path: /etc/apt/sources.list\n"
            "    permissions: '0644'\n"
            "    content: |\n"
            f"      deb http://snapshot.debian.org/archive/debian/{DEBIAN_SNAPSHOT}/ trixie main\n"
            f"      deb http://snapshot.debian.org/archive/debian/{DEBIAN_SNAPSHOT}/ trixie-updates main\n"
            f"      deb http://snapshot.debian.org/archive/debian-security/{DEBIAN_SNAPSHOT}/ trixie-security main\n"
            "  - path: /etc/apt/apt.conf.d/99snapshot\n"
            "    permissions: '0644'\n"
            "    content: Acquire::Check-Valid-Until \"false\";\n"
            "runcmd:\n"
            "  - rm -f /etc/apt/sources.list.d/*.sources /etc/apt/sources.list.d/*.list\n"
            "  - systemctl restart ssh\n"
        )
        meta_data.write_text(f"instance-id: {guest}\nlocal-hostname: {guest}\n")
        run(["curl", "--fail", "--location", "--retry", "3", "--output", str(base), CLOUD_IMAGE_URL])
        digest = subprocess.run(["sha256sum", str(base)], check=True, text=True,
                                capture_output=True).stdout.split()[0]
        if digest != CLOUD_IMAGE_SHA256:
            raise RuntimeError(f"Debian cloud image hash mismatch: {digest}")
        run(["qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", str(base), str(disk)])
        run(["cloud-localds", str(seed), str(user_data), str(meta_data)])
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            ssh_port = listener.getsockname()[1]
        known_hosts = work / "known_hosts"
        known_hosts.write_text(
            f"[127.0.0.1]:{ssh_port} {host_public_key[0]} {host_public_key[1]}\n"
        )
        known_hosts.chmod(0o600)
        process = subprocess.Popen(
            ["qemu-system-x86_64", "-machine", "q35", "-accel", "tcg,thread=multi",
             "-cpu", "max", "-m", "2048", "-smp", "2", "-display", "none",
             "-serial", "file:" + str(console), "-no-reboot",
             "-drive", f"if=virtio,file={disk},format=qcow2",
             "-drive", f"if=virtio,file={seed},format=raw,media=cdrom,readonly=on",
             "-netdev", f"user,id=net0,hostfwd=tcp:127.0.0.1:{ssh_port}-:22",
             "-device", "virtio-net-pci,netdev=net0"],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)

        ssh_base = ["ssh", "-F", "/dev/null", "-i", str(key), "-p", str(ssh_port),
                    "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
                    "-o", f"UserKnownHostsFile={known_hosts}", "-o", "ConnectTimeout=5",
                    f"{guest}@127.0.0.1"]
        for attempt in range(360):
            if process.poll() is not None:
                raise RuntimeError(f"TCG guest exited during boot; console at {console}")
            ready = subprocess.run(ssh_base + ["true"], stdout=subprocess.DEVNULL,
                                   stderr=subprocess.DEVNULL)
            if ready.returncode == 0:
                break
            if attempt == 359:
                raise RuntimeError(f"TCG guest SSH did not become ready; console at {console}")
            time.sleep(2)

        subprocess.run(ssh_base + ["sudo", "cloud-init", "status", "--wait"], check=True)
        candidate_path = f"/home/{guest}/candidate"
        remote = f"{guest}@127.0.0.1:{candidate_path}/"
        subprocess.run(ssh_base + ["mkdir", "-p", candidate_path], check=True)
        receipt_path = work / "candidate-receipt.json"
        receipt_path.write_text(json.dumps(provenance, sort_keys=True) + "\n")
        run(["scp", "-F", "/dev/null", "-i", str(key), "-P", str(ssh_port),
             "-o", "StrictHostKeyChecking=yes", "-o", f"UserKnownHostsFile={known_hosts}",
             str(receipt_path), f"{guest}@127.0.0.1:{candidate_path}/candidate-receipt.json"])
        for package in debs:
            run(["scp", "-F", "/dev/null", "-i", str(key), "-P", str(ssh_port),
                 "-o", "StrictHostKeyChecking=yes", "-o", f"UserKnownHostsFile={known_hosts}",
                 str(package), remote])
        install = (
            "sudo apt-get -o Acquire::Check-Valid-Until=false update && "
            "sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends apparmor apparmor-utils && "
            "sudo systemctl enable --now apparmor && "
            f"sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends python3 qemu-system-x86 qemu-utils {candidate_path}/*.deb && "
            "sudo systemctl enable --now libvirtd && "
            "sudo aa-status --enabled && "
            "sudo virsh -c qemu:///system uri"
        )
        setup = subprocess.run(ssh_base + [install], check=True, text=True, capture_output=True)
        if setup.stdout.strip().splitlines()[-1] != "qemu:///system":
            raise RuntimeError("guest libvirt did not select qemu:///system")
        remote_tests = "/tmp/nex-ci/tests/integration"
        subprocess.run(ssh_base + [f"mkdir -p {remote_tests}"], check=True)
        for source_path in system_guest_source_files():
            name = source_path.name
            run(["scp", "-F", "/dev/null", "-i", str(key), "-P", str(ssh_port),
                 "-o", "StrictHostKeyChecking=yes", "-o", f"UserKnownHostsFile={known_hosts}",
                 str(source_path),
                 f"{guest}@127.0.0.1:{remote_tests}/{name}"])
        selected_modes = ("midcommit", "branch", "deep32") if system_mode == "all" else (system_mode,)
        for selected_mode in selected_modes:
            if selected_mode == "deep32":
                remote_result = f"/tmp/recovery-s0-deep32-{guest}.json"
                local_result = evidence / "deep32-result.json"
                probe_args = (f"--system-deep32-probe --candidate-receipt "
                              f"{candidate_path}/candidate-receipt.json")
            else:
                remote_result = f"/tmp/recovery-s0-apparmor-{selected_mode}-{guest}.json"
                local_result = evidence / ("branch-result.json" if selected_mode == "branch" else "result.json")
                probe_args = f"--system-probe --system-mode {selected_mode}"
            probe_command = (f"sudo python3 {remote_tests}/libvirt_recovery_ci.py {probe_args} "
                             f"--evidence {remote_result}")
            try:
                run(ssh_base + [probe_command], timeout=3600)
            finally:
                copied = subprocess.run(
                    ["scp", "-F", "/dev/null", "-i", str(key), "-P", str(ssh_port),
                     "-o", "StrictHostKeyChecking=yes", "-o", f"UserKnownHostsFile={known_hosts}",
                     f"{guest}@127.0.0.1:{remote_result}", str(local_result)], check=False)
                if copied.returncode:
                    (evidence / f"{selected_mode}-result-copy-missing.txt").write_text(
                        "guest did not export probe result\n")
            if selected_mode == "midcommit":
                check_apparmor_result(local_result)
            elif selected_mode == "branch":
                check_branch_result(local_result)
            else:
                check_deep32_result(local_result, provenance)
        completed = True
    finally:
        if process is not None and process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=20)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=10)
        if completed:
            shutil.rmtree(work, ignore_errors=True)


def system_guest_only(candidate_dir: Path, evidence: Path, system_mode: str) -> int:
    workspace = Path.cwd().resolve()
    candidate = candidate_dir.resolve()
    if not candidate.is_relative_to(workspace / ".cache"):
        raise RuntimeError("prebuilt candidate must be under the ignored workspace .cache directory")
    if not evidence.is_absolute() or not evidence.resolve().is_relative_to(workspace / ".cache"):
        raise RuntimeError("system guest evidence must remain in the ignored workspace .cache directory")
    if evidence.exists():
        raise RuntimeError("system guest evidence directory must be new")
    debs = check_packages(candidate, SYSTEM_CANDIDATE_PACKAGES)
    provenance = candidate_provenance(candidate, debs)
    evidence.mkdir(parents=True)
    apparmor_guest_run(evidence / "apparmor-system", candidate, system_mode=system_mode)
    receipt = {"candidate": provenance, "system_mode": system_mode,
               "result_sha256": {path.name: hashlib.sha256(path.read_bytes()).hexdigest()
                                 for path in (evidence / "apparmor-system").glob("*-result.json")
                                 if path.is_file()}}
    (evidence / "execution-provenance.json").write_text(json.dumps(receipt, indent=2) + "\n")
    return 0


def probe_run(evidence: Path) -> int:
    source = Path(__file__).with_name("recovery_s0.py")
    spec = importlib.util.spec_from_file_location("recovery_s0", source)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot import shared S0 probe from {source}")
    probe_module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(probe_module)
    probe_module.URI = "qemu:///session"
    probe_module.DOMAIN_TYPE = "qemu"
    probe_module.REMOTE_CONFIG = {"expect_uuid_revert_overlays": True}
    home = Path.home()
    work = home / "recovery-s0-ci" / "work"
    work.mkdir(parents=True, exist_ok=True)
    probe_module.ROOT = work
    probe_module.REPORT = evidence / "probe-result.json"
    versions = subprocess.run(
        ["dpkg-query", "-W", "-f=${binary:Package} ${Version}\\n", *CANDIDATE_PACKAGES,
         "qemu-system-x86", "qemu-utils"], check=True, text=True, capture_output=True
    ).stdout
    if Path("/dev/kvm").exists():
        raise RuntimeError("TCG test container unexpectedly has /dev/kvm")
    results = [probe_module.probe_disks(1, paced=False),
               probe_module.probe_disks(2, paced=False)]
    media_result = readonly_media_revert_probe(work)
    report = {
        "status": "passed" if all(result.get("status") == "passed" for result in results) and
                  media_result.get("status") == "passed" else "failed",
        "uri": probe_module.URI,
        "domain_type": probe_module.DOMAIN_TYPE,
        "accelerator": "TCG; no /dev/kvm passed to container",
        "packages": versions.splitlines(),
        "cases": results,
        "readonly_media_revert": media_result,
    }
    evidence.mkdir(parents=True, exist_ok=True)
    (evidence / "result.json").write_text(json.dumps(report, indent=2) + "\n")
    summary = {
        "status": report["status"],
        "uri": report["uri"],
        "domain_type": report["domain_type"],
        "accelerator": report["accelerator"],
        "packages": report["packages"],
        "cases": [{
            "name": case["name"],
            "disk_count": case["disk_count"],
            "mode": case["mode"],
            "status": case["status"],
            "steps": [step["name"] for step in case["steps"]],
        } for case in results],
        "readonly_media_revert": {
            key: media_result.get(key) for key in (
                "status", "snapshot_media_policy", "restored_original_source",
                "media_overlay_created", "writable_disk_head_valid")
        },
    }
    print(json.dumps(summary, indent=2), flush=True)
    return 0 if report["status"] == "passed" else 1


def system_probe_run(evidence: Path, mode: str = "midcommit") -> int:
    """Run public no-delete interruption under enforcing system libvirt."""
    source = Path(__file__).with_name("recovery_s0.py")
    spec = importlib.util.spec_from_file_location("recovery_s0", source)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot import shared S0 probe from {source}")
    probe_module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(probe_module)
    consolidation_source = Path(__file__).with_name("recovery_s0_consolidate.py")
    spec = importlib.util.spec_from_file_location("recovery_s0_consolidate", consolidation_source)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot import public consolidation probe from {consolidation_source}")
    consolidation = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(consolidation)
    enabled = Path("/sys/module/apparmor/parameters/enabled")
    if not enabled.is_file() or enabled.read_text().strip().upper() != "Y":
        raise RuntimeError("guest kernel does not have AppArmor enabled")
    run(["aa-status", "--enabled"])
    run(["systemctl", "is-active", "apparmor"])
    run(["virsh", "-c", "qemu:///system", "uri"])

    expected = {
        package: SOURCE_VERSION for package in SYSTEM_CANDIDATE_PACKAGES
    }
    observed = {}
    package_output = subprocess.run(
        ["dpkg-query", "-W", "-f=${binary:Package} ${Version}\\n", *SYSTEM_CANDIDATE_PACKAGES],
        check=True, text=True, capture_output=True,
    ).stdout
    for line in package_output.splitlines():
        package, version = line.split(maxsplit=1)
        observed[package.split(":", 1)[0]] = version
    if any(observed.get(package) != version for package, version in expected.items()):
        raise RuntimeError("system guest does not have the exact matched libvirt package closure")

    if mode not in {"midcommit", "branch"}:
        raise ValueError("system public consolidation mode must be midcommit or branch")
    run_id = uuid.uuid4().hex
    domain = f"nex-recovery-s0-public-{mode}-{run_id}"
    image_root = Path("/var/lib/libvirt/images") / f"nex-recovery-s0-ci-{run_id}"
    work = image_root / domain
    image_root.mkdir(mode=0o711, parents=True)
    work.mkdir(mode=0o711)
    (work / "sentinel").write_text(probe_module.SENTINEL)
    config = {
        "root": str(image_root), "image_root": str(image_root), "work": str(work),
        "domain": domain, "mode": mode, "sentinel": probe_module.SENTINEL,
        "boundary_timeout_seconds": 90, "bandwidth_mib": 1,
        "storage_observation": {"validation": "disposable Debian guest local qcow2"},
        "allowed_storage_roots": [str(image_root)], "domain_type": "qemu",
        "require_apparmor": True,
    }
    candidate_source = f'''\
def verify_candidate():
    output = run("dpkg-query", "-W", "-f=${{binary:Package}} ${{Version}}\\\\n",
                 *{SYSTEM_CANDIDATE_PACKAGES!r}).stdout
    rows = [line.split(maxsplit=1) for line in output.splitlines()]
    observed = {{package.split(":", 1)[0]: version for package, version in rows}}
    expected = {expected!r}
    if any(observed.get(package) != version for package, version in expected.items()):
        raise RuntimeError("public probe package closure changed before fixture creation")
    return {{"packages": output.splitlines(), "version_match": True}}
'''
    payload = consolidation.build_remote_payload(
        consolidation.native_helper_source(), config, candidate_source)
    payload_path = Path("/tmp") / f"{domain}-campaign.py"
    payload_path.write_text(payload)
    process = subprocess.run([sys.executable, str(payload_path)], text=True,
                             capture_output=True, timeout=1800)
    inner_path = work / "result.json"
    if not inner_path.is_file():
        case_name = "public-no-delete-branch" if mode == "branch" else "public-no-delete-2disk"
        case = {"case": case_name, "status": "failed-preserved",
                "error": "public campaign did not write its result", "domain": domain}
    else:
        case = json.loads(inner_path.read_text())
        case["case"] = "public-no-delete-branch" if mode == "branch" else "public-no-delete-2disk"
    case["campaign_process"] = {"returncode": process.returncode,
                                "stdout": process.stdout, "stderr": process.stderr}
    if process.returncode != 0 and case.get("status") == "passed":
        case["status"] = "failed-preserved"
        case["error"] = "public campaign process returned a failure status"
    report = {
        "exit_code": 0 if process.returncode == 0 and case.get("status") == "passed" else 1,
        "uri": "qemu:///system", "domain_type": "qemu",
        "scope": system_probe_scope(mode),
        "apparmor": {"kernel_enabled": enabled.read_text().strip(),
                     "service": "active", "status_command": "aa-status --enabled passed"},
        "packages": package_output.splitlines(), "cases": [case],
    }
    evidence.parent.mkdir(parents=True, exist_ok=True)
    evidence.write_text(json.dumps(report, indent=2) + "\n")
    payload_path.unlink(missing_ok=True)
    if report["exit_code"] == 0:
        shutil.rmtree(image_root)
    print(json.dumps({
        "status": "passed" if report["exit_code"] == 0 else "failed",
        "uri": report["uri"],
        "domain_type": report["domain_type"],
        "cases": [{"case": item.get("case"), "status": item.get("status")}
                  for item in report["cases"]],
    }, indent=2), flush=True)
    return report["exit_code"]


def build_deep32_payload(helper_source: str, config: dict, package_campaign: str) -> str:
    payload = (helper_source + "\n" + package_campaign + "\n" +
               inspect.getsource(expected_redefined_parent_graph) + "\n" +
               inspect.getsource(emit_deep32_status) + "\n" +
               inspect.getsource(deep32_data_by_target) + "\n" +
               inspect.getsource(retained_point_reads) + "\n" +
               inspect.getsource(snapshot_source_paths) + "\n" +
               inspect.getsource(retained_saved_point_reads) + "\n" +
               DEEP32_FOREIGN_GUARD + "\nCONFIG = " + repr(config) + "\n" + DEEP32_REMOTE)
    compile(payload, "recovery_s0_deep32_remote.py", "exec")
    return payload


def system_deep32_probe_run(evidence: Path, candidate_receipt_path: Path) -> int:
    """Run one two-disk 32-point public consolidation under system AppArmor."""
    source = Path(__file__).with_name("recovery_s0.py")
    spec = importlib.util.spec_from_file_location("recovery_s0", source)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot import shared S0 helper from {source}")
    probe = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(probe)
    consolidation_source = Path(__file__).with_name("recovery_s0_consolidate.py")
    spec = importlib.util.spec_from_file_location("recovery_s0_consolidate", consolidation_source)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot import public consolidation helpers from {consolidation_source}")
    consolidation = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(consolidation)
    candidate_receipt = json.loads(candidate_receipt_path.read_text())
    enabled = Path("/sys/module/apparmor/parameters/enabled")
    if not enabled.is_file() or enabled.read_text().strip().upper() != "Y":
        raise RuntimeError("guest kernel does not have AppArmor enabled")
    run(["aa-status", "--enabled"])
    run(["systemctl", "is-active", "apparmor"])
    run(["virsh", "-c", "qemu:///system", "uri"])
    packages = subprocess.run(
        ["dpkg-query", "-W", "-f=${binary:Package} ${Version}\\n", *SYSTEM_CANDIDATE_PACKAGES],
        check=True, text=True, capture_output=True).stdout.splitlines()
    observed = {}
    for line in packages:
        package, version = line.split(maxsplit=1)
        observed[package.split(":", 1)[0]] = version
    if any(observed.get(package) != SOURCE_VERSION for package in SYSTEM_CANDIDATE_PACKAGES):
        raise RuntimeError("system guest package closure differs from the selected +nex4 candidate")

    run_id = uuid.uuid4().hex
    domain = f"nex-recovery-s0-deep32-{run_id}"
    image_root = Path("/var/lib/libvirt/images") / f"nex-recovery-s0-deep32-{run_id}"
    work = image_root / domain
    image_root.mkdir(mode=0o711, parents=True)
    work.mkdir(mode=0o711)
    (work / "sentinel").write_text(probe.SENTINEL)
    config = {"image_root": str(image_root), "work": str(work), "domain": domain,
              "sentinel": probe.SENTINEL, "candidate": candidate_receipt}
    expected = {package: SOURCE_VERSION for package in SYSTEM_CANDIDATE_PACKAGES}
    package_campaign = f'''\
def verify_candidate():
    output = run("dpkg-query", "-W", "-f=${{binary:Package}} ${{Version}}\\\\n",
                 *{SYSTEM_CANDIDATE_PACKAGES!r}).stdout
    rows = [line.split(maxsplit=1) for line in output.splitlines()]
    observed = {{package.split(":", 1)[0]: version for package, version in rows}}
    expected = {expected!r}
    if any(observed.get(package) != version for package, version in expected.items()):
        raise RuntimeError("candidate package closure changed before deep32 fixture creation")
    return {{"versions": output.splitlines(), "version_match": True}}
'''
    payload = build_deep32_payload(consolidation.native_helper_source(), config, package_campaign)
    payload_path = Path("/tmp") / f"{domain}-campaign.py"
    payload_path.write_text(payload)
    process = subprocess.run([sys.executable, str(payload_path)], text=True,
                             capture_output=True, timeout=7200)
    inner_path = work / "result.json"
    if inner_path.is_file():
        case = json.loads(inner_path.read_text())
    else:
        case = {"case": "public-no-delete-deep32", "status": "failed-preserved",
                "domain": domain, "error": "deep32 campaign did not write its result"}
    case["case"] = "public-no-delete-deep32"
    case["domain"] = domain
    if process.returncode != 0 and case.get("status") == "passed":
        case["status"] = "failed-preserved"
        case["error"] = "deep32 campaign process returned failure status"
    report = {"exit_code": 0 if process.returncode == 0 and case.get("status") == "passed" else 1,
              "uri": "qemu:///system", "domain_type": "qemu", "scope": DEEP32_SCOPE,
              "apparmor": {"kernel_enabled": enabled.read_text().strip(), "service": "active"},
              "packages": packages, "candidate": candidate_receipt, "case": case,
              "campaign_process": {"returncode": process.returncode,
                                   "stdout": process.stdout, "stderr": process.stderr}}
    evidence.parent.mkdir(parents=True, exist_ok=True)
    evidence.write_text(json.dumps(report, indent=2) + "\n")
    payload_path.unlink(missing_ok=True)
    if report["exit_code"] == 0:
        shutil.rmtree(image_root)
    print(json.dumps({"status": "passed" if report["exit_code"] == 0 else "failed",
                      "domain_type": report["domain_type"], "point_count": case.get("point_count"),
                      "result": str(evidence)}, indent=2), flush=True)
    return report["exit_code"]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--probe", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--system-probe", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--system-mode", choices=("midcommit", "branch"), default="midcommit",
                        help=argparse.SUPPRESS)
    parser.add_argument("--system-deep32-probe", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--candidate-receipt", type=Path, help=argparse.SUPPRESS)
    parser.add_argument("--system-guest-only", action="store_true",
                        help="run a fresh selected system-mode TCG guest without rebuilding the candidate")
    parser.add_argument("--system-guest-mode", choices=("midcommit", "branch", "deep32", "all"),
                        default="midcommit", help=argparse.SUPPRESS)
    parser.add_argument("--evidence", type=Path, help=argparse.SUPPRESS)
    parser.add_argument("--candidate-dir", type=Path,
                        help="reuse a checksummed build under .cache for local runtime-test iteration")
    args = parser.parse_args()
    if args.system_guest_only:
        if args.candidate_dir is None or args.evidence is None:
            parser.error("--system-guest-only requires --candidate-dir and --evidence")
        return system_guest_only(args.candidate_dir, args.evidence, args.system_guest_mode)
    if args.runtime:
        if args.evidence is None:
            parser.error("--evidence is required in runtime mode")
        return runtime_run(args.evidence)
    if args.probe:
        if args.evidence is None:
            parser.error("--evidence is required in probe mode")
        return probe_run(args.evidence)
    if args.system_probe:
        if args.evidence is None:
            parser.error("--evidence is required in system-probe mode")
        return system_probe_run(args.evidence, args.system_mode)
    if args.system_deep32_probe:
        if args.evidence is None or args.candidate_receipt is None:
            parser.error("--system-deep32-probe requires --evidence and --candidate-receipt")
        return system_deep32_probe_run(args.evidence, args.candidate_receipt)
    return host_run(args.candidate_dir)


if __name__ == "__main__":
    raise SystemExit(main())
