#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Install-and-boot scenarios for the image-edition ISO in a local QEMU/KVM lab (development evidence only).

Uses the ISO from image_iso_lab.py. Everything here is SIMULATION: virtual UEFI (OVMF) or SeaBIOS, virtio disks,
user-mode networking; optional virtual Secure Boot (OVMF with enrolled keys) and a virtual TPM2 (swtpm). Nothing
establishes physical-hardware behaviour.

  image_install_lab.py --iso ISO --work DIR --payload-sha SHA single|bios|raid1|raid1-alone|held-array|repair|repair-boot|interrupted

One VM at a time, -smp 2 -m 2048, every wait has its own budget. The installer runs the product code; only the
wizard is a lab stub (see image_iso_lab.py).
"""
import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import image_boot as lab  # noqa: E402  (Console, OVMF paths, check)
from image_update import Ctl  # noqa: E402  (control shell on the second serial port)
from image_iso_lab import LAB_PASSWORD  # noqa: E402

REPO = lab.REPO
GIB = 1 << 30
DISK_GIB = 25  # the image edition floor is 24 GiB
check = lab.check


def info(text):
    print("INFO  " + text, flush=True)


def new_disk(path, gib=DISK_GIB):
    with open(path, "wb") as f:
        f.truncate(gib * GIB)


def ctl_disk(path, mode, serials, hostname="nex-lab-host", hold=False):
    with open(path, "wb") as f:
        f.truncate(1 << 20)
        f.write(f"mode={mode}\nserials={','.join(serials)}\nhostname={hostname}\n{'hold=1' + chr(10) if hold else ''}".encode())


def digest(path, first_last_only=False):
    """SHA-256 of the whole file or of its first and last MiB (sparse files read fast)."""
    h = hashlib.sha256()
    size = os.path.getsize(path)
    with open(path, "rb") as f:
        if first_last_only:
            h.update(f.read(1 << 20))
            f.seek(size - (1 << 20))
            h.update(f.read(1 << 20))
        else:
            for chunk in iter(lambda: f.read(64 << 20), b""):
                h.update(chunk)
    return h.hexdigest()


def partitions(path):
    """{name: (offset, size)} of the GPT on a disk image, from sfdisk (works on plain files)."""
    out = json.loads(subprocess.run(["sfdisk", "-J", path], check=True, capture_output=True, text=True).stdout)
    res = {}
    for p in out["partitiontable"]["partitions"]:
        res[p.get("name", "")] = (p["start"] * 512, p["size"] * 512)
    return res


def region_digest(path, offset, size):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        f.seek(offset)
        left = size
        while left:
            chunk = f.read(min(left, 64 << 20))
            if not chunk:
                break
            h.update(chunk)
            left -= len(chunk)
    return h.hexdigest()


SSH_PORT = 22022
API_PORT = 19443  # the guest's nex-core-local listens on its own address (10.0.2.15:9443); user-mode networking forwards to it


def qemu(work, disks, *, iso=None, ctl=None, uefi=True, vars_file=None, code=None, tpm=None, log="console.log", extra=()):
    """Start one VM. disks: [(path, serial)]. Returns (proc, Console)."""
    cmd = ["qemu-system-x86_64", *lab.qemu_accel(), "-machine", "q35,smm=on" if code and "secboot" in code else "q35",
           "-m", "2048", "-smp", "2", "-display", "none", "-monitor", "none", "-serial", "stdio", "-serial", f"unix:{work}/ctl.sock,server=on,wait=off", "-no-reboot"]
    if uefi:
        cmd += ["-drive", f"if=pflash,format=raw,unit=0,file={code or lab.OVMF_CODE},readonly=on",
                "-drive", f"if=pflash,format=raw,unit=1,file={vars_file}"]
        if code and "secboot" in code:
            cmd += ["-global", "driver=cfi.pflash01,property=secure,value=on"]
    if iso:
        cmd += ["-drive", f"if=none,id=cd,media=cdrom,readonly=on,file={iso}", "-device", "ide-cd,drive=cd,bootindex=1"]
    for i, (path, serial) in enumerate(disks):
        cmd += ["-drive", f"if=none,id=d{i},format=raw,file={path},discard=unmap,detect-zeroes=unmap",
                "-device", f"virtio-blk-pci,drive=d{i},serial={serial},bootindex={i + 2}"]
    if ctl:
        cmd += ["-drive", f"if=none,id=ctl,format=raw,file={ctl}", "-device", "virtio-blk-pci,drive=ctl,serial=NEXLABCTL"]
    if tpm:
        cmd += ["-chardev", f"socket,id=chrtpm,path={tpm}", "-tpmdev", "emulator,id=tpm0,chardev=chrtpm",
                "-device", "tpm-tis,tpmdev=tpm0"]
    cmd += ["-netdev", f"user,id=n0,hostfwd=tcp:127.0.0.1:{SSH_PORT}-:22,hostfwd=tcp:127.0.0.1:{API_PORT}-:9443", "-device", "virtio-net-pci,netdev=n0", "-device", "virtio-rng-pci"] + list(extra)
    sock = os.path.join(work, "ctl.sock")
    if os.path.exists(sock):
        os.unlink(sock)
    proc = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    con = lab.Console(proc, open(os.path.join(work, log), "a"))
    con.ctl_path, con.ctl_log = sock, open(os.path.join(work, "control.log"), "a")
    return proc, con


def stop(proc):
    if proc and proc.poll() is None:
        proc.kill()
    if proc:
        proc.wait(30)


def run_installer(work, iso, disks, ctl, vars_file, budget=480, kill_on=None, label="installer", **kw):
    """Boot the ISO until the installer restarts the host (-no-reboot makes QEMU exit). Returns (exit_state, console_text).
    kill_on: regex; kill the VM as soon as the console shows it (power cut). exit_state: done|killed|timeout|refused."""
    proc, con = qemu(work, disks, iso=iso, ctl=ctl, vars_file=vars_file, log=f"{label}.log", **kw)
    start = time.time()
    state = "timeout"
    try:
        while time.time() - start < budget:
            try:
                con.expect("\n", 2)
            except TimeoutError:
                pass
            except RuntimeError:
                state = "done"  # qemu exited: the installer rebooted the host
                break
            text = con.log_text()
            if kill_on and re.search(kill_on, text):
                stop(proc)
                state = "killed"
                break
            if re.search(r"LAB-VALIDATE-REFUSED|installs on UEFI firmware only|No disk has been changed|Could not stop .*which holds a selected disk", text):
                state = "refused"
                break
            if proc.poll() is not None:
                state = "done"
                break
    finally:
        text = con.log_text()
        stop(proc)
    info(f"{label}: {state} after {time.time() - start:.0f}s of {budget}s")
    return state, text


class Shell:
    """Root shell on the installed host's second serial port (a lab-only unit the ISO's lab seed adds to /etc)."""

    def __init__(self, con):
        self.con, self.proc = con, con.proc
        self.ctl = Ctl(con.ctl_path, con.ctl_log)

    def sh(self, cmd, timeout=60):
        return self.ctl.sh(cmd, timeout)


def power_off(shell, proc):
    """Clean power-off through the guest; the VM is killed if it has not stopped in 90 s."""
    try:
        shell.sh("sync", 30)
        shell.ctl.send("systemctl poweroff")
    except Exception:
        pass
    try:
        proc.wait(90)
    except subprocess.TimeoutExpired:
        pass


def login(con, timeout=240):
    """Wait for the kernel (OVMF feeds both serial ports to its boot menu, so nothing is sent earlier), then knock
    on the control shell until it answers; returns a Shell or None."""
    end = time.time() + timeout
    while time.time() < end and con.proc.poll() is None:
        t = con.log_text()
        if "Linux version" in t[max(t.rfind("BdsDxe: loading"), 0):]:
            break
        time.sleep(2)
    else:
        return None
    shell = Shell(con)
    while time.time() < end and con.proc.poll() is None:
        shell.ctl.drain()
        shell.ctl.send("echo BOOT-UP")
        shell.ctl._read(time.time() + 4)
        if "BOOT-UP" in shell.ctl.buf:
            time.sleep(5)  # late answers to earlier knocks
            shell.ctl.drain()
            shell.sh("true", 30)
            return shell
    return None


def console_login(con, timeout=180):
    """Sign in as root on the serial console the way an operator does (agetty -> login -> PAM): a wrong password is
    refused first, then the installer's password must open a root shell."""
    def type_line(text):
        con.proc.stdin.write((text + "\n").encode())
        con.proc.stdin.flush()
    try:
        con.expect("login: ", timeout)
        type_line("root")
        con.expect("Password: ", 30)
        type_line("not-the-installer-password")
        con.expect("Login incorrect", 30)
        con.expect("login: ", 30)
        type_line("root")
        con.expect("Password: ", 30)
        type_line(LAB_PASSWORD)
        con.expect("~# ", 90)  # login flushes anything typed before the shell is up
        rc, out = con.sh("id -un", 60)
    except (TimeoutError, RuntimeError) as e:
        info(f"console login: {e}"[:300])
        return False
    return rc == 0 and re.sub(r"\x1b\[[0-9;?]*[A-Za-z]", "", out).splitlines()[-1].strip() == "root"


