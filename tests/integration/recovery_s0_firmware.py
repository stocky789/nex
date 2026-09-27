#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Probe UEFI, Secure Boot and vTPM rollback on one disposable Core candidate.

The candidate is an explicitly selected nested Core lab. All VM disks, firmware
state and snapshot heads belong to the probe's generated UUID under its own
libvirt image directory. An incomplete native operation preserves that subtree.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import shutil
import stat
import subprocess
import sys
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
BASE = ROOT / ".cache/recovery-s0-core/artifacts/debian-13-genericcloud-amd64-20260831-2587.qcow2"
BASE_SHA256 = "85a969b7e99d7c817414136033df18c58d5c45ac8d27bb36e8ccb67173d2d4e3"
REMOTE_CONFIG = None
SENTINEL = "NEX recovery S0 disposable lab"
PROFILES = {
    "bios": {"uefi": False, "secure_boot": False, "tpm": False},
    "uefi-secure-boot-off": {"uefi": True, "secure_boot": False, "tpm": False},
    "uefi-secure-boot-on": {"uefi": True, "secure_boot": True, "tpm": False},
    "uefi-secure-boot-vtpm20": {"uefi": True, "secure_boot": True, "tpm": True},
}
DEFAULT_PROFILE = "uefi-secure-boot-vtpm20"
NEX3_MANIFEST_SHA256 = "116615ecd128c157d390d96ada07281d53b77d4afb5c61dc628a20ef6450588e"
NEX3_LIBVIRT_PACKAGES = (
    "libvirt-clients", "libvirt-common", "libvirt-daemon", "libvirt-daemon-common",
    "libvirt-daemon-config-network", "libvirt-daemon-config-nwfilter", "libvirt-daemon-driver-network",
    "libvirt-daemon-driver-nodedev", "libvirt-daemon-driver-nwfilter", "libvirt-daemon-driver-qemu",
    "libvirt-daemon-driver-secret", "libvirt-daemon-driver-storage", "libvirt-daemon-log",
    "libvirt-daemon-system", "libvirt0")


def public_delete_domain_type(profile, domain_type):
    if profile != "uefi-secure-boot-vtpm20" or domain_type != "qemu":
        raise ValueError("public firmware deletion requires Secure Boot/vTPM with qemu TCG")
    return domain_type


def public_blockcommit_argv(domain, target, top, base):
    if target not in ("vda", "vdb"):
        raise ValueError("public firmware deletion only supports the selected two disks")
    return ["blockcommit", domain, target, "--top", str(top), "--base", str(base), "--wait", "--verbose"]


def public_current_head_argv(domain, heads):
    if set(heads) != {"vda", "vdb"}:
        raise ValueError("public firmware deletion requires both selected disk heads")
    command = ["snapshot-create-as", domain, "--no-metadata", "--disk-only", "--atomic"]
    for target in ("vda", "vdb"):
        command.extend(("--diskspec", f"{target},snapshot=external,file={heads[target]}"))
    return command


def firmware_compare_receipt(argv, comparison):
    return {"argv": [str(argument) for argument in argv], "returncode": comparison.returncode,
            "stdout": comparison.stdout, "stderr": comparison.stderr}


def unserializable_firmware_paths(value, field="$", found=None):
    found = [] if found is None else found
    if isinstance(value, Path):
        found.append({"field": field, "value": str(value)})
    elif isinstance(value, dict):
        for key, item in value.items():
            unserializable_firmware_paths(item, f"{field}.{key}", found)
    elif isinstance(value, (list, tuple)):
        for index, item in enumerate(value):
            unserializable_firmware_paths(item, f"{field}[{index}]", found)
    return found


def persist_firmware_receipt(path, result):
    path = Path(path)
    payload = json.dumps(result, indent=2) + "\n"
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_text(payload)
    temporary.chmod(0o600)
    temporary.replace(path)


def firmware_chain_with_backing(head, backing_chain):
    if not backing_chain or str(backing_chain[0]) == str(head):
        raise ValueError("backing chain must begin with the exact parent image")
    return [str(head), *(str(path) for path in backing_chain)]


def rewrite_firmware_retained_snapshot(raw, *, snapshot_name, expected_parent, a_sources, d_sources):
    import xml.etree.ElementTree as ET

    root = ET.fromstring(raw)
    if root.findtext("name") != snapshot_name:
        raise ValueError("snapshot XML name differs from selected retained point")
    parent = root.find("parent")
    if parent is None or parent.findtext("name") != expected_parent:
        raise ValueError("retained snapshot parent differs from selected deleted point")
    replaced = []
    for disk in root.findall("./domain/devices/disk[@device='disk']"):
        target = disk.find("target")
        source = disk.find("source")
        if target is None or target.get("dev") not in a_sources or source is None:
            continue
        dev = target.get("dev")
        if source.get("file") != str(a_sources[dev]):
            raise ValueError(f"retained snapshot saved source differs from A for {dev}")
        source.set("file", str(d_sources[dev]))
        replaced.append(dev)
    if set(replaced) != set(a_sources):
        raise ValueError("retained snapshot did not contain exactly the selected A disk sources")
    root.remove(parent)
    return ET.tostring(root, encoding="unicode") + "\n", replaced


def exact_unlink_precondition(path, work):
    path, work = Path(path), Path(work)
    info = path.lstat()
    if path.parent != work or path.is_symlink() or not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
        raise ValueError("selected A image is not a unique regular direct child of the private run directory")
    return info.st_size


def firmware_snapshot_disk_paths(raw):
    import xml.etree.ElementTree as ET

    root = ET.fromstring(raw)
    saved, post = {}, {}
    for disk in root.findall("./domain/devices/disk[@device='disk']"):
        target = disk.find("target")
        source = disk.find("source")
        if target is None or source is None or not source.get("file"):
            continue
        saved[target.get("dev")] = source.get("file")
    for disk in root.findall("./disks/disk"):
        source = disk.find("source")
        if source is not None and source.get("file"):
            post[disk.get("name")] = source.get("file")
    return saved, post


def verify_firmware_snapshot_mapping(docs, d_sources, a_sources, b_sources):
    if set(docs) != {"point-a", "point-b"}:
        raise ValueError("unexpected metadata points before consolidation")
    a_saved, a_post = firmware_snapshot_disk_paths(docs["point-a"])
    b_saved, b_post = firmware_snapshot_disk_paths(docs["point-b"])
    if a_saved != d_sources or a_post != a_sources or b_saved != a_sources or b_post != b_sources:
        raise ValueError("actual snapshot XML does not map the selected A/B disk heads")
    return {"point-a": {"saved": a_saved, "post": a_post},
            "point-b": {"saved": b_saved, "post": b_post}}


def firmware_cloud_init(name, public_key):
    user_data = ("#cloud-config\n"
                 "disable_root: false\n"
                 "ssh_pwauth: false\n"
                 "ssh_authorized_keys:\n"
                 f"  - {public_key}\n"
                 "packages:\n"
                 "  - qemu-guest-agent\n"
                 "  - mokutil\n"
                 "  - efibootmgr\n"
                 "  - tpm2-tools\n"
                 "runcmd:\n"
                 "  - [ systemctl, enable, --now, qemu-guest-agent ]\n")
    meta_data = f"instance-id: {name}\nlocal-hostname: {name}\n"
    return user_data, meta_data


def secure_boot_is_enabled(output):
    return "1\nSecureBoot enabled" in output


def require_qemu_confinement(context, domain_uuid):
    expected = f"libvirt-{domain_uuid} (enforce)"
    if context != expected:
        raise ValueError(f"QEMU process confinement differs from the domain profile: {context!r}")
    return expected


def public_firmware_domain_xml(name, vm_uuid, nvram, disk_a, disk_b, seed):
    return f"""<domain type='qemu'><name>{name}</name><uuid>{vm_uuid}</uuid>
<memory unit='MiB'>2048</memory><currentMemory unit='MiB'>2048</currentMemory><vcpu>2</vcpu>
<os firmware='efi'><type arch='x86_64' machine='q35'>hvm</type><firmware><feature enabled='yes' name='enrolled-keys'/><feature enabled='yes' name='secure-boot'/></firmware><boot dev='hd'/><nvram>{nvram}</nvram></os>
<features><acpi/><apic/><smm state='on'/></features><clock offset='utc'/><devices><emulator>/usr/bin/qemu-system-x86_64</emulator>
<disk type='file' device='disk'><driver name='qemu' type='qcow2' cache='none'/><source file='{disk_a}'/><target dev='vda' bus='virtio'/></disk>
<disk type='file' device='disk'><driver name='qemu' type='qcow2' cache='none'/><source file='{disk_b}'/><target dev='vdb' bus='virtio'/></disk>
<disk type='file' device='cdrom'><driver name='qemu' type='raw'/><source file='{seed}'/><target dev='sda' bus='sata'/><readonly/></disk>
<interface type='network'><source network='default'/><model type='virtio'/></interface><channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0'/></channel>
<graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1'/><video><model type='vga' heads='1' primary='yes'/></video>
<tpm model='tpm-crb'><backend type='emulator' version='2.0'/></tpm></devices></domain>"""


def firmware_data_disk_mount_command():
    return 'mountpoint -q /mnt/recovery || mount /dev/vdb /mnt/recovery; test "$(findmnt -n -o SOURCE /mnt/recovery)" = /dev/vdb'


def firmware_marker_readback_command(root_marker="/root/recovery-marker", data_marker="/mnt/recovery/recovery-marker"):
    return ("printf 'source=%s\\nroot=%s\\ndata=%s\\n' "
            "\"$(findmnt -n -o SOURCE /mnt/recovery)\" "
            f"\"$(cat {shlex.quote(str(root_marker))})\" \"$(cat {shlex.quote(str(data_marker))})\"")


