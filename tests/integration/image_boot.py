#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Boot test of the Nex Core image under QEMU/KVM + OVMF.

Installs a built release (image/out/release) with the real `nex-hostimage install` onto loop devices inside
a privileged container (the tools image from image/build.sh), boots the disk(s) and checks:
read-only erofs root on dm-verity, persistent /etc overlay and /var on nex-data, persistence across a reboot,
and (two disks) the md RAID1 nex-data assembled by the initrd.

  tests/integration/image_boot.py [--layout single|raid1] [--release DIR] [--keep]

Needs docker (privileged, /dev), qemu-system-x86_64 with KVM, OVMF, and `image/build.sh all` output.
"""
import argparse
import glob
import os
import queue
import re
import shutil
import subprocess
import struct
import sys
import tempfile
import threading
import time

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
OVMF_CODE = next((p for p in ("/usr/share/OVMF/OVMF_CODE_4M.fd", "/usr/share/edk2/OVMF_CODE_4M.fd", "/usr/share/OVMF/x64/OVMF_CODE.4m.fd") if os.path.exists(p)), None)
OVMF_VARS = next((p for p in ("/usr/share/OVMF/OVMF_VARS_4M.fd", "/usr/share/edk2/OVMF_VARS_4M.fd", "/usr/share/OVMF/x64/OVMF_VARS.4m.fd") if os.path.exists(p)), None)
DISK_BYTES = 14 << 30

SEED = r"""
set -eu
mkdir -p /seed/systemd/system/multi-user.target.wants /seed/systemd/system-preset
printf 'nex-image-test\n' > /seed/hostname
cat > /seed/systemd/system-preset/00-nex-test.preset <<'EOT'
enable nex-test.service
EOT
ln -s /etc/systemd/system/nex-test.service /seed/systemd/system/multi-user.target.wants/nex-test.service
cat > /seed/systemd/system/nex-test.service <<'EOT'
[Unit]
Description=Nex image boot probe (test only)
After=ssh.service nex-firstboot.service
[Service]
Type=oneshot
StandardOutput=journal
ExecStart=/etc/nex-test.sh
EOT
cat > /seed/nex-test.sh <<'EOT'
#!/bin/sh
# A serial getty on ttyS0 hangs up other openers of the tty, so markers go through the kernel log, which
# reaches the serial console (console=ttyS0) and is not rate limited once printk_devkmsg is on.
echo on > /proc/sys/kernel/printk_devkmsg
p() { printf '<6>NEXTEST %s=%s\n' "$1" "$(printf %s "$2" | tr '\n ' ';_')" > /dev/kmsg; }
sleep 8
p root "$(findmnt -no FSTYPE,OPTIONS / | tr ' ' ,)"
p verity "$(dmsetup status root 2>&1 | tr ' ' '_')"
p etc "$(findmnt -no FSTYPE /etc)"
p var "$(findmnt -no SOURCE,FSTYPE /var | tr ' ' ,)"
p mdstat "$(grep -Ec '^md[0-9]+ : .*raid1' /proc/mdstat)"
p mddegraded "$(cat /sys/block/md*/md/degraded 2>/dev/null | head -1)"
p usrwrite "$(touch /usr/x 2>&1 | tr ' ' _; echo rc=$?)"
p hostname "$(hostname)"
p sshkey "$(cat /etc/ssh/ssh_host_ed25519_key.pub 2>&1 | cut -d' ' -f2)"
p sshd "$(systemctl is-active ssh)"
p failed "$(systemctl --failed --no-legend | awk '{print $2}' | tr '\n' ,)"
p bootcheck "$(systemctl is-active nex-boot-check.service)"
p persist "$(cat /etc/nex-persist /var/nex-persist 2>/dev/null | tr '\n' ,)"
if [ ! -e /var/nex-persist ]; then
    echo persisted > /etc/nex-persist; echo persisted > /var/nex-persist; sync; p round1 written
    p DONE 1
    sleep 2
    exec systemctl poweroff