def ssh_root(work, password, cmd):
    """One password-only SSH login as root through the forwarded port (SSH_ASKPASS feeds the password)."""
    askpass = os.path.join(work, "askpass.sh")
    with open(askpass, "w") as f:
        f.write('#!/bin/sh\nprintf "%s\\n" "$NEXLAB_PW"\n')
    os.chmod(askpass, 0o700)
    env = dict(os.environ, SSH_ASKPASS=askpass, SSH_ASKPASS_REQUIRE="force", NEXLAB_PW=password, DISPLAY="lab")
    r = subprocess.run(["ssh", "-p", str(SSH_PORT), "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR",
                        "-o", "PreferredAuthentications=password,keyboard-interactive", "-o", "PubkeyAuthentication=no", "-o", "NumberOfPasswordPrompts=1",
                        "-o", "ConnectTimeout=20", "root@127.0.0.1", cmd], env=env, capture_output=True, text=True, timeout=60)
    return r.returncode, (r.stdout + r.stderr).strip()


def curl(*args):
    r = subprocess.run(["curl", "-sk", "--max-time", "20", "-w", "\n%{http_code}", *args], capture_output=True, text=True)
    body, _, code = r.stdout.rpartition("\n")
    return int(code or 0), body


def api_root(work, password):
    """nex-core-local through the forwarded management port: /healthz, a refused and an accepted root sign-in, the session's
    CSRF token on a state-changing call, and GET /api/local/v1/updates with the session."""
    base, jar = f"https://127.0.0.1:{API_PORT}", os.path.join(work, "api.cookies")
    if os.path.exists(jar):
        os.unlink(jar)
    origin = ["-H", f"Origin: {base}"]  # the API accepts a state-changing call from its own origin only
    json_header = ["-H", "Content-Type: application/json", *origin]
    code, _ = curl(f"{base}/healthz")
    check(code == 200, "API: /healthz on the host address answers 200", str(code))
    code, _ = curl("-c", jar, *json_header, "-d", json.dumps({"username": "root", "password": "not-the-installer-password"}), f"{base}/api/local/v1/session")
    check(code == 401, "API: sign-in with a wrong root password is refused (401)", str(code))
    code, body = curl("-c", jar, *json_header, "-d", json.dumps({"username": "root", "password": password}), f"{base}/api/local/v1/session")
    csrf = json.loads(body).get("csrf_token", "") if code == 200 else ""
    check(code == 200 and csrf, "API: sign-in with the installer's root password opens a session (200, CSRF token)", f"{code} {body[:80]}")
    code, body = curl("-b", jar, f"{base}/api/local/v1/updates")
    check(code == 200, "API: GET /api/local/v1/updates with the session answers 200", f"{code} {body[:160]}")
    info(f"API updates answer: {body[:200]}")
    code, _ = curl("-b", jar, "-X", "DELETE", *origin, f"{base}/api/local/v1/session")
    check(code == 403, "API: a state-changing call without the CSRF token is refused (403)", str(code))
    code, _ = curl("-b", jar, "-X", "DELETE", *origin, "-H", f"X-CSRF-Token: {csrf}", f"{base}/api/local/v1/session")
    check(code == 200, "API: the same call with the CSRF token is accepted (200)", str(code))
    code, _ = curl("-b", jar, f"{base}/api/local/v1/updates")
    check(code == 401, "API: the signed-out session no longer reads /updates (401)", str(code))


