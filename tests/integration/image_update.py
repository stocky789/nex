#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""A/B update and rollback proof of the Nex Core image under QEMU/KVM + OVMF (single disk, one VM at a time).

Needs two releases built by image/build.sh (sequence 1 and 2, the same pinned trust, e.g.
NEX_LABEL=2026.10.1 NEX_SEQUENCE=1 NEX_IMAGE_OUT=<dir1> NEX_UPDATE_TRUST=<trust.json> image/build.sh all) and the
development keys of scripts/update-publish.py keygen. The script publishes both into a static tree, serves it over
a local HTTP server that honours Range, installs release 1 on a fresh disk and drives the on-host engine
(`nex-update`, the same engine nex-core-local serves) through a root shell on the VM's second serial port:

  1. boot release 1; data written to /etc and /var; unit and boot-assessment wiring observed
  2. tampered blob: apply refuses it, the host keeps running release 1
  3. fault-injected failing nex-boot-check: stage -> activate -> boot of release 2 fails its assessment ->
     the host is back on release 1 with rolled_back / boot_failed
  4. fault removed: stage -> activate -> release 2 boots, nex-boot-check passes, systemd-bless-boot commits it
  5. rollback to release 1
  /etc and /var data are compared after every boot.

  tests/integration/image_update.py --rel1 DIR/release --rel2 DIR/release --keys DIR --trust DIR/trust.json [--workdir DIR] [--keep]

Resource caps (the machine is shared): the install runs in a container capped at 4 CPUs and 8 GiB, QEMU gets
-smp 2 -m 2048, one VM exists at a time and every wait has its own budget (default 8 minutes).
"""
import argparse
import http.server
import json
import os
import re
import shutil
import socket
import socketserver
import subprocess
import sys
import tempfile
import threading
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import image_boot as lab  # noqa: E402  (Console, OVMF paths, DISK_BYTES)

REPO = lab.REPO
HOST_ADDR = "10.0.2.2"  # QEMU user-mode networking: the host as the guest sees it
RELEASE_LABELS = ("2026.10.1", "2026.10.2")

SEED = r"""
set -eu
mkdir -p /seed/nex /seed/systemd/system/multi-user.target.wants /seed/systemd/system-preset
printf 'nex-update-lab\n' > /seed/hostname
printf 'base_url=http://%(host)s:%(port)s\nchannel=dev\n' > /seed/nex/updates.conf
printf 'enable nex-lab-shell.service\n' > /seed/systemd/system-preset/00-nex-lab.preset
ln -s /etc/systemd/system/nex-lab-shell.service /seed/systemd/system/multi-user.target.wants/nex-lab-shell.service
cat > /seed/nex-lab-net.sh <<'EOT'
#!/bin/sh
# QEMU user-mode network, static: no DHCP client involved.
nic=$(ls /sys/class/net | grep -v '^lo$' | head -1)
ip link set "$nic" up
ip addr add 10.0.2.15/24 dev "$nic" 2> /dev/null || true
ip route replace default via 10.0.2.2
EOT
chmod 755 /seed/nex-lab-net.sh
cat > /seed/systemd/system/nex-lab-shell.service <<'EOT'
[Unit]
Description=Lab control shell on the second serial port (test only)
After=local-fs.target
[Service]
ExecStartPre=/etc/nex-lab-net.sh
ExecStartPre=/bin/sh -c 'stty -F /dev/ttyS1 -echo; echo NEXLAB-READY > /dev/ttyS1'
Environment=PS1= SYSTEMD_PAGER=cat SYSTEMD_COLORS=0
ExecStart=/bin/sh
StandardInput=tty
StandardOutput=tty
StandardError=tty
TTYPath=/dev/ttyS1
TTYReset=no
TTYVHangup=no
Restart=always
EOT
"""


class Ctl:
    """The root shell on the VM's second serial port, over a unix socket."""

    def __init__(self, path, log):
        self.path, self.log, self.buf = path, log, ""
        self.sock = None
        for _ in range(100):
            try:
                self.sock = socket.socket(socket.AF_UNIX)
                self.sock.connect(path)
                break
            except OSError:
                self.sock.close()
                time.sleep(0.2)
        else:
            raise RuntimeError("cannot connect to the control socket")
        self.sock.settimeout(1)

    def _read(self, deadline):
        while time.time() < deadline:
            try:
                data = self.sock.recv(65536)
            except socket.timeout:
                continue
            if not data:
                raise RuntimeError("control socket closed")
            text = data.decode(errors="replace").replace("\r", "")
            self.log.write(text)
            self.log.flush()
            self.buf += text
            return True
        return False

    def drain(self):
        self.buf = ""

    def expect(self, text, timeout):
        end = time.time() + timeout
        while text not in self.buf:
            if not self._read(end):
                raise TimeoutError(f"{text!r} not seen in {timeout}s; tail {self.buf[-400:]!r}")
        self.buf = self.buf.split(text, 1)[1]

    def send(self, cmd):
        self.sock.sendall((cmd + "\n").encode())

    def sh(self, cmd, timeout=60):
        mark = f"__END{time.monotonic_ns()}__"
        self.send(f"{cmd}; echo {mark}$?")
        rx = re.compile(re.escape(mark) + r"(\d+)\n")
        end = time.time() + timeout
        while True:
            m = rx.search(self.buf)
            if m:
                body, self.buf = self.buf[:m.start()], self.buf[m.end():]
                return int(m.group(1)), re.sub(r"^(# )+", "", body.strip())
            if not self._read(end):
                raise TimeoutError(f"{cmd!r}: no result in {timeout}s; tail {self.buf[-400:]!r}")


