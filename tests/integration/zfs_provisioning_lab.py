#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Prepare an isolated one/two-host QEMU lab for ZFS provisioning checks.

Every disk QEMU sees is a new qcow2 file beneath one new ``.cache`` directory.
The Debian cloud image is only a read-only backing file.  This fixture does not
touch libvirt, host block devices, mounts, bridges, or any existing lab.
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import tempfile
import time


ROOT = Path(__file__).resolve().parents[2]
CACHE = ROOT / ".cache"
BASE = CACHE / "appliance/debian-13-genericcloud-amd64-20260831-2587.qcow2"
PREFIX = "nex-zfs-provisioning-lab."


def free_port():
    with socket.socket() as value:
        value.bind(("127.0.0.1", 0))
        return value.getsockname()[1]


def docker(*args):
    return subprocess.run(["docker", "run", "--rm", "--user", f"{os.getuid()}:{os.getgid()}", *args], check=True,
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def check_lab(value):
    lab = value.resolve(strict=True)
    assert lab.parent == CACHE.resolve() and lab.name.startswith(PREFIX), "not this disposable lab"
    assert not lab.is_symlink(), "lab must not be a symlink"
    assert json.loads((lab / "lab.json").read_text()) == {"disposable": True, "fixture": "zfs-provisioning-v1"}
    for name in ("id_ed25519", "known_hosts", "ports.json", "core1.qcow2", "core1-data1.qcow2", "core1-data2.qcow2"):
        assert (lab / name).is_file() and not (lab / name).is_symlink(), name
    return lab


def ssh(lab, name, command, check=True):
    ports = json.loads((lab / "ports.json").read_text())
    return subprocess.run(["ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=3",
                           "-o", "StrictHostKeyChecking=accept-new", "-o", f"UserKnownHostsFile={lab / 'known_hosts'}",
                           "-i", str(lab / "id_ed25519"), "-p", str(ports[name]), "root@127.0.0.1", command],
                          check=check, capture_output=True, text=True)


def seed(lab, name, public_key):
    directory = lab / f"seed-{name}"
    directory.mkdir(mode=0o700)
    (directory / "user-data").write_text(
        "#cloud-config\n"
        f"hostname: nex-zfs-{name}\nmanage_etc_hosts: true\nssh_pwauth: false\ndisable_root: false\n"
        f"ssh_authorized_keys:\n  - {public_key}\n")
    (directory / "meta-data").write_text(f"instance-id: nex-zfs-provisioning-{name}\nlocal-hostname: nex-zfs-{name}\n")
    docker("-v", f"{lab}:{lab}", "--entrypoint", "xorriso", "nex-iso-builder", "-as", "mkisofs", "-quiet",
           "-output", str(lab / f"{name}-seed.iso"), "-volid", "cidata", "-joliet", "-rock", str(directory))
    for file in directory.iterdir():
        file.unlink()
    directory.rmdir()


def image(lab, name, backing=None, size="8G"):
    args = ["-v", f"{lab}:{lab}"]
    if backing:
        args.extend(("-v", f"{BASE}:{BASE}:ro", "--entrypoint", "qemu-img", "nex-appliance-builder",
                     "create", "-f", "qcow2", "-F", "qcow2", "-b", str(BASE), str(lab / name), size))
    else:
        args.extend(("--entrypoint", "qemu-img", "nex-appliance-builder", "create", "-f", "qcow2", str(lab / name), size))
    docker(*args)


def launch(lab, name):
    ports = json.loads((lab / "ports.json").read_text())
    data = sorted(lab.glob(f"{name}-data*.qcow2"))
    command = ["qemu-system-x86_64", "-accel", "kvm", "-cpu", "host", "-m", "4096", "-smp", "2", "-display", "none", "-monitor", "none",
               "-serial", f"file:{lab / (name + '.serial.log')}", "-qmp", f"unix:{lab / (name + '.qmp.sock')},server=on,wait=off",
               "-drive", f"file={lab / (name + '.qcow2')},format=qcow2,if=none,id={name}-system",
               "-device", f"virtio-blk-pci,drive={name}-system,bootindex=1",
               "-drive", f"file={lab / (name + '-seed.iso')},format=raw,media=cdrom,readonly=on",
               ]
    for image_path in data:
        number = image_path.stem.rsplit("data", 1)[1]
        drive = f"{name}-data{number}"
        command.extend(("-drive", f"file={image_path},format=qcow2,if=none,id={drive}",
                        "-device", f"virtio-blk-pci,drive={drive},serial=NEX-{name.upper()}-DATA-{number}"))
    command.extend(("-netdev", f"user,id={name}-net,hostfwd=tcp:127.0.0.1:{ports[name]}-:22",
                    "-device", f"virtio-net-pci,netdev={name}-net,mac=52:54:00:93:00:{1 if name == 'core1' else 2:02x}"))
    (lab / f"{name}-command.json").write_text(json.dumps(command, indent=2) + "\n")
    with (lab / f"{name}-qemu.log").open("w") as log:
        process = subprocess.Popen(command, stdout=log, stderr=log, start_new_session=True)
    (lab / f"{name}-qemu.pid").write_text(str(process.pid) + "\n")
    for _ in range(120):
        if process.poll() is not None:
            raise RuntimeError((lab / f"{name}-qemu.log").read_text(errors="replace")[-4000:])
        reply = ssh(lab, name, "hostname", check=False)
        if reply.returncode == 0 and reply.stdout.strip() == f"nex-zfs-{name}":
            return
        time.sleep(1)
    raise RuntimeError(f"{name} did not accept SSH within 120 seconds")


def prepare(hosts):
    assert BASE.is_file() and not BASE.is_symlink(), "missing read-only cached Debian cloud image"
    assert hosts in (1, 2), "one or two disposable Core hosts only"
    lab = Path(tempfile.mkdtemp(prefix=PREFIX, dir=CACHE))
    try:
        lab.chmod(0o700)
        (lab / "lab.json").write_text(json.dumps({"disposable": True, "fixture": "zfs-provisioning-v1"}) + "\n")
        subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "nex-zfs-lab", "-f", str(lab / "id_ed25519")], check=True)
        public_key = (lab / "id_ed25519.pub").read_text().strip()
        ports = {f"core{number}": free_port() for number in range(1, hosts + 1)}
        (lab / "ports.json").write_text(json.dumps(ports, indent=2) + "\n")
        for number in range(1, hosts + 1):
            name = f"core{number}"
            image(lab, f"{name}.qcow2", backing=True, size="64G")
            image(lab, f"{name}-data1.qcow2")
            image(lab, f"{name}-data2.qcow2")
            seed(lab, name, public_key)
        for number in range(1, hosts + 1):
            launch(lab, f"core{number}")
        baseline = {name: ssh(lab, name, "set -eu; hostname; lsblk -dn -o NAME,SERIAL,TYPE,SIZE; test -c /dev/kvm", check=True).stdout
                    for name in ports}
        (lab / "baseline.json").write_text(json.dumps({"backing_image": BASE.name, "hosts": baseline,
            "network": "QEMU user networking with per-host loopback SSH forwards"}, indent=2) + "\n")
    except BaseException:
        # A partial lab leaves detached QEMU processes and disk images behind. Remove only this
        # newly created disposable directory and only the processes it recorded.
        stop(lab)
        shutil.rmtree(lab, ignore_errors=True)
        raise
    print(lab)
    return lab