fi
p DONE 1
EOT
chmod 755 /seed/nex-test.sh
"""


class Console:
    """The VM's serial console: commands go to stdin, output is matched from stdout."""

    def __init__(self, proc, log=None):
        self.proc, self.buf, self.q, self.log = proc, "", queue.Queue(), log
        self.text = ""
        threading.Thread(target=self._pump, daemon=True).start()

    def _pump(self):
        for chunk in iter(lambda: os.read(self.proc.stdout.fileno(), 65536), b""):
            text = chunk.decode(errors="replace")
            self.text += text
            if self.log:
                self.log.write(text)
                self.log.flush()
            self.q.put(text)
        self.q.put(None)

    def log_text(self):
        return self.text.replace("\r", "")

    def expect(self, text, timeout=240):
        end = time.time() + timeout
        while time.time() < end:
            i = self.buf.find(text)
            if i >= 0:
                out, self.buf = self.buf[:i], self.buf[i + len(text):]
                return out
            try:
                chunk = self.q.get(timeout=1)
            except queue.Empty:
                continue
            if chunk is None:
                raise RuntimeError("qemu exited; console tail: " + self.buf[-800:])
            self.buf += chunk
        raise TimeoutError(f"{text!r} not seen in {timeout}s; console tail: {self.buf[-800:]!r}")

    def sh(self, cmd, timeout=60):
        mark = f"__END{time.monotonic_ns()}__"
        # the terminal echoes the command; only the expanded status line (digits) matches the pattern
        self.proc.stdin.write(f"{cmd}; echo {mark}$?\n".encode())
        self.proc.stdin.flush()
        end = time.time() + timeout
        rx = re.compile(re.escape(mark) + r"(\d+)\r?\n")
        while time.time() < end:
            m = rx.search(self.buf)
            if m:
                body, self.buf = self.buf[:m.start()], self.buf[m.end():]
                body = body.replace("\r", "")
                return int(m.group(1)), body.split("\n", 1)[1].strip() if "\n" in body else body.strip()
            try:
                chunk = self.q.get(timeout=1)
            except queue.Empty:
                continue
            if chunk is None:
                raise RuntimeError("qemu exited; console tail: " + self.buf[-800:])
            self.buf += chunk
        raise TimeoutError(f"{cmd!r}: no result in {timeout}s; tail {self.buf[-600:]!r}")


def docker_caps():
    """The lab caps for every container (2 CPUs, 4g; NEX_DOCKER_CPUS and NEX_DOCKER_MEMORY override)."""
    return ["--cpus", os.environ.get("NEX_DOCKER_CPUS", "2"), "--memory", os.environ.get("NEX_DOCKER_MEMORY", "4g")]


def host_mount(path):
    """The path the Docker daemon sees for a bind mount source. On a runner whose job runs in a container the
    workspace is a Docker volume: NEX_HOST_WORKSPACE names it on the Docker host, as for scripts/ci-docker.sh."""
    path = os.path.abspath(path)
    host = os.environ.get("NEX_HOST_WORKSPACE")
    if not host:
        return path
    workspace = os.environ.get("GITHUB_WORKSPACE") or os.getcwd()
    if path == workspace or path.startswith(workspace + "/"):
        return host + path[len(workspace):]
    sys.exit(f"cannot mount {path}: only paths under {workspace} exist on the Docker host")


def hostimage_for(release, override=None):
    """The static nex-hostimage built beside the release (image/build.sh writes <out>/nex-hostimage and <out>/release).
    A missing file would be created by Docker as a root-owned directory, so refuse it here."""
    path = os.path.abspath(override or os.path.join(os.path.dirname(os.path.abspath(release)), "nex-hostimage"))
    if not os.path.isfile(path):
        sys.exit(f"no nex-hostimage at {path}; build the release with image/build.sh or pass --hostimage")
    return path