class RangeHandler(http.server.BaseHTTPRequestHandler):
    """Static files with Range and If-Range; flips one byte of the blob named by server.tamper while armed."""

    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        self.server.requests.append(f"{self.command} {self.path} {self.headers.get('Range', '')}".strip())

    def do_GET(self):
        path = os.path.normpath(os.path.join(self.server.root, self.path.lstrip("/").split("?")[0]))
        if not path.startswith(self.server.root + os.sep) or not os.path.isfile(path):
            self.send_response(404)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        st = os.stat(path)
        etag = f'"{st.st_size}-{int(st.st_mtime)}"'
        start, status = 0, 200
        rng = re.match(r"bytes=(\d+)-$", self.headers.get("Range", ""))
        if rng and self.headers.get("If-Range", etag) == etag and int(rng.group(1)) < st.st_size:
            start, status = int(rng.group(1)), 206
        self.send_response(status)
        self.send_header("Accept-Ranges", "bytes")
        self.send_header("ETag", etag)
        self.send_header("Content-Length", str(st.st_size - start))
        if status == 206:
            self.send_header("Content-Range", f"bytes {start}-{st.st_size - 1}/{st.st_size}")
        self.end_headers()
        tamper = self.server.tamper and path.endswith(self.server.tamper)
        limit = None
        if self.server.cut and path.endswith(self.server.cut[0]):
            limit, self.server.cut = start + self.server.cut[1], None
        with open(path, "rb") as f:
            f.seek(start)
            pos = start
            while chunk := f.read(1 << 20):
                if limit is not None and pos + len(chunk) > limit:
                    self.wfile.write(chunk[:max(0, limit - pos)])
                    self.wfile.flush()
                    self.close_connection = True
                    return
                if tamper and pos <= 4096 < pos + len(chunk):
                    chunk = bytearray(chunk)
                    chunk[4096 - pos] ^= 0xFF
                    chunk = bytes(chunk)
                pos += len(chunk)
                try:
                    self.wfile.write(chunk)
                except (BrokenPipeError, ConnectionResetError):
                    return


