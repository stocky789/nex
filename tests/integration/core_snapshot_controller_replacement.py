#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Re-enroll one selected disposable Core with an independent empty Controller."""

from __future__ import annotations

import argparse
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import re
import secrets
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))
import core_local_snapshot_api as snapshot_api  # noqa: E402


REMOTE_PREFLIGHT = r'''import hashlib, json, os, subprocess, sys
config=json.loads(sys.stdin.readline())
state=json.loads(open("/var/lib/nex-core-agent/state.json").read())
if state.get("host_id") != config["host_id"] or state.get("pool_id") != config["pool_id"]:
 raise RuntimeError("Core identity differs from selected fixture")
if state.get("controller_server_name", "") != config["server_name"]:
 raise RuntimeError("server_name must match the Core's current TLS server-name setting")
entry=state.get("journal", {}).get(config["operation_id"])
if not entry or entry.get("status") != "succeeded" or entry.get("command", {}).get("kind") != "snapshot-create":
 raise RuntimeError("selected Core operation is not durably succeeded")
command=entry["command"]
if (command.get("vm", {}).get("id") != config["vm_id"] or
    command.get("snapshot", {}).get("name") != config["name"] or
    command.get("snapshot", {}).get("description", "") != config["description"]):
 raise RuntimeError("selected Core operation does not match the exact snapshot evidence")
pending=[key for key,value in state.get("journal", {}).items()
         if value.get("command", {}).get("kind") == "snapshot-create" and
         value.get("status") in ("pending", "needs_attention")]
if pending:
 raise RuntimeError("Core has an unresolved snapshot journal; reconcile it before Controller replacement")
service=subprocess.run(["systemctl", "is-active", "nex-core-local.service"],check=True,capture_output=True,text=True).stdout.strip()
if service != "active":
 raise RuntimeError("Core local service is not active")
path="/var/lib/nex-core-agent/managed-vms/"+config["operation_id"]+".json"
raw=open(path,"rb").read(); managed=json.loads(raw); cmd=managed.get("command", {})
if (cmd.get("kind") != "snapshot-create" or cmd.get("operation_id") != config["operation_id"] or
    cmd.get("vm", {}).get("id") != config["vm_id"] or cmd.get("snapshot", {}).get("name") != config["name"] or
    managed.get("result", {}).get("status") != "succeeded" or managed.get("recovery_settled") is not True):
 raise RuntimeError("native Core snapshot journal is not exactly settled")
print(json.dumps({"host_id":state["host_id"],"pool_id":state["pool_id"],"controller":state.get("controller", ""),
 "server_name":state.get("controller_server_name", ""),"managed_journal_sha256":hashlib.sha256(raw).hexdigest(),
 "service":service,"unresolved_snapshots":0}))
'''


REMOTE_COOKIE_SETUP = r'''jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar), urllib.request.HTTPSHandler(context=context))
'''


REMOTE_ROOT_LOGIN_SETUP = r'''def login_with_temporary_root_password(password):
    state = subprocess.run(["passwd", "-S", "root"], check=True, capture_output=True, text=True).stdout.split()
    if len(state) < 2 or state[1] != "L":
        raise RuntimeError("Core root account was not locked before local API login")
    try:
        subprocess.run(["chpasswd"], input="root:" + password + "\n", check=True,
                       capture_output=True, text=True, timeout=10)
        state = subprocess.run(["passwd", "-S", "root"], check=True, capture_output=True, text=True).stdout.split()
        if len(state) < 2 or state[1] == "L":
            raise RuntimeError("temporary Core root password did not unlock PAM login")
        return call("/api/local/v1/session", "POST", {"username": "root", "password": password}, expected=200)
    finally:
        subprocess.run(["passwd", "-l", "root"], check=True, capture_output=True, text=True, timeout=10)
        state = subprocess.run(["passwd", "-S", "root"], check=True, capture_output=True, text=True).stdout.split()
        if len(state) < 2 or state[1] != "L":
            raise RuntimeError("Core root account did not return to its locked state after login")
'''


REMOTE_MARKER_VERIFY = r'''def verify_markers():
    for marker in config["markers"]:
        result = subprocess.run(["qemu-io", "-f", "qcow2", "-c",
            "read -P " + marker["pattern"] + " " + str(marker["offset"]) + " " + str(marker["length"]),
            marker["path"]], capture_output=True, text=True, timeout=30)
        output = result.stdout + result.stderr
        if result.returncode or "read " + str(marker["length"]) + "/" + str(marker["length"]) not in output or "pattern verification failed" in output.lower():
            raise RuntimeError("VM disk marker changed during Controller replacement")
'''