def scratch_dir(release, prefix):
    """A new work directory beside the release (its build output directory, which exists)."""
    return tempfile.mkdtemp(prefix=prefix, dir=os.path.dirname(os.path.abspath(release)))


def detach_loops(work):
    """`docker kill` gives the install's EXIT trap no chance to run, so the loop devices on the disk images in work
    stay attached to the host: detach them by backing file from a fresh privileged container."""
    images = sorted(glob.glob(os.path.join(work, "*.img")))
    if not images:
        return
    script = 'for f in "$@"; do for l in $(losetup -j "$f" -O NAME -n); do losetup -d "$l"; done; done'
    subprocess.run(["docker", "run", "--rm", "--privileged", *docker_caps(), "-v", "/dev:/dev",
                    "-v", f"{host_mount(work)}:{work}", "nex-image-tools", "sh", "-c", script, "sh", *images],
                   capture_output=True, timeout=120)


def run_install(work, release, hostimage, script, timeout, what):
    """Run an install script in the tools container, privileged for loop devices, with the work directory at /w, the
    release at /rel and the host tool at /usr/bin/nex-hostimage. Returns stdout; exits on failure or timeout."""
    name = f"nex-{what}-{os.getpid()}"
    cmd = ["docker", "run", "--rm", "--name", name, "--privileged", *docker_caps(), "-v", "/dev:/dev",
           "-v", f"{host_mount(work)}:/w", "-v", f"{host_mount(release)}:/rel:ro",
           "-v", f"{host_mount(hostimage)}:/usr/bin/nex-hostimage:ro", "nex-image-tools", "sh", "-c", script]
    try:
        res = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        subprocess.run(["docker", "kill", name], capture_output=True)
        detach_loops(work)
        sys.exit(f"{what}: install step exceeded its {timeout}s budget; container killed and its loop devices detached")
    if res.returncode:
        sys.exit(f"{what}: docker step failed (rc {res.returncode}):\n{res.stdout[-1500:]}\n{res.stderr[-1500:]}")
    return res.stdout


def require_host_tools():
    """Everything a boot lab needs on the host, named up front instead of failing deep inside a step."""
    for need, hint in ((OVMF_CODE, "OVMF firmware (OVMF_CODE)"), (OVMF_VARS, "OVMF firmware (OVMF_VARS)"),
                       (shutil.which("docker"), "docker"), (shutil.which("qemu-system-x86_64"), "qemu-system-x86_64")):
        if not need:
            sys.exit(f"missing {hint}")
    if not os.access("/dev/kvm", os.R_OK | os.W_OK):
        sys.exit("no usable /dev/kvm (the QEMU labs need KVM: the runner must expose it and the user must be in the kvm group)")


def install(work, release, hostimage, layout, disks, timeout):
    """Step 1: write the release onto the disk images. Has its own budget; the boot budget starts afterwards."""
    script = SEED + """
set -eu
loops=""
trap 'for l in $(echo $loops | tr , " "); do losetup -d $l; done' EXIT
for d in %s; do l=$(losetup -fP --show /w/$d); loops="$loops,$l"; done
loops=${loops#,}
nex-hostimage install --layout %s --disks $loops --image-dir /rel --etc-seed /seed
""" % (" ".join(disks), layout)
    print(run_install(work, release, hostimage, script, timeout, "image-boot"))
    if layout == "raid1":
        for d in disks:
            if clear_newer_md_fields(os.path.join(work, d)):
                print(f"INFO  {d}: cleared md superblock fields written by this host's newer kernel", flush=True)