def verify_public_candidate(candidate_dir):
    candidate_dir = Path(candidate_dir)
    if candidate_dir.is_symlink():
        raise ValueError("selected candidate directory must not be a symlink")
    candidate_dir = candidate_dir.resolve(strict=True)
    if not candidate_dir.is_relative_to((ROOT / ".cache").resolve()):
        raise ValueError("selected candidate must remain under the ignored workspace cache")
    manifest = candidate_dir / "SHA256SUMS"
    if not manifest.is_file() or hashlib.sha256(manifest.read_bytes()).hexdigest() != NEX3_MANIFEST_SHA256:
        raise ValueError("candidate manifest hash differs from the pinned +nex3 build")
    listed = {}
    for line in manifest.read_text().splitlines():
        fields = line.split(maxsplit=1)
        if len(fields) == 2:
            listed[fields[1].lstrip("* ")] = fields[0]
    packages = []
    for name in NEX3_LIBVIRT_PACKAGES:
        matches = list(candidate_dir.glob(f"{name}_11.3.0-3+deb13u3+nex3_*.deb"))
        if len(matches) != 1:
            raise ValueError(f"candidate does not contain exactly one selected {name} package")
        package = matches[0]
        if hashlib.sha256(package.read_bytes()).hexdigest() != listed.get(package.name):
            raise ValueError(f"selected candidate package hash differs from its manifest: {name}")
        metadata = parse_deb_metadata(run(["dpkg-deb", "-f", package, "Package", "Version", "Architecture"]).stdout)
        if metadata.get("Package") != name or metadata.get("Version") != "11.3.0-3+deb13u3+nex3" or metadata.get("Architecture") not in ("amd64", "all"):
            raise ValueError(f"selected candidate package metadata is invalid: {name}")
        packages.append({"name": name, "filename": package.name,
                         "sha256": hashlib.sha256(package.read_bytes()).hexdigest(),
                         "architecture": metadata["Architecture"]})
    return {"manifest_sha256": NEX3_MANIFEST_SHA256, "packages": packages}


def parse_deb_metadata(output):
    metadata = {}
    for line in output.splitlines():
        key, separator, value = line.partition(":")
        if not separator or key in metadata:
            raise ValueError("package metadata is malformed")
        metadata[key] = value.strip()
    return metadata


def run(args, *, check=True, input=None, timeout=180):
    result = subprocess.run(args, input=input, text=True, capture_output=True, timeout=timeout)
    if check and result.returncode:
        raise RuntimeError(f"{args[0]} failed: {result.stderr.strip() or result.stdout.strip()}")
    return result


def validate_lab(path):
    path = Path(path)
    assert not path.is_symlink(), "candidate lab must not be a symlink"
    lab = path.resolve(strict=True)
    assert lab.parent == (ROOT / ".cache").resolve() and lab.name.startswith("recovery-s0-core."), "candidate must be a selected recovery S0 lab"
    assert json.loads((lab / "lab.json").read_text()) == {"disposable": True, "fixture": "cluster-ha-v1"}, "candidate lab is not marked disposable"
    for name in ("id_ed25519", "id_ed25519.pub", "known_hosts", "ports.json"):
        item = lab / name
        assert item.is_file() and not item.is_symlink(), f"candidate lab is missing {name}"
    assert (lab / "id_ed25519").stat().st_mode & 0o077 == 0, "candidate SSH key permissions are too broad"
    return lab


def ssh_args(lab, node):
    ports = json.loads((lab / "ports.json").read_text())
    assert node in ("core1", "core2", "core3") and node in ports, "select one Core guest from this lab"
    return ["ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
            "-o", "StrictHostKeyChecking=yes", "-o", f"UserKnownHostsFile={lab / 'known_hosts'}",
            "-i", str(lab / "id_ed25519"), "-p", str(ports[node]), "root@127.0.0.1"]