# Two 32 MiB members with different content stand for partitions left over from an earlier install.
ASSUME_CLEAN = (
    "d=/var/tmp/mdexp; rm -rf $d; mkdir -p $d; head -c 33554432 /dev/urandom > $d/a; head -c 33554432 /dev/urandom > $d/b; "
    "la=$(losetup -f --show $d/a); lb=$(losetup -f --show $d/b); res=; "
    "for mode in --assume-clean resync; do "
    "mdadm --zero-superblock $la $lb 2>/dev/null; flag=$mode; [ $mode = resync ] && flag=; "
    "mdadm --create /dev/md90 --run --level=1 --metadata=1.2 --bitmap=internal --raid-devices=2 $flag $la $lb >/dev/null 2>&1; "
    "mdadm --wait /dev/md90 >/dev/null 2>&1; echo check > /sys/block/md90/md/sync_action; sleep 1; mdadm --wait /dev/md90 >/dev/null 2>&1; "
    "res=\"$res $mode:mismatch_cnt=$(cat /sys/block/md90/md/mismatch_cnt)\"; mdadm --stop /dev/md90 >/dev/null 2>&1; done; "
    "losetup -d $la $lb; rm -rf $d; echo $res")


def root_access(work, md=False):
    """After-boot checks of the installer's root password at every door: console, SSH, nex-core-local (and the md experiment)."""
    def run(sh, con):
        check(console_login(con), "root: console login (wrong password refused, the installer's password opens a root shell)")
        rc, out = ssh_root(work, "not-the-installer-password", "true")
        check(rc != 0 and "Permission denied" in out, "root: SSH with a wrong password is refused", out[-120:])
        rc, out = ssh_root(work, LAB_PASSWORD, "id -un")
        check(rc == 0 and out.endswith("root"), "root: SSH password login with the installer's password", out[-160:])
        api_root(work, LAB_PASSWORD)
        rc, out = sh.sh("getent passwd | wc -l; getent shadow | wc -l; wc -l < /etc/shadow", 30)
        info("passwd entries / shadow entries / lines of /etc/shadow: " + out.replace("\n", " / "))
        if md:
            rc, out = sh.sh(ASSUME_CLEAN, 120)
            info("fresh mirror of members with different content, then a scrub: " + out.splitlines()[-1])
    return run