def clear_newer_md_fields(path):
    """The install runs on the workstation's kernel, which may be newer than the image's (6.12). A newer md
    stamps fields into the v1.2 superblock's reserved area (logical_block_size) that 6.12.107 rejects with
    "does not have a valid v1.2 superblock". The real installer runs the image's kernel generation, so this
    lab step removes the stamp and fixes the checksum. Returns True when it changed anything."""
    with open(path, "r+b") as f:
        f.seek(2 * 512)  # primary GPT entry array
        entries = f.read(128 * 128)
        first = None
        for i in range(128):
            e = entries[i * 128:(i + 1) * 128]
            if e[56:].decode("utf-16-le", "ignore").split("\0")[0] == "nex-data":
                first = struct.unpack("<Q", e[32:40])[0]
                break
        if first is None:
            return False
        off = first * 512 + 4096
        f.seek(off)
        head = f.read(256)
        if struct.unpack("<I", head[:4])[0] != 0xA92B4EFC or not any(head[224:256]):
            return False
        max_dev = struct.unpack("<I", head[220:224])[0]
        f.seek(off)
        sb = bytearray(f.read(256 + max_dev * 2 + (-(max_dev * 2) % 4)))
        sb[224:256] = bytes(32)
        sb[216:220] = bytes(4)
        total = sum(struct.unpack("<%dI" % (len(sb) // 4), bytes(sb)))
        sb[216:220] = struct.pack("<I", ((total & 0xFFFFFFFF) + (total >> 32)) & 0xFFFFFFFF)
        f.seek(off)
        f.write(sb)
    return True


def boot(work, disks, vars_file, log):
    cmd = ["qemu-system-x86_64", "-enable-kvm", "-cpu", "host", "-machine", "q35", "-m", "2048", "-smp", "2",
           "-display", "none", "-monitor", "none", "-serial", "stdio",
           "-drive", f"if=pflash,format=raw,unit=0,file={OVMF_CODE},readonly=on",
           "-drive", f"if=pflash,format=raw,unit=1,file={vars_file}"]
    for i, d in enumerate(disks):
        cmd += ["-drive", f"if=none,id=d{i},format=raw,file={work}/{d}",
                "-device", f"virtio-blk-pci,drive=d{i},serial=nexdisk{i},bootindex={i + 1}"]
    cmd += ["-netdev", "user,id=n0", "-device", "virtio-net-pci,netdev=n0", "-device", "virtio-rng-pci"]
    proc = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    return proc, Console(proc, open(log, "a") if log else None)


def check(cond, what, detail=""):
    print(("PASS " if cond else "FAIL ") + what + (f"  [{detail}]" if detail and not cond else ""), flush=True)
    if not cond:
        check.failed += 1


check.failed = 0


def rounds_for(layout):
    """(label, disks attached). raid1: both disks, then each disk alone."""
    if layout == "single":
        return [("boot 1", ["d0.img"]), ("reboot", ["d0.img"])]
    return [("both disks", ["d0.img", "d1.img"]), ("disk 0 alone", ["d0.img"]), ("disk 1 alone", ["d1.img"])]


def verify(got, label, layout, attached, first, results):
    root = got.get("root", "")
    check(root.startswith("erofs") and ",ro," in "," + root + ",", f"{label}: / is read-only erofs", root)
    check("verity" in got.get("verity", "") and "_V" in got.get("verity", "") + "_", f"{label}: root is dm-verity and verified", got.get("verity"))
    check(got.get("etc") == "overlay", f"{label}: /etc is the overlay", got.get("etc"))
    check(got.get("var", "").endswith("ext4"), f"{label}: /var is ext4 on nex-data", got.get("var"))
    if layout == "raid1":
        check(got.get("var", "").startswith("/dev/md") and got.get("mdstat") == "1", f"{label}: /var on md RAID1", str(got))
        want = "0" if len(attached) == 2 else "1"
        check(got.get("mddegraded") == want, f"{label}: md degraded={want}", got.get("mddegraded"))
    check("ead-only" in got.get("usrwrite", ""), f"{label}: /usr is not writable", got.get("usrwrite"))
    check(got.get("hostname") == "nex-image-test", f"{label}: seeded hostname", got.get("hostname"))
    check(len(got.get("sshkey", "")) > 40 and got.get("sshd") == "active", f"{label}: SSH host key generated at first boot, sshd active", str(got.get("sshd")))
    print(f"INFO  {label}: failed units={got.get('failed')!r} nex-boot-check={got.get('bootcheck')}", flush=True)
    if first:
        check("persisted" not in got.get("persist", ""), f"{label}: fresh /etc and /var", got.get("persist"))
    else:
        check(got.get("persist") == "persisted,persisted,", f"{label}: /etc edit and /var data persisted", got.get("persist"))
        check(got.get("sshkey") == results[0].get("sshkey"), f"{label}: SSH host key unchanged", got.get("sshkey"))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--layout", choices=("single", "raid1"), default="single")
    ap.add_argument("--release", default=os.path.join(REPO, "image/out/release"))
    ap.add_argument("--keep", action="store_true")
    ap.add_argument("--step", choices=("all", "install", "boot"), default="all",
                    help="install: write the disks only; boot: boot existing disks in --workdir (needs a prior install)")
    ap.add_argument("--workdir", help="directory for disk images and logs (default: a new one beside --release)")
    ap.add_argument("--hostimage", help="static nex-hostimage (default: <release>/../nex-hostimage)")
    ap.add_argument("--install-timeout", type=int, default=300, help="seconds for the install step")
    ap.add_argument("--boot-timeout", type=int, default=240, help="seconds for each boot to reach the probe")
    a = ap.parse_args()
    require_host_tools()
    if a.step != "boot" and not os.path.exists(os.path.join(a.release, "manifest.json")):
        sys.exit(f"no release in {a.release}; run image/build.sh all first")
    disks = ["d0.img"] if a.layout == "single" else ["d0.img", "d1.img"]
    fresh = not a.workdir
    work = os.path.abspath(a.workdir) if a.workdir else scratch_dir(a.release, "nex-image-boot-")
    os.makedirs(work, exist_ok=True)
    proc = None
    try:
        vars_file = os.path.join(work, "vars.fd")
        if a.step in ("all", "install"):
            for d in disks:
                with open(os.path.join(work, d), "wb") as f:
                    f.truncate(DISK_BYTES)
            shutil.copy(OVMF_VARS, vars_file)
            t0 = time.time()
            install(work, os.path.abspath(a.release), hostimage_for(a.release, a.hostimage), a.layout, disks, a.install_timeout)
            print(f"INFO  install step: {time.time() - t0:.0f}s of {a.install_timeout}s", flush=True)
        if a.step in ("all", "boot"):
            results = []
            for n, (label, attached) in enumerate(rounds_for(a.layout)):
                t0 = time.time()
                proc, con = boot(work, attached, vars_file, os.path.join(work, "console.log"))
                con.expect("NEXTEST DONE=1", a.boot_timeout)
                print(f"INFO  {label}: probe reached after {time.time() - t0:.0f}s of {a.boot_timeout}s", flush=True)
                got = dict(re.findall(r"NEXTEST (\w+)=(\S*)", con.log_text()))
                results.append(got)
                verify(got, label, a.layout, attached, n == 0, results)
                if n == 0:
                    # The probe powers the guest off cleanly after writing the markers (a real reboot).
                    try:
                        proc.wait(90)
                    except subprocess.TimeoutExpired:
                        print("INFO  guest did not power off in 90 s; killed", flush=True)
                if proc.poll() is None:
                    proc.kill()
                proc.wait(30)
                proc = None
    finally:
        if proc and proc.poll() is None:
            proc.kill()
        if not a.keep and fresh:
            shutil.rmtree(work, ignore_errors=True)
        elif a.keep:
            print(f"INFO  kept {work}", flush=True)
    sys.exit(1 if check.failed else 0)


if __name__ == "__main__":
    main()
