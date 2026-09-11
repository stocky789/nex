#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise a disposable installed guest's durable disk I/O through Nex.

The script never chooses a VM or Core itself: callers supply an ignored lab directory
and IDs file.  ``write`` creates an 8 MiB random file, fsyncs it and verifies its
SHA-256 inside the guest.  ``guest-reboot`` uses the Controller power API, then
reconnects through the Core's private NAT and verifies the same hash.  ``verify`` is
for the storage-lab owner to run after a separately coordinated Core reboot.
"""
import argparse
import base64
import http.client
import json
from pathlib import Path
import shlex
import ssl
import sys
import time
import urllib.parse

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))
from storage_repositories_lab_boot import check_lab, ssh  # noqa: E402


def core(lab, command, check=True):
    result = ssh(lab, command, check=check)
    if check and result.returncode:
        raise RuntimeError(result.stderr.strip())
    return result.stdout


def guest_ip(lab, vm):
    text = core(lab, f"virsh -c qemu:///system domifaddr {shlex.quote(vm)} --source lease || true")
    for line in text.splitlines():
        fields = line.split()
        if len(fields) >= 4 and "." in fields[3]:
            return fields[3].split("/", 1)[0]
    raise RuntimeError("the running guest has no IPv4 DHCP lease")


def guest(lab, vm, guest_key, guest_user, guest_sudo, script, check=True):
    ip = guest_ip(lab, vm)
    encoded = base64.b64encode(script.encode()).decode()
    command = (
        f"printf %s {shlex.quote(encoded)} | base64 -d | "
        f"ssh -o BatchMode=yes -o StrictHostKeyChecking=no -o ConnectTimeout=5 "
        f"-i {shlex.quote(guest_key)} {shlex.quote(guest_user)}@{shlex.quote(ip)} "
        f"{('sudo -n ' if guest_sudo else '')}sh -s"
    )
    return core(lab, command, check=check)


class API:
    def __init__(self, url, password):
        parsed = urllib.parse.urlparse(url)
        self.url, self.host, self.port = url.rstrip("/"), parsed.hostname, parsed.port or 443
        self.password = password
        self.csrf = ""
        self.cookie = ""
        self.login()

    def request(self, method, path, body=None):
        conn = http.client.HTTPSConnection(self.host, self.port, context=ssl._create_unverified_context(), timeout=20)
        headers = {"Origin": self.url}
        if self.cookie:
            headers["Cookie"] = self.cookie
        if body is not None:
            headers.update({"Content-Type": "application/json", "X-CSRF-Token": self.csrf})
        conn.request(method, path, None if body is None else json.dumps(body), headers)
        response = conn.getresponse()
        raw = response.read()
        data = json.loads(raw) if raw else {}
        cookie = response.getheader("Set-Cookie")
        if cookie:
            self.cookie = cookie.split(";", 1)[0]
        conn.close()
        if not 200 <= response.status < 300:
            raise RuntimeError(f"{method} {path}: {response.status} {data.get('error', data)}")
        return data

    def login(self):
        session = self.request("POST", "/api/v1/session", {"username": "root", "password": self.password})
        self.csrf = session["csrf_token"]

    def reboot(self, vm):
        reply = self.request("POST", f"/api/v1/vms/{vm}/power", {"request_id": f"guest-io-reboot-{int(time.time())}", "action": "reboot"})
        operation = reply.get("operation", reply).get("id")
        if not operation:
            raise RuntimeError(f"power response has no operation: {reply}")
        until = time.monotonic() + 180
        while time.monotonic() < until:
            for item in self.request("GET", "/api/v1/operations"):
                if item["id"] == operation and item["status"] not in ("queued", "dispatched", "running"):
                    if item["status"] != "succeeded":
                        raise RuntimeError(f"reboot operation {item['status']}: {item.get('error', '')}")
                    return operation
            time.sleep(1)
        raise RuntimeError("guest reboot operation did not finish within 180 seconds")


def wait_for_hash(lab, vm, guest_key, guest_user, guest_sudo, hash_file, previous_boot_id=""):
    until = time.monotonic() + 180
    last = ""
    while time.monotonic() < until:
        try:
            changed = "" if not previous_boot_id else f"test \"$(cat /proc/sys/kernel/random/boot_id)\" != {shlex.quote(previous_boot_id)}; "
            return guest(lab, vm, guest_key, guest_user, guest_sudo, "set -e; " + changed + f"sha256sum -c {shlex.quote(hash_file)}; cat /proc/sys/kernel/random/boot_id; uname -r; . /etc/os-release; printf '%s %s\\n' \"$ID\" \"$VERSION_ID\"")
        except Exception as exc:  # The SSH transport is expected to drop during restart.
            last = str(exc)
            time.sleep(2)
    raise RuntimeError(f"guest did not return with its hash within 180 seconds: {last}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lab-dir", type=Path, required=True, help="explicitly disposable nex-storage-repositories-lab.* directory")
    parser.add_argument("--ids", type=Path, required=True, help="ignored JSON with vm and storage IDs")
    parser.add_argument("--stage", choices=("write", "guest-reboot", "verify"), required=True)
    parser.add_argument("--guest-key", required=True, help="private key path inside the disposable Core guest")
    parser.add_argument("--guest-user", default="root")
    parser.add_argument("--guest-sudo", action="store_true", help="run guest checks through passwordless sudo")
    parser.add_argument("--hash-file", default="/root/nex-storage-io.sha256", help="guest checksum manifest to verify")
    parser.add_argument("--controller", default="https://127.0.0.1:44443")
    parser.add_argument("--password-file", type=Path, default=ROOT / ".cache/storage-lab/runtime/password")
    parser.add_argument("--result", type=Path, default=ROOT / ".cache/storage-guest-io/result.json")
    parser.add_argument("--execute", action="store_true", help="permit guest writes or Controller power operations")
    args = parser.parse_args()
    lab, ids = check_lab(args.lab_dir), json.loads(args.ids.read_text())
    vm = ids["vm"]
    result = {"stage": args.stage, "vm": vm, "storage": ids.get("storage", ""), "completed": False}
    if args.stage in ("write", "guest-reboot") and not args.execute:
        raise SystemExit("--execute is required for writes and Controller power operations")
    if args.stage == "write":
        result["guest"] = guest(lab, vm, args.guest_key, args.guest_user, args.guest_sudo, "set -eu; umask 077; dd if=/dev/urandom of=/root/nex-storage-io.bin bs=1M count=8 conv=fsync status=none; sha256sum /root/nex-storage-io.bin > /root/nex-storage-io.sha256; sync; sha256sum -c /root/nex-storage-io.sha256")
    elif args.stage == "guest-reboot":
        result["boot_id_before"] = guest(lab, vm, args.guest_key, args.guest_user, args.guest_sudo, "cat /proc/sys/kernel/random/boot_id").strip()
        result["operation"] = API(args.controller, args.password_file.read_text().strip()).reboot(vm)
        result["guest"] = wait_for_hash(lab, vm, args.guest_key, args.guest_user, args.guest_sudo, args.hash_file, result["boot_id_before"])
    else:
        result["guest"] = wait_for_hash(lab, vm, args.guest_key, args.guest_user, args.guest_sudo, args.hash_file)
    result["completed"] = True
    args.result.parent.mkdir(parents=True, exist_ok=True)
    args.result.write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