def reboot(lab, name):
    before = ssh(lab, name, "cat /proc/sys/kernel/random/boot_id").stdout.strip()
    ssh(lab, name, "systemctl reboot", check=False)
    for _ in range(180):
        reply = ssh(lab, name, "cat /proc/sys/kernel/random/boot_id", check=False)
        if reply.returncode == 0 and reply.stdout.strip() != before:
            print(reply.stdout, end="")
            return
        time.sleep(1)
    raise RuntimeError(f"{name} did not return after reboot")


def running(lab, name):
    path = lab / f"{name}-qemu.pid"
    if not path.is_file():
        return False
    try:
        pid = int(path.read_text().strip())
    except ValueError:
        return False
    try:
        return str(lab) in Path(f"/proc/{pid}/cmdline").read_text(errors="replace")
    except OSError:
        return False


def labels(lab):
    return [path.name[: -len("-qemu.pid")] for path in sorted(lab.glob("*-qemu.pid"))]


def stop(lab):
    """Terminate only the QEMU processes this lab recorded and still owns."""
    for name in labels(lab):
        if running(lab, name):
            os.kill(int((lab / f"{name}-qemu.pid").read_text().strip()), signal.SIGTERM)
    for _ in range(50):
        if not any(running(lab, name) for name in labels(lab)):
            return
        time.sleep(0.2)
    for name in labels(lab):
        if running(lab, name):
            os.kill(int((lab / f"{name}-qemu.pid").read_text().strip()), signal.SIGKILL)


def destroy(lab):
    stop(lab)
    shutil.rmtree(lab)
    print(f"destroyed {lab}")


def start(lab):
    for name in json.loads((lab / "ports.json").read_text()):
        if not running(lab, name):
            launch(lab, name)
    print(lab)


def add_disks(lab, name):
    assert not running(lab, name), f"{name} is running"
    existing = {path.stem.rsplit("data", 1)[1] for path in lab.glob(f"{name}-data*.qcow2")}
    numbers = [str(number) for number in range(1, 7) if str(number) not in existing][:2]
    assert len(numbers) == 2, "this fixture allows six disposable data disks per host"
    for number in numbers:
        image(lab, f"{name}-data{number}.qcow2")
    print(" ".join(f"/dev/disk/by-id/virtio-NEX-{name.upper()}-DATA-{number}" for number in numbers))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prepare", action="store_true")
    parser.add_argument("--hosts", type=int, default=2)
    parser.add_argument("--lab-dir", type=Path)
    parser.add_argument("--host", choices=("core1", "core2"), default="core1")
    parser.add_argument("--command", help="run a diagnostic through an explicitly selected disposable host")
    parser.add_argument("--reboot", action="store_true", help="reboot an explicitly selected disposable host")
    parser.add_argument("--start", action="store_true", help="start the stopped hosts in an explicitly selected disposable lab")
    parser.add_argument("--add-disks", action="store_true", help="add two stopped-host disposable data disks for retry isolation")
    parser.add_argument("--destroy", action="store_true", help="stop and delete an explicitly selected disposable lab")
    args = parser.parse_args()
    if args.prepare:
        prepare(args.hosts)
    else:
        assert args.lab_dir and (args.command or args.reboot or args.start or args.add_disks or args.destroy), "select a lab and an action"
        lab = check_lab(args.lab_dir)
        if args.destroy:
            destroy(lab)
            raise SystemExit(0)
        if args.start:
            start(lab)
        elif args.add_disks:
            add_disks(lab, args.host)
        else:
            assert args.host in json.loads((lab / "ports.json").read_text()), "host is not in this lab"
        if args.reboot:
            reboot(lab, args.host)
        elif args.command:
            print(ssh(lab, args.host, args.command).stdout, end="")