REMOTE = r'''import http.cookiejar, json, os, re, ssl, subprocess, sys, time, urllib.error, urllib.request

config = json.loads(sys.stdin.readline())
cert_path = "/var/lib/nex/core-local/tls.crt"
san = subprocess.run(["openssl", "x509", "-in", cert_path, "-noout", "-ext", "subjectAltName"],
                     check=True, capture_output=True, text=True).stdout
match = re.search(r"IP Address:([0-9.]+)", san)
if not match:
    raise RuntimeError("Core certificate has no IPv4 SAN")
origin = "https://" + match.group(1) + ":9443"
context = ssl.create_default_context(cafile=cert_path)
''' + REMOTE_COOKIE_SETUP + r'''
''' + REMOTE_ROOT_LOGIN_SETUP + r'''
''' + REMOTE_MARKER_VERIFY + r'''

def call(path, method="GET", body=None, csrf=None, expected=200):
    headers = {"Accept": "application/json", "Origin": origin}
    data = None
    if body is not None:
        data = json.dumps(body, separators=(",", ":")).encode()
        headers["Content-Type"] = "application/json"
    if csrf:
        headers["X-CSRF-Token"] = csrf
    request = urllib.request.Request(origin + path, data=data, headers=headers, method=method)
    try:
        with opener.open(request, timeout=15) as response:
            status, raw = response.status, response.read()
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read()
    if status != expected:
        raise RuntimeError("Core API returned HTTP " + str(status) + " for " + method + " " + path)
    return json.loads(raw) if raw else None

def virsh(*args):
    return subprocess.run(["virsh", "-c", "qemu:///system", *args], check=True,
                          capture_output=True, text=True, timeout=20).stdout.strip()

def read_journal():
    state = json.loads(open("/var/lib/nex-core-agent/state.json").read())
    entry = state.get("journal", {}).get(config["operation_id"])
    if not entry or entry.get("command", {}).get("kind") != "snapshot-create":
        raise RuntimeError("selected Core snapshot journal is missing")
    command = entry["command"]
    if (command.get("vm", {}).get("id") != config["vm_id"] or
            command.get("snapshot", {}).get("name") != config["name"] or
            command.get("snapshot", {}).get("description", "") != config["description"] or
            entry.get("status") != "succeeded"):
        raise RuntimeError("selected Core journal does not match the successful snapshot")
    return {"operation_id": config["operation_id"], "kind": command["kind"],
            "vm_id": command["vm"]["id"], "name": command["snapshot"]["name"],
            "description": command.get("snapshot", {}).get("description", ""),
            "status": entry["status"]}

def managed_journal_digest():
    path = "/var/lib/nex-core-agent/managed-vms/" + config["operation_id"] + ".json"
    with open(path, "rb") as stream:
        raw = stream.read()
    entry = json.loads(raw)
    command = entry.get("command", {})
    if (command.get("operation_id") != config["operation_id"] or command.get("kind") != "snapshot-create" or
            command.get("vm", {}).get("id") != config["vm_id"] or
            command.get("snapshot", {}).get("name") != config["name"] or
            entry.get("result", {}).get("status") != "succeeded" or entry.get("recovery_settled") is not True):
        raise RuntimeError("native snapshot journal is not exactly settled")
    import hashlib
    return hashlib.sha256(raw).hexdigest()

def vm_signature():
    import hashlib, xml.etree.ElementTree as ET
    xml = virsh("dumpxml", config["vm_name"], "--inactive")
    root = ET.fromstring(xml)
    disks = {}
    for disk in root.findall("./devices/disk[@device='disk']"):
        target, source = disk.find("target"), disk.find("source")
        if target is None or source is None or not source.get("file"):
            continue
        path = source.get("file")
        info = os.stat(path)
        disks[target.get("dev")] = {"path": path, "inode": info.st_ino, "size": info.st_size}
    return {"xml_sha256": hashlib.sha256(xml.encode()).hexdigest(), "disks": disks,
            "snapshots": virsh("snapshot-list", config["vm_name"], "--name").splitlines()}

def read_core():
    host, pool = call("/api/local/v1/host"), call("/api/local/v1/pool")
    if (host.get("host_id") != config["host_id"] or pool.get("pool_id") != config["pool_id"] or
            pool.get("role") != "master" or pool.get("master_host_id") != config["host_id"]):
        raise RuntimeError("selected Core identity or master role changed")
    vm = config["vm_name"]
    if virsh("domuuid", vm).lower() != config["vm_id"].lower() or "shut off" not in virsh("domstate", vm).lower():
        raise RuntimeError("selected VM identity or stopped state changed")
    operation = call("/api/local/v1/pool/vms/" + config["vm_id"] + "/snapshot-operations/" + config["operation_id"])
    catalog = call("/api/local/v1/pool/vms/" + config["vm_id"] + "/snapshots")
    match = [item for item in catalog if item.get("id") == config["operation_id"]]
    if (operation.get("status") != "succeeded" or len(match) != 1 or
            match[0].get("vm_id") != config["vm_id"] or match[0].get("name") != config["name"] or
            match[0].get("description", "") != config["description"]):
        raise RuntimeError("Core did not retain the exact successful operation and catalog entry")
    journal = read_journal()
    return {"host_id": host["host_id"], "controller_url": host.get("controller", ""),
            "pool_id": pool["pool_id"], "pool_name": pool["pool_name"], "role": pool["role"],
            "operation": operation, "catalog": match[0], "journal": journal,
            "managed_journal_sha256": managed_journal_digest(), "vm_signature": vm_signature()}

login = login_with_temporary_root_password(config["root_password"])
csrf = login["csrf_token"]
before = read_core()
verify_markers()
old_controller = call("/api/local/v1/host").get("controller", "")
if old_controller == config["controller_url"] and not config["controller_ca_changed"]:
    raise RuntimeError("replacement Controller endpoint and trust are unchanged")
call("/api/local/v1/enrollment", "POST", {
    "controller_url": config["controller_url"], "ca_pem": config["controller_ca"],
    "token": config["enrollment_token"], "server_name": config["server_name"]}, csrf=csrf, expected=200)
after = read_core()
if (before["journal"] != after["journal"] or
        before["managed_journal_sha256"] != after["managed_journal_sha256"] or
        before["vm_signature"] != after["vm_signature"] or
        after["controller_url"] != config["controller_url"]):
    raise RuntimeError("Controller switch changed Core snapshot journal or VM disk definition")
verify_markers()
print(json.dumps({"before": before, "after": after, "vm_state": "shut off", "disk_markers_verified": len(config["markers"])}))
'''


