#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Boot proof of a Nex Core image that contains nex-core-local: one VM, the host-local API answering.

Needs a release built by image/build.sh with NEX_PACKAGE_DIRS pointing at the image-edition nex-core-local deb
(packaging/core-local/build.py --edition image). The script installs it on a fresh single disk (same container and
`nex-hostimage install` as image_boot.py), boots it under QEMU/KVM + OVMF, and drives a root shell on the VM's second
serial port. In the guest it checks, and prints PASS/FAIL for:

  - the root is a read-only erofs on dm-verity and /usr refuses a write
  - nex-core-local is enabled by the image (vendor wants link, no postinst), active, and answers /healthz
  - libvirtd answers over qemu:///system
  - nex-layers.service is skipped by its condition (no layer enabled), not failed
  - /api/local/v1/updates: refused without a session, refused with a wrong password, 200 after the root login with
    the booted slot and the release label; a mutation without the CSRF token is refused (403) and
    with it reaches the engine
  - the unit list has no failed unit except those that need hardware QEMU does not have
  - the TLS certificate, the agent identity and the engine record persist across a power-off and cold boot

  tests/integration/image_core_api.py --release image/out/release [--label LABEL] [--workdir DIR] [--keep]

Resource caps (the machine is shared): run the script itself under systemd-run with CPUQuota/MemoryMax; the install
container is capped at 2 CPUs and 4 GiB, the VM gets -smp 2 -m 2048, and there is only ever one VM.
"""
import argparse
import json
import os
import secrets
import shutil
import subprocess
import sys
import tempfile
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import image_boot as lab  # noqa: E402
import image_update as upd  # noqa: E402  (VM, Ctl, check)

REPO = lab.REPO
GUEST = "10.0.2.15"  # QEMU user-mode networking: the guest address the lab shell configures

SEED = r"""
set -eu
mkdir -p /seed/systemd/system/multi-user.target.wants /seed/systemd/system-preset
printf 'nex-core-lab\n' > /seed/hostname
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
cat > /seed/nex-lab-api.py <<'EOT'
# Calls the host-local API like a browser: one cookie jar, the CSRF token of the session in a header.
import http.cookiejar, json, os, ssl, sys, urllib.error, urllib.request
ctx = ssl.create_default_context()
ctx.check_hostname, ctx.verify_mode = False, ssl.CERT_NONE
opener = urllib.request.build_opener(urllib.request.HTTPSHandler(context=ctx),
                                     urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
base = "https://10.0.2.15:9443"


def call(method, path, body=None, csrf=None):
    headers = {"Content-Type": "application/json", "Origin": base}
    if csrf:
        headers["X-CSRF-Token"] = csrf
    req = urllib.request.Request(base + path, method=method, headers=headers,
                                 data=None if body is None else json.dumps(body).encode())
    try:
        with opener.open(req, timeout=20) as r:
            return r.status, json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        text = e.read().decode(errors="replace")
        try:
            return e.code, json.loads(text)
        except ValueError:
            return e.code, text


out = {}
out["health"] = call("GET", "/healthz")
if len(sys.argv) > 1 and sys.argv[1] == "health":
    print("API=" + json.dumps(out))
    sys.exit(0)
out["updates_no_session"] = call("GET", "/api/local/v1/updates")[0]
out["login_wrong_password"] = call("POST", "/api/local/v1/session", {"username": "root", "password": "wrong"})[0]
status, session = call("POST", "/api/local/v1/session", {"username": "root", "password": os.environ["NEXLAB_PW"]})
out["login"] = status
csrf = session.get("csrf_token") if isinstance(session, dict) else None
out["updates"] = call("GET", "/api/local/v1/updates")
out["check_without_csrf"] = call("POST", "/api/local/v1/updates/check", {})[0]
out["check_with_csrf"] = call("POST", "/api/local/v1/updates/check", {}, csrf)
out["host"] = call("GET", "/api/local/v1/host")[0]
print("API=" + json.dumps(out))
EOT
"""


def install(work, release, hostimage, timeout):
    script = SEED + """
set -eu
l=$(losetup -fP --show /w/d0.img)
trap 'losetup -d $l' EXIT
nex-hostimage install --layout single --disks $l --image-dir /rel --etc-seed /seed
"""
    lab.run_install(work, release, hostimage, script, timeout, "image-core-api")


def api(ctl, password, mode=""):
    rc, out = ctl.sh(f"NEXLAB_PW='{password}' python3 /etc/nex-lab-api.py {mode}", 90)
    line = next((l for l in out.splitlines() if l.startswith("API=")), None)
    if line is None:
        raise RuntimeError(f"no API result (rc {rc}): {out[-600:]}")
    return json.loads(line[4:])


def wait_api(ctl, password, timeout):
    end = time.time() + timeout
    while time.time() < end:
        rc, out = ctl.sh(f"NEXLAB_PW='{password}' python3 /etc/nex-lab-api.py health 2>&1 | tail -1", 30)
        if '"health": [200' in out:
            return True
        time.sleep(3)
    return False


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--release", default=os.path.join(REPO, "image/out/release"))
    ap.add_argument("--workdir", help="directory for disk images and logs (default: a new one beside --release; only that one is removed)")
    ap.add_argument("--hostimage", help="static nex-hostimage (default: <release>/../nex-hostimage)")
    ap.add_argument("--keep", action="store_true")
    ap.add_argument("--label", default="dev", help="NEX_LABEL the release was built with")
    ap.add_argument("--budget", type=int, default=480, help="seconds for the install and for each boot")
    a = ap.parse_args()
    lab.require_host_tools()
    release = os.path.realpath(a.release)
    hostimage = lab.hostimage_for(release, a.hostimage)
    fresh = not a.workdir
    work = os.path.realpath(a.workdir) if a.workdir else lab.scratch_dir(release, "nex-core-api-")
    os.makedirs(work, exist_ok=True)
    password = secrets.token_urlsafe(16)
    vm = upd.VM(work)
    check = upd.check
    try:
        with open(os.path.join(work, "d0.img"), "wb") as f:
            f.truncate(lab.DISK_BYTES)
        shutil.copy(lab.OVMF_VARS, os.path.join(work, "vars.fd"))
        t0 = time.time()
        install(work, release, hostimage, a.budget)
        upd.info(f"install: {time.time() - t0:.0f}s")

        vm.start()
        upd.info(f"boot 1 reached the shell after {vm.wait_ready(a.budget):.0f}s")
        ctl = vm.ctl
        ctl.sh(f"echo 'root:{password}' | chpasswd")
        check(wait_api(ctl, password, 180), "nex-core-local answers /healthz after boot")

        rc, out = ctl.sh("findmnt -no FSTYPE,OPTIONS /")
        check(out.startswith("erofs") and ",ro" in "," + out.split(None, 1)[-1], "root is erofs and read-only", out)
        rc, out = ctl.sh("touch /usr/lab-write 2>&1")
        check(rc != 0, "a write under /usr is refused", out)
        rc, out = ctl.sh("dmsetup status root 2>/dev/null || dmsetup status | head -3")
        check(" V" in out or out.rstrip().endswith("V"), "root is dm-verity, status V", out)

        rc, out = ctl.sh("systemctl is-active nex-core-local; systemctl is-enabled nex-core-local; "
                         "ls -l /usr/lib/systemd/system/multi-user.target.wants/nex-core-local.service | awk '{print $NF}'; "
                         "ls /var/lib/dpkg 2>&1 | head -1")
        lines = out.splitlines()
        check(lines[:2] == ["active", "enabled"], "nex-core-local is active and enabled", out)
        check("nex-core-local.service" in out and "No such file" in lines[-1], "enabled by the /usr vendor link; no dpkg state on the host", out)
        rc, out = ctl.sh("virsh -c qemu:///system version 2>&1 | head -4; systemctl is-active libvirtd")
        check(rc == 0 and "Running hypervisor: QEMU" in out, "libvirtd answers over qemu:///system", out)
        rc, out = ctl.sh("systemctl show nex-layers.service -p ConditionResult -p ActiveState -p Result; "
                         "ls /var/lib/nex/layers 2>&1 | head -1")
        check("ConditionResult=no" in out and "Result=success" in out and "ActiveState=inactive" in out,
              "nex-layers.service is skipped by its condition, not failed", out)

        got = api(ctl, password)
        check(got["health"][0] == 200, "GET /healthz is 200", got["health"])
        check(got["updates_no_session"] == 401, "GET /updates without a session is 401", got["updates_no_session"])
        check(got["login_wrong_password"] == 401, "login with a wrong root password is 401", got["login_wrong_password"])
        check(got["login"] == 200, "login with the root password is 200", got["login"])
        status, body = got["updates"]
        check(status == 200 and body.get("running") is False, "GET /updates is 200 and no update operation is running", got["updates"])
        booted = [s for s in body.get("slots", []) if s.get("role") == "booted"]
        check(len(booted) == 1 and booted[0].get("label") == a.label and booted[0].get("root_hash"),
              "exactly one booted slot, with the release label and root hash", body.get("slots"))
        check(len(body.get("slots", [])) == 1, "no other slot is reported on a fresh install", body.get("slots"))
        # state_format is the format retained state needs (RequiredStateFormat): 0 on a fresh host with no ZFS pool.
        check(body.get("state_format") == 0 and body.get("phase") == "idle", "phase idle, no retained state needing a newer format", body)
        rc, cmdline = ctl.sh("cat /proc/cmdline")
        check(booted and booted[0].get("root_hash", "x")[:16] in cmdline.replace("-", ""), "the booted slot's root hash is the kernel command line's", cmdline)
        check(got["check_without_csrf"] == 403, "POST /updates/check without the CSRF token is 403", got["check_without_csrf"])
        check(got["check_with_csrf"][0] != 403 and got["check_with_csrf"][0] != 401,
              "POST /updates/check with the CSRF token reaches the engine", got["check_with_csrf"])
        check(got["host"] == 200, "GET /host is 200", got["host"])

        rc, out = ctl.sh("systemctl --failed --no-legend --plain | awk '{print $1}'")
        failed = sorted(out.split())
        upd.info(f"failed units: {failed}")
        check(set(failed) <= {"ipmievd.service"}, "no failed unit except ipmievd (no BMC in QEMU)", failed)
        rc, ident1 = ctl.sh("sha256sum /var/lib/nex/core-local/tls.crt | cut -c1-16; cat /var/lib/nex-core-agent/*.json 2>/dev/null | sha256sum | cut -c1-16; ls /var/lib/nex-core-agent")
        upd.info(f"identity before power-off: {ident1!r}")

        # Cold boot: the certificate, the agent identity and the update record are on /var and survive.
        ctl.send("poweroff")
        vm.proc.wait(60)
        vm.start()
        upd.info(f"boot 2 reached the shell after {vm.wait_ready(a.budget):.0f}s")
        ctl = vm.ctl
        check(wait_api(ctl, password, 180), "nex-core-local answers /healthz after a cold boot")
        rc, ident2 = ctl.sh("sha256sum /var/lib/nex/core-local/tls.crt | cut -c1-16; cat /var/lib/nex-core-agent/*.json 2>/dev/null | sha256sum | cut -c1-16; ls /var/lib/nex-core-agent")
        check(ident1 == ident2, "TLS certificate and agent state are unchanged after the cold boot", (ident1, ident2))
        got = api(ctl, password)
        check(got["login"] == 200 and got["updates"][0] == 200 and got["updates"][1].get("slots") == body["slots"],
              "the root login and GET /updates still work after the cold boot", got["updates"])
    finally:
        vm.stop()
        if not a.keep and fresh:
            shutil.rmtree(work, ignore_errors=True)
    print(f"{upd.check.failed} failed")
    sys.exit(1 if upd.check.failed else 0)


if __name__ == "__main__":
    main()
