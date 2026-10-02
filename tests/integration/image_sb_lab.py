#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Virtual Secure Boot and virtual TPM2 observations for the Nex Core image (SIMULATION, one VM at a time).

Not physical hardware: OVMF with variables built by virt-fw-vars (Microsoft keys in db via --enroll-redhat, the Nex
development certificate in MokList, the phase 0 S5 layout) and swtpm in a container. Needs two signed development
releases (image_sign_release.py over the update-lab releases), the update-lab keys and trust (image_update.py), the
variable stores of virt-fw-vars and the development key directory.

  image_sb_lab.py --rel1 SIGNED1 --rel2 SIGNED2 --unsigned-rel1 REL1 --keys DIR --trust T --vars-sb VARS --vars-off VARS
                  --hostimage BIN --workdir DIR

Steps: SB-enforcing boot of the signed UKI (shim -> sd-boot -> UKI trusted through the MOK); the same disk with an
unsigned UKI and with a tampered signed UKI (refused); the unsigned UKI with Secure Boot off (boots); nex-update
apply and rollback with Secure Boot enforcing and a vTPM attached, recording PCR 4, 7 and 11 of every boot.
"""
import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import threading
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import image_update as up  # noqa: E402
import image_boot as lab  # noqa: E402
from image_install_lab import partitions  # noqa: E402

SECBOOT_CODE = next((p for p in ("/usr/share/edk2/x64/OVMF_CODE.secboot.4m.fd", "/usr/share/OVMF/x64/OVMF_CODE.secboot.4m.fd") if os.path.exists(p)), None)
check, info = up.check, up.info
PCR_CMD = ("for n in 4 7 11; do echo pcr$n=$(cat /sys/class/tpm/tpm0/pcr-sha256/$n 2>/dev/null); done | tr '\\n' ' '; "
           "ls /sys/class/tpm | tr '\\n' ' '")


class SBVM(up.VM):
    """The update-lab VM with Secure Boot firmware (or not) and an optional vTPM."""
    secure = True
    code = None
    vars_file = None
    tpm_sock = None

    def start(self):
        sock = os.path.join(self.work, "ctl.sock")
        for stale in (sock, os.path.join(self.work, "qmp.sock")):
            if os.path.exists(stale):
                os.unlink(stale)
        code = SECBOOT_CODE if self.secure else lab.OVMF_CODE
        cmd = ["qemu-system-x86_64", "-enable-kvm", "-cpu", "host", "-machine", "q35,smm=on" if self.secure else "q35",
               "-m", "2048", "-smp", "2", "-display", "none", "-monitor", "none", "-serial", "stdio", "-serial", f"unix:{sock},server=on,wait=off",
               "-qmp", f"unix:{self.work}/qmp.sock,server=on,wait=off"]
        if self.secure:
            cmd += ["-global", "driver=cfi.pflash01,property=secure,value=on"]
        cmd += ["-drive", f"if=pflash,format=raw,unit=0,file={code},readonly=on",
                "-drive", f"if=pflash,format=raw,unit=1,file={self.vars_file}",
                "-drive", f"if=none,id=d0,format=raw,file={self.work}/d0.img,discard=unmap,detect-zeroes=unmap",
                "-device", "virtio-blk-pci,drive=d0,serial=nexdisk0,bootindex=1",
                "-netdev", "user,id=n0", "-device", "virtio-net-pci,netdev=n0", "-device", "virtio-rng-pci"]
        if self.tpm_sock:
            cmd += ["-chardev", f"socket,id=chrtpm,path={self.tpm_sock}", "-tpmdev", "emulator,id=tpm0,chardev=chrtpm",
                    "-device", "tpm-tis,tpmdev=tpm0"]
        self.proc = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        self.con = lab.Console(self.proc, open(os.path.join(self.work, "console.log"), "a"))
        self.ctl = up.Ctl(sock, open(os.path.join(self.work, "control.log"), "a"))


def put_uki(work, name, source):
    """Replace the UKI file of the ESP in the disk image (host side, mtools, no root)."""
    off = [o for n, (o, s) in partitions(os.path.join(work, "d0.img")).items() if n.startswith("nex-esp") or n == "ESP"]
    if not off:
        raise RuntimeError(f"no ESP found: {partitions(os.path.join(work, 'd0.img'))}")
    subprocess.run(["mcopy", "-o", "-i", f"{work}/d0.img@@{off[0]}", source, f"::/EFI/Linux/{name}"], check=True,
                   env=dict(os.environ, MTOOLS_SKIP_CHECK="1"))


def refused_boot(vm, seconds):
    """Boot and report whether a kernel started within the budget, plus the firmware/shim text that explains a refusal."""
    vm.start()
    end = time.time() + seconds
    while time.time() < end:
        time.sleep(3)
        if vm.kernel_running():
            break
    text = vm.con.log_text()
    started = "Linux version" in text
    vm.stop()
    clean = re.sub(r"\x1b\[[0-9;?]*[A-Za-z]", "", text)
    clues = sorted(set(m.group(0) for m in re.finditer(r"(Access Denied|Security Violation|Failed to load image|Verification failed|MokManager|Enroll key from disk|Perform MOK management|No bootable|BdsDxe: failed)[^\n]{0,60}", clean)))
    return started, clues[:4]


def secure_state(ctl):
    rc, out = ctl.sh("bootctl status 2>&1 | grep -i 'secure boot'; cat /sys/kernel/security/lockdown 2>&1; "
                     "dmesg | grep -i -m3 'secure boot\\|lockdown' | cut -c1-100")
    return out.replace("\n", " | ")


def pcrs(ctl, label, log):
    rc, out = ctl.sh(PCR_CMD, 30)
    log[label] = out
    info(f"{label}: {out}")
    return dict(re.findall(r"(pcr\d+)=(\w*)", out))


def start_swtpm(work):
    d = os.path.join(work, "tpm")
    shutil.rmtree(d, ignore_errors=True)
    os.makedirs(d)
    name = f"nex-swtpm-{os.getpid()}"
    subprocess.run(["docker", "run", "-d", "--rm", "--name", name, "--cpus", "1", "--memory", "256m", "-u", f"{os.getuid()}:{os.getgid()}",
                    "-v", f"{d}:/tpm", "nex-lab-gtk:phase6", "swtpm", "socket", "--tpmstate", "dir=/tpm", "--ctrl", "type=unixio,path=/tpm/ctrl.sock",
                    "--tpm2", "--flags", "not-need-init,startup-clear", "--log", "file=/tpm/swtpm.log"], check=True, capture_output=True)
    for _ in range(50):
        if os.path.exists(os.path.join(d, "ctrl.sock")):
            return name, os.path.join(d, "ctrl.sock")
        time.sleep(0.2)
    raise RuntimeError("swtpm control socket did not appear")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--rel1", required=True, help="signed release 1")
    ap.add_argument("--rel2", required=True, help="signed release 2")
    ap.add_argument("--unsigned-rel1", required=True)
    ap.add_argument("--keys", required=True)
    ap.add_argument("--trust", required=True)
    ap.add_argument("--vars-sb", required=True)
    ap.add_argument("--vars-off", required=True)
    ap.add_argument("--hostimage", required=True)
    ap.add_argument("--workdir", required=True)
    ap.add_argument("--budget", type=int, default=420)
    ap.add_argument("--skip-negative", action="store_true", help="skip step 2 (unsigned/tampered UKI, Secure Boot off); lab 1 ran it")
    a = ap.parse_args()
    work = os.path.abspath(a.workdir)
    shutil.rmtree(work, ignore_errors=True)
    os.makedirs(work)
    rel1, rel2, unsigned = (os.path.abspath(p) for p in (a.rel1, a.rel2, a.unsigned_rel1))
    vm, server, swtpm = SBVM(work), None, None
    log = {}
    try:
        source = os.path.join(work, "source.tar.gz")
        subprocess.run(["tar", "czf", source, "-C", up.REPO, "internal/nexupdate", "scripts/update-publish.py"], check=True)
        compliance = os.path.join(os.path.dirname(rel1), "base.manifest")
        if not os.path.exists(compliance):
            open(compliance, "w").write("lab\n")
        pub = up.publish(work, rel1, rel2, a.keys, os.path.abspath(a.trust), (source, compliance), 3)
        server = up.Server(pub)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        port = server.server_address[1]
        with open(os.path.join(work, "d0.img"), "wb") as f:
            f.truncate(lab.DISK_BYTES)
        up.install(work, rel1, os.path.abspath(a.hostimage), port, a.budget)
        info("release 1 (signed UKI) installed")
        uki1 = next(n for n in os.listdir(rel1) if n.endswith(".efi"))
        unsigned_uki = os.path.join(unsigned, next(n for n in os.listdir(unsigned) if n.endswith(".efi")))

        # ---- 1. Secure Boot enforcing, Nex certificate in MokList, vTPM attached
        swtpm, sock = start_swtpm(work)
        vm.vars_file = os.path.join(work, "vars.fd")
        shutil.copy(a.vars_sb, vm.vars_file)
        vm.secure, vm.tpm_sock = True, sock
        vm.start()
        t = vm.wait_ready(a.budget)
        ctl = vm.ctl
        st = secure_state(ctl)
        info(f"SB boot ready after {t:.0f}s; {st}")
        check("enabled" in st.lower() or "lockdown" in st.lower(), "virtual Secure Boot: signed UKI booted with Secure Boot enforcing", st)
        facts = up.facts(ctl)
        check(facts["build_id"] == "2026.10.1-1", "SB: release 1 runs", facts["build_id"])
        p1 = pcrs(ctl, "release 1 boot 1", log)
        vm.stop()
        vm.start(); vm.wait_ready(a.budget)
        p1b = pcrs(vm.ctl, "release 1 boot 2 (same UKI)", log)
        check(p1.get("pcr11") and p1.get("pcr11") == p1b.get("pcr11"), "vTPM: PCR 11 is the same on a second boot of the same UKI", f"{p1} {p1b}")
        vm.stop()

        # ---- 2. unsigned UKI, tampered UKI, Secure Boot off
        if not a.skip_negative:
            put_uki(work, uki1, unsigned_uki)
            started, clues = refused_boot(vm, 75)
            check(not started, "virtual Secure Boot: an unsigned UKI is refused (no kernel started)", f"{clues}")
            info(f"unsigned UKI under SB: kernel started={started}; firmware/shim text: {clues}")
            tampered = os.path.join(os.path.dirname(rel1), "tampered", uki1)
            if os.path.exists(tampered):
                put_uki(work, uki1, tampered)
                started, clues = refused_boot(vm, 75)
                check(not started, "virtual Secure Boot: a tampered signed UKI is refused (no kernel started)", f"{clues}")
                info(f"tampered UKI under SB: kernel started={started}; text: {clues}")
            put_uki(work, uki1, unsigned_uki)
            vm.secure = False
            shutil.copy(a.vars_off, vm.vars_file)
            vm.tpm_sock = None
            vm.start(); vm.wait_ready(a.budget)
            off = secure_state(vm.ctl)
            check("disabled" in off.lower() or "enabled" not in off.lower(), "Secure Boot off: the unsigned UKI boots", off)
            info(f"SB off: {off}")
            vm.stop()

        # ---- 3. update and rollback under Secure Boot with the vTPM
        put_uki(work, uki1, os.path.join(rel1, uki1))
        shutil.copy(a.vars_sb, vm.vars_file)
        vm.secure, vm.tpm_sock = True, sock
        vm.start(); vm.wait_ready(a.budget)
        ctl = vm.ctl
        ctl.sh("echo persisted > /etc/nex-persist; echo persisted > /var/nex-persist; sync")
        rc, txt = ctl.sh("nex-update check 2>&1", 120)
        check(rc == 0 and '"sequence": 2' in txt, "SB: nex-update check offers release 2", txt[-200:])
        vm.ctl.drain()
        vm.ctl.send(f"nex-update apply -request-id sb-apply-{up.RUN} -activate reboot > /tmp/lastcmd.out 2>&1")
        vm.wait_ready(min(a.budget, 420))
        f2 = up.facts(vm.ctl)
        check(f2["build_id"] == "2026.10.2-2", "SB: update applied; release 2 (signed UKI) boots under Secure Boot", f2["build_id"])
        check("enabled" in secure_state(vm.ctl).lower() or "lockdown" in secure_state(vm.ctl).lower(), "SB: still enforcing on release 2")
        p2 = pcrs(vm.ctl, "release 2 after update", log)
        check(f2["persist"] == "persisted,persisted,", "SB update: /etc and /var data survived", f2["persist"])
        info(f"update phase={f2['status']['phase']} esp={f2['uki']}")
        # Rolled back during the counted trial boot of release 2 (phase booted), the way the Updates panel offers it. Lab 1 sent this
        # blind and waited 420 s for a reboot that never came: the engine had refused with already_armed. The answer is read now.
        vm.ctl.drain()
        rc, txt = None, ""
        try:
            rc, txt = vm.ctl.sh(f"nex-update rollback -request-id sb-roll-{up.RUN} 2>&1", 60)
        except (TimeoutError, RuntimeError):
            pass  # the host started rebooting before the answer reached the console
        info(f"rollback issued in the trial boot ({f2['status']['phase']}): exit={rc} {txt[-300:]!r}")
        check(rc in (None, 0), "SB: the engine accepts rollback during the trial boot of release 2", txt[-300:])
        vm.wait_ready(min(a.budget, 420))
        f3 = up.facts(vm.ctl)
        check(f3["build_id"] == "2026.10.1-1", "SB: rollback returns to release 1 under Secure Boot", f3["build_id"])
        p3 = pcrs(vm.ctl, "release 1 after rollback", log)
        info("PCR 11 across release 1 -> update -> release 2 -> rollback: "
             f"{p1.get('pcr11', '')[:16]} -> {p2.get('pcr11', '')[:16]} -> {p3.get('pcr11', '')[:16]}")
        check(bool(p1.get("pcr11")) and p1.get("pcr11") != p2.get("pcr11"), "vTPM: PCR 11 differs between release 1 and release 2", f"{p1} {p2}")
        check(p1.get("pcr11") == p3.get("pcr11"), "vTPM: PCR 11 returns to the release 1 value after rollback", f"{p1} {p3}")
        with open(os.path.join(work, "pcrs.json"), "w") as f:
            json.dump(log, f, indent=1)
    finally:
        vm.stop()
        if swtpm:
            subprocess.run(["docker", "kill", swtpm], capture_output=True)
    sys.exit(1 if check.failed else 0)


if __name__ == "__main__":
    main()