def marker_readback_config(markers: list[dict]) -> list[dict]:
    if len(markers) != 2:
        raise RuntimeError("snapshot evidence must contain two disk marker readbacks")
    converted = []
    for marker in markers:
        target = marker.get("target")
        pattern = marker.get("pattern", "")
        source = marker.get("source", "")
        readback = marker.get("readback", "")
        match = re.match(r"^read (\d+)/(\d+) bytes at offset (\d+)(?:\n|$)", readback)
        if (target not in ("vda", "vdb") or not re.fullmatch(r"0x[0-9a-fA-F]+", pattern) or
                not isinstance(source, str) or not source.startswith("/") or not match or
                match.group(1) != match.group(2) or "pattern verification failed" in readback.lower()):
            raise RuntimeError("snapshot disk marker readback is incomplete or invalid")
        converted.append({"target": target, "pattern": pattern, "path": source,
                          "length": int(match.group(1)), "offset": int(match.group(3)),
                          "readback": readback})
    if {item["target"] for item in converted} != {"vda", "vdb"}:
        raise RuntimeError("snapshot disk marker readbacks do not cover both VM disks")
    return converted


def snapshot_fixture(prep: Path, evidence_path: Path, expected_hash: str, package_version: str) -> dict:
    evidence_path = evidence_path.expanduser().resolve(strict=True)
    if evidence_path.parent != prep or evidence_path.is_symlink() or evidence_path.stat().st_mode & 0o077:
        raise RuntimeError("snapshot success evidence must be a private file in the selected preparation")
    match = re.fullmatch(r"core-local-snapshot-api-([A-Za-z0-9._-]+)-result\.json", evidence_path.name)
    if not match:
        raise RuntimeError("snapshot success evidence filename must identify its attempt")
    request_path = prep / f"core-local-snapshot-api-{match.group(1)}-request.json"
    if request_path.is_symlink() or not request_path.is_file() or request_path.stat().st_mode & 0o077:
        raise RuntimeError("matching snapshot request evidence must be a private regular file")
    request = snapshot_api.load_json(request_path)
    result = snapshot_api.load_json(evidence_path)
    vm = snapshot_api.load_json(prep / "managed-vm.json")
    ids = snapshot_api.load_json(prep / "corea-ids.json")
    candidate = result.get("core_candidate") or {}
    observed_package_version = candidate.get("version")
    core = result.get("core_result") or {}
    if result.get("status") != "passed" or result.get("controller_restart") is None:
        raise RuntimeError("a completed successful Core snapshot walkthrough is required")
    if candidate.get("binary_sha256") != expected_hash or observed_package_version != package_version:
        raise RuntimeError("Core candidate identity differs from the requested verification")
    before_service = core.get("core_service_before_restart") or {}
    after_service = core.get("core_service_after_restart") or {}
    replay = core.get("exact_replay_after_restart") or {}
    operation = core.get("operation_after_restart") or {}
    catalog = core.get("master_catalog_after_restart") or {}
    if (before_service.get("pid") == after_service.get("pid") or after_service.get("state") != "active" or
            after_service.get("binary_sha256") != expected_hash or operation.get("status") != "succeeded" or
            replay.get("operation_id") != request.get("operation_id")):
        raise RuntimeError("Core service restart, exact replay, or operation evidence is incomplete")
    markers = marker_readback_config(core.get("markers_after_restart", []))
    resources = snapshot_api.load_json(prep / "resources.json")
    storage_root = Path(resources.get("storage_path", ""))
    if not storage_root.is_absolute():
        raise RuntimeError("snapshot evidence has no absolute VM storage root")
    vm_dir = storage_root.resolve() / "nex-vms" / vm.get("vm_id", "")
    if any(Path(marker["path"]).resolve().parent != vm_dir for marker in markers):
        raise RuntimeError("snapshot marker paths do not belong to the selected VM directory")
    op = request.get("operation_id")
    body = request.get("body") or {}
    if (not op or body.get("operation_id") != op or
            set(body) != {"operation_id", "name", "description"} or
            catalog.get("id") != op or catalog.get("vm_id") != vm.get("vm_id") or
            body.get("name") != request.get("snapshot_name") or
            body.get("description", "") != request.get("description", "") or
            catalog.get("name") != request.get("snapshot_name") or
            catalog.get("description", "") != request.get("description", "")):
        raise RuntimeError("private snapshot evidence does not prove the exact successful catalog entry")
    if (vm.get("host_id") != ids.get("host_id") or vm.get("pool_id") != ids.get("pool_id") or
            core.get("vm_state") != "shut off" or len(core.get("markers", [])) != 2):
        raise RuntimeError("snapshot evidence does not match the selected stopped two-disk VM")
    return {"request": request, "result": result, "vm": vm, "ids": ids, "markers": markers}