PROBE = {
    "root": "findmnt -no FSTYPE,OPTIONS / | tr ' ' ,",
    "verity": "dmsetup status root | tr ' ' _",
    "etc": "findmnt -no FSTYPE /etc",
    "var": "findmnt -no SOURCE,FSTYPE /var | tr ' ' ,",
    "mdarr": "grep -Ec '^md[0-9]+ : .*raid1' /proc/mdstat",
    "mddeg": "cat /sys/block/md*/md/degraded 2>/dev/null | head -1",
    "usrwrite": "touch /usr/x 2>&1 | tr ' ' _",
    "hostname": "hostname",
    "ifaces": "cat /etc/network/interfaces /etc/network/interfaces.d/* 2>/dev/null | tr '\\n ' ';_' | cut -c1-300",
    "ip": "ip -4 -br addr | grep -v '^lo' | tr -s ' ' _ | tr '\\n' ';'",
    "sshkey": "cut -d' ' -f2 /etc/ssh/ssh_host_ed25519_key.pub",
    "sshd": "systemctl is-active ssh",
    "core": "systemctl is-active nex-core-local",
    "health": "for i in $(seq 1 20); do r=$(python3 -c \"import ssl,urllib.request;c=ssl._create_unverified_context();print(urllib.request.urlopen('https://'+__import__('subprocess').check_output(['hostname','-I']).split()[0].decode()+':9443/healthz',context=c,timeout=10).status)\" 2>&1 | tail -1); case $r in *200) break;; esac; sleep 2; done; echo $r",
    "payload": "ls /var/lib/nex/payload/*/ | tr '\\n' ' '",
    "payloadsum": "for d in /var/lib/nex/payload/*/; do for f in controller.qcow2; do [ -f $d$f ] && echo $(sha256sum $d$f | cut -d' ' -f1) $(basename $d); done; done",
    "failed": "systemctl --failed --no-legend | awk '{print $2}' | tr '\\n' ,",
    "shadow": "cut -d: -f1,2 /etc/shadow | cut -c1-16 | tr '\\n' ' '; passwd -S root 2>&1 | tr ' ' _",
    "cmdline": "tr ' ' '\\n' < /proc/cmdline | grep -E '^(roothash)=' | cut -c1-30",
}