class Server(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True

    def __init__(self, root):
        super().__init__(("127.0.0.1", 0), RangeHandler)
        self.root, self.tamper, self.requests = os.path.realpath(root), None, []
        self.cut = None  # (blob name suffix, bytes): drop the next such download after that many bytes


def check(cond, what, detail=""):
    print(("PASS " if cond else "FAIL ") + what + (f"  [{detail}]" if detail and not cond else ""), flush=True)
    if not cond:
        check.failed += 1


check.failed = 0


def info(text):
    print("INFO  " + text, flush=True)


def publish(work, rel1, rel2, keys, trust, source_files):
    pub = os.path.join(work, "pub")
    script = os.path.join(REPO, "scripts/update-publish.py")
    key = os.path.join(keys, "release.key")
    # The images pin this exact trust.json; serving a freshly signed one would only change its dates.
    os.makedirs(pub, exist_ok=True)
    for name in ("trust.json", "trust.sig"):
        shutil.copy(os.path.join(os.path.dirname(trust), name), os.path.join(pub, name))
    source, compliance = source_files
    for rel in (rel1, rel2):
        subprocess.run([sys.executable, script, "publish", "--base", pub, "--release-dir", rel, "--channel", "dev",
                        "--release-key", key, "--source", source, "--compliance", compliance], check=True)
    return pub


def install(work, rel1, hostimage, port, timeout):
    script = (SEED % {"host": HOST_ADDR, "port": port}) + """
set -eu
l=$(losetup -fP --show /w/d0.img)
trap 'losetup -d $l' EXIT
nex-hostimage install --layout single --disks $l --image-dir /rel --etc-seed /seed
"""
    name = f"nex-image-update-{os.getpid()}"
    cmd = ["docker", "run", "--rm", "--name", name, "--privileged", "--cpus", "4", "--memory", "8g", "-v", "/dev:/dev",
           "-v", f"{work}:/w", "-v", f"{rel1}:/rel:ro", "-v", f"{hostimage}:/usr/bin/nex-hostimage:ro",
           "nex-image-tools", "sh", "-c", script]
    try:
        res = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        subprocess.run(["docker", "kill", name], capture_output=True)
        sys.exit(f"install exceeded {timeout}s; container killed")
    if res.returncode:
        sys.exit(f"install failed:\n{res.stdout[-1500:]}\n{res.stderr[-1500:]}")


class VM:
    def __init__(self, work):
        self.work, self.proc, self.con, self.ctl, self.seen = work, None, None, None, set()

    def start(self):
        sock = os.path.join(self.work, "ctl.sock")
        if os.path.exists(sock):
            os.unlink(sock)
        cmd = ["qemu-system-x86_64", "-enable-kvm", "-cpu", "host", "-machine", "q35", "-m", "2048", "-smp", "2",
               "-display", "none", "-monitor", "none", "-serial", "stdio", "-serial", f"unix:{sock},server=on,wait=off",
               "-drive", f"if=pflash,format=raw,unit=0,file={lab.OVMF_CODE},readonly=on",
               "-drive", f"if=pflash,format=raw,unit=1,file={self.work}/vars.fd",
               "-drive", f"if=none,id=d0,format=raw,file={self.work}/d0.img",
               "-device", "virtio-blk-pci,drive=d0,serial=nexdisk0,bootindex=1",
               "-netdev", "user,id=n0", "-device", "virtio-net-pci,netdev=n0", "-device", "virtio-rng-pci"]
        self.proc = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        self.con = lab.Console(self.proc, open(os.path.join(self.work, "console.log"), "a"))
        self.ctl = Ctl(sock, open(os.path.join(self.work, "control.log"), "a"))

    def wait_ready(self, timeout):
        """Wait until a shell with a boot id not seen before answers. The console marker
        alone is not enough: the shell takes the tty a moment after it and drops what was typed meanwhile,
        and one run lost the marker, so the shell is asked for its boot id until a new one appears."""
        t0 = time.time()
        uuid = re.compile(r"BOOTID=([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b")
        knock = 0
        while time.time() - t0 < timeout:
            if time.time() >= knock:
                self.ctl.send("echo BOOTID=$(cat /proc/sys/kernel/random/boot_id)")
                knock = time.time() + 3
            self.ctl._read(time.time() + 1)
            for found in uuid.findall(self.ctl.buf):
                if found not in self.seen:  # late answers of an earlier boot are not a new boot
                    self.seen.add(found)
                    self.ctl.drain()
                    return time.time() - t0
            self.ctl.buf = self.ctl.buf[-4000:]
        raise TimeoutError(f"no new boot answered within {timeout}s; tail {self.ctl.buf[-300:]!r}")

    def stop(self):
        if self.proc and self.proc.poll() is None:
            self.proc.kill()
            self.proc.wait(30)


def facts(ctl):
    """Everything the checks need from a running host, from the root shell."""
    # gpt-auto mounts the ESP at /boot (an automount: listing it mounts it).
    def out(cmd, timeout=60):
        return ctl.sh(cmd, timeout)[1]
    f = {
        "build_id": out(". /usr/lib/os-release; echo $BUILD_ID"),
        "root": out("findmnt -no FSTYPE,OPTIONS /"),
        "cmdline_roothash": out("tr ' ' '\\n' < /proc/cmdline | sed -n 's/^roothash=//p'"),
        "uki": out("ls /boot/EFI/Linux | tr '\\n' ' '"),
        "persist": out("cat /etc/nex-persist /var/nex-persist 2>&1 | tr '\\n' ,"),
        "sshkey": out("cut -d' ' -f2 /etc/ssh/ssh_host_ed25519_key.pub"),
        "machine_id": out("cat /etc/machine-id"),
    }
    f["status"] = json.loads(out("nex-update status 2>/dev/null", 120))
    return f


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--rel1", required=True, help="release directory of sequence 1 (image/build.sh output, .../release)")
    ap.add_argument("--rel2", required=True, help="release directory of sequence 2")
    ap.add_argument("--keys", required=True, help="development key directory (update-publish.py keygen)")
    ap.add_argument("--trust", required=True, help="the trust.json pinned into both images (its trust.sig sits beside it)")
    ap.add_argument("--hostimage", help="static nex-hostimage (default: <rel1>/../nex-hostimage)")
    ap.add_argument("--workdir")
    ap.add_argument("--keep", action="store_true")
    ap.add_argument("--budget", type=int, default=480, help="seconds for each wait (install, boot, apply)")
    a = ap.parse_args()
    for need, hint in ((lab.OVMF_CODE, "OVMF firmware"), (shutil.which("docker"), "docker"), (shutil.which("qemu-system-x86_64"), "qemu-system-x86_64")):
        if not need:
            sys.exit(f"missing {hint}")
    rel1, rel2 = os.path.abspath(a.rel1), os.path.abspath(a.rel2)
    hostimage = os.path.abspath(a.hostimage or os.path.join(rel1, "..", "nex-hostimage"))
    work = os.path.abspath(a.workdir) if a.workdir else tempfile.mkdtemp(prefix="nex-image-update-", dir=os.path.join(REPO, "image/out"))
    os.makedirs(work, exist_ok=True)
    vm, server = VM(work), None
    try:
        # Lab stand-ins for the licence components (a real release ships its AGPL source and compliance manifest).
        source = os.path.join(work, "source.tar.gz")
        subprocess.run(["tar", "czf", source, "-C", REPO, "internal/nexupdate", "scripts/update-publish.py"], check=True)
        compliance = os.path.join(os.path.dirname(rel1), "base.manifest")
        pub = publish(work, rel1, rel2, a.keys, os.path.abspath(a.trust), (source, compliance))
        server = Server(pub)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        port = server.server_address[1]
        slot2 = json.load(open(os.path.join(pub, "releases/2/manifest.json")))["components"][0]
        info(f"serving {pub} on 127.0.0.1:{port} (guest sees {HOST_ADDR}); release 2 slot-image blob sha256-{slot2['sha256'][:12]}...")

        for d in ("d0.img",):
            with open(os.path.join(work, d), "wb") as f:
                f.truncate(lab.DISK_BYTES)
        shutil.copy(lab.OVMF_VARS, os.path.join(work, "vars.fd"))
        t0 = time.time()
        install(work, rel1, hostimage, port, a.budget)
        info(f"install of release 1: {time.time() - t0:.0f}s")

        # ---- 1. boot release 1
        vm.start()
        info(f"boot 1 ready after {vm.wait_ready(a.budget):.0f}s")
        ctl = vm.ctl
        ctl.sh("echo persisted > /etc/nex-persist; echo persisted > /var/nex-persist; sync")
        f1 = facts(ctl)
        st = f1["status"]
        check(f1["build_id"] == "2026.10.1-1", "boot 1: release 1 runs", f1["build_id"])
        check(f1["root"].startswith("erofs ro"), "boot 1: / is read-only erofs", f1["root"])
        check(st["phase"] == "idle" and st["rollback_floor"] == 0, "boot 1: engine idle", st["phase"])
        check(ctl.sh("test -f /usr/share/nex/update/trust.json")[0] == 0, "boot 1: trust.json is pinned in the verity root")
        check(ctl.sh("test -x /usr/libexec/nex/nex-boot-check && systemctl cat nex-state-guard.service > /dev/null")[0] == 0, "boot 1: boot check and state guard units are in the image")
        rc, txt = ctl.sh("systemctl show boot-complete.target -p Wants -p Requires -p RequiredBy -p After; systemctl show nex-boot-check.service -p RequiredBy -p Before -p After")
        info("boot-complete wiring: " + txt.replace("\n", " | "))
        check("nex-boot-check.service" in txt, "boot 1: nex-boot-check is wired to boot-complete.target", txt)
        rc, txt = ctl.sh("systemctl list-dependencies --after --plain nex-boot-check.service", 60)
        info(f"nex-boot-check is ordered (transitively) after {len(txt.splitlines())} units; time-sync/chrony: "
             + ",".join(u for u in txt.split() if "chrony" in u or "time-sync" in u or "network-online" in u))
        check(not any(w in txt for w in ("chrony-wait", "time-sync.target", "network-online.target")), "boot 1: nex-boot-check is not ordered behind chrony-wait, time-sync or network-online", txt[-300:])
        check(ctl.sh("systemctl is-active nex-state-guard.service | grep -qx 'active\\|inactive'; nex-update state-guard")[0] == 0, "boot 1: state guard passes on fresh state")
        check(ctl.sh(f"curl -fsS -o /dev/null http://{HOST_ADDR}:{port}/trust.json || python3 -c \"import urllib.request;urllib.request.urlopen('http://{HOST_ADDR}:{port}/trust.json')\"")[0] == 0, "boot 1: the guest reaches the update server")

        # ---- 2. check, then a tampered blob
        rc, txt = ctl.sh("nex-update check 2>&1", 120)
        check(rc == 0 and '"sequence": 2' in txt, "check: release 2 is offered over HTTP", txt[-300:])
        server.tamper = f"sha256-{slot2['sha256']}"
        rc, txt = ctl.sh("nex-update apply -request-id lab-tamper-0001 -activate reboot 2>&1 | tail -5", a.budget)
        server.tamper = None
        st = json.loads(ctl.sh("nex-update status 2>/dev/null", 60)[1])
        err = (st.get("operation") or {}).get("error") or {}
        check(err.get("code") == "blob_mismatch", "tampered blob refused with blob_mismatch", json.dumps(st.get("operation")))
        check(st["phase"] in ("idle", "checking") and not st.get("slots", [{}])[0].get("role") == "staged" and all(s["role"] != "staged" for s in st["slots"]), "tampered blob: nothing staged, host stays on release 1", st["phase"])
        check(facts(ctl)["build_id"] == "2026.10.1-1", "tampered blob: release 1 still running")
        info(f"server saw {len(server.requests)} requests so far")

        # ---- 3. fault-injected failing boot assessment
        ctl.sh("mkdir -p /etc/systemd/system/nex-boot-check.service.d; "
               "printf '[Service]\\nEnvironment=NEX_BOOT_CHECK_TRIES=3 NEX_BOOT_CHECK_WAIT=5\\n' > /etc/systemd/system/nex-boot-check.service.d/lab.conf; "
               "for u in libvirtd.service libvirtd.socket libvirtd-ro.socket libvirtd-admin.socket; do ln -sf /dev/null /etc/systemd/system/$u; done; sync")
        server.cut = (f"sha256-{slot2['sha256']}", 120 << 20)
        rc, txt = ctl.sh("nex-update apply -request-id lab-stage-0001 -activate stage_only 2>&1 | tail -3", a.budget)
        st = json.loads(ctl.sh("nex-update status 2>/dev/null", 60)[1])
        part = ctl.sh("ls -l /var/lib/nex/image/staging/2/ 2>&1 | tail -3")[1]
        info(f"download cut after 120 MiB: phase={st['phase']} error={(st.get('operation') or {}).get('error')} staging: {part}")
        check(st["phase"] != "staged" and ".part" in part, "dropped download: nothing staged, the partial file is kept", part)
        before = len(server.requests)
        rc, txt = ctl.sh("nex-update apply -request-id lab-stage-0002 -activate stage_only 2>&1 | tail -3", a.budget)
        st = json.loads(ctl.sh("nex-update status 2>/dev/null", 60)[1])
        resumed = [r for r in server.requests[before:] if "bytes=" in r]
        check(any(int(re.search(r"bytes=(\d+)-", r).group(1)) > 0 for r in resumed), "resume: the next apply continued the download with a Range request", str(resumed))
        check(st["phase"] == "staged" and any(s["role"] == "staged" and s["sequence"] == 2 for s in st["slots"]), "stage: release 2 written to the inactive slot, nothing armed", st["phase"])
        fs = facts(ctl)
        check("2026.10.2" not in fs["uki"], "stage: the next boot is still release 1 (no release 2 UKI on the ESP)", fs["uki"])
        ctl.drain()
        ctl.send("nex-update activate -request-id lab-act-0001 2>&1 | tail -3")
        t0 = time.time()
        vm.wait_ready(a.budget)
        info(f"first boot of release 2 ... back in control after {time.time() - t0:.0f}s")
        fs = facts(ctl)
        info(f"after the failing trial: build={fs['build_id']} esp={fs['uki']}")
        # Release 2 started, failed its assessment, was marked bad; the host must be on release 1 again.
        if fs["build_id"] != "2026.10.1-1":
            vm.wait_ready(a.budget)  # still on the trial; the reboot after the bad mark comes next
            fs = facts(ctl)
        st = fs["status"]
        check(fs["build_id"] == "2026.10.1-1", "failed trial: back on release 1", fs["build_id"])
        check(st["phase"] == "rolled_back" and (st.get("failure") or {}).get("code") == "boot_failed", "failed trial: reported rolled_back / boot_failed", json.dumps({k: st.get(k) for k in ("phase", "failure")}))
        check(re.search(r"2026\.10\.2_2\+0-\d", fs["uki"]) is not None, "failed trial: release 2's entry is exhausted (+0-N)", fs["uki"])
        check(fs["persist"] == "persisted,persisted,", "failed trial: /etc and /var data survived", fs["persist"])
        check(fs["sshkey"] == f1["sshkey"] and fs["machine_id"] == f1["machine_id"], "failed trial: SSH host key and machine-id unchanged")
        rc, txt = ctl.sh("journalctl -b -1 -u nex-boot-check --no-pager -o cat 2>&1 | tail -4")
        info("trial boot's assessment: " + txt.replace("\n", " | "))

        # ---- 4. fault removed: update succeeds
        ctl.sh("rm -f /etc/systemd/system/libvirtd.service /etc/systemd/system/libvirtd.socket /etc/systemd/system/libvirtd-ro.socket "
               "/etc/systemd/system/libvirtd-admin.socket /etc/systemd/system/nex-boot-check.service.d/lab.conf; sync")
        ctl.drain()
        ctl.send("nex-update apply -request-id lab-apply-0002 -activate reboot 2>&1 | tail -3")
        t0 = time.time()
        vm.wait_ready(a.budget)
        info(f"apply + activate: back in control on the new version after {time.time() - t0:.0f}s")
        time.sleep(15)  # the assessment and the bless run after the control shell is up
        fs = facts(ctl)
        st = fs["status"]
        check(fs["build_id"] == "2026.10.2-2", "update: release 2 runs", fs["build_id"])
        check(fs["cmdline_roothash"] == json.loads(open(os.path.join(pub, "releases/2/manifest.json")).read())["components"][0]["verity_root_hash"], "update: running roothash is the signed manifest's")
        check(st["phase"] == "committed" and any(s["role"] == "previous" and s["sequence"] == 1 for s in st["slots"]), "update: committed, release 1 retained as previous", json.dumps(st["slots"]))
        check(re.search(r"2026\.10\.2_2\.efi", fs["uki"]) is not None and "2026.10.2_2+" not in fs["uki"].replace("2026.10.2_2+0", "x"), "update: release 2's boot entry lost its counter (blessed)", fs["uki"])
        rc, txt = ctl.sh("journalctl -b -u systemd-bless-boot -u nex-boot-check --no-pager -o cat 2>&1 | tail -6")
        info("bless: " + txt.replace("\n", " | "))
        check("Marked boot as 'good'" in txt or "good" in txt, "update: systemd-bless-boot marked the boot good", txt)
        check(fs["persist"] == "persisted,persisted,", "update: /etc and /var data survived", fs["persist"])
        check(fs["sshkey"] == f1["sshkey"] and fs["machine_id"] == f1["machine_id"], "update: SSH host key and machine-id unchanged")
        check(fs["root"].startswith("erofs ro"), "update: / is read-only erofs", fs["root"])

        # ---- 5. rollback
        ctl.drain()
        ctl.send("nex-update rollback -request-id lab-roll-0001 2>&1 | tail -3")
        vm.wait_ready(a.budget)
        time.sleep(10)
        fs = facts(ctl)
        check(fs["build_id"] == "2026.10.1-1", "rollback: release 1 runs again", fs["build_id"])
        check(fs["persist"] == "persisted,persisted,", "rollback: /etc and /var data survived", fs["persist"])
        info(f"final: phase={fs['status']['phase']} slots={json.dumps(fs['status']['slots'])} esp={fs['uki']}")
    finally:
        vm.stop()
        if server:
            server.shutdown()
        if not a.keep and not a.workdir:
            shutil.rmtree(work, ignore_errors=True)
        else:
            info(f"kept {work}")
    sys.exit(1 if check.failed else 0)


if __name__ == "__main__":
    main()