def inner_probe(cfg):
    """This function is sent over the lab's loopback-only SSH connection."""
    if cfg.get("public_deletion"):
        return inner_public_deletion_probe(cfg)
    import grp
    import pwd
    import xml.etree.ElementTree as ET

    work = Path(cfg["work"])
    base = Path(cfg["base"])
    profile = cfg["profile"]
    settings = PROFILES[profile]
    running_capture = cfg["running_capture"]
    preserve_snapshot = cfg["preserve_snapshot"]
    assert not running_capture or profile == "bios", "running capture is available only for the BIOS profile"
    sentinel = work / "sentinel"
    assert sentinel.read_text() == SENTINEL, "private disposable-lab sentinel differs"
    assert work.parent == Path("/var/lib/libvirt/images/nex-recovery-s0-firmware"), "work path escaped the private recovery directory"
    assert not work.is_symlink() and not base.is_symlink() and base.is_file(), "probe paths must be regular files"
    packages = ("nex-core-local", "libvirt-daemon-system", "qemu-system-x86",
                "qemu-utils", "ovmf", "swtpm", "swtpm-tools", "python3-virt-firmware")
    version = run(["dpkg-query", "-W", "-f=${binary:Package}=${Version}\n", *packages], check=False)
    if version.returncode:
        raise RuntimeError("candidate package versions unavailable: " + version.stderr.strip())
    candidate = {"os_release": Path("/etc/os-release").read_text(), "packages": version.stdout,
                 "virsh": run(["virsh", "--version"]).stdout.strip(),
                 "qemu": run(["qemu-system-x86_64", "--version"]).stdout.splitlines()[0],
                 "domcapabilities": run(["virsh", "-c", "qemu:///system", "domcapabilities", "--virttype", "kvm", "--arch", "x86_64", "--machine", "q35"]).stdout}
    run(["virsh", "-c", "qemu:///system", "net-info", "default"])
    image_root = Path("/var/lib/libvirt/images")
    image_root.mkdir(parents=True, exist_ok=True)
    uid = pwd.getpwnam("libvirt-qemu").pw_uid
    gid = grp.getgrnam("kvm").gr_gid
    key = cfg["public_key"]
    result = {"candidate": candidate, "profile": profile, "status": "incomplete", "steps": []}
    name = "nex-recovery-s0-fw-" + uuid.uuid4().hex[:12]
    vm_uuid = str(uuid.uuid4())
    disk = work / "guest.qcow2"
    seed = work / "seed.iso"
    nvram = Path(f"/var/lib/libvirt/qemu/nvram/{vm_uuid}_VARS.fd") if settings["uefi"] else None
    tpm = Path(f"/var/lib/libvirt/swtpm/{vm_uuid}") if settings["tpm"] else None
    head = work / "snapshot-head.qcow2"
    xml_path = work / "domain.xml"
    base_info = json.loads(run(["qemu-img", "info", "--output=json", base]).stdout)
    result["base_image"] = {"name": base.name, "format": base_info.get("format"), "virtual_size": base_info.get("virtual-size")}

    def virsh(*args, check=True):
        return run(["virsh", "-c", "qemu:///system", *args], check=check, timeout=240)

    def domain_state():
        return virsh("domstate", name).stdout.strip().lower()

    def wait_stopped():
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            if domain_state() == "shut off":
                return
            time.sleep(1)
        raise TimeoutError("probe VM did not stop")

    def state_manifest(path):
        if path.is_file() and not path.is_symlink():
            files = [path]
        elif path.is_dir() and not path.is_symlink():
            files = [path, *sorted(path.rglob("*"))]
        else:
            raise RuntimeError(f"expected regular libvirt state at {path}")
        manifest = []
        for item in files:
            info = item.lstat()
            entry = {"relative": str(item.relative_to(path) if path.is_dir() else item.name),
                     "type": "directory" if item.is_dir() else "file",
                     "mode": oct(stat.S_IMODE(info.st_mode)), "uid": info.st_uid, "gid": info.st_gid}
            if item.is_file() and not item.is_symlink():
                with item.open("rb") as contents:
                    entry["size"] = info.st_size
                    entry["sha256"] = hashlib.file_digest(contents, "sha256").hexdigest()
            manifest.append(entry)
        return manifest

    def preserve_metadata(source, target):
        info = source.lstat()
        if source.is_symlink():
            raise RuntimeError(f"refusing to preserve unexpected symlink {source}")
        os.chown(target, info.st_uid, info.st_gid)
        os.chmod(target, stat.S_IMODE(info.st_mode))
        if source.is_dir():
            for item in source.rglob("*"):
                if item.is_symlink():
                    raise RuntimeError(f"refusing to preserve unexpected symlink {item}")
                info = item.lstat()
                os.chown(target / item.relative_to(source), info.st_uid, info.st_gid)
                os.chmod(target / item.relative_to(source), stat.S_IMODE(info.st_mode))

    def copy_state(source, target):
        if target.exists() or target.is_symlink():
            raise RuntimeError(f"refusing to replace existing recovery copy {target}")
        if source.is_dir():
            shutil.copytree(source, target, copy_function=shutil.copy2, symlinks=True)
        elif source.is_file() and not source.is_symlink():
            shutil.copy2(source, target)
        else:
            raise RuntimeError(f"cannot copy missing or symlinked state {source}")
        preserve_metadata(source, target)

    def restore_state(saved, target):
        if target.is_symlink() or (target.exists() and target.is_dir() != saved.is_dir()):
            raise RuntimeError(f"state target changed type: {target}")
        if target.is_dir():
            shutil.rmtree(target)
        elif target.exists():
            target.unlink()
        copy_state(saved, target)

    def guest(command, *, check=True, timeout=240):
        reply = json.loads(virsh("qemu-agent-command", name, json.dumps({"execute": "guest-exec", "arguments": {
            "path": "/bin/sh", "arg": ["-ec", command], "capture-output": True}})).stdout)["return"]
        pid = reply["pid"]
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            status = json.loads(virsh("qemu-agent-command", name, json.dumps({"execute": "guest-exec-status", "arguments": {"pid": pid}})).stdout)["return"]
            if status.get("exited"):
                output = __import__("base64").b64decode(status.get("out-data", "")).decode(errors="replace")
                error = __import__("base64").b64decode(status.get("err-data", "")).decode(errors="replace")
                if check and status.get("exitcode") != 0:
                    raise RuntimeError("guest command failed: " + error.strip())
                return {"exitcode": status.get("exitcode"), "stdout": output, "stderr": error}
            time.sleep(1)
        raise TimeoutError("guest command did not complete")

    def guest_freeze_status():
        status = json.loads(virsh("qemu-agent-command", name, json.dumps({
            "execute": "guest-fsfreeze-status"})).stdout)["return"]
        if status not in ("thawed", "frozen"):
            raise RuntimeError(f"guest agent returned unknown filesystem-freeze state: {status!r}")
        return status

    def secure_boot_observation(stage):
        variable = guest("python3 -c 'import glob, pathlib; p=glob.glob(\"/sys/firmware/efi/efivars/SecureBoot-*\"); print(\"SecureBoot EFI variable absent\" if not p else \"SecureBoot EFI variable value=\" + str(pathlib.Path(p[0]).read_bytes()[4]))'")
        value = variable["stdout"].strip()
        mokutil = guest("mokutil --sb-state", check=False)
        observation = {"stage": stage, "efi_variable": value, "mokutil": {
            "exit_code": mokutil["exitcode"], "stdout": mokutil["stdout"].strip(), "stderr": mokutil["stderr"].strip()}}
        result.setdefault("secure_boot_observations", []).append(observation)
        if settings["secure_boot"]:
            if value != "SecureBoot EFI variable value=1" or "SecureBoot enabled" not in mokutil["stdout"]:
                raise RuntimeError(f"guest did not prove Secure Boot enabled: {observation}")
        elif value not in ("SecureBoot EFI variable absent", "SecureBoot EFI variable value=0"):
            raise RuntimeError(f"guest SecureBoot EFI variable is unexpectedly enabled: {value!r}")
        return value + "; mokutil exit=" + str(mokutil["exitcode"]) + "; stdout=" + mokutil["stdout"].strip() + "; stderr=" + mokutil["stderr"].strip()

    def boot():
        state = domain_state()
        if state == "shut off":
            virsh("start", name)
        elif state == "paused":
            virsh("resume", name)
        elif state != "running":
            raise RuntimeError(f"probe VM is in unexpected state before guest check: {state}")
        deadline = time.monotonic() + 300
        while time.monotonic() < deadline:
            try:
                return guest("systemctl is-active --quiet qemu-guest-agent; printf ready")
            except Exception:
                time.sleep(2)
        raise TimeoutError("guest agent did not become ready")

    try:
        if settings["uefi"]:
            assert "efi" in candidate["domcapabilities"], "Core libvirt does not advertise UEFI"
        if settings["secure_boot"]:
            if settings["tpm"]:
                assert "emulator" in candidate["domcapabilities"] and "tpm-crb" in candidate["domcapabilities"], "Core libvirt does not advertise the requested vTPM profile"
            descriptor = Path("/usr/share/qemu/firmware/40-edk2-x86_64-secure-enrolled.json")
            assert descriptor.is_file() and "secure-boot" in descriptor.read_text() and "enrolled-keys" in descriptor.read_text(), "Core Secure Boot firmware descriptor is missing"
            result["secure_boot_firmware_descriptor"] = descriptor.read_text()
        run(["qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", base, disk, "16G"])
        os.chown(disk, uid, gid)
        guest_packages = ["qemu-guest-agent"]
        if settings["uefi"]:
            guest_packages.extend(("mokutil", "efibootmgr"))
        if settings["tpm"]:
            guest_packages.append("tpm2-tools")
        user_data = ("#cloud-config\ndisable_root: false\nssh_pwauth: false\nssh_authorized_keys:\n  - " + key +
                     "\npackages:\n" + "".join(f"  - {package}\n" for package in guest_packages) +
                     "runcmd:\n  - [ systemctl, enable, --now, qemu-guest-agent ]\n")
        seed_dir = work / "seed"
        seed_dir.mkdir()
        (seed_dir / "user-data").write_text(user_data)
        (seed_dir / "meta-data").write_text(f"instance-id: {name}\nlocal-hostname: {name}\n")
        run(["genisoimage", "-quiet", "-output", seed, "-volid", "cidata", "-joliet", "-rock", seed_dir])
        if settings["uefi"]:
            key_state = "yes" if settings["secure_boot"] else "no"
            firmware = (f"<os firmware='efi'><type arch='x86_64' machine='q35'>hvm</type><firmware>"
                        f"<feature enabled='{key_state}' name='enrolled-keys'/><feature enabled='{key_state}' name='secure-boot'/>"
                        f"</firmware><boot dev='hd'/><nvram>{nvram}</nvram></os>")
        else:
            firmware = "<os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os>"
        smm = "<smm state='on'/>" if settings["secure_boot"] else ""
        tpm_device = "<tpm model='tpm-crb'><backend type='emulator' version='2.0'/></tpm>" if settings["tpm"] else ""
        xml = f"""<domain type='kvm'><name>{name}</name><uuid>{vm_uuid}</uuid>
<memory unit='MiB'>1024</memory><currentMemory unit='MiB'>1024</currentMemory><vcpu>2</vcpu>
{firmware}<features><acpi/><apic/>{smm}</features><cpu mode='host-model'/><clock offset='utc'/>
<devices><emulator>/usr/bin/qemu-system-x86_64</emulator>
<disk type='file' device='disk'><driver name='qemu' type='qcow2' cache='none'/><source file='{disk}'/><target dev='vda' bus='virtio'/></disk>
<disk type='file' device='cdrom'><driver name='qemu' type='raw'/><source file='{seed}'/><target dev='sda' bus='sata'/><readonly/></disk>
<interface type='network'><source network='default'/><model type='virtio'/></interface>
<channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0'/></channel>
<graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1'/><video><model type='vga' heads='1' primary='yes'/></video>
{tpm_device}</devices></domain>"""
        xml_path.write_text(xml)
        run(["virsh", "define", xml_path])
        result["uuid"] = vm_uuid
        result["firmware_xml"] = {"firmware": "uefi" if settings["uefi"] else "bios",
                                  "firmware_features": [f"secure-boot={'yes' if settings['secure_boot'] else 'no'}",
                                                        f"enrolled-keys={'yes' if settings['secure_boot'] else 'no'}"] if settings["uefi"] else [],
                                  "nvram": str(nvram) if settings["uefi"] else None,
                                  "smm": "on" if settings["secure_boot"] else "off",
                                  "tpm": "tpm-crb/emulator/2.0" if settings["tpm"] else None}
        defined = ET.fromstring(virsh("dumpxml", name, "--inactive").stdout)
        os_xml = defined.find("os")
        assert (os_xml is not None and os_xml.get("firmware") == "efi") == settings["uefi"], "defined firmware mode differs from selected profile"
        assert (defined.find("./devices/tpm") is not None) == settings["tpm"], "defined TPM device differs from selected profile"
        graphics = defined.find("./devices/graphics")
        video = defined.find("./devices/video/model")
        assert graphics is not None and graphics.get("type") == "vnc" and graphics.get("listen") == "127.0.0.1", "defined console differs from the tested Core VM shape"
        assert video is not None and video.get("type") == "vga", "defined video differs from the tested Core VM shape"
        if settings["uefi"]:
            assert os_xml.find("nvram").text == str(nvram), "defined NVRAM path differs from the UUID-scoped path"
            firmware_features = {feature.get("name"): feature.get("enabled") for feature in os_xml.findall("./firmware/feature")}
            expected_key = "yes" if settings["secure_boot"] else "no"
            assert firmware_features == {"secure-boot": expected_key, "enrolled-keys": expected_key}, "defined UEFI features differ from selected profile"
        smm = defined.find("./features/smm")
        assert (smm is not None and smm.get("state") == "on") == settings["secure_boot"], "defined SMM setting differs from selected profile"
        boot()
        guest("mkdir -p /root/nex-recovery-s0")
        if settings["uefi"]:
            guest("test -d /sys/firmware/efi")
            result["efi_boot_entries_initial"] = guest("efibootmgr -v")["stdout"].strip()
            firmware_observation = secure_boot_observation("initial-boot")
        else:
            firmware_observation = guest("test ! -d /sys/firmware/efi; printf 'BIOS boot observed'")["stdout"]
            assert "BIOS boot observed" in firmware_observation
        if settings["tpm"]:
            sealed = "nex recovery S0 test secret"
            setup_tpm = "printf %s " + shlex.quote(sealed) + " > /root/nex-recovery-s0/sealed-value; " + \
                "tpm2_createprimary -C o -G rsa -c /run/nex-s0-primary.ctx >/dev/null; " + \
                "tpm2_create -C /run/nex-s0-primary.ctx -u /root/nex-recovery-s0/sealed.pub -r /root/nex-recovery-s0/sealed.priv -i /root/nex-recovery-s0/sealed-value >/dev/null; " + \
                "tpm2_load -C /run/nex-s0-primary.ctx -u /root/nex-recovery-s0/sealed.pub -r /root/nex-recovery-s0/sealed.priv -c /run/nex-s0-sealed.ctx >/dev/null; " + \
                "test \"$(tpm2_unseal -c /run/nex-s0-sealed.ctx)\" = " + shlex.quote(sealed)
            guest(setup_tpm)
        else:
            guest("test ! -e /dev/tpm0 && test ! -e /dev/tpmrm0")
        result["steps"].append({"name": "guest-firmware-profile-boot", "status": "passed",
                                "profile": profile, "guest_observation": firmware_observation.strip()})
        guest("printf %s baseline > /root/nex-recovery-s0/disk-marker; sync")
        if running_capture:
            if profile != "bios":
                raise RuntimeError("running capture is available only for the BIOS profile")
            try:
                virsh("snapshot-create-as", name, "nex-recovery-s0-live-point", "--disk-only", "--atomic", "--quiesce",
                      "--diskspec", f"vda,snapshot=external,file={head}")
            except Exception as capture_error:
                recovery = {"status": "unknown"}
                try:
                    freeze_status = guest_freeze_status()
                    recovery["observed_freeze_status"] = freeze_status
                    if freeze_status == "frozen":
                        thaw = json.loads(virsh("qemu-agent-command", name, json.dumps({
                            "execute": "guest-fsfreeze-thaw"})).stdout)["return"]
                        recovery["thaw_result"] = thaw
                        recovery["observed_freeze_status_after_thaw"] = guest_freeze_status()
                    recovery["status"] = "thawed" if recovery.get("observed_freeze_status_after_thaw", freeze_status) == "thawed" else "frozen"
                except Exception as thaw_error:
                    recovery["error"] = str(thaw_error)
                result["quiesce_failure_recovery"] = recovery
                raise RuntimeError(f"quiesced snapshot failed: {capture_error}; freeze recovery: {recovery}") from capture_error
            result["snapshot_xml"] = virsh("snapshot-dumpxml", name, "nex-recovery-s0-live-point").stdout
            freeze_status = guest_freeze_status()
            result["guest_filesystem_freeze_after_capture"] = freeze_status
            if freeze_status != "thawed":
                raise RuntimeError("libvirt returned from quiesced snapshot while the guest filesystem remained frozen")
            guest("printf %s future > /root/nex-recovery-s0/disk-marker; sync")
            virsh("shutdown", name)
            wait_stopped()
            virsh("snapshot-revert", name, "nex-recovery-s0-live-point", "--force", "--paused")
            state_after_revert = domain_state()
            result["running_snapshot_revert_domain_state"] = state_after_revert
            if state_after_revert != "paused":
                if state_after_revert in ("running", "paused"):
                    virsh("destroy", name)
                    wait_stopped()
                    result["domain_state_after_stopping_unexpected_revert"] = domain_state()
                raise RuntimeError(f"running snapshot revert did not leave the guest paused: {state_after_revert}")
            virsh("destroy", name)
            wait_stopped()
            result["domain_state_after_stopping_paused_revert"] = domain_state()
            boot()
            marker = guest("test ! -d /sys/firmware/efi; cat /root/nex-recovery-s0/disk-marker")["stdout"].strip()
            if marker != "baseline":
                raise RuntimeError(f"running quiesced snapshot revert returned marker {marker!r}")
            result["steps"].append({"name": "running-quiesced-capture-revert", "status": "passed",
                                    "guest_marker": marker, "guest_filesystem_freeze_after_capture": freeze_status,
                                    "domain_state_after_revert": state_after_revert,
                                    "domain_state_after_stopping_paused_revert": result["domain_state_after_stopping_paused_revert"]})
            virsh("shutdown", name)
            wait_stopped()
            if preserve_snapshot:
                result["snapshot_delete"] = {"status": "not-run", "reason": "snapshot and firmware artifacts intentionally preserved"}
                result["artifacts_preserved"] = True
                result["status"] = "passed"
                return result
            deletion = virsh("snapshot-delete", name, "nex-recovery-s0-live-point", check=False)
            result["snapshot_delete"] = {"exit_code": deletion.returncode, "stdout": deletion.stdout.strip(), "stderr": deletion.stderr.strip()}
            if deletion.returncode:
                raise RuntimeError("libvirt refused running-capture snapshot deletion; preserving its artifacts")
            boot()
            if guest("test ! -d /sys/firmware/efi; cat /root/nex-recovery-s0/disk-marker")["stdout"].strip() != "baseline":
                raise RuntimeError("running-capture disk marker changed after snapshot deletion")
            virsh("shutdown", name)
            wait_stopped()
            result["steps"].append({"name": "running-capture-delete-and-reopen", "status": "passed"})
            result["status"] = "passed"
            return result

        virsh("shutdown", name)
        wait_stopped()
        baseline_state = {}
        if settings["uefi"]:
            if nvram is None or not nvram.is_file() or nvram.is_symlink():
                raise RuntimeError("runtime NVRAM was not materialized at the UUID path declared by firmware.go")
            saved_nvram = work / "saved-VARS.fd"
            copy_state(nvram, saved_nvram)
            baseline_state["nvram"] = state_manifest(nvram)
        if settings["tpm"]:
            if tpm is None or not tpm.is_dir() or tpm.is_symlink():
                raise RuntimeError("runtime swtpm state was not materialized at the UUID path declared by firmware.go")
            saved_tpm = work / "saved-swtpm"
            copy_state(tpm, saved_tpm)
            baseline_state["swtpm"] = state_manifest(tpm)
        result["state_at_snapshot"] = baseline_state
        virsh("snapshot-create-as", name, "nex-recovery-s0-point", "--disk-only", "--atomic", "--diskspec", f"vda,snapshot=external,file={head}")
        result["snapshot_xml"] = virsh("snapshot-dumpxml", name, "nex-recovery-s0-point").stdout
        boot()
        guest("printf %s future > /root/nex-recovery-s0/disk-marker; sync")
        if settings["tpm"]:
            guest("tpm2_createprimary -C o -G rsa -c /run/nex-s0-future-primary.ctx >/dev/null; " +
                  "printf %s future-secret > /run/nex-s0-future-secret; " +
                  "tpm2_create -C /run/nex-s0-future-primary.ctx -u /run/nex-s0-future.pub -r /run/nex-s0-future.priv -i /run/nex-s0-future-secret >/dev/null; " +
                  "tpm2_load -C /run/nex-s0-future-primary.ctx -u /run/nex-s0-future.pub -r /run/nex-s0-future.priv -c /run/nex-s0-future.ctx >/dev/null; " +
                  "tpm2_evictcontrol -C o -c /run/nex-s0-future.ctx 0x81010010 >/dev/null")
        if settings["uefi"]:
            efi_result = guest("set -- $(findmnt -n -o SOURCE /boot/efi); esp=$1; disk=/dev/$(lsblk -no PKNAME \"$esp\"); part=$(lsblk -no PARTN \"$esp\"); loader=$(find /boot/efi/EFI -type f \\( -iname shimx64.efi -o -iname grubx64.efi \\) | head -n1); test -n \"$disk\" -a -n \"$part\" -a -n \"$loader\"; loader=\\\\${loader#/boot/efi/}; loader=$(printf %s \"$loader\" | sed 's#/#\\\\#g'); efibootmgr --create --disk \"$disk\" --part \"$part\" --label 'Nex Recovery S0 future' --loader \"$loader\"; efibootmgr -v | grep 'Nex Recovery S0 future'", check=False)
            if efi_result["exitcode"]:
                raise RuntimeError("could not create the post-point EFI entry: " + efi_result["stderr"].strip())
        guest("sync")
        virsh("shutdown", name)
        wait_stopped()
        virsh("snapshot-revert", name, "nex-recovery-s0-point", "--force")
        result["domain_state_after_stopped_revert"] = domain_state()
        if result["domain_state_after_stopped_revert"] != "shut off":
            raise RuntimeError("stopped snapshot revert did not leave the guest shut off before paired firmware-state verification")
        restored_native = {key: state_manifest(path) for key, path in (("nvram", nvram), ("swtpm", tpm)) if path is not None}
        result["state_after_native_revert"] = restored_native
        native_matches = restored_native == baseline_state
        result["native_revert_restored_firmware_and_tpm_files"] = native_matches
        if not native_matches:
            if settings["uefi"]:
                restore_state(saved_nvram, nvram)
            if settings["tpm"]:
                restore_state(saved_tpm, tpm)
            paired_state = {key: state_manifest(path) for key, path in (("nvram", nvram), ("swtpm", tpm)) if path is not None}
            result["paired_state_restore"] = paired_state
            if paired_state != baseline_state:
                raise RuntimeError("paired firmware-state restore did not reproduce baseline data and metadata")
        boot()
        guest("test \"$(cat /root/nex-recovery-s0/disk-marker)\" = baseline")
        if settings["tpm"]:
            guest("tpm2_createprimary -C o -G rsa -c /run/nex-s0-primary.ctx >/dev/null; " +
                  "tpm2_load -C /run/nex-s0-primary.ctx -u /root/nex-recovery-s0/sealed.pub -r /root/nex-recovery-s0/sealed.priv -c /run/nex-s0-sealed.ctx >/dev/null; " +
                  "test \"$(tpm2_unseal -c /run/nex-s0-sealed.ctx)\" = 'nex recovery S0 test secret'; " +
                  "! tpm2_readpublic -c 0x81010010 >/dev/null 2>&1")
        if settings["uefi"]:
            efi_entries_after_rollback = guest("efibootmgr -v")["stdout"]
            result["efi_boot_entries_after_rollback"] = efi_entries_after_rollback.strip()
            if "Nex Recovery S0 future" in efi_entries_after_rollback:
                raise RuntimeError("future EFI entry remained after disk and firmware-state rollback")
            after = secure_boot_observation("after-rollback")
        else:
            after = guest("test ! -d /sys/firmware/efi; printf 'BIOS boot observed'")["stdout"]
        if not settings["tpm"]:
            guest("test ! -e /dev/tpm0 && test ! -e /dev/tpmrm0")
        if settings["uefi"]:
            if settings["secure_boot"]:
                assert "SecureBoot enabled" in after, "guest did not observe Secure Boot enabled after rollback"
        result["post_rollback"] = {"disk_marker": "baseline", "sealed_secret_unsealed": settings["tpm"],
                                   "firmware_guest_observation": after.strip()}
        result["steps"].append({"name": "stopped-snapshot-revert-profile", "status": "passed",
                                "native_firmware_restore": native_matches, "paired_restore_used": not native_matches})
        virsh("shutdown", name)
        wait_stopped()
        if preserve_snapshot:
            result["snapshot_delete"] = {"status": "not-run", "reason": "snapshot and firmware artifacts intentionally preserved"}
            result["artifacts_preserved"] = True
            result["status"] = "passed"
            return result
        deletion = virsh("snapshot-delete", name, "nex-recovery-s0-point", check=False)
        result["snapshot_delete"] = {"exit_code": deletion.returncode, "stdout": deletion.stdout.strip(), "stderr": deletion.stderr.strip()}
        if deletion.returncode:
            raise RuntimeError("libvirt refused snapshot deletion; preserving its artifacts")
        boot()
        guest("test \"$(cat /root/nex-recovery-s0/disk-marker)\" = baseline")
        if settings["tpm"]:
            guest("tpm2_createprimary -C o -G rsa -c /run/nex-s0-primary.ctx >/dev/null; " +
                  "tpm2_load -C /run/nex-s0-primary.ctx -u /root/nex-recovery-s0/sealed.pub -r /root/nex-recovery-s0/sealed.priv -c /run/nex-s0-sealed.ctx >/dev/null; " +
                  "test \"$(tpm2_unseal -c /run/nex-s0-sealed.ctx)\" = 'nex recovery S0 test secret'; " +
                  "! tpm2_readpublic -c 0x81010010 >/dev/null 2>&1")
        if settings["uefi"]:
            efi_entries_after_delete = guest("efibootmgr -v")["stdout"]
            result["efi_boot_entries_after_delete"] = efi_entries_after_delete.strip()
            if "Nex Recovery S0 future" in efi_entries_after_delete:
                raise RuntimeError("future EFI entry remained after snapshot deletion")
            post_delete = secure_boot_observation("after-delete-and-reboot")
        else:
            post_delete = guest("test ! -d /sys/firmware/efi; printf 'BIOS boot observed'")["stdout"]
        if not settings["tpm"]:
            guest("test ! -e /dev/tpm0 && test ! -e /dev/tpmrm0")
        if settings["uefi"]:
            if settings["secure_boot"] and "SecureBoot enabled" not in post_delete:
                raise RuntimeError("guest did not observe Secure Boot enabled after snapshot deletion")
        result["post_delete_guest_observation"] = post_delete.strip()
        virsh("shutdown", name)
        wait_stopped()
        result["steps"].append({"name": "snapshot-delete-and-reopen", "status": "passed"})
        result["status"] = "passed"
    except Exception as exc:
        result["status"] = "failed"
        result["error"] = str(exc)
    finally:
        if result["status"] == "passed" and preserve_snapshot:
            result["cleanup"] = "artifacts-preserved"
        elif result["status"] == "passed":
            state = virsh("domstate", name, check=False)
            if state.returncode == 0 and state.stdout.strip().lower() != "shut off":
                result["status"] = "incomplete"
                result["cleanup"] = "preserved-running-domain"
            else:
                undefine_args = ["undefine", name, "--snapshots-metadata"]
                if settings["uefi"]:
                    undefine_args.append("--nvram")
                if settings["tpm"]:
                    undefine_args.append("--tpm")
                undefine = virsh(*undefine_args, check=False)
                if undefine.returncode or virsh("dominfo", name, check=False).returncode == 0:
                    result["status"] = "incomplete"
                    result["cleanup"] = "preserved-uncertain-undefine"
                else:
                    nvram_present = nvram is not None and nvram.exists()
                    tpm_present = tpm is not None and tpm.exists()
                    result["cleanup"] = {"domain_undefined": True, "nvram_removed": not nvram_present,
                                         "swtpm_state_removed": not tpm_present}
                    if nvram_present or tpm_present:
                        result["status"] = "incomplete"
                    else:
                        shutil.rmtree(work)
    return result