def validate_origin(value: str) -> tuple[str, int]:
    parsed = urllib.parse.urlsplit(value)
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password or parsed.path not in ("", "/") or parsed.query or parsed.fragment:
        raise ValueError("Controller URL must be an HTTPS origin")
    port = parsed.port or 443
    return parsed.hostname, port


def controller_endpoints(controller_url: str, listen: str) -> tuple[str, str, str, str, int]:
    hostname, port = validate_origin(controller_url)
    match = re.fullmatch(r"([^:]+):(\d+)", listen)
    if not match or int(match.group(2)) != port:
        raise RuntimeError("Controller B listen port must match its advertised HTTPS origin")
    public_base = controller_url.rstrip("/")
    admin_listen = "127.0.0.1:" + str(port)
    return hostname, public_base, admin_listen, "https://" + admin_listen, port


def verify_private_dir(path: Path) -> Path:
    if path.is_symlink():
        raise RuntimeError("replacement directory must not be a symlink")
    resolved = path.resolve()
    if resolved.parent != (ROOT / ".cache").resolve() or not resolved.name.startswith("core-snapshot-controller-b-"):
        raise RuntimeError("replacement directory must be a new private Core snapshot Controller B fixture")
    if resolved.exists():
        raise RuntimeError("replacement directory already exists; choose a fresh fixture")
    return resolved


def preflight_failure_path(prep: Path, replacement: Path) -> Path:
    return prep / (replacement.name + "-preflight-ssh-failures.jsonl")


def private_tree_digest(path: Path) -> str:
    if path.is_symlink() or not path.is_dir():
        raise RuntimeError("Controller A data directory is missing or unsafe")
    digest = hashlib.sha256()
    for child in sorted(path.rglob("*")):
        if child.is_symlink():
            raise RuntimeError("Controller A data directory contains a symlink")
        if child.is_file():
            relative = child.relative_to(path).as_posix().encode()
            digest.update(len(relative).to_bytes(4, "big"))
            digest.update(relative)
            digest.update(hashlib.sha256(child.read_bytes()).digest())
        elif not child.is_dir():
            raise RuntimeError("Controller A data directory contains an unsupported entry")
    return digest.hexdigest()


