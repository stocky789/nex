#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Boot test of the Nex Controller guest image (the two-disk Controller profile) under QEMU/KVM + OVMF.

Boots `system.img` (image/build.sh controller output, or appliance/build.py --profile controller) the way Core's
vmhost engine defines the VM: the system disk raw and read-only, a blank controller-data.qcow2 as the second virtio
disk, a NoCloud seed (label cidata) on SATA, UEFI without Secure Boot. It checks that the guest comes up, that the
read-only erofs root, the /etc overlay and /var on the data disk are what they should be, and that the Controller
answers HTTPS with the deployment's own certificate and publishes the readiness marker. Then it powers off and boots
again on the same data disk (persistence), and finally boots a copy of the system disk with one flipped byte in the
root partition, which must not reach readiness.

  tests/integration/controller_image_boot.py --release DIR [--keep] [--skip-tamper]

DIR holds system.img. One VM at a time, -smp 2 -m 2048. Needs qemu-system-x86_64 with KVM, OVMF, mkfs.vfat and mtools; qemu-img, or Docker with the nex-image-tools image.
"""
import argparse
import base64
import json
import os
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import image_boot as lab  # noqa: E402  (OVMF paths, lab container caps, workspace mounts)

OVMF_CODE, OVMF_VARS = lab.OVMF_CODE, lab.OVMF_VARS
MAC = "52:54:00:c0:de:01"
PORT = 18443
PASSWORD = "Nex-image-boot-test-1"


def sh(*args, **kw):
    return subprocess.run(args, check=True, **kw)


def make_certificate(work):
    key, cert = f"{work}/tls.key", f"{work}/tls.crt"
    sh("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2", "-subj", "/CN=nex-controller-boot-test",
       "-addext", "subjectAltName=IP:10.0.2.15", "-keyout", key, "-out", cert, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    return open(cert).read(), open(key).read(), cert


def make_seed(work, certificate, key):
    """The engine's NoCloud seed (internal/vmhost/engine.go seed()) as a FAT volume labelled cidata, DHCP network."""
    bootstrap = json.dumps({"admin_password": PASSWORD, "tls_certificate": certificate, "tls_private_key": key, "hostname": "nex-controller",
                            "network_mode": "dhcp", "address_cidr": "", "gateway": "", "dns": []})
    user = ("#cloud-config\nhostname: nex-controller\nmanage_etc_hosts: true\nssh_pwauth: true\ndisable_root: false\nwrite_files:\n"
            "  - path: /etc/nex-controller/bootstrap.json\n    owner: root:root\n    permissions: '0600'\n    encoding: b64\n"
            "    content: " + base64.b64encode(bootstrap.encode()).decode() + "\n")
    meta = "instance-id: nex-controller-boot-test\nlocal-hostname: nex-controller\n"
    network = f"version: 2\nethernets:\n  management:\n    match:\n      macaddress: '{MAC}'\n    set-name: nex0\n    dhcp4: true\n"
    seed = f"{work}/seed.img"
    with open(seed, "wb") as f:
        f.truncate(8 << 20)
    sh("mkfs.vfat", "-n", "CIDATA", seed, stdout=subprocess.DEVNULL)
    for name, body in (("user-data", user), ("meta-data", meta), ("network-config", network)):
        path = f"{work}/{name}"
        with open(path, "w") as f:
            f.write(body)
        sh("mcopy", "-i", seed, path, f"::{name}")
    return seed