def probe(con):
    got = {}
    try:
        rc, out = con.sh("timeout 150 systemctl is-system-running --wait", 170)
        info(f"system state before the probe: {out.strip()[-40:]}")
    except TimeoutError:
        info("the system did not settle before the probe")
    for k, cmd in PROBE.items():
        try:
            rc, out = con.sh(cmd, 60)
        except TimeoutError:
            rc, out = -1, "TIMEOUT"
        got[k] = out.strip()
    return got


def verify_host(got, label, layout, payload_sha, expect_host="nex-lab-host", degraded=None):
    root = got.get("root", "")
    check(root.startswith("erofs") and ",ro," in "," + root + ",", f"{label}: / is read-only erofs", root)
    check("verity" in got.get("verity", "") and "_V" in got.get("verity", "") + "_", f"{label}: root is dm-verity, status V", got.get("verity"))
    check(got.get("etc") == "overlay", f"{label}: /etc is the overlay", got.get("etc"))
    check(got.get("var", "").endswith("ext4"), f"{label}: /var is ext4 on nex-data", got.get("var"))
    if layout == "raid1":
        check(got.get("var", "").startswith("/dev/md") and got.get("mdarr") == "1", f"{label}: /var on md RAID1", got.get("var"))
        if degraded is not None:
            check(got.get("mddeg") == degraded, f"{label}: md degraded={degraded}", got.get("mddeg"))
    check("ead-only" in got.get("usrwrite", ""), f"{label}: /usr is not writable", got.get("usrwrite"))
    check(got.get("hostname") == expect_host, f"{label}: installer-seeded hostname", got.get("hostname"))
    check("br0" in got.get("ifaces", "") or "nex0" in got.get("ifaces", ""), f"{label}: installer-seeded network config", got.get("ifaces"))
    check(len(got.get("sshkey", "")) > 40 and got.get("sshd") == "active", f"{label}: SSH host key generated at first boot, sshd active", str(got.get("sshd")))
    check(got.get("core") == "active" and got.get("health", "").endswith("200"), f"{label}: nex-core-local active, /healthz 200", f"{got.get('core')} {got.get('health')}")
    check(payload_sha in got.get("payloadsum", ""), f"{label}: payload under /var/lib/nex/payload/<sha256>/ with matching hash", got.get("payloadsum"))
    info(f"{label}: ip={got.get('ip')} failed-units={got.get('failed')!r} cmdline={got.get('cmdline')}")


def boot_and_probe(work, disks, vars_file, label, layout, payload_sha, poweroff=True, degraded=None, after=None, **kw):
    proc, con = qemu(work, disks, vars_file=vars_file, log=f"{label.replace(' ', '_')}.log", **kw)
    t0 = time.time()
    try:
        ok = login(con, 300)
        check(bool(ok), f"{label}: booted to the lab control shell after {time.time() - t0:.0f}s")
        if not ok:
            return None
        got = probe(ok)
        verify_host(got, label, layout, payload_sha, degraded=degraded)
        if after:
            try:
                after(ok, con)
            except (TimeoutError, RuntimeError, subprocess.TimeoutExpired) as e:
                check(False, f"{label}: follow-up checks", str(e)[:300])
        if poweroff:
            power_off(ok, proc)
            try:
                proc.wait(90)
            except subprocess.TimeoutExpired:
                pass
        return got
    finally:
        stop(proc)


