#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""A/B update and rollback proof of the Nex Core image under QEMU/KVM + OVMF (single disk, one VM at a time).

Needs two releases built by image/build.sh (sequence 1 and 2, the same pinned trust, e.g.
NEX_LABEL=2026.10.1 NEX_SEQUENCE=1 NEX_IMAGE_OUT=<dir1> NEX_UPDATE_TRUST=<trust.json> image/build.sh all) and the
development keys of scripts/update-publish.py keygen. The script publishes both into a static tree, serves it over
a local HTTP server that honours Range, installs release 1 on a fresh disk and drives the on-host engine
(`nex-update`, the same engine nex-core-local serves) through a root shell on the VM's second serial port:

  1. boot release 1; data written to /etc and /var; unit, boot-assessment and ESP-not-mounted wiring observed
  2. tampered blob: apply refuses it, the host keeps running release 1
  3. dropped download, Range resume, stage_only; fault-injected failing nex-boot-check: release 2 gets its three
     counted boots, the check fails each, the last try is marked bad -> back on release 1, rolled_back / boot_failed
  4. fault removed: apply -activate reboot -> release 2 boots, nex-boot-check passes, bless commits it
     (nex-update-reconcile.service alone updates the record)
  5. rollback to release 1
  6. power cuts (kill -9 / QMP quit) during the slot write, after it before arming, with a .efi.new on the ESP,
     after arming before the bless, and in the counted trial boot; plus CLI/service exclusion during an apply
  7. optional state guard: only with --rel1-state-format 2 and an observed format-2 release 1 over state needing 3:
     engine refusal, then release 1 armed by hand: its guard fails at boot (read live), fallback to release 2.
     Format-3 release 1 skips this incompatible scenario explicitly; no downgrade coverage is claimed.
  8. in the same run: rollback, a failing check with fallback, and the happy path once more
  /etc and /var data are compared after every boot.

  tests/integration/image_update.py --rel1 DIR/release --rel2 DIR/release --keys DIR --trust DIR/trust.json [--workdir DIR] [--keep]

Resource caps (the machine is shared): the install runs in a container capped at 2 CPUs and 4 GiB, QEMU gets
-smp 2 -m 2048, one VM exists at a time and every wait has its own budget (default 8 minutes).
"""
import argparse
import http.server
import json
import os
import re
import shlex
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
# Request ids are unique per run: the engine replays a repeated id idempotently without acting, so a reused id
# makes a step silently do nothing. Every id below carries this token.
RUN = time.strftime("r%m%d%H%M%S")

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
KillSignal=SIGKILL
TimeoutStopSec=2
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
        end = time.time() + lab.scaled(timeout)
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
        end = time.time() + lab.scaled(timeout)
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


def publish(work, rel1, rel2, keys, trust, source_files, rel1_state_format=3):
    pub = os.path.join(work, "pub")
    script = os.path.join(REPO, "scripts/update-publish.py")
    key = os.path.join(keys, "release.key")
    # The images pin this exact trust.json; serving a freshly signed one would only change its dates.
    os.makedirs(pub, exist_ok=True)
    for name in ("trust.json", "trust.sig"):
        shutil.copy(os.path.join(os.path.dirname(trust), name), os.path.join(pub, name))
    source, compliance = source_files
    for rel, fmt in ((rel1, rel1_state_format), (rel2, 3)):
        subprocess.run([sys.executable, script, "publish", "--base", pub, "--release-dir", rel, "--channel", "dev",
                        "--release-key", key, "--source", source, "--compliance", compliance, "--state-format", str(fmt)], check=True)
    return pub


def install(work, rel1, hostimage, port, timeout):
    script = (SEED % {"host": HOST_ADDR, "port": port}) + """