def controller_a_fingerprint(prep: Path) -> dict:
    state = private_tree_digest(prep / "controller-state")
    tls = {}
    for name in ("tls.crt", "tls.key"):
        path = prep / name
        if path.is_symlink() or not path.is_file():
            raise RuntimeError("Controller A TLS evidence is missing or unsafe")
        tls[name] = hashlib.sha256(path.read_bytes()).hexdigest()
    return {"state_sha256": state, "tls": tls}


def controller_has_snapshot(managed: dict, snapshot_id: str, vm_id: str,
                            name: str, description: str) -> bool:
    if managed.get("pool_vm_snapshots_reported") is not True:
        return False
    return any(item.get("id") == snapshot_id and item.get("vm_id") == vm_id and
               item.get("name") == name and item.get("description", "") == description
               for item in managed.get("pool_vm_snapshots", []))


def adopted_snapshot_operation(operations: list[dict], host_id: str, snapshot_id: str) -> dict | None:
    request_id = "adopt-outcome:" + host_id + ":" + snapshot_id
    return next((operation for operation in operations if operation.get("host_id") == host_id and
                 operation.get("kind") == "snapshot-create" and operation.get("status") == "succeeded" and
                 operation.get("request_id") == request_id), None)


def file_sha256(path: Path, expected: str) -> str:
    if path.is_symlink() or not path.is_file():
        raise RuntimeError("Controller B candidate artifact must be a regular non-symlink file")
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    if digest != expected:
        raise RuntimeError("Controller B candidate artifact hash differs from the requested value")
    return digest


def lab_fingerprint(lab: Path) -> str:
    lab = lab.resolve(strict=True)
    digest = hashlib.sha256(str(lab).encode())
    for name in ("lab.json", "known_hosts", "ports.json", "id_ed25519"):
        path = lab / name
        if path.is_symlink() or not path.is_file():
            raise RuntimeError("selected disposable lab fingerprint input is missing or unsafe")
        content_digest = hashlib.sha256(path.read_bytes()).digest()
        digest.update(name.encode() + b"\0" + content_digest)
    return digest.hexdigest()


def controller_api(base: str, cert: Path, password: str, method: str, path: str, body=None,
                   jar=None, csrf: str = "", timeout: int = 10):
    context = ssl.create_default_context(cafile=str(cert))
    context.check_hostname = False
    cookie_jar = jar if jar is not None else http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}),
        urllib.request.HTTPCookieProcessor(cookie_jar), urllib.request.HTTPSHandler(context=context))
    headers = {"Accept": "application/json", "Origin": base}
    data = None
    if body is not None:
        data = json.dumps(body, separators=(",", ":")).encode()
        headers["Content-Type"] = "application/json"
    if csrf:
        headers["X-CSRF-Token"] = csrf
    request = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    with opener.open(request, timeout=timeout) as response:
        raw = response.read()
    return json.loads(raw) if raw else None


def admin_session(base: str, cert: Path, password: str):
    jar = http.cookiejar.CookieJar()
    login = controller_api(base, cert, password, "POST", "/api/v1/session", {"username": "root", "password": password}, jar)
    return jar, login["csrf_token"]


def wait_ready(base: str, cert: Path, password: str, timeout: int = 30):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            jar, csrf = admin_session(base, cert, password)
            controller_api(base, cert, password, "GET", "/api/v1/hosts", jar=jar, csrf=csrf)
            return jar, csrf
        except (OSError, urllib.error.URLError, TimeoutError) as exc:
            last = exc
            time.sleep(.2)
    raise RuntimeError("fresh Controller B did not become ready: " + repr(last)) from last