def payload_sha_of(iso_work):
    return open(os.path.join(iso_work, "payload.sha256")).read().strip()


def scenario_single(a):
    work, vars_file = fresh_dir(a, "single")
    d = os.path.join(work, "d0.img"); new_disk(d)
    ctl = os.path.join(work, "ctl.img"); ctl_disk(ctl, "install", ["NEXDISK0"])
    state, text = run_installer(work, a.iso, [(d, "NEXDISK0")], ctl, vars_file, label="install-single")
    check(state == "done", "single: installer ran to its restart (virtual UEFI)", state)
    if state != "done":
        print(text[-1500:])
        return
    boot_and_probe(work, [(d, "NEXDISK0")], vars_file, "single boot", "single", a.payload_sha, after=root_access(work))


def fresh_dir(a, name):
    work = os.path.join(a.work, name)
    shutil.rmtree(work, ignore_errors=True)
    os.makedirs(work)
    vars_file = os.path.join(work, "vars.fd")
    shutil.copy(lab.OVMF_VARS, vars_file)
    return work, vars_file


def scatter(path):
    """Fill a decoy disk with recognisable random data at the start, the middle and the end (sparse file)."""
    size = os.path.getsize(path)
    with open(path, "r+b") as f:
        for off in (0, size // 2, size - (4 << 20)):
            f.seek(off)
            f.write(os.urandom(4 << 20))


def scenario_bios(a):
    work, vars_file = fresh_dir(a, "bios")
    d = os.path.join(work, "d0.img"); new_disk(d); scatter(d)
    ctl = os.path.join(work, "ctl.img"); ctl_disk(ctl, "install", ["NEXDISK0"])
    before = (digest(d, True), digest(d))
    state, text = run_installer(work, a.iso, [(d, "NEXDISK0")], ctl, None, uefi=False, budget=300, label="install-bios", kill_on=r"UEFI firmware only")
    refused = "UEFI firmware only" in text
    check(refused, "legacy BIOS (SeaBIOS) boot of the installer is refused with the UEFI-only message", state)
    after = (digest(d, True), digest(d))
    check(before[0] == after[0], "BIOS refusal: first and last MiB of the disk unchanged (SHA-256)")
    check(before[1] == after[1], "BIOS refusal: the whole disk unchanged (SHA-256)")
    for line in text.splitlines():
        if "UEFI firmware only" in line:
            info(line.strip()[:200]); break


def installer_lines(text, pattern):
    for line in text.splitlines():
        if "nex-installer" in line and re.search(pattern, line):
            info(line.strip()[:200])


def scenario_raid1(a):
    """Two selected disks and a third, unselected one carrying recognisable data; install, boot both, root access, md experiment."""
    work, vars_file = fresh_dir(a, "raid1")
    disks = []
    for i in range(2):
        p = os.path.join(work, f"d{i}.img"); new_disk(p); disks.append((p, f"NEXDISK{i}"))
    decoy = os.path.join(work, "decoy.img"); new_disk(decoy); scatter(decoy)
    ctl = os.path.join(work, "ctl.img"); ctl_disk(ctl, "install", [s for _, s in disks])
    before = digest(decoy)
    state, text = run_installer(work, a.iso, disks + [(decoy, "NEXDECOY")], ctl, vars_file, label="install-raid1")
    check(state == "done", "raid1: installer ran to its restart with a third, unselected disk attached (virtual UEFI)", state)
    if state != "done":
        print(text[-1500:])
        return
    installer_lines(text, "mirror|Installing|complete|Copying")
    check(digest(decoy) == before, "wrong-disk: the unselected third disk is byte-identical after the install (full SHA-256)")
    check(not partitions_safe(decoy), "wrong-disk: the unselected disk has no partition table of ours")
    boot_and_probe(work, disks + [(decoy, "NEXDECOY")], vars_file, "raid1 boot", "raid1", a.payload_sha, degraded="0", after=root_access(work, md=True))
    check(digest(decoy) == before, "wrong-disk: the unselected disk is still byte-identical after the installed host ran with it attached")


def raid_disks(a):
    work = os.path.join(a.work, "raid1")
    disks = [(os.path.join(work, f"d{i}.img"), f"NEXDISK{i}") for i in range(2)]
    if not all(os.path.exists(p) for p, _ in disks):
        sys.exit("this scenario needs the disks of the raid1 scenario first")
    return work, disks, os.path.join(work, "vars.fd")


def scenario_alone(a):
    """Each member of the installed mirror booted by itself."""
    work, disks, vars_file = raid_disks(a)
    for i in range(2):
        boot_and_probe(work, [disks[i]], vars_file, f"raid1 disk {i} alone", "raid1", a.payload_sha, degraded="1", after=root_access(work))


def partitions_safe(path):
    try:
        return partitions(path)
    except (subprocess.CalledProcessError, KeyError):
        return {}


CUTS = (r"nex-installer: laying out", r"nex-installer: .*: writing .* to nex-root")


def scenario_interrupted(a):
    """The VM is killed at two points of the write (start of the disk layout, start of the slot write), then the installer runs again."""
    work, vars_file = fresh_dir(a, "interrupted")
    d = os.path.join(work, "d0.img"); new_disk(d)
    ctl = os.path.join(work, "ctl.img"); ctl_disk(ctl, "install", ["NEXDISK0"])
    for n, cut in enumerate(CUTS, 1):
        state, text = run_installer(work, a.iso, [(d, "NEXDISK0")], ctl, vars_file, label=f"install-cut{n}", kill_on=cut, budget=360)
        check(state == "killed", f"interrupted: power cut {n} (kill -9 of QEMU) at /{cut}/", state)
        info(f"after cut {n}: partitions={sorted(partitions_safe(d))}")
    state, text = run_installer(work, a.iso, [(d, "NEXDISK0")], ctl, vars_file, label="install-rerun")
    check(state == "done", "interrupted: a fresh installer run on the twice-interrupted disk completes", state)
    boot_and_probe(work, [(d, "NEXDISK0")], vars_file, "after re-run", "single", a.payload_sha, after=root_access(work))


READ_SET = "cd /var/nexdata-set && find . -type f | sort | xargs sha256sum"
KNOWN_SET = ("mkdir -p /var/nexdata-set/sub; head -c 6291456 /dev/urandom > /var/nexdata-set/blob; echo kept > /var/nexdata-set/sub/note; "
             "head -c 5000 /dev/urandom > /var/nexdata-set/sub/small; sync; " + READ_SET)
ESPS = "for d in a b; do mount -o ro /dev/vd${d}1 /mnt && echo esp-$d:$(find /mnt -name 'nex-core_*.efi' | tr '\\n' ,); umount /mnt; done"


def hashed(text):
    return sorted(l for l in text.splitlines() if re.match(r"^[0-9a-f]{64}  ", l))


def scenario_repair(a):
    work, disks, vars_file = raid_disks(a)
    proc, con = qemu(work, disks, vars_file=vars_file, log="repair-prep.log")
    try:
        sh = login(con, 300)
        check(bool(sh), "repair prep: the 2-disk host booted")
        rc, out = sh.sh(KNOWN_SET, 60)
        known = hashed(out)
        with open(os.path.join(work, "known.txt"), "w") as f:
            f.write("\n".join(known) + "\n")
        check(len(known) == 3, "repair prep: three known files written to nex-data", out[-200:])
        power_off(sh, proc)
    finally:
        stop(proc)
    data = {}
    for p, _ in disks:
        off, size = partitions(p)["nex-data"]
        data[p] = (off, size, region_digest(p, off, size))
    info("nex-data partition SHA-256 before: " + " ".join(v[2][:16] for v in data.values()))
    # damage the re-layable part: the ESP of disk 0 and the first slot of disk 1
    for p, name in ((disks[0][0], "nex-esp"), (disks[1][0], "nex-root-a")):
        off, size = partitions(p)[name]
        with open(p, "r+b") as f:
            f.seek(off); f.write(bytes(1 << 20))
    # Repair is refused while a mirror member is missing: nothing to keep, nothing written.
    ctl = os.path.join(work, "ctl-repair-one.img"); ctl_disk(ctl, "repair", [disks[0][1]])
    before = digest(disks[0][0])
    state, text = run_installer(work, a.iso, [disks[0]], ctl, vars_file, label="repair-incomplete", budget=300)
    check(state == "refused" and "No complete Nex Core data volume" in text, "repair refused when nex-data is incomplete (one of two mirror members attached)", state)
    check(digest(disks[0][0]) == before, "repair refusal: the disk is byte-identical afterwards (full SHA-256)")
    ctl = os.path.join(work, "ctl-repair.img"); ctl_disk(ctl, "repair", [s for _, s in disks])
    state, text = run_installer(work, a.iso, disks, ctl, vars_file, label="repair")
    check(state == "done", "repair: the installer ran Repair image on both disks to its restart", state)
    installer_lines(text, "Repair|complete|re-laying|writing")
    same = all(region_digest(p, *data[p][:2]) == data[p][2] for p, _ in disks)
    check(same, "repair: the nex-data partition bytes are identical on both disks (SHA-256 of the whole partition)")


def scenario_repair_boot(a):
    """After Repair: the known files on nex-data hash the same, both ESPs hold the UKI, root signs in, then each disk boots alone."""
    work, disks, vars_file = raid_disks(a)
    known = hashed(open(os.path.join(work, "known.txt")).read())

    def after(sh, con):
        rc, out = sh.sh(READ_SET, 60)
        check(hashed(out) == known, "repair: the known file set on nex-data hashes the same after Repair", out[-200:])
        rc, out = sh.sh(ESPS, 60)
        check(out.count(".efi") >= 2, "repair: both ESPs hold the release's UKI again", out)
        root_access(work)(sh, con)
    boot_and_probe(work, disks, vars_file, "after repair, both disks", "raid1", a.payload_sha, degraded="0", after=after)
    for i in range(2):
        boot_and_probe(work, [disks[i]], vars_file, f"after repair, disk {i} alone", "raid1", a.payload_sha, degraded="1")


def scenario_held_array(a):
    """Install on disks that an operator holds open as a mounted mirror: the installer must refuse and change nothing."""
    work, disks, vars_file = raid_disks(a)
    ctl = os.path.join(work, "ctl-hold.img"); ctl_disk(ctl, "install", [s for _, s in disks], hold=True)
    before = [digest(p) for p, _ in disks]
    state, text = run_installer(work, a.iso, disks, ctl, vars_file, label="install-held", budget=300)
    held = re.search(r"LAB-HOLD: .*mounted=1", text)
    check(bool(held), "held array: the lab shell holds the mirror assembled read-only and mounted before the installer starts", held.group(0) if held else text[-400:])
    check(state == "refused" and "which holds a selected disk" in text, "held array: the installer refuses to re-lay a disk of a mounted array", state)
    check([digest(p) for p, _ in disks] == before, "held array: both disks are byte-identical afterwards (full SHA-256)")


SCENARIOS = {"single": scenario_single, "raid1": scenario_raid1, "raid1-alone": scenario_alone, "bios": scenario_bios,
             "interrupted": scenario_interrupted, "held-array": scenario_held_array, "repair": scenario_repair,
             "repair-boot": scenario_repair_boot}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--iso", required=True)
    ap.add_argument("--work", required=True)
    ap.add_argument("--payload-sha", required=True, help="SHA-256 of the lab payload fixture printed by image_iso_lab.py")
    ap.add_argument("scenario", choices=sorted(SCENARIOS))
    a = ap.parse_args()
    a.iso, a.work = os.path.abspath(a.iso), os.path.abspath(a.work)
    os.makedirs(a.work, exist_ok=True)
    SCENARIOS[a.scenario](a)
    sys.exit(1 if check.failed else 0)


if __name__ == "__main__":
    main()