set -eu
l=$(losetup -fP --show /w/d0.img)
trap 'losetup -d $l' EXIT
nex-hostimage install --layout single --disks $l --image-dir /rel --etc-seed /seed
"""
    lab.run_install(work, rel1, hostimage, script, timeout, "image-update")


class VM:
    def __init__(self, work):
        self.work, self.proc, self.con, self.ctl, self.seen = work, None, None, None, set()

    def start(self):
        sock = os.path.join(self.work, "ctl.sock")
        for stale in (sock, os.path.join(self.work, "qmp.sock")):
            if os.path.exists(stale):
                os.unlink(stale)
        cmd = ["qemu-system-x86_64", *lab.qemu_accel(), "-machine", "q35", "-m", "2048", "-smp", "2",
               "-display", "none", "-monitor", "none", "-serial", "stdio", "-serial", f"unix:{sock},server=on,wait=off",
               "-qmp", f"unix:{self.work}/qmp.sock,server=on,wait=off",
               "-drive", f"if=pflash,format=raw,unit=0,file={lab.OVMF_CODE},readonly=on",
               "-drive", f"if=pflash,format=raw,unit=1,file={self.work}/vars.fd",
               "-drive", f"if=none,id=d0,format=raw,file={self.work}/d0.img",
               "-device", "virtio-blk-pci,drive=d0,serial=nexdisk0,bootindex=1",
               "-netdev", "user,id=n0", "-device", "virtio-net-pci,netdev=n0", "-device", "virtio-rng-pci"]
        self.proc = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        self.con = lab.Console(self.proc, open(os.path.join(self.work, "console.log"), "a"))
        self.ctl = Ctl(sock, open(os.path.join(self.work, "control.log"), "a"))

    def kernel_running(self):
        """True once the kernel of the current boot has printed its banner. OVMF merges BOTH serial ports into its console input,
        so anything sent to the control shell before that is typed into the boot menu (sd-boot hotkeys: `e` edit, `c` console mode,
        `j`/`k` move the selection...) and silently selects another entry. Found by run 15: the knock chose release 1 over the armed
        release 2, and the test reported the counted boots as not happening."""
        t = self.con.log_text()
        return "Linux version" in t[max(t.rfind("BdsDxe: loading"), 0):]

    def wait_ready(self, timeout):
        """Wait until a shell with a boot id not seen before answers. The console marker
        alone is not enough: the shell takes the tty a moment after it and drops what was typed meanwhile,
        and one run lost the marker, so the shell is asked for its boot id until a new one appears
        (only while a kernel is running, see kernel_running)."""
        t0 = time.time()
        uuid = re.compile(r"BOOTID=([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b")
        knock = 0
        while time.time() - t0 < lab.scaled(timeout):
            if time.time() >= knock and self.kernel_running():
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

    def cut_power(self, how):
        """Power cut: kill -9 of QEMU ("kill9") or QMP `quit` ("qmp"), then start the VM again on the same disk and firmware variables."""
        if how == "qmp":
            q = socket.socket(socket.AF_UNIX)
            q.settimeout(5)
            q.connect(os.path.join(self.work, "qmp.sock"))
            q.recv(65536)
            q.sendall(b'{"execute":"qmp_capabilities"}\n')
            q.recv(65536)
            q.sendall(b'{"execute":"quit"}\n')
            try:
                self.proc.wait(15)
            except subprocess.TimeoutExpired:
                pass
            q.close()
        self.stop()
        self.start()


ESP_TYPE = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"


# Shell: $m is the ESP mount point. Core mounts the ESP transiently under /run/nex/esp/<node> while it works, so an existing
# mount is reused; otherwise it is mounted at /run/lab-esp (option $1) and ESP_DONE unmounts it again. The lab takes the same
# /run/nex/esp.lock as Core and nex-bless-boot (fd 8) around the whole use, so it never collides with their transient mount.
ESP_MOUNT = ("mkdir -p /run/nex; exec 8>/run/nex/esp.lock; flock -w 120 8; "
             "d=$(lsblk -rpno PATH,PARTTYPE | awk '$2==\"%s\"{print $1; exit}'); m=$(findmnt -nro TARGET -S \"$d\" | head -n 1); own=; "
             "if [ -z \"$m\" ]; then m=/run/lab-esp; mkdir -p $m; mount -o %%s \"$d\" $m && own=1; fi; ") % ESP_TYPE
ESP_DONE = "; r=$?; [ -z \"$own\" ] || umount $m || r=1; exec 8>&-; (exit $r)"


def esp_ls(ctl):
    """The UKI file names on the ESP. The image mounts the ESP nowhere, so it is mounted here only for the listing."""
    rc, out = ctl.sh((ESP_MOUNT % "ro") + "ls $m/EFI/Linux | tr '\\n' ' '" + ESP_DONE)
    return out if rc == 0 else f"ESP-LISTING-FAILED {out}"


def raw_phase(ctl):
    """The engine record's phase as written on disk, read without running the engine (what the boot-time service left)."""
    return ctl.sh("tr -d ' \\n' < /var/lib/nex/image/state.json | grep -o '\"phase\":\"[a-z_]*\"' | head -3 | tr '\\n' ' '")[1]


def status(ctl, timeout=60):
    """`nex-update status` as JSON. The root shell prints job-control lines ("[1] + Done ...") for background jobs, so only the object is parsed."""
    text = ctl.sh("nex-update status 2>/dev/null", timeout)[1]
    return json.loads(text[text.find("{"):text.rfind("}") + 1])