def inner_public_deletion_probe(cfg):
    """Stopped Secure Boot/vTPM point-A public deletion with paired point-B restore."""
    import base64
    import grp
    import pwd
    import xml.etree.ElementTree as ET

    work = Path(cfg["work"])
    receipt_path = Path(cfg["receipt_path"])
    base = Path(cfg["base"])
    if public_delete_domain_type(cfg["profile"], cfg["domain_type"]) != "qemu":
        raise RuntimeError("unexpected public deletion domain type")
    if (work.parent != Path("/var/lib/libvirt/images/nex-recovery-s0-firmware") or
            not work.is_dir() or work.is_symlink() or
            (work / "sentinel").read_text() != SENTINEL or
            receipt_path.parent != work.parent or receipt_path.exists() or receipt_path.is_symlink() or
            not base.is_file() or base.is_symlink()):
        raise RuntimeError("selected disposable firmware fixture is invalid")
    if cfg.get("candidate_manifest_sha256") != NEX3_MANIFEST_SHA256:
        raise RuntimeError("selected +nex3 candidate manifest is not the approved firmware proof input")
    runtime_packages = ("qemu-system-x86", "qemu-utils", "ovmf", "swtpm", "swtpm-tools")
    packages = (*NEX3_LIBVIRT_PACKAGES, *runtime_packages)
    versions = run(["dpkg-query", "-W", "-f=${binary:Package}=${Version}\\n", *packages])
    observed_versions = {package.split(":", 1)[0]: version
                         for package, version in (line.split("=", 1) for line in versions.stdout.splitlines())}
    if any(observed_versions.get(package) != "11.3.0-3+deb13u3+nex3" for package in NEX3_LIBVIRT_PACKAGES):
        raise RuntimeError("installed libvirt package closure differs from the pinned +nex3 candidate")
    expected_runtime = {"qemu-system-x86": "1:10.0.13+ds-0+deb13u1",
                        "qemu-utils": "1:10.0.13+ds-0+deb13u1"}
    if any(observed_versions.get(package) != version for package, version in expected_runtime.items()):
        raise RuntimeError("QEMU runtime differs from the pinned +nex3 firmware test profile")
    candidate = {"manifest_sha256": cfg["candidate_manifest_sha256"], "packages": versions.stdout,
                 "virsh": run(["virsh", "--version"]).stdout.strip(),
                 "qemu": run(["qemu-system-x86_64", "--version"]).stdout.splitlines()[0],
                 "domcapabilities": run(["virsh", "-c", "qemu:///system", "domcapabilities",
                                         "--virttype", "qemu", "--arch", "x86_64",
                                         "--machine", "q35"]).stdout}
    caps = candidate["domcapabilities"]
    if "efi" not in caps or "emulator" not in caps or "tpm-crb" not in caps:
        raise RuntimeError("qemu TCG domain capabilities lack required UEFI/vTPM devices")
    descriptor = Path("/usr/share/qemu/firmware/40-edk2-x86_64-secure-enrolled.json")
    if not descriptor.is_file() or "secure-boot" not in descriptor.read_text() or "enrolled-keys" not in descriptor.read_text():
        raise RuntimeError("Secure Boot enrolled-key firmware descriptor is unavailable")

    name = "nex-recovery-s0-fw-delete-" + uuid.uuid4().hex[:10]
    vm_uuid = str(uuid.uuid4())
    d = {"vda": work / "D-vda.qcow2", "vdb": work / "D-vdb.qcow2"}
    a = {"vda": work / "A-vda.qcow2", "vdb": work / "A-vdb.qcow2"}
    b = {"vda": work / "B-vda.qcow2", "vdb": work / "B-vdb.qcow2"}
    c = {"vda": work / "current-vda.qcow2", "vdb": work / "current-vdb.qcow2"}
    seed_dir = work / "seed"
    seed = work / "seed.iso"
    nvram = Path(f"/var/lib/libvirt/qemu/nvram/{vm_uuid}_VARS.fd")
    tpm = Path(f"/var/lib/libvirt/swtpm/{vm_uuid}")
    saved_nvram, saved_tpm = work / "point-b-VARS.fd", work / "point-b-swtpm"
    uid, gid = pwd.getpwnam("libvirt-qemu").pw_uid, grp.getgrnam("kvm").gr_gid
    result = {"status": "incomplete", "domain_type": "qemu", "candidate": candidate,
              "profile": "uefi-secure-boot-vtpm20", "steps": [], "snapshot_ids": ["point-a", "point-b"]}

    def virsh(*args, check=True):
        return run(["virsh", "-c", "qemu:///system", *args], check=check, timeout=300)

    def state_manifest(path):
        files = [path, *sorted(path.rglob("*"))] if path.is_dir() else [path]
        entries = []
        for item in files:
            info = item.lstat()
            if item.is_symlink():
                raise RuntimeError(f"unexpected symlink in firmware state: {item}")
            row = {"relative": str(item.relative_to(path)) if path.is_dir() else item.name,
                   "type": "directory" if item.is_dir() else "file", "mode": oct(stat.S_IMODE(info.st_mode)),
                   "uid": info.st_uid, "gid": info.st_gid}
            if item.is_file():
                row.update(size=info.st_size, sha256=hashlib.file_digest(item.open("rb"), "sha256").hexdigest())
            entries.append(row)
        return entries

    def copy_state(source, target):
        if target.exists() or target.is_symlink():
            raise RuntimeError(f"refusing to overwrite saved firmware state {target}")
        if source.is_dir():
            shutil.copytree(source, target, copy_function=shutil.copy2)
        elif source.is_file() and not source.is_symlink():
            shutil.copy2(source, target)
        else:
            raise RuntimeError(f"missing firmware state {source}")
        for dst in ([target, *target.rglob("*")] if target.is_dir() else [target]):
            src = source / dst.relative_to(target) if source.is_dir() else source
            os.chown(dst, src.stat().st_uid, src.stat().st_gid)
            os.chmod(dst, stat.S_IMODE(src.stat().st_mode))

    def restore_state(saved, target):
        if target.is_dir():
            shutil.rmtree(target)
        elif target.exists():
            target.unlink()
        copy_state(saved, target)

    def guest(command, *, check=True, timeout=300):
        response = json.loads(virsh("qemu-agent-command", name, json.dumps({"execute": "guest-exec", "arguments": {
            "path": "/bin/sh", "arg": ["-ec", command], "capture-output": True}})).stdout)["return"]
        pid = response["pid"]
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            status = json.loads(virsh("qemu-agent-command", name, json.dumps({"execute": "guest-exec-status", "arguments": {"pid": pid}})).stdout)["return"]
            if status.get("exited"):
                out = base64.b64decode(status.get("out-data", "")).decode(errors="replace")
                err = base64.b64decode(status.get("err-data", "")).decode(errors="replace")
                if check and status.get("exitcode") != 0:
                    raise RuntimeError("guest command failed: " + err.strip())
                return {"exitcode": status.get("exitcode"), "stdout": out, "stderr": err}
            time.sleep(1)
        raise TimeoutError("guest command did not complete")

    def wait_stopped():
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            state = virsh("domstate", name).stdout.strip().lower()
            if state == "shut off":
                return
            time.sleep(1)
        raise TimeoutError("firmware deletion VM did not stop")

    def boot():
        if virsh("domstate", name).stdout.strip().lower() == "shut off":
            virsh("start", name)
        deadline = time.monotonic() + 300
        while time.monotonic() < deadline:
            try:
                guest("systemctl is-active --quiet qemu-guest-agent")
                return
            except Exception:
                time.sleep(2)
        raise TimeoutError("guest agent was not ready")

    def snapshot_docs():
        labels = [line for line in virsh("snapshot-list", name, "--name").stdout.splitlines() if line]
        return {label: virsh("snapshot-dumpxml", name, label).stdout for label in labels}

    try:
        base_info = json.loads(run(["qemu-img", "info", "--output=json", base]).stdout)
        result["base_image"] = {"format": base_info.get("format"), "virtual_size": base_info.get("virtual-size")}
        run(["qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", base, d["vda"], "16G"])
        run(["qemu-img", "create", "-f", "qcow2", d["vdb"], "512M"])
        for path in d.values():
            os.chown(path, uid, gid)
        seed_dir.mkdir()
        key = cfg["public_key"]
        user_data, meta_data = firmware_cloud_init(name, key)
        (seed_dir / "user-data").write_text(user_data)
        (seed_dir / "meta-data").write_text(meta_data)
        run(["genisoimage", "-quiet", "-output", seed, "-volid", "cidata", "-joliet", "-rock", seed_dir])
        xml = public_firmware_domain_xml(name, vm_uuid, nvram, d["vda"], d["vdb"], seed)
        definition = work / "domain.xml"
        definition.write_text(xml)
        validator = shutil.which("virt-xml-validate")
        if validator:
            run([validator, str(definition), "domain"])
        if virsh("dominfo", name, check=False).returncode == 0:
            raise RuntimeError("generated domain name already exists")
        virsh("define", definition)
        result["uuid"] = virsh("domuuid", name).stdout.strip()
        boot()
        guest("test -d /sys/firmware/efi; mkfs.ext4 -F /dev/vdb >/dev/null; mkdir -p /mnt/recovery; mount /dev/vdb /mnt/recovery; test \"$(findmnt -n -o SOURCE /mnt/recovery)\" = /dev/vdb; printf %s base-root > /root/recovery-marker; printf %s base-data > /mnt/recovery/recovery-marker; sync")
        secure_boot = guest("python3 -c 'import glob,pathlib; p=glob.glob(\"/sys/firmware/efi/efivars/SecureBoot-*\"); print(pathlib.Path(p[0]).read_bytes()[4])'; mokutil --sb-state")
        if not secure_boot_is_enabled(secure_boot["stdout"]):
            raise RuntimeError("initial guest did not report Secure Boot enabled")
        secret = "nex firmware point-b retained secret"
        guest("printf %s " + shlex.quote(secret) + " > /root/point-b-secret; tpm2_createprimary -C o -G rsa -c /run/s0-primary.ctx >/dev/null; tpm2_create -C /run/s0-primary.ctx -u /root/point-b.pub -r /root/point-b.priv -i /root/point-b-secret >/dev/null")
        virsh("shutdown", name); wait_stopped()
        virsh("snapshot-create-as", name, "point-a", "--disk-only", "--atomic",
              "--diskspec", f"vda,snapshot=external,file={a['vda']}",
              "--diskspec", f"vdb,snapshot=external,file={a['vdb']}")
        boot()
        guest(f"{firmware_data_disk_mount_command()}; printf %s point-b-root > /root/recovery-marker; printf %s point-b-data > /mnt/recovery/recovery-marker; sync")
        virsh("shutdown", name); wait_stopped()
        virsh("snapshot-create-as", name, "point-b", "--disk-only", "--atomic",
              "--diskspec", f"vda,snapshot=external,file={b['vda']}",
              "--diskspec", f"vdb,snapshot=external,file={b['vdb']}")
        copy_state(nvram, saved_nvram); copy_state(tpm, saved_tpm)
        point_b_state = {"nvram": state_manifest(nvram), "swtpm": state_manifest(tpm)}
        virsh(*public_current_head_argv(name, {target: str(path) for target, path in c.items()}))
        boot()
        guest(f"{firmware_data_disk_mount_command()}; printf %s future-root > /root/recovery-marker; printf %s future-data > /mnt/recovery/recovery-marker; sync")
        guest("printf future-secret > /run/future-secret; tpm2_createprimary -C o -G rsa -c /run/future-primary.ctx >/dev/null; tpm2_create -C /run/future-primary.ctx -u /run/future.pub -r /run/future.priv -i /run/future-secret >/dev/null; tpm2_load -C /run/future-primary.ctx -u /run/future.pub -r /run/future.priv -c /run/future.ctx >/dev/null; tpm2_evictcontrol -C o -c /run/future.ctx 0x81010010 >/dev/null")
        future_efi = guest("set -- $(findmnt -n -o SOURCE /boot/efi); esp=$1; disk=/dev/$(lsblk -no PKNAME \"$esp\"); part=$(lsblk -no PARTN \"$esp\"); loader=$(find /boot/efi/EFI -type f \\( -iname shimx64.efi -o -iname grubx64.efi \\) | head -n1); loader=${loader#/boot/efi/}; loader=$(printf %s \"$loader\" | sed 's#/#\\\\#g'); efibootmgr --create --disk \"$disk\" --part \"$part\" --label 'Nex future point' --loader \"$loader\"; efibootmgr -v | grep 'Nex future point'")
        virsh("shutdown", name); wait_stopped()
        result["point_b_firmware_state"] = point_b_state
        docs = snapshot_docs()
        if virsh("snapshot-current", name, "--name").stdout.strip() != "point-b":
            raise RuntimeError("point B is not the current metadata snapshot before consolidation")
        refs = {"vda": str(a["vda"]), "vdb": str(a["vdb"])}
        bases = {"vda": str(d["vda"]), "vdb": str(d["vdb"])}
        mapping = verify_firmware_snapshot_mapping(
            docs, bases, refs, {target: str(b[target]) for target in ("vda", "vdb")})
        result["snapshot_xml_before"] = docs
        result["snapshot_mapping_before"] = mapping
        result["steps"].append({"name": "stopped-two-point-capture-and-mapping", "status": "passed"})
        a_references, b_references, c_references = {}, {}, {}
        for target in ("vda", "vdb"):
            a_references[target] = work / f"before-commit-{target}.qcow2"
            b_references[target] = work / f"before-rebase-{target}.qcow2"
            c_references[target] = work / f"before-consolidation-current-{target}.qcow2"
            run(["qemu-img", "convert", "-f", "qcow2", "-O", "qcow2", refs[target], a_references[target]])
            run(["qemu-img", "convert", "-f", "qcow2", "-O", "qcow2", b[target], b_references[target]])
            run(["qemu-img", "convert", "-f", "qcow2", "-O", "qcow2", c[target], c_references[target]])
        result["logical_reference_checks"] = {}
        for target, images in (("vda", (refs["vda"], b["vda"], c["vda"])),
                               ("vdb", (refs["vdb"], b["vdb"], c["vdb"]))):
            references = (a_references[target], b_references[target], c_references[target])
            result["logical_reference_checks"][target] = {}
            for label, image, reference in zip(("point_a", "point_b", "current_c"), images, references):
                comparison = run(["qemu-img", "compare", "-f", "qcow2", "-F", "qcow2", image, reference])
                result["logical_reference_checks"][target][label] = firmware_compare_receipt(
                    ["qemu-img", "compare", "-f", "qcow2", "-F", "qcow2", image, reference], comparison)
        result["steps"].append({"name": "complete-logical-reference-images-verified", "status": "passed"})
        virsh("start", name)
        virsh("suspend", name)
        if virsh("domstate", name).stdout.strip().lower() != "paused":
            raise RuntimeError("public blockcommit did not start from a paused guest")
        pid_file = Path("/run/libvirt/qemu") / f"{name}.pid"
        qemu_pid = pid_file.read_text().strip()
        if not qemu_pid.isdigit():
            raise RuntimeError("cannot identify the QEMU process before public blockcommit")
        confinement_path = Path("/proc") / qemu_pid / "attr/current"
        confinement = confinement_path.read_text().strip()
        expected_confinement = require_qemu_confinement(confinement, vm_uuid)
        result["qemu_confinement"] = {"pid": qemu_pid, "expected": expected_confinement,
                                      "observed": confinement}
        for target in ("vda", "vdb"):
            command = public_blockcommit_argv(name, target, refs[target], bases[target])
            commit = run(["virsh", "-c", "qemu:///system", *command], check=False, timeout=600)
            result.setdefault("blockcommit", {})[target] = {"argv": command, "returncode": commit.returncode,
                "stdout": commit.stdout, "stderr": commit.stderr, "initial_state": "paused"}
            if commit.returncode:
                raise RuntimeError(f"public blockcommit failed for {target}: {commit.stderr.strip()}")
            job = virsh("blockjob", name, target, "--info", check=False)
            result["blockcommit"][target]["job_after"] = {"returncode": job.returncode,
                "stdout": job.stdout, "stderr": job.stderr}
            if "block commit" in (job.stdout + job.stderr).lower():
                raise RuntimeError(f"blockcommit job remains active for {target}")
            if (virsh("domstate", name).stdout.strip().lower() != "paused" or
                    pid_file.read_text().strip() != qemu_pid):
                raise RuntimeError("QEMU identity or paused state changed during public blockcommit")
            comparison = run(["qemu-img", "compare", "-f", "qcow2", "-F", "qcow2", bases[target], a_references[target]])
            result["logical_reference_checks"][target]["committed_d_equals_point_a"] = firmware_compare_receipt(
                ["qemu-img", "compare", "-f", "qcow2", "-F", "qcow2", bases[target], a_references[target]],
                comparison)
        virsh("destroy", name)
        wait_stopped()
        result["steps"].append({"name": "paused-two-disk-public-no-delete-blockcommit", "status": "passed"})
        for target in ("vda", "vdb"):
            run(["qemu-img", "rebase", "-u", "-f", "qcow2", "-F", "qcow2", "-b", bases[target], b[target]])
            run(["qemu-img", "check", b[target]])
            comparison = run(["qemu-img", "compare", "-f", "qcow2", "-F", "qcow2", b[target], b_references[target]])
            result["logical_reference_checks"][target]["rebased_b_equals_point_b"] = firmware_compare_receipt(
                ["qemu-img", "compare", "-f", "qcow2", "-F", "qcow2", b[target], b_references[target]],
                comparison)
            chain = json.loads(run(["qemu-img", "info", "--backing-chain", "--output=json", b[target]]).stdout)
            chain_paths = [item.get("filename") for item in chain]
            d_chain = json.loads(run(["qemu-img", "info", "--backing-chain", "--output=json", bases[target]]).stdout)
            d_chain_paths = [item.get("filename") for item in d_chain]
            if not d_chain_paths or d_chain_paths[0] != bases[target]:
                raise RuntimeError(f"postcommit D {target} chain does not begin at the exact selected source: {d_chain_paths}")
            result.setdefault("postcommit_d_backing_chains", {})[target] = d_chain_paths
            expected_b_chain = firmware_chain_with_backing(b[target], d_chain_paths)
            if chain_paths != expected_b_chain:
                raise RuntimeError(f"rebased point-B {target} chain differs from B over the full D suffix: {chain_paths}")
            current_chain = json.loads(run(["qemu-img", "info", "--backing-chain", "--output=json", c[target]]).stdout)
            current_paths = [item.get("filename") for item in current_chain]
            expected_current_chain = firmware_chain_with_backing(c[target], expected_b_chain)
            if current_paths != expected_current_chain:
                raise RuntimeError(f"current C {target} chain differs from C over B over the full D suffix: {current_paths}")
        b_xml, changed = rewrite_firmware_retained_snapshot(docs["point-b"], snapshot_name="point-b",
            expected_parent="point-a", a_sources=refs, d_sources=bases)
        if set(changed) != {"vda", "vdb"}:
            raise RuntimeError("retained B snapshot did not rewrite both A sources")
        b_xml_file = work / "point-b-reparented.xml"
        b_xml_file.write_text(b_xml)
        virsh("snapshot-create", name, b_xml_file, "--redefine")
        docs = snapshot_docs()
        if virsh("snapshot-current", name, "--name").stdout.strip() != "point-b":
            raise RuntimeError("point-B redefinition changed the current snapshot identity")
        actual_b = ET.fromstring(docs["point-b"])
        if actual_b.findtext("./parent/name") is not None:
            raise RuntimeError("actual point-B snapshot XML still has point-A as parent")
        actual_saved, actual_post = firmware_snapshot_disk_paths(docs["point-b"])
        expected_b = {target: str(b[target]) for target in ("vda", "vdb")}
        if actual_saved != bases or actual_post != expected_b:
            raise RuntimeError(f"actual point-B saved/post sources differ after redefine: {(actual_saved, actual_post)}")
        result["snapshot_xml_redefined"] = docs
        result["steps"].append({"name": "point-b-reparented-and-rebased-over-d", "status": "passed"})
        deleted = virsh("snapshot-delete", name, "point-a", "--metadata", check=False)
        result["metadata_delete"] = {"argv": ["snapshot-delete", name, "point-a", "--metadata"],
                                     "returncode": deleted.returncode, "stdout": deleted.stdout, "stderr": deleted.stderr}
        if deleted.returncode:
            raise RuntimeError("metadata-only point-A deletion failed: " + deleted.stderr.strip())
        docs = snapshot_docs()
        if set(docs) != {"point-b"} or any(str(path) in json.dumps(docs) for path in a.values()):
            raise RuntimeError("actual retained snapshot inventory still references point A")
        if virsh("snapshot-current", name, "--name").stdout.strip() != "point-b":
            raise RuntimeError("metadata-only A deletion changed the current point-B identity")
        inactive = virsh("dumpxml", name, "--inactive").stdout
        chains = {}
        for target in ("vda", "vdb"):
            comparison = run(["qemu-img", "compare", "-f", "qcow2", "-F", "qcow2", c[target], c_references[target]])
            result["logical_reference_checks"][target]["current_c_equals_preconsolidation"] = firmware_compare_receipt(
                ["qemu-img", "compare", "-f", "qcow2", "-F", "qcow2", c[target], c_references[target]],
                comparison)
            chains[target] = {"current": json.loads(run(["qemu-img", "info", "--backing-chain", "--output=json", c[target]]).stdout),
                              "point_b": json.loads(run(["qemu-img", "info", "--backing-chain", "--output=json", b[target]]).stdout),
                              "current_reference": str(c_references[target])}
            suffix = result["postcommit_d_backing_chains"][target]
            if [image.get("filename") for image in chains[target]["current"]] != firmware_chain_with_backing(c[target], firmware_chain_with_backing(b[target], suffix)):
                raise RuntimeError(f"stopped current C {target} chain changed after A deletion")
            if [image.get("filename") for image in chains[target]["point_b"]] != firmware_chain_with_backing(b[target], suffix):
                raise RuntimeError(f"stopped retained B {target} chain changed after A deletion")
        result["current_c_stopped_chain_equivalence"] = {
            "passed": True, "method": "byte-equivalent logical qemu-img compare against pre-consolidation C heads",
            "disks": {target: str(c_references[target].name) for target in ("vda", "vdb")}}
        invalid_a = {str(path) for path in a.values()}
        fixture_sources = [inactive]
        fixture_sources.extend(docs.values())
        fixture_sources.extend(virsh("dumpxml", name).stdout for _ in (0,))
        all_domains = [line for line in virsh("list", "--all", "--name").stdout.splitlines() if line]
        foreign_inventory = []
        for domain_name in all_domains:
            modes = []
            for suffix in ([], ["--inactive"]):
                xml_reply = virsh("dumpxml", domain_name, *suffix, check=False)
                if xml_reply.returncode:
                    raise RuntimeError(f"cannot inspect domain XML before A unlink: {domain_name}")
                xml = xml_reply.stdout
                if any(path in xml for path in invalid_a):
                    raise RuntimeError(f"domain XML still references a selected A top: {domain_name}")
                modes.append(xml)
                root = ET.fromstring(xml)
                for source in root.findall(".//devices/disk/source"):
                    candidate_path = source.get("file")
                    if not candidate_path:
                        continue
                    chain_reply = run(["qemu-img", "info", "--force-share", "--backing-chain", "--output=json", candidate_path], check=False)
                    if chain_reply.returncode:
                        raise RuntimeError(f"cannot inspect domain disk chain before A unlink: {candidate_path}")
                    if any(path in chain_reply.stdout for path in invalid_a):
                        raise RuntimeError(f"domain disk chain still references a selected A top: {candidate_path}")
            labels_reply = virsh("snapshot-list", domain_name, "--name", check=False)
            if labels_reply.returncode:
                raise RuntimeError(f"cannot enumerate domain snapshots before A unlink: {domain_name}")
            saved_points = {}
            for label in [line.strip() for line in labels_reply.stdout.splitlines() if line.strip()]:
                saved = virsh("snapshot-dumpxml", domain_name, label, check=False)
                if saved.returncode or any(path in saved.stdout for path in invalid_a):
                    raise RuntimeError(f"snapshot metadata still references A or is unreadable: {domain_name}/{label}")
                saved_root = ET.fromstring(saved.stdout)
                for source in saved_root.findall(".//domain/devices/disk/source"):
                    candidate_path = source.get("file")
                    if not candidate_path:
                        continue
                    chain_reply = run(["qemu-img", "info", "--force-share", "--backing-chain", "--output=json", candidate_path], check=False)
                    if chain_reply.returncode or any(path in chain_reply.stdout for path in invalid_a):
                        raise RuntimeError(f"saved snapshot chain references A or is unreadable: {domain_name}/{label}/{candidate_path}")
                saved_points[label] = saved.stdout
            if domain_name != name:
                foreign_state = virsh("domstate", domain_name, check=False)
                if foreign_state.returncode or foreign_state.stdout.strip().lower() != "shut off":
                    raise RuntimeError(f"foreign domain state is not safely inspectable: {domain_name}")
            foreign_inventory.append({"domain": domain_name, "definitions": modes, "snapshots": saved_points})
        inventory = json.dumps({"fixture_xml": fixture_sources, "snapshots": docs,
                                "inactive_xml": inactive, "chains": chains, "domains": foreign_inventory})
        if any(path in inventory for path in invalid_a):
            raise RuntimeError("pre-unlink full reference scan found an A reference")
        result["pre_unlink_reference_scan"] = {"passed": True, "snapshot_xml": docs,
            "inactive_domain_xml": inactive, "qemu_img_chains": chains,
            "scope": "all domain live/inactive XML and snapshot XML, source chains and fixture current chains",
            "domains": foreign_inventory}
        unlinked = []
        for path in a.values():
            size = exact_unlink_precondition(path, work)
            path.unlink()
            if path.exists():
                raise RuntimeError(f"exact A top remains after unlink: {path.name}")
            unlinked.append({"name": path.name, "size": size, "absent": True})
        virsh("snapshot-revert", name, "point-b", "--force")
        if virsh("domstate", name).stdout.strip().lower() != "shut off":
            raise RuntimeError("point-B revert did not leave VM stopped")
        restore_state(saved_nvram, nvram); restore_state(saved_tpm, tpm)
        restored_state = {"nvram": state_manifest(nvram), "swtpm": state_manifest(tpm)}
        if restored_state != point_b_state:
            raise RuntimeError("paired point-B NVRAM/swtpm restore did not match saved data and metadata")
        boot()
        markers = guest(f"{firmware_data_disk_mount_command()}; {firmware_marker_readback_command()}")
        if markers["stdout"].splitlines() != ["source=/dev/vdb", "root=point-b-root", "data=point-b-data"]:
            raise RuntimeError("reopened point B did not restore both disk markers")
        sb = guest("python3 -c 'import glob,pathlib; p=glob.glob(\"/sys/firmware/efi/efivars/SecureBoot-*\"); print(pathlib.Path(p[0]).read_bytes()[4])'; mokutil --sb-state")
        if not secure_boot_is_enabled(sb["stdout"]):
            raise RuntimeError("reopened point B did not retain Secure Boot enabled")
        tpm_check = guest("tpm2_createprimary -C o -G rsa -c /run/reopen-primary.ctx >/dev/null; tpm2_load -C /run/reopen-primary.ctx -u /root/point-b.pub -r /root/point-b.priv -c /run/reopen-secret.ctx >/dev/null; test \"$(tpm2_unseal -c /run/reopen-secret.ctx)\" = '" + secret + "'; ! tpm2_readpublic -c 0x81010010 >/dev/null 2>&1")
        efi = guest("efibootmgr -v")
        if "Nex future point" in efi["stdout"]:
            raise RuntimeError("post-B EFI boot entry survived paired rollback")
        guest("test \"$(cat /root/recovery-marker)\" != future-root; test \"$(cat /mnt/recovery/recovery-marker)\" != future-data")
        result["reopen"] = {"disk_markers": markers["stdout"].strip(), "secure_boot": sb["stdout"].strip(),
            "sealed_secret_unsealed": True, "future_persistent_tpm_handle_absent": True,
            "future_efi_entry_absent": True, "paired_state": restored_state,
            "unlinked_a": unlinked, "tpm_observation": tpm_check["stdout"]}
        result["steps"].append({"name": "full-reference-scan-metadata-delete-unlink-paired-state-reopen", "status": "passed"})
        virsh("shutdown", name); wait_stopped()
        result["status"] = "passed"
    except Exception as exc:
        result["status"] = "failed"
        result["error"] = str(exc)
    finally:
        receipt_persisted = False
        result["cleanup"] = {"status": "pending"}
        try:
            persist_firmware_receipt(receipt_path, result)
            result["operation_receipt"] = {"path": receipt_path.name, "persisted_before_cleanup": True}
            receipt_persisted = True
        except (OSError, TypeError, ValueError) as exc:
            fields = unserializable_firmware_paths(result)
            try:
                state = virsh("domstate", name, check=False).stdout.strip()
            except Exception as state_error:
                state = "unavailable: " + str(state_error)
            result = {"status": "incomplete",
                      "error": "could not persist pre-cleanup firmware result: " + str(exc),
                      "unserializable_fields": fields,
                      "native_result_persisted": False,
                      "cleanup": {"work_preserved": True, "domain_state": state}}
        if receipt_persisted:
            if result["status"] == "passed":
                undefine = virsh("undefine", name, "--snapshots-metadata", "--nvram", "--tpm", check=False)
                result["cleanup"] = {"undefine_returncode": undefine.returncode,
                                     "domain_absent": virsh("dominfo", name, check=False).returncode != 0,
                                     "work_preserved": True}
                if undefine.returncode or not result["cleanup"]["domain_absent"]:
                    result["status"] = "incomplete"
                else:
                    shutil.rmtree(work)
                    result["cleanup"]["work_preserved"] = False
            else:
                result["cleanup"] = {"work_preserved": True, "domain_state": virsh("domstate", name, check=False).stdout.strip()}
            try:
                persist_firmware_receipt(receipt_path, result)
            except (OSError, TypeError, ValueError) as exc:
                result["status"] = "incomplete"
                result["error"] = "could not persist post-cleanup firmware result: " + str(exc)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lab-dir", type=Path, required=True)
    parser.add_argument("--node", choices=("core1", "core2", "core3"), required=True)
    parser.add_argument("--evidence", type=Path, required=True, help="private ignored .cache directory")
    parser.add_argument("--profile", choices=tuple(PROFILES), default=DEFAULT_PROFILE)
    parser.add_argument("--public-deletion", action="store_true",
                        help="run the stopped Secure Boot/vTPM two-disk public-deletion proof under qemu TCG")
    parser.add_argument("--candidate-manifest-sha256", default="",
                        help="required exact selected libvirt candidate manifest hash for public deletion")
    parser.add_argument("--candidate-dir", type=Path,
                        help="selected +nex3 candidate directory; required for public deletion")
    parser.add_argument("--running-capture", action="store_true", help="also test a quiesced live disk snapshot (BIOS profile only)")
    parser.add_argument("--preserve-snapshot", action="store_true", help="stop after capture/revert verification; preserve the domain, snapshot and firmware files without deletion")
    args = parser.parse_args()
    if args.running_capture and args.profile != "bios":
        parser.error("--running-capture is supported only with --profile bios")
    if args.public_deletion:
        try:
            public_delete_domain_type(args.profile, "qemu")
        except ValueError as exc:
            parser.error(str(exc))
        if args.candidate_manifest_sha256 != NEX3_MANIFEST_SHA256:
            parser.error("--public-deletion requires the pinned +nex3 candidate manifest SHA256")
        if args.candidate_dir is None:
            parser.error("--public-deletion requires --candidate-dir")
        try:
            candidate_receipt = verify_public_candidate(args.candidate_dir)
        except (OSError, ValueError, RuntimeError) as exc:
            parser.error(str(exc))
    else:
        candidate_receipt = None
    lab = validate_lab(args.lab_dir)
    assert not args.evidence.exists(), "evidence directory must be new"
    assert BASE.is_file() and not BASE.is_symlink(), "verified read-only Debian cloud image is missing"
    if hashlib.file_digest(BASE.open("rb"), "sha256").hexdigest() != BASE_SHA256:
        raise RuntimeError("cached Debian cloud image digest differs from the verified fixture")
    evidence = args.evidence.resolve()
    assert evidence.is_relative_to((ROOT / ".cache").resolve()), "raw evidence must remain in ignored .cache"
    run_id = uuid.uuid4().hex
    work = f"/var/lib/libvirt/images/nex-recovery-s0-firmware/{run_id}"
    receipt_path = f"/var/lib/libvirt/images/nex-recovery-s0-firmware/{run_id}.result.json"
    local_key = (lab / "id_ed25519.pub").read_text().strip()
    remote_cfg = {"work": work, "base": f"{work}/base.qcow2", "public_key": local_key,
                  "profile": args.profile, "running_capture": args.running_capture,
                  "preserve_snapshot": args.preserve_snapshot,
                  "public_deletion": args.public_deletion, "domain_type": "qemu" if args.public_deletion else "kvm",
                  "candidate_manifest_sha256": args.candidate_manifest_sha256, "receipt_path": receipt_path}
    ssh = ssh_args(lab, args.node)
    run(ssh + ["set -eu; mkdir -p /var/lib/libvirt/images/nex-recovery-s0-firmware; mkdir -m 0711 " + shlex.quote(work) +
               "; printf %s " + shlex.quote(SENTINEL) + " > " + shlex.quote(work + "/sentinel")])
    run(["scp", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
         "-o", f"UserKnownHostsFile={lab / 'known_hosts'}", "-i", str(lab / "id_ed25519"),
         "-P", str(json.loads((lab / "ports.json").read_text())[args.node]), str(BASE),
         f"root@127.0.0.1:{work}/base.qcow2"], timeout=600)
    source = (Path(__file__).read_text()
              .replace("ROOT = Path(__file__).resolve().parents[2]", "ROOT = Path('/')")
              .replace("REMOTE_CONFIG = None", f"REMOTE_CONFIG = {remote_cfg!r}"))
    evidence.mkdir(parents=True, mode=0o700)
    reply = run(ssh + ["python3", "-"], input=source, check=False, timeout=1800)
    (evidence / "remote.stdout").write_text(reply.stdout)
    (evidence / "remote.stderr").write_text(reply.stderr)
    try:
        result = json.loads(reply.stdout)
    except json.JSONDecodeError as exc:
        if not args.public_deletion:
            raise RuntimeError(f"remote firmware probe produced invalid evidence; private output: {evidence}") from exc
        receipt = run(ssh + ["cat", "--", receipt_path], check=False)
        if receipt.returncode:
            raise RuntimeError(f"remote firmware probe produced invalid evidence and no pre-cleanup receipt; private output: {evidence}") from exc
        (evidence / "pre-cleanup-result.json").write_text(receipt.stdout)
        result = json.loads(receipt.stdout)
        result["status"] = "incomplete"
        result["final_result_serialization"] = "failed after pre-cleanup receipt was persisted"
        (evidence / "result.json").write_text(json.dumps(result, indent=2) + "\n")
        raise RuntimeError(f"remote firmware probe final result was invalid; pre-cleanup receipt preserved at {evidence}") from exc
    (evidence / "result.json").write_text(json.dumps(result, indent=2) + "\n")
    if args.public_deletion:
        receipt = run(ssh + ["cat", "--", receipt_path], check=False)
        if receipt.returncode:
            if result.get("status") == "passed":
                raise RuntimeError(f"pre-cleanup firmware receipt is unavailable; private output: {evidence}")
            result["pre_cleanup_receipt"] = {"available": False}
        else:
            (evidence / "pre-cleanup-result.json").write_text(receipt.stdout)
            result["pre_cleanup_receipt"] = {"available": True}
            if result.get("status") == "passed":
                run(ssh + ["rm", "--", receipt_path], check=False)
    if args.public_deletion:
        result["selected_candidate"] = candidate_receipt
        (evidence / "result.json").write_text(json.dumps(result, indent=2) + "\n")
    if reply.returncode or result.get("status") != "passed":
        raise RuntimeError(f"remote firmware probe {result.get('status', 'failed')}; private evidence: {evidence}")
    print(evidence)


if __name__ == "__main__":
    if REMOTE_CONFIG is None:
        main()
    else:
        result = inner_probe(REMOTE_CONFIG)
        print(json.dumps(result))
        if result.get("status") != "passed":
            sys.exit(1)