def start_controller_b(directory: Path, binary: Path, source: Path, listen: str,
                       hostname: str, server_name: str):
    directory.mkdir(mode=0o700)
    cert, key = directory / "tls.crt", directory / "tls.key"
    password_file = directory / "password"
    password = secrets.token_urlsafe(32)
    fd = os.open(password_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as stream:
        stream.write(password)
        stream.write("\n")
    sans = ["DNS:" + hostname] if not re.fullmatch(r"[0-9.]+", hostname) else ["IP:" + hostname]
    if server_name and server_name != hostname:
        sans.append("DNS:" + server_name)
    subprocess.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
        "-nodes", "-keyout", str(key), "-out", str(cert), "-days", "1", "-subj", "/CN=Nex disposable Controller B",
        "-addext", "subjectAltName=" + ",".join(sans)], check=True, capture_output=True)
    key.chmod(0o600)
    data_dir = directory / "controller-state"
    if data_dir.exists() or data_dir.is_symlink():
        raise RuntimeError("Controller B database path must be new and empty")
    log_fd = os.open(directory / "controller.log", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    proc = subprocess.Popen([str(binary), "-data-dir", str(data_dir), "-listen", listen,
        "-tls-cert", str(cert), "-tls-key", str(key), "-admin-username", "root",
        "-admin-password-file", str(password_file), "-source-archive", str(source)],
        cwd=directory, stdin=subprocess.DEVNULL, stdout=log_fd, stderr=log_fd, start_new_session=True)
    os.close(log_fd)
    # Caller supplies the advertised origin; the listen address is only the bind socket.
    return proc, cert, password, data_dir


def ssh_code(lab: Path, core: str, remote: str, config: dict, failure_path: Path) -> dict:
    command = "python3 -c 'import base64;exec(base64.b64decode(\"" + __import__("base64").b64encode(remote.encode()).decode() + "\"))'"
    args = snapshot_api.ssh_run_args(lab, core)
    try:
        result = subprocess.run(args + [command], input=json.dumps(config), capture_output=True,
                                text=True, timeout=90)
    except subprocess.TimeoutExpired as exc:
        snapshot_api.append_private(failure_path, {"timeout": True})
        raise RuntimeError("Core verification timed out; private failure details retained") from exc
    if result.returncode:
        snapshot_api.append_private(failure_path, {"returncode": result.returncode,
            "stderr": result.stderr, "stdout": result.stdout})
        raise RuntimeError("Core verification failed; private failure details retained")
    return json.loads(result.stdout)


def ssh_run(lab: Path, core: str, config: dict, failure_path: Path) -> dict:
    return ssh_code(lab, core, REMOTE, config, failure_path)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lab-dir", type=Path, required=True)
    parser.add_argument("--snapshot-prep-dir", type=Path, required=True)
    parser.add_argument("--snapshot-evidence", type=Path, required=True,
                        help="private successful create + Core service-restart result file")
    parser.add_argument("--replacement-dir", type=Path, required=True)
    parser.add_argument("--controller-url", required=True, help="new HTTPS origin reachable by the selected Core")
    parser.add_argument("--listen", required=True, help="Controller B bind address and port")
    parser.add_argument("--server-name", default="", help="optional TLS name previously configured for this Core")
    parser.add_argument("--expected-core-binary-sha256", required=True)
    parser.add_argument("--expected-installed-package-version", required=True)
    parser.add_argument("--expected-lab-fingerprint", required=True,
                        help="SHA-256 from lab_fingerprint for this exact validated recovery lab")
    parser.add_argument("--controller-binary", type=Path, required=True,
                        help="separately built Controller B binary; Controller A artifacts are not reused")
    parser.add_argument("--controller-binary-sha256", required=True)
    parser.add_argument("--controller-source-archive", type=Path, required=True)
    parser.add_argument("--controller-source-sha256", required=True)
    parser.add_argument("--execute", action="store_true", help="required because this switches the selected Core to Controller B")
    args = parser.parse_args()
    if not args.execute:
        parser.error("pass --execute only after the successful snapshot and Core service-restart evidence exists")
    if not re.fullmatch(r"[0-9a-f]{64}", args.expected_core_binary_sha256):
        parser.error("expected Core binary SHA-256 must be lowercase hex")
    if not re.fullmatch(r"[0-9a-f]{64}", args.expected_lab_fingerprint):
        parser.error("expected lab fingerprint must be lowercase hex")
    if not re.fullmatch(r"[0-9a-f]{64}", args.controller_binary_sha256) or not re.fullmatch(r"[0-9a-f]{64}", args.controller_source_sha256):
        parser.error("Controller B candidate SHA-256 values must be lowercase hex")
    lab = snapshot_api.ssh_run_lab(args.lab_dir)
    observed_lab_fingerprint = lab_fingerprint(lab)
    if observed_lab_fingerprint != args.expected_lab_fingerprint:
        raise RuntimeError("selected recovery lab fingerprint differs from the reviewed value")
    prep = args.snapshot_prep_dir.expanduser().resolve(strict=True)
    if prep.parent != (ROOT / ".cache").resolve() or not prep.name.startswith("recovery-corea-snapshot-api-prep-") or prep.stat().st_mode & 0o077:
        raise RuntimeError("snapshot fixture must be the selected private Core API preparation")
    fixture = snapshot_fixture(prep, args.snapshot_evidence, args.expected_core_binary_sha256,
                               args.expected_installed_package_version)
    replacement = verify_private_dir(args.replacement_dir.expanduser())
    host_name, base_b, admin_listen, admin_base, port = controller_endpoints(args.controller_url, args.listen)
    core_ids = fixture["ids"]
    vm = fixture["vm"]
    controller_prep = snapshot_api.load_json(prep / "controller-prepared.json")
    old_cert = prep / "tls.crt"
    old_cert_digest = hashlib.sha256(old_cert.read_bytes()).hexdigest()
    binary = args.controller_binary.expanduser().absolute()
    source = args.controller_source_archive.expanduser().absolute()
    binary_digest = file_sha256(binary, args.controller_binary_sha256)
    source_digest = file_sha256(source, args.controller_source_sha256)
    password = snapshot_api.read_private(prep / "password")
    old_hosts = snapshot_api.controller_request("https://" + controller_prep["listen"], old_cert,
        password, "GET", "/api/v1/hosts")
    old_host = next((host for host in old_hosts if host.get("id") == core_ids["host_id"]), None)
    managed = ((old_host or {}).get("inventory") or {}).get("managed") or {}
    if (not old_host or old_host.get("pool_id") != core_ids["pool_id"] or old_host.get("connection") != "fresh" or
            not controller_has_snapshot(managed, fixture["request"]["operation_id"], vm["vm_id"],
                fixture["request"]["snapshot_name"], fixture["request"]["description"]) or
            managed.get("recovery_reservations_reported") is not True or managed.get("recovery_reservations")):
        raise RuntimeError("Controller A has not freshly observed the completed snapshot and no pending master reservation")
    preflight_config = {"host_id": core_ids["host_id"], "pool_id": core_ids["pool_id"],
        "vm_id": vm["vm_id"], "name": fixture["request"]["snapshot_name"],
        "description": fixture["request"]["description"], "operation_id": fixture["request"]["operation_id"],
        "server_name": args.server_name}
    core_preflight = ssh_code(lab, "core1", REMOTE_PREFLIGHT, preflight_config,
        preflight_failure_path(prep, replacement))
    if core_preflight.get("host_id") != core_ids["host_id"] or core_preflight.get("pool_id") != core_ids["pool_id"]:
        raise RuntimeError("selected Core preflight identity differs")
    try:
        controller_a = snapshot_api.find_controller(prep, controller_prep["listen"], None)
    except RuntimeError as exc:
        raise RuntimeError("owned Controller A must be running and uniquely identifiable before replacement") from exc
    process_b = None
    stopped_a = None
    controller_a_digest = None
    controller_b_was_empty = False
    enrollment_attempted = False
    result_file = replacement / "replacement-result.json"
    if result_file.exists():
        raise RuntimeError("replacement result already exists; choose a fresh private directory")
    try:
        process_b, cert_b, password_b, data_b = start_controller_b(replacement, binary, source,
            args.listen, host_name, args.server_name)
        jar_b, csrf_b = wait_ready(admin_base, cert_b, password_b)
        site = controller_api(admin_base, cert_b, password_b, "POST", "/api/v1/sites",
            {"name": "Disposable replacement site"}, jar=jar_b, csrf=csrf_b)
        token = controller_api(admin_base, cert_b, password_b, "POST", "/api/v1/enrollment-tokens",
            {"site_id": site["id"]}, jar=jar_b, csrf=csrf_b)["token"]
        if controller_api(admin_base, cert_b, password_b, "GET", "/api/v1/hosts", jar=jar_b, csrf=csrf_b):
            raise RuntimeError("Controller B was not empty before Core enrollment")
        controller_b_was_empty = True
        stopped_a = snapshot_api.stop_owned_controller(prep, controller_prep["listen"], controller_a[0])
        controller_a_digest = controller_a_fingerprint(prep)
        save_private_result(replacement / "controller-a-fingerprint.json", controller_a_digest)
        root_password = snapshot_api.read_private(prep / "root-password")
        if len(root_password) > 1024 or any(ch in root_password for ch in ":\r\n\x00"):
            raise RuntimeError("selected Core password file contains invalid chpasswd input")
        config = {"root_password": root_password,
            "host_id": core_ids["host_id"], "pool_id": core_ids["pool_id"], "vm_id": vm["vm_id"],
            "vm_name": vm["name"], "operation_id": fixture["request"]["operation_id"],
            "name": fixture["request"]["snapshot_name"], "description": fixture["request"]["description"],
            "controller_url": base_b, "controller_ca": cert_b.read_text(), "enrollment_token": token,
            "controller_ca_changed": hashlib.sha256(cert_b.read_bytes()).hexdigest() != old_cert_digest,
            "server_name": args.server_name, "markers": fixture["markers"]}
        if not config["controller_ca_changed"]:
            raise RuntimeError("Controller B must have a distinct TLS CA")
        enrollment_attempted = True
        core_result = ssh_run(lab, "core1", config, replacement / "ssh-failures.jsonl")
        adopted = snapshot_api.wait_controller_adoption(replacement, admin_listen, cert_b, password_b,
            core_ids["host_id"], core_ids["pool_id"], fixture["request"]["operation_id"], vm["vm_id"],
            fixture["request"]["snapshot_name"], fixture["request"]["description"], 90)
        hosts_b = controller_api(admin_base, cert_b, password_b, "GET", "/api/v1/hosts", jar=jar_b, csrf=csrf_b)
        host_b = next((host for host in hosts_b if host.get("id") == core_ids["host_id"]), None)
        managed_b = ((host_b or {}).get("inventory") or {}).get("managed") or {}
        if (managed_b.get("recovery_reservations_reported") is not True or
                managed_b.get("recovery_reservations")):
            raise RuntimeError("Controller B did not adopt the settled empty recovery-reservation view")
        operations = controller_api(admin_base, cert_b, password_b, "GET", "/api/v1/operations", jar=jar_b, csrf=csrf_b)
        if adopted_snapshot_operation(operations, core_ids["host_id"], fixture["request"]["operation_id"]) is None:
            raise RuntimeError("Controller B did not adopt the Core's successful host-scoped snapshot outcome")
        pools_b = controller_api(admin_base, cert_b, password_b, "GET", "/api/v1/pools", jar=jar_b, csrf=csrf_b)
        pool_b = next((pool for pool in pools_b if pool.get("id") == core_ids["pool_id"]), None)
        if (not pool_b or pool_b.get("master_host_id") != core_ids["host_id"] or
                pool_b.get("name") != core_result["after"].get("pool_name")):
            raise RuntimeError("Controller B did not adopt the unchanged Core pool identity and name")
        controller_a_unchanged = controller_a_fingerprint(prep) == controller_a_digest
        if not controller_a_unchanged:
            raise RuntimeError("Controller A database or CA changed during replacement")
        save_private_result(replacement / "controller-b-process.json", {"pid": process_b.pid,
            "argv": process_b.args, "data_dir": str(data_b), "certificate": str(cert_b),
            "listen": args.listen, "controller_url": base_b,
            "binary": str(binary), "binary_sha256": binary_digest,
            "source_archive": str(source), "source_archive_sha256": source_digest})
        save_private_result(result_file, {"status": "passed", "lab_fingerprint": observed_lab_fingerprint,
            "core_preflight": core_preflight, "core": core_result,
            "controller_b_catalog": adopted, "controller_b_operation_adopted": True,
            "controller_b_empty_before_enrollment": controller_b_was_empty,
            "controller_a_data_preserved": controller_a_unchanged,
            "controller_a_fingerprint": controller_a_digest,
            "controller_b_database_independent": data_b.is_dir()})
    except Exception as exc:
        recovery_error = None
        if not enrollment_attempted and stopped_a is not None:
            try:
                snapshot_api.restart_controller(prep, stopped_a, old_cert, password)
            except Exception as restart_error:
                recovery_error = str(restart_error)
        if process_b is not None and not enrollment_attempted and (stopped_a is None or recovery_error is None) and process_b.poll() is None:
            process_b.terminate()
            try:
                process_b.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process_b.kill()
                process_b.wait(timeout=10)
        save_private_result(result_file, {"status": "failed", "error": str(exc),
            "controller_a_stopped": stopped_a is not None,
            "controller_a_restart_error": recovery_error,
            "controller_a_data_preserved": (prep / "controller-state").is_dir()})
        raise
    # Controller B remains available for inspection. Controller A stays stopped; its CA and
    # database are retained untouched so the replacement's independence is reviewable.
    print(json.dumps({"status": "passed", "steps": ["private success evidence checked",
        "Controller B empty database and new CA", "Core switched through local enrollment",
        "host pool and snapshot catalog adopted"]}))
    return 0


def save_private_result(path: Path, result: dict) -> None:
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as stream:
        json.dump(result, stream, indent=2, sort_keys=True)
        stream.write("\n")


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print("snapshot Controller replacement failed: " + str(exc), file=sys.stderr)
        raise SystemExit(1)