def await_commit(ctl, patience=150):
    """Wait for the boot-time reconcile to leave the record committed, reading the record file only (no nex-update command of ours).
    The assessment, the bless and the reconcile service run after the control shell is up, and a loaded runner takes longer than any
    fixed pause. When it never commits, print what booted, the loader's entries, the update command's output and both boots' logs."""
    phase_raw = raw_phase(ctl)
    for _ in range(lab.scaled(patience) // 5):
        if '"committed"' in phase_raw:
            break
        time.sleep(5)
        phase_raw = raw_phase(ctl)
    if '"committed"' not in phase_raw:
        info("DIAG " + ctl.sh("cat /tmp/lastcmd.out | tail -15; echo == build; . /usr/lib/os-release; echo $BUILD_ID; echo == cmdline; cat /proc/cmdline | cut -c1-200; "
                              "echo == loader variables; ls /sys/firmware/efi/efivars | grep -i -E 'loader|boot' | head -30; "
                              "for v in LoaderEntryDefault LoaderEntryOneShot LoaderEntryLastBooted LoaderEntrySelected LoaderBootCountPath; do "
                              "f=$(ls /sys/firmware/efi/efivars/$v-* 2>/dev/null | head -1); [ -n \"$f\" ] && echo \"$v=$(tail -c +5 $f | tr -d '\\\\000')\"; done; "
                              "echo == loader view; " + (ESP_MOUNT % "ro") + "bootctl --esp-path=$m --no-pager list 2>&1 | head -50; echo == loader.conf; cat $m/loader/loader.conf; ls -la $m/EFI/Linux" + ESP_DONE + "; "
                              "echo == this boot; journalctl -b 0 --no-pager | grep -E 'nex-update|nex-boot-check|bless|systemd-boot|reboot' | tail -40 | cut -c1-250; "
                              "echo == previous boot; journalctl -b -1 --no-pager | grep -E 'nex-update|nex-boot-check|bless|reboot|Reached target' | tail -30 | cut -c1-250", 120)[1])
    return phase_raw


def facts(ctl):
    """Everything the checks need from a running host, from the root shell."""
    def out(cmd, timeout=60):
        return ctl.sh(cmd, timeout)[1]
    f = {
        "build_id": out(". /usr/lib/os-release; echo $BUILD_ID"),
        "root": out("findmnt -no FSTYPE,OPTIONS /"),
        "cmdline_roothash": out("tr ' ' '\\n' < /proc/cmdline | sed -n 's/^roothash=//p'"),
        "uki": esp_ls(ctl),
        "persist": out("cat /etc/nex-persist /var/nex-persist 2>&1 | tr '\\n' ,"),
        "sshkey": out("cut -d' ' -f2 /etc/ssh/ssh_host_ed25519_key.pub"),
        "machine_id": out("cat /etc/machine-id"),
    }
    # A reconcile or apply that holds the engine lock makes `status` report the record as it is on disk (running=true) without
    # folding the boot in, so wait for the engine to be idle before reading the phase.
    f["status"] = status(ctl, 120)
    for _ in range(60):
        if not f["status"].get("running"):
            break
        time.sleep(3)
        f["status"] = status(ctl, 120)
    return f


def boot_until(vm, budget, want, limit=8):
    """Wait for new boots until the host runs the wanted build_id (counted trial boots may reboot on their own)."""
    seen = []
    for _ in range(limit):
        vm.wait_ready(budget)
        b = vm.ctl.sh(". /usr/lib/os-release; echo $BUILD_ID")[1]
        seen.append(b)
        if b == want:
            return seen
    return seen


def trial_diag(vm, seen):
    """A failing trial must fall back to release 1 after its counted boots. When it does not, the host is usually
    rebooting, so its shell cannot be asked: print what the boot check and the loader said, from the console log."""
    if seen and seen[-1] == "2026.10.1-1":
        return
    keep = re.compile(r"nex-boot-check|boot tries|bless|Marked boot|BdsDxe: loading|esp\.lock|target is busy|umount|nex-esp-sync|nex-update", re.I)
    lines = [l.strip() for l in vm.con.log_text().splitlines() if keep.search(l)]
    info("DIAG the failing trial never fell back; console lines: " + " | ".join(l[:160] for l in lines[-60:]))


STATE_GUARD_METADATA = 'set -e; cat "$1"; printf "\\nNEX_LAB_RETAINED_ROOT\\n"; cat "$2"'


def state_guard_images_match(rc, observed):
    if rc != 0:
        return False
    roots = observed.split("\nNEX_LAB_RETAINED_ROOT\n")
    if len(roots) != 2:
        return False
    for text, expected in zip(roots, ({"BUILD_ID": "2026.10.2-2", "NEX_STATE_FORMAT": "3"},
                                     {"BUILD_ID": "2026.10.1-1", "NEX_STATE_FORMAT": "2"})):
        fields = {}
        for line in text.splitlines():
            key, separator, value = line.strip().partition("=")
            if separator and key in expected:
                try:
                    values = shlex.split(value, comments=True)
                except ValueError:
                    return False
                if key in fields or len(values) != 1:
                    return False
                fields[key] = values[0]
        if fields != expected:
            return False
    return True


def prove_state_guard(vm, budget, same_data, rel1_state_format):
    if rel1_state_format != 2:
        info("SKIP section 7: release 1 is not selected as format 2; older-format refusal/fallback not covered")
        return
    ctl = vm.ctl
    previous = next((s for s in status(ctl)["slots"] if s["sequence"] == 1 and s.get("slot") in ("a", "b")), None)
    if previous is None:
        sys.exit("section 7: cannot identify release 1's retained root; no downgrade proof attempted")
    slot = previous["slot"]
    rc, observed = ctl.sh(
        "(set -eu; m=$(mktemp -d /run/nex-lab-state.XXXXXX); "
        "trap 'umount \"$m\" 2>/dev/null || true; rmdir \"$m\"' EXIT; "
        f"mount -t erofs -o ro /dev/disk/by-partlabel/nex-root-{slot} \"$m\"; "
        f"sh -c {shlex.quote(STATE_GUARD_METADATA)} sh /usr/lib/os-release \"$m/usr/lib/os-release\"; )")
    if not state_guard_images_match(rc, observed):
        sys.exit(f"section 7: release 1 must actually read format 2 and release 2 format 3 "
                 f"(observed {observed!r}, exit {rc}); no downgrade proof attempted")
    ctl.sh("mkdir -p /var/lib/nex-core-agent && echo '{}' > /var/lib/nex-core-agent/zfs-pool.json; sync")
    check(ctl.sh("nex-update state-guard")[0] == 0, "state guard: passes on release 2 (reads 3) over state that needs 3")
    rc, txt = ctl.sh(f"nex-update rollback -request-id lab-roll2-{RUN} 2>&1 | tail -3")
    check("state_format_downgrade" in txt, "state guard: the engine refuses rollback to release 1 (reads 2) over state that needs 3 (state_format_downgrade)", txt)
    before = esp_ls(ctl)
    # Leave time to read the guard live during the failed trial boot.
    ctl.sh("mkdir -p /etc/systemd/system/nex-boot-check.service.d; "
           "printf '[Service]\\nEnvironment=NEX_BOOT_CHECK_TRIES=2 NEX_BOOT_CHECK_WAIT=45\\n' > /etc/systemd/system/nex-boot-check.service.d/lab.conf; sync")
    ctl.sh((ESP_MOUNT % "rw") + "cd $m/EFI/Linux && mv nex-core_2026.10.1_1.efi nex-core_2026.10.1_1+3.efi && "
           "mv nex-core_2026.10.2_2.efi nex-core_2026.10.2_2+0-3.efi && cd / && sync" + ESP_DONE)
    info(f"state-guard lab: ESP {before} -> {esp_ls(ctl)}")
    vm.ctl.drain()
    vm.ctl.send("systemctl reboot")
    t0 = time.time()
    first, builds = None, []
    for _ in range(8):
        vm.wait_ready(budget)
        ctl = vm.ctl
        b = ctl.sh(". /usr/lib/os-release; echo $BUILD_ID")[1]
        builds.append(b)
        if b == "2026.10.1-1" and first is None:
            try:
                first = ctl.sh("systemctl is-failed nex-state-guard.service; systemctl is-active libvirtd.service nex-core-local.service 2>&1 | tr '\\n' ' '; journalctl -b -u nex-state-guard --no-pager -o cat | tail -2", 25)[1]
            except TimeoutError:
                first = "UNREAD: the guest rebooted under the read"
        if b == "2026.10.2-2":
            break
    info(f"state-guard boots: {builds} after {time.time() - t0:.0f}s; on release 1: {first!r}")
    lines = (first or "").split("\n") + [""]
    check(builds[:1] == ["2026.10.1-1"] and lines[0].strip() == "failed" and lines[1].split()[:1] not in ([], ["active"]),
          "state guard (observed live): release 1 booted over newer state, its guard failed and libvirtd did not start", repr(first))
    check(builds[-1] == "2026.10.2-2", "state guard: the counted boots ran out and the host is back on release 2 (reads 3)", str(builds))
    time.sleep(lab.scaled(20))
    fs = facts(ctl)
    info(f"state guard final: ESP {fs['uki']} phase {fs['status']['phase']} failure {fs['status'].get('failure')}")
    check(ctl.sh("nex-update state-guard")[0] == 0, "state guard: release 2 reads the retained state")
    same_data(fs, "state guard fallback")
    rc, txt = ctl.sh("journalctl --no-pager -o cat -u nex-state-guard -u nex-boot-check 2>&1 | grep -c 'retained state needs a newer state_format'")
    check(txt.strip().isdigit() and int(txt.strip()) >= 1, "state guard (from the journal, after the fact): nex-boot-check failed the boot on release 1 because of the retained-state guard", txt)
    ctl.sh("rm -f /var/lib/nex-core-agent/zfs-pool.json /etc/systemd/system/nex-boot-check.service.d/lab.conf; sync")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--rel1", required=True, help="release directory of sequence 1 (image/build.sh output, .../release)")
    ap.add_argument("--rel2", required=True, help="release directory of sequence 2")
    ap.add_argument("--keys", required=True, help="development key directory (update-publish.py keygen)")
    ap.add_argument("--trust", required=True, help="the trust.json pinned into both images (its trust.sig sits beside it)")
    ap.add_argument("--rel1-state-format", type=int, default=3, help="NEX_STATE_FORMAT release 1 was built with (use 2 for the state-guard proof)")
    ap.add_argument("--hostimage", help="static nex-hostimage (default: <rel1>/../nex-hostimage)")
    ap.add_argument("--workdir")
    ap.add_argument("--resume", action="store_true", help="continue on the disk in --workdir after the fallback step (skips install and steps 1-3)")
    ap.add_argument("--from-6d", action="store_true", help="continue on the disk of an earlier run at section 6d (implies --resume; needs that run's --run token)")
    ap.add_argument("--run", help="request-id token of the earlier run (with --from-6d)")
    ap.add_argument("--keep", action="store_true")
    ap.add_argument("--budget", type=int, default=480, help="seconds for each wait (install, boot, apply)")
    a = ap.parse_args()
    global RUN
    if a.run:
        RUN = a.run
    a.resume = a.resume or a.from_6d
    lab.require_host_tools()
    rel1, rel2 = os.path.abspath(a.rel1), os.path.abspath(a.rel2)
    hostimage = None if a.resume else lab.hostimage_for(rel1, a.hostimage)
    work = os.path.abspath(a.workdir) if a.workdir else lab.scratch_dir(rel1, "nex-image-update-")
    os.makedirs(work, exist_ok=True)
    vm, server = VM(work), None
    try:
        # Lab stand-ins for the licence components (a real release ships its AGPL source and compliance manifest).
        source = os.path.join(work, "source.tar.gz")
        if not (a.resume and os.path.exists(source)):  # a resumed run republishes the same bytes
            subprocess.run(["tar", "czf", source, "-C", REPO, "internal/nexupdate", "scripts/update-publish.py"], check=True)
        compliance = os.path.join(os.path.dirname(rel1), "base.manifest")
        pub = publish(work, rel1, rel2, a.keys, os.path.abspath(a.trust), (source, compliance), a.rel1_state_format)
        server = Server(pub)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        port = server.server_address[1]
        slot2 = json.load(open(os.path.join(pub, "releases/2/manifest.json")))["components"][0]
        info(f"serving {pub} on 127.0.0.1:{port} (guest sees {HOST_ADDR}); release 2 slot-image blob sha256-{slot2['sha256'][:12]}...")

        if not a.resume:
            for d in ("d0.img",):
                with open(os.path.join(work, d), "wb") as f:
                    f.truncate(lab.DISK_BYTES)
            shutil.copy(lab.OVMF_VARS, os.path.join(work, "vars.fd"))
            t0 = time.time()
            install(work, rel1, hostimage, port, a.budget)
            info(f"install of release 1: {time.time() - t0:.0f}s")

        # ---- 1. boot release 1
        slot2_hash = json.load(open(os.path.join(pub, "releases/2/manifest.json")))["components"][0]["verity_root_hash"]
        PERSIST = "persisted,persisted,"
        vm.start()
        boot1 = vm.wait_ready(a.budget)
        ctl = vm.ctl
        if a.resume:
            # Continue on the disk of an earlier run (the guest's update server address is rewritten to this run's port).
            ctl.sh("sed -i 's/^base_url=.*/base_url=http:\\/\\/%s:%d/' /etc/nex/updates.conf" % (HOST_ADDR, port))
        else:
            info(f"boot 1 ready after {boot1:.0f}s")
            ctl.sh("echo persisted > /etc/nex-persist; echo persisted > /var/nex-persist; sync")
        f1 = facts(ctl)
        st = f1["status"]

        def same_data(fs, what):
            check(fs["persist"] == PERSIST, f"{what}: /etc and /var data survived", fs["persist"])
            check(fs["sshkey"] == f1["sshkey"] and fs["machine_id"] == f1["machine_id"], f"{what}: SSH host key and machine-id unchanged")

        def reboot_cmd(cmd, what):
            """Run an update command that ends in a reboot; its output goes to a file so a refusal is visible."""
            vm.ctl.drain()
            vm.ctl.send(f"{cmd} > /tmp/lastcmd.out 2>&1")
            t0 = time.time()
            try:
                vm.wait_ready(min(a.budget, 300))
            except TimeoutError:
                out = vm.ctl.sh("cat /tmp/lastcmd.out | tail -12")[1]
                check(False, f"{what}: the host rebooted", out)
                raise
            return time.time() - t0

        if not a.resume:
            check(f1["build_id"] == "2026.10.1-1", "boot 1: release 1 runs", f1["build_id"])
            check(f1["root"].startswith("erofs ro"), "boot 1: / is read-only erofs", f1["root"])
            check(st["phase"] == "idle" and st["rollback_floor"] == 0, "boot 1: engine idle", st["phase"])
            check(ctl.sh("test -f /usr/share/nex/update/trust.json")[0] == 0, "boot 1: trust.json is pinned in the verity root")
            check(ctl.sh("test -x /usr/libexec/nex/nex-boot-check && systemctl cat nex-state-guard.service > /dev/null")[0] == 0, "boot 1: boot check and state guard units are in the image")
            rc, txt = ctl.sh("findmnt /boot; findmnt /efi; systemctl show boot.mount efi.mount -p LoadState | tr '\\n' ' '")
            check("masked" in txt and "TARGET" not in txt, "boot 1: the ESP is not mounted at /boot or /efi (gpt-auto masked)", txt)
            check("EFI/Linux" not in ctl.sh("ls /boot /efi 2>&1")[1] and "nex-core_2026.10.1_1.efi" in f1["uki"], "boot 1: ESP readable only by an explicit mount", f1["uki"])
            rc, txt = ctl.sh("systemctl show boot-complete.target -p Wants -p Requires -p RequiredBy -p After; systemctl show nex-boot-check.service -p RequiredBy -p Before -p After")
            check("nex-boot-check.service" in txt, "boot 1: nex-boot-check is wired to boot-complete.target", txt)
            rc, txt = ctl.sh("systemctl list-dependencies --after --plain nex-boot-check.service", 60)
            check(not any(w in txt for w in ("chrony-wait", "time-sync.target", "network-online.target")), "boot 1: nex-boot-check is not ordered behind chrony-wait, time-sync or network-online", txt[-300:])
            check(ctl.sh("nex-update state-guard")[0] == 0, "boot 1: state guard passes on fresh state")
            check(ctl.sh(f"curl -fsS -o /dev/null http://{HOST_ADDR}:{port}/trust.json || python3 -c \"import urllib.request;urllib.request.urlopen('http://{HOST_ADDR}:{port}/trust.json')\"")[0] == 0, "boot 1: the guest reaches the update server")
            rc, txt = ctl.sh("systemctl show nex-update-reconcile.service -p Result -p ActiveState -p ExecMainStatus | tr '\\n' ' '")
            check("Result=success" in txt, "boot 1: nex-update-reconcile.service ran after boot", txt)

            # ---- 2. check, then a tampered blob
            rc, txt = ctl.sh("nex-update check 2>&1", 120)
            check(rc == 0 and '"sequence": 2' in txt, "check: release 2 is offered over HTTP", txt[-300:])
            server.tamper = f"sha256-{slot2['sha256']}"
            rc, txt = ctl.sh(f"nex-update apply -request-id lab-tamper-{RUN} -activate reboot 2>&1 | tail -5", a.budget)
            server.tamper = None
            st = status(ctl)
            err = (st.get("operation") or {}).get("error") or {}
            check(err.get("code") == "blob_mismatch", "tampered blob refused with blob_mismatch", json.dumps(st.get("operation")))
            check(st["phase"] in ("idle", "checking") and all(s["role"] != "staged" for s in st["slots"]), "tampered blob: nothing staged, host stays on release 1", st["phase"])
            check(facts(ctl)["build_id"] == "2026.10.1-1", "tampered blob: release 1 still running")

            # ---- 3. dropped download, Range resume, stage_only, then a failing boot check over the counted tries
            ctl.sh("mkdir -p /etc/systemd/system/nex-boot-check.service.d; "
                   "printf '[Service]\\nEnvironment=NEX_BOOT_CHECK_TRIES=3 NEX_BOOT_CHECK_WAIT=5\\n' > /etc/systemd/system/nex-boot-check.service.d/lab.conf; "
                   "for u in libvirtd.service libvirtd.socket libvirtd-ro.socket libvirtd-admin.socket; do ln -sf /dev/null /etc/systemd/system/$u; done; sync")
            server.cut = (f"sha256-{slot2['sha256']}", 120 << 20)
            rc, txt = ctl.sh(f"nex-update apply -request-id lab-stage1-{RUN} -activate stage_only 2>&1 | tail -3", a.budget)
            st = status(ctl)
            part = ctl.sh("ls -l /var/lib/nex/image/staging/2/ 2>&1 | tail -3")[1]
            check(st["phase"] != "staged" and ".part" in part, "dropped download: nothing staged, the partial file is kept", part)
            before = len(server.requests)
            rc, txt = ctl.sh(f"nex-update apply -request-id lab-stage2-{RUN} -activate stage_only 2>&1 | tail -3", a.budget)
            st = status(ctl)
            resumed = [r for r in server.requests[before:] if "bytes=" in r]
            check(any(int(re.search(r"bytes=(\d+)-", r).group(1)) > 0 for r in resumed), "resume: the next apply continued the download with a Range request", str(resumed))
            info(f"resume Range requests: {resumed}")
            check(st["phase"] == "staged" and any(s["role"] == "staged" and s["sequence"] == 2 for s in st["slots"]), "stage_only: release 2 written to the inactive slot, nothing armed", st["phase"])
            fs = facts(ctl)
            check("2026.10.2" not in fs["uki"], "stage_only: the next boot is still release 1 (no release 2 UKI on the ESP)", fs["uki"])
            ctl.drain()
            ctl.send(f"nex-update activate -request-id lab-act1-{RUN} 2>&1 | tail -3")
            t0 = time.time()
            seen = boot_until(vm, a.budget, "2026.10.1-1", limit=8)
            info(f"failing trial: boots seen {seen} after {time.time() - t0:.0f}s")
            trial_diag(vm, seen)
            fs = facts(vm.ctl)
            ctl = vm.ctl
            st = fs["status"]
            check(fs["build_id"] == "2026.10.1-1", "failed trial: back on release 1", fs["build_id"])
            check(st["phase"] == "rolled_back" and (st.get("failure") or {}).get("code") == "boot_failed", "failed trial: reported rolled_back / boot_failed", json.dumps({k: st.get(k) for k in ("phase", "failure")}))
            check(re.search(r"2026\.10\.2_2\+0-3\b", fs["uki"]) is not None, "failed trial: release 2's entry used all three tries (+0-3)", fs["uki"])
            rc, txt = ctl.sh("journalctl -u nex-boot-check --no-pager -o cat 2>&1 | grep -c 'boot tries left'")
            tl = int(txt.strip() or 0) if rc == 0 or txt.strip().isdigit() else 0
            check(tl >= 2, "failed trial: the first counted boots rebooted without marking bad (tries left, then the last try marked bad)", f"{tl} 'tries left' lines")
            rc, txt = ctl.sh("journalctl -u nex-boot-check -u systemd-bless-boot --no-pager -o cat 2>&1 | grep -i \"tries left\\|Marked boot\\|nex-boot-check:\" | tail -8")
            info("trial assessments: " + txt.replace("\n", " | "))
            same_data(fs, "failed trial")


        if not a.from_6d:
            # ---- 4. fault removed: update succeeds
            ctl.sh("rm -f /etc/systemd/system/libvirtd.service /etc/systemd/system/libvirtd.socket /etc/systemd/system/libvirtd-ro.socket "
                   "/etc/systemd/system/libvirtd-admin.socket /etc/systemd/system/nex-boot-check.service.d/lab.conf; sync")
            # The engine refuses to reboot a host whose VM inventory it cannot read (fail closed), so libvirtd must be back.
            rc, txt = ctl.sh("systemctl daemon-reload; systemctl start libvirtd.service; timeout 60 virsh -q -c qemu:///system list --all; echo virsh=$?", 120)
            check("virsh=0" in txt, "fault removed: libvirtd answers again on release 1", txt)
            dt = reboot_cmd(f"nex-update apply -request-id lab-apply-{RUN} -activate reboot", "apply -activate reboot")
            ctl = vm.ctl
            info(f"apply + activate: back in control on the new version after {dt:.0f}s")
            phase_raw = await_commit(ctl)
            check('"committed"' in phase_raw, "update: nex-update-reconcile.service alone left the record committed (read before any CLI call)", phase_raw)
            fs = facts(ctl)
            st = fs["status"]
            check(fs["build_id"] == "2026.10.2-2", "update: release 2 runs", fs["build_id"])
            check(fs["cmdline_roothash"] == slot2_hash, "update: running roothash is the signed manifest's")
            check(st["phase"] == "committed" and any(s["role"] == "previous" and s["sequence"] == 1 for s in st["slots"]), "update: committed, release 1 retained as previous", json.dumps(st["slots"]))
            check("2026.10.2_2.efi" in fs["uki"] and "2026.10.2_2+" not in fs["uki"], "update: release 2's boot entry lost its counter (blessed)", fs["uki"])
            for _ in range(12):  # the bless runs after the boot check; run 17 read the journal a moment too early
                rc, txt = ctl.sh("journalctl -b -u systemd-bless-boot -u nex-boot-check --no-pager -o cat 2>&1 | tail -30")
                if "Marked boot as 'good'" in txt or "Finished systemd-bless-boot" in txt:
                    break
                time.sleep(5)
            check("Marked boot as 'good'" in txt or "Finished systemd-bless-boot" in txt, "update: systemd-bless-boot marked the boot good", txt)
            rc, txt = ctl.sh("systemctl show nex-update-reconcile.service -p Result -p ExecMainStatus -p ActiveEnterTimestamp | tr '\\n' ' '; journalctl -b -u nex-update-reconcile --no-pager -o short-precise | tail -12 | cut -c1-300")
            check("Result=success" in txt and "ExecMainStatus=0" in txt, "update: nex-update-reconcile.service succeeded on this boot", txt)
            same_data(fs, "update")
            check(fs["root"].startswith("erofs ro"), "update: / is read-only erofs", fs["root"])

            # ---- 5. rollback
            reboot_cmd(f"nex-update rollback -request-id lab-roll1-{RUN}", "rollback")
            ctl = vm.ctl
            time.sleep(lab.scaled(20))
            fs = facts(ctl)
            check(fs["build_id"] == "2026.10.1-1", "rollback: release 1 runs again", fs["build_id"])
            same_data(fs, "rollback")
            info(f"after rollback: phase={fs['status']['phase']} esp={fs['uki']}")

        # ---- 6. power cuts. After each cut the host must run the old or the new version coherently.
        def coherent(label, expect_builds):
            """After a cut: a booted, coherent host: running build is one of expect_builds, / is erofs, data intact."""
            vm.wait_ready(a.budget)
            c = vm.ctl
            time.sleep(3)
            b = c.sh(". /usr/lib/os-release; echo $BUILD_ID")[1]
            rh = c.sh("tr ' ' '\\n' < /proc/cmdline | sed -n 's/^roothash=//p'")[1]
            check(b in expect_builds, f"{label}: boots {b}", f"expected one of {expect_builds}")
            check(c.sh("findmnt -no FSTYPE,OPTIONS / | grep -q '^erofs ro'")[0] == 0 and c.sh("cat /etc/nex-persist /var/nex-persist | tr '\\n' ,")[1] == PERSIST,
                  f"{label}: / read-only erofs, /etc and /var data intact")
            info(f"{label}: build {b}, roothash {rh[:12]}, esp {esp_ls(c)}")
            return c, b

        def wait_for(c, shell_cond, trigger):
            """Run `trigger` in the background in the guest, wait until `shell_cond` holds, print a marker."""
            c.drain()
            c.send(f"( {trigger} ) > /dev/null 2>&1 & n=0; until {shell_cond} || [ $n -ge 3000 ]; do n=$((n+1)); sleep 0.02; done; echo CUT''NOW")
            c.expect("CUTNOW", a.budget)

        if not a.from_6d:
            # 6a. during the slot write (the re-download of release 2 comes first, then the write)
            wait_for(ctl, "tr -d ' \\n' < /var/lib/nex/image/state.json | grep -q '\"phase\":\"writing\"'", f"nex-update apply -request-id lab-cut-{RUN} -activate stage_only")
            time.sleep(1.0)
            vm.cut_power("kill9")
            ctl, b = coherent("power cut during the slot write (kill -9)", ("2026.10.1-1",))
            info("record after the cut: " + raw_phase(ctl))
            # Resumed with the same request id; while it runs, a second apply and a cancel from other processes (the lock is held
            # for the whole operation, so both are refused and the running apply is neither cancelled nor loses its staging), and
            # the reconcile service (reads only).
            ctl.drain()
            ctl.send(f"nex-update apply -request-id lab-cut-{RUN} -activate stage_only > /tmp/resume.out 2>&1 &")
            time.sleep(1)  # the resumed apply holds the lock (its download alone takes longer)
            r1 = ctl.sh(f"nex-update apply -request-id lab-other-{RUN} -activate stage_only > /tmp/o1.out 2>&1 & p=$!; "
                        f"nex-update cancel -request-id lab-cancel-{RUN} > /tmp/o2.out 2>&1; wait $p; "
                        "echo APPLY2: $(tail -c 300 /tmp/o1.out); echo CANCEL: $(tail -c 300 /tmp/o2.out)")
            r3 = ctl.sh("systemctl start nex-update-reconcile.service; echo rc=$?")
            info(f"concurrent: {r1[1][:400]!r}; reconcile service start -> {r3[1]!r}")
            check("APPLY2: " in r1[1] and "update_in_progress" in r1[1].split("APPLY2:")[1].split("CANCEL:")[0], "exclusion: a second CLI apply while one runs is refused with update_in_progress", r1[1])
            check("CANCEL: " in r1[1] and "update_in_progress" in r1[1].split("CANCEL:")[1], "exclusion: a cancel from a second process while an apply runs is refused with update_in_progress", r1[1])
            check("rc=0" in r3[1], "exclusion: the reconcile service coexists with a running apply (reads, never writes)", r3[1])
            # The phase turns "staged" a moment before the operation is finished and recorded; cutting power in that gap leaves
            # an interrupted operation behind and fails every later step, so wait for the operation itself.
            t0 = time.time()
            while True:
                st = status(ctl)
                op = st.get("operation") or {}
                if (op.get("id") == f"lab-cut-{RUN}" and op.get("finished_at")) or time.time() - t0 > 300:
                    break
                time.sleep(2)
            check(op.get("id") == f"lab-cut-{RUN}" and op.get("phase") != "cancelled" and op.get("finished_at"), "exclusion: the resumed apply finished on its own (not cancelled by the second process)", json.dumps(op))
            check(st["phase"] == "staged" and any(s["role"] == "staged" and s["sequence"] == 2 for s in st["slots"]), "after the torn slot write: the resumed apply stages release 2 cleanly", st["phase"])

            # 6b. after the slot write, before arming (staged): cut, boot old, activate still works
            vm.cut_power("kill9")
            ctl, b = coherent("power cut after the write, before arm (kill -9)", ("2026.10.1-1",))
            st = status(ctl)
            listing = esp_ls(ctl)
            # The release-2 entry a rollback leaves behind is marked bad (+0-N); a cut must not leave a live (armed) one or a partial file.
            check(st["phase"] == "staged" and re.search(r"2026\.10\.2_2(\.efi|\+[1-9])|\.new", listing) is None, "staged survives the cut; nothing armed and no partial file", f"{st['phase']} {listing}")

            # 6c. a half-written .efi.new on the ESP: what a cut in the middle of the UKI copy leaves. Injected (the copy's window is
            # milliseconds wide), cut again, and the next activate must arm over it.
            ctl.sh((ESP_MOUNT % "rw") + "head -c 5000000 $m/EFI/Linux/nex-core_2026.10.1_1.efi > $m/EFI/Linux/nex-core_2026.10.2_2+3.efi.new; sync" + ESP_DONE)
            vm.cut_power("kill9")
            ctl, b = coherent("power cut with a half-written .efi.new on the ESP (kill -9)", ("2026.10.1-1",))
            listing = esp_ls(ctl)
            info(f"ESP after the cut with a torn file: {listing}")
            st = status(ctl)
            check(".efi.new" in listing and st["phase"] == "staged", "torn .efi.new: the loader ignored it, release 1 booted, the release is still staged", f"{st['phase']} {listing}")
            info(f"record after .new cuts: phase {st['phase']}")

        # 6d. after arm, before bless: cut as soon as the +3 entry exists (QMP quit). That is inside the engine's arm (the entry is
        # renamed into place, the directory is flushed and the bytes verified only afterwards): the loader may or may not list it.
        # Old or new must boot coherently; then the same request id must finish the arm and reboot, and a second cut in the counted
        # trial boot, before the bless, must again leave a coherent host.
        if not a.from_6d:
            st = status(ctl)
            if st["phase"] == "staged":
                trigger = f"nex-update activate -request-id lab-act3-{RUN}"
            else:
                trigger = f"nex-update apply -request-id lab-act3-{RUN} -activate reboot"
            wait_for(ctl, "ls /run/nex/esp/*/EFI/Linux/*2_2+3.efi > /dev/null 2>&1", trigger)
            vm.cut_power("qmp")
            ctl, b = coherent("power cut after arm, before the reboot (QMP quit)", ("2026.10.1-1", "2026.10.2-2"))
        else:
            b = ctl.sh(". /usr/lib/os-release; echo $BUILD_ID")[1]  # booted after the earlier run's cut
        info(f"first boot after arm: {b}, esp {esp_ls(ctl)}")
        if b == "2026.10.1-1":
            st = status(ctl)
            info(f"record after the arm cut: phase {st['phase']}, operation {json.dumps(st.get('operation'))[:200]}")
            vm.ctl.drain()
            vm.ctl.send(f"nex-update activate -request-id lab-act3-{RUN} > /tmp/act3.out 2>&1")
            t0 = time.time()
            ok = False
            while time.time() - t0 < lab.scaled(150) and not ok:
                t = vm.con.log_text()
                ok = slot2_hash in t[max(t.rfind("BdsDxe: loading"), 0):] and "Linux version" in t[max(t.rfind("BdsDxe: loading"), 0):]
                time.sleep(0.5)
            if not ok:
                out = vm.ctl.sh("tail -c 400 /tmp/act3.out; echo; tail -c 300 /var/lib/nex/image/state.json | head -c 0; tr -d ' \\n' < /var/lib/nex/image/state.json | grep -o '\"phase\":\"[a-z_]*\"' | head -3", 30)[1]
            check(ok, "arm cut: repeating the request id finishes the arm and reboots into release 2's counted trial", "" if ok else out)
            if not ok:
                vm.ctl.send("systemctl reboot")
            time.sleep(6)  # in the boot, before the boot check, the bless and the reconcile service
            b = "2026.10.2-2"
        if b == "2026.10.2-2":
            vm.cut_power("kill9")  # in the trial boot, before the bless
            ctl, b = coherent("power cut in the counted trial boot, before the bless (kill -9)", ("2026.10.1-1", "2026.10.2-2"))
        seen = []
        for _ in range(12):
            try:
                b = ctl.sh(". /usr/lib/os-release; echo $BUILD_ID", 20)[1]
                listing = esp_ls(ctl)
            except TimeoutError:  # the host rebooted under us (a failed counted boot): wait for the next one
                vm.wait_ready(a.budget)
                ctl = vm.ctl
                continue
            seen.append(b)
            if b == "2026.10.2-2" and "2026.10.2_2+" not in listing:
                break
            time.sleep(10)
        fs = facts(ctl)
        check(fs["build_id"] == "2026.10.2-2" and "2026.10.2_2.efi" in fs["uki"] and "2026.10.2_2+" not in fs["uki"] and ".new" not in fs["uki"],
              "after the cuts: release 2 boots, passes its check and is blessed; the torn .efi.new is gone", f"{seen} {fs['uki']}")
        check(fs["status"]["phase"] == "committed", "after the cuts: record committed", fs["status"]["phase"])
        same_data(fs, "after the power cuts")

        # ---- 7. optional older-format refusal and boot fallback (actual retained image checked first)
        prove_state_guard(vm, a.budget, same_data, a.rel1_state_format)
        ctl = vm.ctl

        # ---- 8. once more in the same run, from release 2 over the retained records of everything above:
        # rollback, then a failing boot check (fallback to release 1 after the counted boots), then the happy path.
        reboot_cmd(f"nex-update rollback -request-id lab-roll3-{RUN}", "second rollback")
        ctl = vm.ctl
        time.sleep(lab.scaled(20))
        fs = facts(ctl)
        check(fs["build_id"] == "2026.10.1-1", "again: rollback to release 1 works once the state needs no newer format", fs["build_id"])
        same_data(fs, "again: rollback")
        ctl.sh("mkdir -p /etc/systemd/system/nex-boot-check.service.d; "
               "printf '[Service]\\nEnvironment=NEX_BOOT_CHECK_TRIES=3 NEX_BOOT_CHECK_WAIT=5\\n' > /etc/systemd/system/nex-boot-check.service.d/lab.conf; "
               "for u in libvirtd.service libvirtd.socket libvirtd-ro.socket libvirtd-admin.socket; do ln -sf /dev/null /etc/systemd/system/$u; done; sync")
        rc, txt = ctl.sh(f"nex-update apply -request-id lab-stage3-{RUN} -activate stage_only 2>&1 | tail -3", a.budget)
        st = status(ctl)
        check(st["phase"] == "staged" and any(s["role"] == "staged" and s["sequence"] == 2 for s in st["slots"]), "again: release 2 staged", f"{st['phase']} {txt[-200:]}")
        ctl.drain()
        ctl.send(f"nex-update activate -request-id lab-act4-{RUN} 2>&1 | tail -3")
        t0 = time.time()
        seen = boot_until(vm, a.budget, "2026.10.1-1", limit=8)
        info(f"again, failing trial: boots seen {seen} after {time.time() - t0:.0f}s")
        trial_diag(vm, seen)
        ctl = vm.ctl
        fs = facts(ctl)
        st = fs["status"]
        check(fs["build_id"] == "2026.10.1-1", "again, failed trial: back on release 1", fs["build_id"])
        check(st["phase"] == "rolled_back" and (st.get("failure") or {}).get("code") == "boot_failed", "again, failed trial: rolled_back / boot_failed", json.dumps({k: st.get(k) for k in ("phase", "failure")}))
        check(re.search(r"2026\.10\.2_2\+0-3\b", fs["uki"]) is not None, "again, failed trial: release 2's entry used all three tries", fs["uki"])
        same_data(fs, "again, failed trial")
        ctl.sh("rm -f /etc/systemd/system/libvirtd.service /etc/systemd/system/libvirtd.socket /etc/systemd/system/libvirtd-ro.socket "
               "/etc/systemd/system/libvirtd-admin.socket /etc/systemd/system/nex-boot-check.service.d/lab.conf; sync")
        rc, txt = ctl.sh("systemctl daemon-reload; systemctl start libvirtd.service; timeout 60 virsh -q -c qemu:///system list --all; echo virsh=$?", 120)
        check("virsh=0" in txt, "again: libvirtd answers again", txt)
        dt = reboot_cmd(f"nex-update apply -request-id lab-apply2-{RUN} -activate reboot", "again: apply -activate reboot")
        ctl = vm.ctl
        await_commit(ctl)
        fs = facts(ctl)
        st = fs["status"]
        check(fs["build_id"] == "2026.10.2-2" and fs["cmdline_roothash"] == slot2_hash, "again, happy path: release 2 runs, roothash is the signed manifest's", fs["build_id"])
        check(st["phase"] == "committed" and "2026.10.2_2.efi" in fs["uki"] and "2026.10.2_2+" not in fs["uki"], "again, happy path: committed and blessed", f"{st['phase']} {fs['uki']}")
        same_data(fs, "again, happy path")
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