class VM:
    """One qemu process; its serial console goes to a file and the guest agent answers on a unix socket."""

    def __init__(self, work, name, system, data, seed):
        self.name, self.work = name, work
        self.log = f"{work}/{name}.serial.log"
        self.qga = f"{work}/{name}.qga.sock"
        vars_copy = f"{work}/{name}.vars.fd"
        shutil.copy(OVMF_VARS, vars_copy)
        argv = ["qemu-system-x86_64", *lab.qemu_accel(), "-machine", "q35", "-smp", "2", "-m", "2048", "-display", "none", "-no-reboot",
                "-drive", f"if=pflash,format=raw,unit=0,readonly=on,file={OVMF_CODE}", "-drive", f"if=pflash,format=raw,unit=1,file={vars_copy}",
                "-drive", f"file={system},format=raw,if=none,id=system,readonly=on", "-device", "virtio-blk-pci,drive=system,bootindex=1",
                "-drive", f"file={data},format=qcow2,if=none,id=data", "-device", "virtio-blk-pci,drive=data",
                "-device", "ich9-ahci,id=ahci", "-drive", f"file={seed},format=raw,if=none,id=seed", "-device", "ide-hd,drive=seed,bus=ahci.0",
                "-netdev", f"user,id=n0,hostfwd=tcp:127.0.0.1:{PORT}-:8443", "-device", f"virtio-net-pci,netdev=n0,mac={MAC}",
                "-chardev", f"socket,id=qga,path={self.qga},server=on,wait=off", "-device", "virtio-serial", "-device", "virtserialport,chardev=qga,name=org.qemu.guest_agent.0",
                "-serial", f"file:{self.log}"]
        self.proc = subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)

    def serial(self):
        try:
            return open(self.log, errors="replace").read().replace("\r", "")
        except FileNotFoundError:
            return ""

    def alive(self):
        return self.proc.poll() is None

    def stop(self, graceful=False):
        if self.alive():
            if graceful:
                try:
                    self.guest("systemctl poweroff", wait=False)
                    self.proc.wait(timeout=lab.scaled(60))
                except Exception:
                    pass
            if self.alive():
                self.proc.kill()
        self.proc.wait()

    def guest(self, command, wait=True, timeout=60):
        """Run a shell command in the guest through the guest agent; returns (exit code, stdout)."""
        s = socket.socket(socket.AF_UNIX)
        s.settimeout(lab.scaled(timeout))
        s.connect(self.qga)
        f = s.makefile("rwb")

        def call(execute, arguments=None):
            f.write((json.dumps({"execute": execute, "arguments": arguments or {}}) + "\n").encode())
            f.flush()
            return json.loads(f.readline().lstrip(b"\xff"))["return"]  # guest-sync-delimited's reply starts with 0xFF
        call("guest-sync-delimited", {"id": 4242})  # the reply may follow stale bytes; the id marks it
        pid = call("guest-exec", {"path": "/bin/sh", "arg": ["-c", command], "capture-output": True})["pid"]
        if not wait:
            return 0, ""
        end = time.time() + lab.scaled(timeout)
        while time.time() < end:
            st = call("guest-exec-status", {"pid": pid})
            if st["exited"]:
                return st.get("exitcode", -1), base64.b64decode(st.get("out-data", "")).decode(errors="replace").strip()
            time.sleep(0.5)
        raise TimeoutError(command)


def ready(cert, deadline, vm, exit_means_not_ready=False):
    """Wait for the readiness marker over HTTPS with only this deployment's certificate trusted."""
    ctx = ssl.create_default_context(cafile=cert)
    ctx.check_hostname = False
    end = time.time() + lab.scaled(deadline)
    while time.time() < end:
        if not vm.alive():
            if exit_means_not_ready:  # the guest panicked on the failed root and qemu runs with -no-reboot
                return None
            raise RuntimeError(f"{vm.name}: qemu exited: {vm.proc.stderr.read().decode()[-400:]}")
        try:
            with urllib.request.urlopen(f"https://127.0.0.1:{PORT}/appliance-ready.json", context=ctx, timeout=3) as r:
                if r.status == 200:
                    return json.loads(r.read())
        except (OSError, ValueError):
            pass
        time.sleep(3)
    return None


def check(condition, message):
    print(("ok   " if condition else "FAIL ") + message, flush=True)
    if not condition:
        raise SystemExit(f"FAILED: {message}")


