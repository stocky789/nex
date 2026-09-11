#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Boot one private Debian Core-lab overlay for storage repository QA.

The Debian cloud image remains read-only.  The new qcow2 overlay, NoCloud seed,
SSH key, QMP socket and loopback-forwarded port all live below ``.cache``.
No workstation mount, libvirt service, disk or network is used.
"""
import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time


ROOT = Path(__file__).resolve().parents[2]
BASE = ROOT / ".cache/appliance/debian-13-genericcloud-amd64-20260831-2587.qcow2"


def port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def check_lab(value):
    lab = value.resolve(strict=True)
    assert lab.parent == (ROOT / ".cache").resolve()
    assert lab.name.startswith("nex-storage-repositories-lab.")
    assert not lab.is_symlink()
    for name in ("host.qcow2", "seed.iso", "id_ed25519", "known_hosts", "ports.json"):
        path = lab / name
        assert path.is_file() and not path.is_symlink(), name
    return lab


def docker(*args):
    return subprocess.run(["docker", "run", "--rm", "--user", f"{os.getuid()}:{os.getgid()}", *args], check=True,
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def ssh(lab, command, check=True):
    p = json.loads((lab / "ports.json").read_text())
    return subprocess.run(["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=3", "-o", "StrictHostKeyChecking=accept-new",
                           "-o", f"UserKnownHostsFile={lab / 'known_hosts'}", "-i", str(lab / "id_ed25519"),
                           "-p", str(p["ssh"]), "root@127.0.0.1", command], check=check, capture_output=True, text=True)


def prepare():
    assert BASE.is_file() and not BASE.is_symlink(), "missing cached Debian cloud image"
    lab = Path(tempfile.mkdtemp(prefix="nex-storage-repositories-lab.", dir=ROOT / ".cache"))
    lab.chmod(0o700)
    subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "nex-storage-lab", "-f", str(lab / "id_ed25519")], check=True)
    public_key = (lab / "id_ed25519.pub").read_text().strip()
    seed = lab / "seed"
    seed.mkdir(mode=0o700)
    # Do not place package credentials or backend passwords in the cloud-init seed.
    (seed / "user-data").write_text("#cloud-config\n"
        "hostname: nex-storage-lab\nmanage_etc_hosts: true\nssh_pwauth: false\n"
        "disable_root: false\nssh_authorized_keys:\n  - " + public_key + "\n")
    (seed / "meta-data").write_text("instance-id: nex-storage-repositories-lab\nlocal-hostname: nex-storage-lab\n")
    docker("-v", f"{BASE}:{BASE}:ro", "-v", f"{lab}:{lab}", "--entrypoint", "qemu-img", "nex-appliance-builder",
           "create", "-f", "qcow2", "-F", "qcow2", "-b", str(BASE), str(lab / "host.qcow2"), "64G")
    docker("-v", f"{lab}:{lab}", "--entrypoint", "xorriso", "nex-iso-builder", "-as", "mkisofs", "-quiet",
           "-output", str(lab / "seed.iso"), "-volid", "cidata", "-joliet", "-rock", str(seed))
    # The seed has been packaged.  Keep the private key only in this ignored lab directory.
    for file in seed.iterdir():
        file.unlink()
    seed.rmdir()
    p = {"ssh": port()}
    (lab / "ports.json").write_text(json.dumps(p) + "\n")
    command = ["qemu-system-x86_64", "-accel", "kvm", "-cpu", "host", "-m", "6144", "-smp", "4", "-display", "none", "-monitor", "none",
               "-serial", f"file:{lab / 'serial.log'}", "-qmp", f"unix:{lab / 'qmp.sock'},server=on,wait=off",
               "-drive", f"file={lab / 'host.qcow2'},format=qcow2,if=virtio", "-drive", f"file={lab / 'seed.iso'},format=raw,media=cdrom,readonly=on",
               "-netdev", f"user,id=n0,hostfwd=tcp:127.0.0.1:{p['ssh']}-:22", "-device", "virtio-net-pci,netdev=n0,mac=52:54:00:92:11:01"]
    (lab / "command.json").write_text(json.dumps(command, indent=2) + "\n")
    with (lab / "qemu.log").open("w") as log:
        proc = subprocess.Popen(command, stdout=log, stderr=log, start_new_session=True)
    (lab / "qemu.pid").write_text(str(proc.pid))
    for _ in range(120):
        if proc.poll() is not None:
            raise RuntimeError((lab / "qemu.log").read_text(errors="replace")[-4000:])
        reply = ssh(lab, "hostname", check=False)
        if reply.returncode == 0 and reply.stdout.strip() == "nex-storage-lab":
            (lab / "baseline.json").write_text(json.dumps({"result": "booted", "hostname": reply.stdout.strip(),
                "network": "QEMU user networking with a loopback SSH forward", "backing_image": BASE.name}, indent=2) + "\n")
            print(lab)
            return lab
        time.sleep(1)
    raise RuntimeError("private Core lab did not accept SSH within 120 seconds")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prepare", action="store_true")
    parser.add_argument("--lab-dir", type=Path)
    parser.add_argument("--command", help="run a read-only diagnostic through the private lab SSH")
    args = parser.parse_args()
    if args.prepare:
        prepare()
    else:
        assert args.lab_dir and args.command
        print(ssh(check_lab(args.lab_dir), args.command).stdout, end="")