def flip_root_byte(source, target):
    shutil.copyfile(source, target)
    layout = json.loads(subprocess.run(["sfdisk", "-J", target], check=True, capture_output=True, text=True).stdout)["partitiontable"]
    root = next(p for p in layout["partitions"] if p["type"].lower() == "4f68bce3-e8cd-4db1-96e7-fbcaf984b709")
    offset = root["start"] * layout.get("sectorsize", 512) + 4096 + 17  # inside the first data block, which the mount reads
    with open(target, "r+b") as f:
        f.seek(offset)
        byte = f.read(1)
        f.seek(offset)
        f.write(bytes([byte[0] ^ 0xFF]))


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--release", required=True, help="directory with system.img")
    ap.add_argument("--keep", action="store_true")
    ap.add_argument("--skip-tamper", action="store_true")
    ap.add_argument("--boot-deadline", type=int, default=420)
    args = ap.parse_args()
    if not OVMF_CODE or not OVMF_VARS:
        sys.exit("OVMF firmware not found")
    for tool in ("qemu-system-x86_64", "mkfs.vfat", "mcopy", "openssl", "sfdisk"):
        if not shutil.which(tool):
            sys.exit(f"missing {tool}")
    lab.need_accelerator()
    # image/build.sh controller writes system.img; appliance/build.py --profile controller copies it as controller-system.img.
    system = next((p for p in (os.path.join(args.release, n) for n in ("system.img", "controller-system.img")) if os.path.isfile(p)), None)
    if system is None:
        sys.exit(f"no system.img or controller-system.img in {args.release}")
    work = lab.scratch_dir(args.release, "nex-controller-boot-")
    vm = None
    try:
        certificate, key, cert_path = make_certificate(work)
        seed = make_seed(work, certificate, key)
        data = f"{work}/controller-data.qcow2"
        if shutil.which("qemu-img"):
            sh("qemu-img", "create", "-f", "qcow2", data, "2G", stdout=subprocess.DEVNULL)
        else:  # the pinned tools image of image/build.sh carries qemu-utils
            sh("docker", "run", "--rm", *lab.docker_caps(), "-v", f"{lab.host_mount(work)}:/w", "-u", f"{os.getuid()}:{os.getgid()}", "nex-image-tools", "qemu-img", "create", "-f", "qcow2", "/w/controller-data.qcow2", "2G", stdout=subprocess.DEVNULL)

        vm = VM(work, "boot1", system, data, seed)
        marker = ready(cert_path, args.boot_deadline, vm)
        print(vm.serial()[-1500:])
        check(marker == {"bootstrap_version": 1, "status": "ready"}, "first boot: Controller answers with the deployment certificate and the ready marker")
        for label, command, want in (
            ("root is a read-only erofs on dm-verity", "findmnt -no FSTYPE,OPTIONS / ; dmsetup status | head -3", ("erofs", "ro")),
            ("/etc is an overlay", "findmnt -no FSTYPE /etc", ("overlay",)),
            ("/var is ext4 on the data disk", "findmnt -no SOURCE,FSTYPE /var", ("/dev/vdb", "ext4")),
            ("/usr is not writable", "touch /usr/x 2>&1; echo rc=$?", ("Read-only", "rc=1")),
            ("no apt or update helper in the guest", "ls /usr/local/lib/nex/apply-update /etc/systemd/system/nex-controller-update.path /usr/bin/apt-get 2>&1 | grep -c 'No such file'", ("3",)),
            ("only the Controller's state is on /var", "ls /var/lib/nex-controller | head -20", ("controller.db",)),
        ):
            code, out = vm.guest(command)
            check(all(w in out for w in want), f"{label}: {out!r}")
        code, out = vm.guest("systemctl --failed --no-legend | awk '{print $2}' | tr '\\n' ,")
        print("failed units:", out or "(none)")
        vm.stop(graceful=True)

        vm = VM(work, "boot2", system, data, seed)
        marker = ready(cert_path, args.boot_deadline, vm)
        check(marker == {"bootstrap_version": 1, "status": "ready"}, "reboot on the same data disk: ready again, state persisted")
        code, out = vm.guest("ls /var/lib/nex-controller/appliance-initialized /etc/nex-controller/tls.key; test -e /etc/nex-controller/bootstrap.json && echo BOOTSTRAP-LEFT || echo bootstrap-removed")
        check("bootstrap-removed" in out and "appliance-initialized" in out, f"bootstrap credentials gone, identity kept: {out!r}")
        vm.stop(graceful=True)

        if not args.skip_tamper:
            bad = f"{work}/tampered.img"
            flip_root_byte(system, bad)
            vm = VM(work, "tampered", bad, data, seed)
            marker = ready(cert_path, 150, vm, exit_means_not_ready=True)
            log = vm.serial()
            print(log[-3000:])
            check(marker is None, "tampered system disk: the Controller never becomes ready")
            check("verity" in log and "is corrupted" in log and any(m in log for m in ("Dependency failed for", "Failed to mount", "sysroot.mount: Mount process exited")),
                  "tampered system disk: the console shows dm-verity refusing the block and the root mount failing")
            vm.stop()
        print("controller image boot test: PASS")
    finally:
        if vm is not None:
            vm.stop()
        if args.keep:
            print("kept", work)
        else:
            shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    main()
