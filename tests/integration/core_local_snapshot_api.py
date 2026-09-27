#!/usr/bin/env python3
"""Exercise the Core-local stopped snapshot API on one selected disposable Core."""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid


ROOT = Path(__file__).resolve().parents[2]


def postnative_snapshot_wrapper(operation_id: str, gate_dir: str | Path,
                                real_virsh: str = "/usr/bin/virsh") -> str:
    """Build the one-shot crash gate used after a managed native snapshot succeeds."""
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}", operation_id):
        raise ValueError("invalid snapshot operation ID")
    gate = Path(gate_dir)
    if not gate.is_absolute():
        raise ValueError("snapshot crash gate path must be absolute")
    expected = "nex-" + operation_id
    return f'''#!/bin/bash
set -u
real={shlex.quote(real_virsh)}
gate={shlex.quote(str(gate))}
expected={shlex.quote(expected)}
command_seen=0
snapshot_operand=0
match=0
for arg in "$@"; do
  if [[ "$command_seen" == 1 ]]; then
    snapshot_operand=$((snapshot_operand + 1))
    if [[ "$snapshot_operand" == 2 && "$arg" == "$expected" ]]; then match=1; fi
  elif [[ "$arg" == snapshot-create-as ]]; then
    command_seen=1
  fi
done
if [[ "$match" == 1 ]]; then
  mkdir -p "$gate"
  chmod 0700 "$gate"
  exec 9>"$gate/count.lock"
  flock -x 9
  count=0
  if [[ -f "$gate/count" ]]; then read -r count < "$gate/count" || count=0; fi
  count=$((count + 1))
  printf '%s\\n' "$count" > "$gate/count"
  {{ printf 'count=%s at=' "$count"; date -u +%Y-%m-%dT%H:%M:%SZ; printf '%q ' "$@"; printf '\\n'; }} >> "$gate/invocations.log"
  chmod 0600 "$gate/count" "$gate/invocations.log"
  flock -u 9
  if [[ "$count" -ne 1 ]]; then
    printf 'refused duplicate snapshot-create-as invocation %s\\n' "$count" > "$gate/duplicate-refused"
    chmod 0600 "$gate/duplicate-refused"
    exit 98
  fi
  if "$real" "$@"; then rc=0; else rc=$?; fi
  printf '%s\\n' "$rc" > "$gate/virsh-exit"
  chmod 0600 "$gate/virsh-exit"
  if [[ "$rc" -ne 0 ]]; then exit "$rc"; fi
  {{ printf 'exit=%s at=' "$rc"; date -u +%Y-%m-%dT%H:%M:%SZ; }} > "$gate/native-complete.tmp"
  chmod 0600 "$gate/native-complete.tmp"
  mv "$gate/native-complete.tmp" "$gate/native-complete"
  deadline=$((SECONDS + 600))
  while [[ ! -e "$gate/release" ]]; do
    if (( SECONDS >= deadline )); then
      printf 'wrapper wait timeout\\n' > "$gate/wait-timeout"
      chmod 0600 "$gate/wait-timeout"
      exit 124
    fi
    sleep 0.05
  done
  exit 0
fi
exec "$real" "$@"
'''


REMOTE = r'''import hashlib, http.cookiejar, json, os, re, ssl, subprocess, sys, time, urllib.error, urllib.request, xml.etree.ElementTree as ET

config = json.loads(sys.stdin.readline())
cert_path = "/var/lib/nex/core-local/tls.crt"
san = subprocess.run(["openssl", "x509", "-in", cert_path, "-noout", "-ext", "subjectAltName"],
                     check=True, capture_output=True, text=True).stdout
match = re.search(r"IP Address:([0-9.]+)", san)
if not match:
    raise RuntimeError("Core certificate has no IPv4 SAN")
origin = "https://" + match.group(1) + ":9443"
context = ssl.create_default_context(cafile=cert_path)
jar = None
opener = None

def reset_opener():
    global jar, opener
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar), urllib.request.HTTPSHandler(context=context))

reset_opener()

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

login = call("/api/local/v1/session", "POST", {"username": "root", "password": config["root_password"]}, expected=200)
csrf = login["csrf_token"]
subprocess.run(["passwd", "-l", "root"], check=True, capture_output=True, text=True)
if subprocess.run(["passwd", "-S", "root"], check=True, capture_output=True, text=True).stdout.split()[1] != "L":
    raise RuntimeError("Core root account did not return to its locked state after login")
host = call("/api/local/v1/host")
pool = call("/api/local/v1/pool")
if host.get("host_id") != config["host_id"] or pool.get("pool_id") != config["pool_id"] or pool.get("role") != "master":
    raise RuntimeError("selected Core is not the expected enrolled pool master")
if config["vm_id"] != config["marker_vm_id"] or len(config["markers"]) != 2:
    raise RuntimeError("private managed-VM marker evidence does not match the selected VM")
if virsh("domuuid", config["vm_name"]).lower() != config["vm_id"].lower():
    raise RuntimeError("selected VM UUID differs")
if "shut off" not in virsh("domstate", config["vm_name"]).lower():
    raise RuntimeError("selected VM is not stopped")
existing_snapshots = virsh("snapshot-list", config["vm_name"], "--name").splitlines()
native_name = "nex-" + config["operation_id"]
if config["resume"]:
    if any(name != native_name for name in existing_snapshots):
        raise RuntimeError("preserved operation VM has unrelated native snapshots")
elif existing_snapshots:
    raise RuntimeError("selected VM already has snapshots")

body = {"operation_id": config["operation_id"], "name": config["name"], "description": config["description"]}
path = "/api/local/v1/pool/vms/" + config["vm_id"] + "/snapshots"
operation_path = "/api/local/v1/pool/vms/" + config["vm_id"] + "/snapshot-operations/" + config["operation_id"]
previous = None
if config["resume"]:
    previous = call(operation_path)
    if (previous.get("operation_id") != config["operation_id"] or
            previous.get("status") not in ("pending", "needs_attention", "succeeded")):
        raise RuntimeError("preserved request does not identify a resumable Core snapshot operation")
accepted = call(path, "POST", body, csrf, expected=202)
allowed_statuses = ("pending", "succeeded", "needs_attention") if config["resume"] else ("pending", "succeeded")
if accepted.get("operation_id") != config["operation_id"] or accepted.get("status") not in allowed_statuses:
    raise RuntimeError("snapshot request was not durably accepted")
retry = call(path, "POST", body, csrf, expected=202)
if retry.get("operation_id") != accepted.get("operation_id"):
    raise RuntimeError("exact snapshot retry returned a different operation")
conflict = dict(body, name=body["name"] + " changed")
call(path, "POST", conflict, csrf, expected=409)

deadline = time.monotonic() + config["timeout_seconds"]
while True:
    result = call(operation_path)
    if result.get("operation_id") != config["operation_id"]:
        raise RuntimeError("operation poll returned another operation")
    if result.get("status") == "succeeded":
        break
    if result.get("status") in ("failed", "needs_attention"):
        call("/api/local/v1/session", "DELETE", csrf=csrf, expected=200)
        print(json.dumps({"status": result["status"], "accepted": accepted, "exact_retry": retry,
                          "previous_operation": previous, "operation": result,
                          "native_snapshots": virsh("snapshot-list", config["vm_name"], "--name").splitlines()}, sort_keys=True))
        raise SystemExit(0)
    if result.get("status") != "pending" or time.monotonic() >= deadline:
        raise RuntimeError("snapshot operation did not reach success before timeout")
    time.sleep(1)

catalog = call(path)
matches = [item for item in catalog if item.get("id") == config["operation_id"]]
if (len(matches) != 1 or matches[0].get("vm_id") != config["vm_id"] or
        matches[0].get("name") != config["name"] or matches[0].get("description") != config["description"]):
    raise RuntimeError("Core master catalog does not contain exactly the completed snapshot")
if "nex-" + config["operation_id"] not in virsh("snapshot-list", config["vm_name"], "--name").splitlines():
    raise RuntimeError("native snapshot metadata is absent after reported success")
if "shut off" not in virsh("domstate", config["vm_name"]).lower():
    raise RuntimeError("stopped snapshot changed the VM power state")
domain = ET.fromstring(virsh("dumpxml", config["vm_name"], "--inactive"))
sources = {}
for disk in domain.findall("./devices/disk"):
    target, source = disk.find("target"), disk.find("source")
    if target is not None and source is not None and source.get("file"):
        sources[target.get("dev")] = source.get("file")
markers = []
for marker in config["markers"]:
    target = marker["target"]
    source = sources.get(target)
    if not source:
        raise RuntimeError("stopped XML has no current source for marker target " + target)
    read = subprocess.run(["qemu-io", "-f", "qcow2", "-c",
                           "read -P " + marker["pattern"] + " " + str(marker["offset"]) + " " + str(marker["length"]), source],
                          capture_output=True, text=True, timeout=30)
    output = read.stdout + read.stderr
    if read.returncode != 0 or "read " + str(marker["length"]) + "/" + str(marker["length"]) not in output or "pattern verification failed" in output.lower():
        raise RuntimeError("stopped snapshot current disk marker did not verify for " + target)
    markers.append({"target": target, "pattern": marker["pattern"], "source": source, "readback": read.stdout.strip()})
call("/api/local/v1/session", "DELETE", csrf=csrf, expected=200)

def service_identity():
    package_version = subprocess.run(["dpkg-query", "-W", "-f=${Version}", "nex-core-local"],
                                     check=True, capture_output=True, text=True).stdout.strip()
    state = subprocess.run(["systemctl", "is-active", "nex-core-local.service"],
                           check=True, capture_output=True, text=True).stdout.strip()
    pid = int(subprocess.run(["systemctl", "show", "nex-core-local.service", "-p", "MainPID", "--value"],
                             check=True, capture_output=True, text=True).stdout.strip())
    executable = os.path.realpath("/proc/" + str(pid) + "/exe")
    digest = hashlib.sha256(open("/proc/" + str(pid) + "/exe", "rb").read()).hexdigest()
    if (package_version != config["package_version"] or state != "active" or pid < 2 or
            executable != "/usr/lib/nex/nex-core-local" or digest != config["binary_sha256"]):
        raise RuntimeError("Core service identity changed during restart verification")
    return {"package_version": package_version, "state": state, "pid": pid,
            "executable": executable, "binary_sha256": digest}

def pinned_https_ready():
    request = urllib.request.Request(origin + "/api/local/v1/host",
                                     headers={"Accept": "application/json", "Origin": origin})
    try:
        with opener.open(request, timeout=3) as response:
            return response.status in (200, 401)
    except urllib.error.HTTPError as error:
        return error.code == 401

def wait_for_service_restart(before_pid, deadline, identity_fn, https_ready_fn,
                             now_fn=time.monotonic, sleep_fn=time.sleep):
    last_error = None
    while now_fn() < deadline:
        try:
            identity = identity_fn()
            if identity["pid"] != before_pid and https_ready_fn():
                return identity
        except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
            # systemd may briefly expose its setup wrapper before execv replaces it.
            last_error = error
        sleep_fn(0.2)
    raise RuntimeError("Core-local service did not reach the verified executable and pinned HTTPS readiness" +
                       (": " + str(last_error) if last_error else ""))

before_restart = service_identity()
subprocess.run(["systemctl", "restart", "nex-core-local.service"], check=True, capture_output=True, text=True, timeout=45)
after_restart = wait_for_service_restart(before_restart["pid"], time.monotonic() + 30,
                                         service_identity, pinned_https_ready)

# The Core service has no Controller connection here. Reauthenticate locally and
# replay the same durable request after its process restart.
subprocess.run(["chpasswd"], input="root:" + config["root_password"] + "\n", check=True,
               capture_output=True, text=True, timeout=10)
reset_opener()
login_after_restart = call("/api/local/v1/session", "POST",
                           {"username": "root", "password": config["root_password"]}, expected=200)
csrf_after_restart = login_after_restart["csrf_token"]
subprocess.run(["passwd", "-l", "root"], check=True, capture_output=True, text=True)
if subprocess.run(["passwd", "-S", "root"], check=True, capture_output=True, text=True).stdout.split()[1] != "L":
    raise RuntimeError("Core root account did not relock after post-restart login")
replay_after_restart = call(path, "POST", body, csrf_after_restart, expected=202)
if replay_after_restart.get("operation_id") != config["operation_id"]:
    raise RuntimeError("post-restart exact replay returned a different operation")
operation_after_restart = call(operation_path)
if (operation_after_restart.get("operation_id") != config["operation_id"] or
        operation_after_restart.get("status") != "succeeded"):
    raise RuntimeError("completed Core operation did not survive the service restart")
catalog_after_restart = call(path)
matches_after_restart = [item for item in catalog_after_restart if item.get("id") == config["operation_id"]]
if (len(matches_after_restart) != 1 or matches_after_restart[0].get("vm_id") != config["vm_id"] or
        matches_after_restart[0].get("name") != config["name"] or
        matches_after_restart[0].get("description") != config["description"]):
    raise RuntimeError("Core master catalog changed across exact replay after service restart")
if "shut off" not in virsh("domstate", config["vm_name"]).lower():
    raise RuntimeError("Core service restart changed the VM power state")
if "nex-" + config["operation_id"] not in virsh("snapshot-list", config["vm_name"], "--name").splitlines():
    raise RuntimeError("native snapshot metadata did not survive Core service restart")
domain_after_restart = ET.fromstring(virsh("dumpxml", config["vm_name"], "--inactive"))
sources_after_restart = {}
for disk in domain_after_restart.findall("./devices/disk"):
    target, source = disk.find("target"), disk.find("source")
    if target is not None and source is not None and source.get("file"):
        sources_after_restart[target.get("dev")] = source.get("file")
markers_after_restart = []
for marker in config["markers"]:
    target = marker["target"]
    source = sources_after_restart.get(target)
    if not source:
        raise RuntimeError("post-restart stopped XML has no source for marker target " + target)
    disk_index = {"vda": 0, "vdb": 1}.get(target)
    if disk_index is None:
        raise RuntimeError("unexpected stopped snapshot disk target " + target)
    base_source = os.path.join(config["storage_root"], "nex-vms", config["vm_id"], "disk-" + str(disk_index) + ".qcow2")
    chain = subprocess.run(["qemu-img", "info", "--backing-chain", "--output=json", source],
                           check=True, capture_output=True, text=True, timeout=30)
    if [item.get("filename") for item in json.loads(chain.stdout)] != [source, base_source]:
        raise RuntimeError("post-restart current disk chain differs from the selected snapshot base for " + target)
    read = subprocess.run(["qemu-io", "-f", "qcow2", "-c",
                           "read -P " + marker["pattern"] + " " + str(marker["offset"]) + " " + str(marker["length"]), source],
                          capture_output=True, text=True, timeout=30)
    output = read.stdout + read.stderr
    if read.returncode != 0 or "read " + str(marker["length"]) + "/" + str(marker["length"]) not in output or "pattern verification failed" in output.lower():
        raise RuntimeError("post-restart current disk marker did not verify for " + target)
    markers_after_restart.append({"target": target, "pattern": marker["pattern"], "source": source,
                                  "readback": read.stdout.strip()})
call("/api/local/v1/session", "DELETE", csrf=csrf_after_restart, expected=200)
print(json.dumps({"status": "passed", "accepted": accepted, "exact_retry": retry, "completed": result,
                  "master_catalog": matches[0], "vm_state": "shut off", "markers": markers,
                  "core_service_before_restart": before_restart,
                  "core_service_after_restart": after_restart,
                  "exact_replay_after_restart": replay_after_restart,
                  "operation_after_restart": operation_after_restart,
                  "master_catalog_after_restart": matches_after_restart[0],
                  "markers_after_restart": markers_after_restart}, sort_keys=True))
'''
CANDIDATE_CHECK = r'''import hashlib, json, os, re, subprocess, sys
import xml.etree.ElementTree as ET
want=json.loads(sys.stdin.readline())
version=subprocess.run(["dpkg-query","-W","-f=${Version}","nex-core-local"],check=True,capture_output=True,text=True).stdout.strip()
state=subprocess.run(["systemctl","is-active","nex-core-local.service"],check=True,capture_output=True,text=True).stdout.strip()
pid=int(subprocess.run(["systemctl","show","nex-core-local.service","-p","MainPID","--value"],check=True,capture_output=True,text=True).stdout.strip())
if pid < 2: raise RuntimeError("Core service has no running main process")
executable_path=os.path.realpath("/proc/"+str(pid)+"/exe")
if executable_path != "/usr/lib/nex/nex-core-local": raise RuntimeError("Core main process is not nex-core-local")
digest=hashlib.sha256(open("/proc/"+str(pid)+"/exe","rb").read()).hexdigest()
if version != want["package_version"] or digest != want["binary_sha256"] or state != "active":
 raise RuntimeError("installed Core candidate identity or service state mismatch")
vm=want["vm_id"]
vm_name=want["vm_name"]
if subprocess.run(["virsh","-c","qemu:///system","domuuid",vm_name],check=True,capture_output=True,text=True).stdout.strip().lower() != vm.lower():
 raise RuntimeError("selected VM UUID is absent from this Core")
if "shut off" not in subprocess.run(["virsh","-c","qemu:///system","domstate",vm_name],check=True,capture_output=True,text=True).stdout.lower():
 raise RuntimeError("selected VM is not shut off")
snapshots=subprocess.run(["virsh","-c","qemu:///system","snapshot-list",vm_name,"--name"],check=True,capture_output=True,text=True).stdout.strip()
expected_native="nex-"+want.get("operation_id","")
if (not want.get("resume") and snapshots) or (want.get("resume") and any(name != expected_native for name in snapshots.splitlines())):
 raise RuntimeError("selected VM already has unrelated snapshots")
xml=subprocess.run(["virsh","-c","qemu:///system","dumpxml",vm_name,"--inactive"],check=True,capture_output=True,text=True).stdout
domain=ET.fromstring(xml)
sources={disk.find("target").get("dev"):disk.find("source").get("file") for disk in domain.findall("./devices/disk") if disk.find("target") is not None and disk.find("source") is not None and disk.find("source").get("file")}
root=os.path.realpath(want["storage_root"] + "/nex-vms/" + vm)
base={"vda":root+"/disk-0.qcow2","vdb":root+"/disk-1.qcow2"}
expected={target:root+"/snapshot-"+want.get("operation_id","")+"-"+target+".qcow2" for target in ("vda","vdb")}
valid_sources = sources == base if not want.get("resume") else (sources == base or sources == expected)
if not valid_sources:
 raise RuntimeError("selected VM disk sources do not match its private fixture manifest")
if want.get("resume") and sources == expected:
 for target,source_path in sources.items():
  chain=subprocess.run(["qemu-img","info","--backing-chain","--output=json",source_path],check=True,capture_output=True,text=True)
  if [item.get("filename") for item in json.loads(chain.stdout)] != [source_path,base[target]]:
   raise RuntimeError("preserved operation snapshot head differs from its expected base")
print(json.dumps({"version":version,"binary_sha256":digest,"service":state,"main_pid":pid,"binary_path":executable_path,"vm_state":"shut off","two_disk_sources_match":True}))
'''


def read_private(path: Path) -> str:
    info = path.stat()
    if not path.is_file() or info.st_mode & 0o077:
        raise RuntimeError(f"private credential file has unsafe permissions: {path.name}")
    return path.read_text().rstrip("\r\n")


def ssh_command(lab: Path, core: str, command: str, input_text: str | None = None) -> str:
    ports = load_json(lab / "ports.json")
    result = subprocess.run([
        "ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
        "-o", "StrictHostKeyChecking=accept-new", "-o", f"UserKnownHostsFile={lab / 'known_hosts'}",
        "-i", str(lab / "id_ed25519"), "-p", str(ports[core]), "root@127.0.0.1", command,
    ], input=input_text, capture_output=True, text=True, timeout=15)
    if result.returncode != 0:
        raise RuntimeError("selected Core SSH command failed: " + command)
    return result.stdout.strip()


def load_json(path: Path) -> dict:
    return json.loads(path.read_text())


def proc_identity(pid: int) -> tuple[int, list[str], Path, str]:
    raw = Path(f"/proc/{pid}/stat").read_text()
    tail = raw[raw.rfind(")") + 2 :].split()
    start_time = int(tail[19])
    argv = [part.decode() for part in Path(f"/proc/{pid}/cmdline").read_bytes().split(b"\0") if part]
    executable = str(Path(f"/proc/{pid}/exe").resolve())
    cwd = Path(f"/proc/{pid}/cwd").resolve()
    return start_time, argv, cwd, executable


def controller_owned(prep: Path, argv: list[str], cwd: Path, executable: str, listen: str) -> bool:
    binary = (prep / "nex-controller").resolve()
    data_dir = (prep / "controller-state").resolve()
    if executable != str(binary):
        return False

    def value(flag: str) -> str:
        return argv[argv.index(flag) + 1] if flag in argv and argv.index(flag) + 1 < len(argv) else ""

    try:
        return (
            (cwd / value("-data-dir")).resolve() == data_dir
            and value("-listen") == listen
            and (cwd / value("-tls-cert")).resolve() == (prep / "tls.crt").resolve()
            and (cwd / value("-tls-key")).resolve() == (prep / "tls.key").resolve()
            and (cwd / value("-admin-password-file")).resolve() == (prep / "password").resolve()
            and (cwd / value("-source-archive")).resolve() == (prep / "nex-source.tar.gz").resolve()
        )
    except OSError:
        return False


def find_controller(prep: Path, listen: str, wanted_pid: int | None) -> tuple[int, int, list[str], Path]:
    matches = []
    candidates = [wanted_pid] if wanted_pid else [int(p.name) for p in Path("/proc").iterdir() if p.name.isdigit()]
    for pid in candidates:
        if pid is None:
            continue
        try:
            start, argv, cwd, executable = proc_identity(pid)
        except (FileNotFoundError, ProcessLookupError, PermissionError, ValueError):
            continue
        if not controller_owned(prep, argv, cwd, executable, listen):
            continue
        matches.append((pid, start, argv, cwd))
    if len(matches) != 1:
        raise RuntimeError("could not uniquely identify the owned ephemeral Controller process")
    return matches[0]


def wait_process_gone(pid: int, start: int, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            current, _, _, _ = proc_identity(pid)
        except (FileNotFoundError, ProcessLookupError, PermissionError, ValueError):
            return
        if current != start:
            return
        time.sleep(0.1)
    raise RuntimeError("owned Controller did not stop after SIGTERM")


def controller_request(base: str, cert: Path, password: str, method: str, path: str, body=None, timeout=10):
    context = __import__("ssl").create_default_context(cafile=str(cert))
    context.check_hostname = False  # trust is pinned to this private ephemeral certificate
    jar = __import__("http.cookiejar").cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar), urllib.request.HTTPSHandler(context=context))
    origin = base
    login = urllib.request.Request(base + "/api/v1/session",
        data=json.dumps({"username": "root", "password": password}).encode(),
        headers={"Content-Type": "application/json", "Origin": origin}, method="POST")
    with opener.open(login, timeout=timeout) as response:
        if response.status != 200:
            raise RuntimeError("ephemeral Controller login failed")
    headers = {"Accept": "application/json", "Origin": origin}
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    with opener.open(request, timeout=timeout) as response:
        return json.loads(response.read())


def adopted_catalog(host: dict, host_id: str, pool_id: str, snapshot_id: str, vm_id: str,
                    name: str, description: str) -> dict | None:
    if host.get("id") != host_id or host.get("pool_id") != pool_id or host.get("connection") != "fresh":
        return None
    managed = ((host.get("inventory") or {}).get("managed") or {})
    if managed.get("pool_vm_snapshots_reported") is not True:
        return None
    return next((item for item in managed.get("pool_vm_snapshots", []) if
                 item.get("id") == snapshot_id and item.get("vm_id") == vm_id and
                 item.get("name") == name and item.get("description") == description), None)


def host_reports_managed_vm(host: dict, vm_id: str) -> bool:
    return managed_vm_inventory_error(host, vm_id) is None


def managed_vm_inventory_error(host: dict, vm_id: str) -> str | None:
    connection = host.get("connection")
    if connection != "fresh":
        return f"Core host inventory is stale or unavailable (connection={connection or 'unknown'})"
    inventory = host.get("inventory") or {}
    check = next((item for item in inventory.get("checks", []) if item.get("name") == "managed-vms"), None)
    if not check or check.get("status") != "healthy" or check.get("freshness") != "fresh":
        status = check.get("status", "missing") if check else "missing"
        freshness = check.get("freshness", "unknown") if check else "unknown"
        return f"Core managed-VM inventory is unverified (status={status}, freshness={freshness})"
    if not any(item.get("id") == vm_id for item in inventory.get("vms", []) if isinstance(item, dict)):
        return "selected VM is absent from the fresh Core-reported managed inventory"
    return None


def attempt_evidence_paths(prep: Path, label: str) -> tuple[Path, Path, Path]:
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,31}", label):
        raise RuntimeError("attempt label must use lowercase letters, digits, and hyphens")
    evidence = prep / f"core-local-snapshot-api-{label}-result.json"
    request = prep / f"core-local-snapshot-api-{label}-request.json"
    failures = prep / f"core-local-snapshot-api-{label}-ssh-failures.jsonl"
    if evidence.exists() or request.exists():
        raise RuntimeError("private result or request file already exists; choose a new attempt label")
    return evidence, request, failures


def load_snapshot_request(path: Path, prep: Path, vm_id: str) -> dict:
    if path.is_symlink():
        raise RuntimeError("preserved snapshot request must not be a symlink")
    request_path = path.expanduser().resolve(strict=True)
    if not request_path.is_relative_to(prep.resolve()):
        raise RuntimeError("preserved snapshot request must be inside the selected private prep directory")
    if not request_path.is_file() or request_path.stat().st_mode & 0o077:
        raise RuntimeError("preserved snapshot request must be a private regular file")
    request = load_json(request_path)
    body = request.get("body")
    operation_id = request.get("operation_id")
    if (not isinstance(operation_id, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}", operation_id) or
            request.get("vm_id") != vm_id or not isinstance(body, dict) or
            set(body) != {"operation_id", "name", "description"} or body.get("operation_id") != operation_id or
            not isinstance(body.get("name"), str) or not body["name"] or
            not isinstance(body.get("description"), str) or request.get("snapshot_name") != body["name"] or
            request.get("description") != body["description"]):
        raise RuntimeError("preserved snapshot request does not match the selected VM or exact request body")
    return {"operation_id": operation_id, "vm_id": vm_id, "snapshot_name": body["name"],
            "description": body["description"], "body": body}


def stop_owned_controller(prep: Path, listen: str, pid: int | None) -> dict:
    found_pid, start, argv, cwd = find_controller(prep, listen, pid)
    os.kill(found_pid, signal.SIGTERM)
    wait_process_gone(found_pid, start, 15)
    return {"pid": found_pid, "start_time": start, "argv": argv, "cwd": str(cwd), "listen": listen}


def restart_controller(prep: Path, stopped: dict, cert_path: Path, password: str) -> subprocess.Popen:
    log = prep / "controller-api-acceptance-restart.log"
    fd = os.open(log, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
    try:
        os.fchmod(fd, 0o600)
        proc = subprocess.Popen(stopped["argv"], cwd=stopped["cwd"], stdin=subprocess.DEVNULL,
                                stdout=fd, stderr=fd, start_new_session=True, close_fds=True)
    finally:
        os.close(fd)
    base = "https://" + stopped["listen"]
    deadline = time.monotonic() + 20
    try:
        while time.monotonic() < deadline:
            if proc.poll() is not None:
                raise RuntimeError("ephemeral Controller exited during restart")
            try:
                controller_request(base, cert_path, password, "GET", "/api/v1/hosts", timeout=2)
                return proc
            except (OSError, urllib.error.URLError, TimeoutError):
                time.sleep(0.2)
        raise RuntimeError("ephemeral Controller did not become ready after restart")
    except Exception:
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired as wait_error:
                raise RuntimeError(f"Controller restart PID {proc.pid} did not exit; inspect before retry") from wait_error
        raise


def wait_controller_adoption(prep: Path, listen: str, cert: Path, password: str,
                             host_id: str, pool_id: str, snapshot_id: str, vm_id: str,
                             name: str, description: str, timeout: int) -> dict:
    base = "https://" + listen
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        hosts = controller_request(base, cert, password, "GET", "/api/v1/hosts")
        host = next((entry for entry in hosts if entry.get("id") == host_id), None)
        if host:
            inv = host.get("inventory") or {}
            managed = inv.get("managed") or {}
            catalog = managed.get("pool_vm_snapshots", [])
            last = {"connection": host.get("connection"), "reported": managed.get("pool_vm_snapshots_reported"),
                    "catalog": catalog}
            if adopted_catalog(host, host_id, pool_id, snapshot_id, vm_id, name, description):
                return last
        time.sleep(1)
    raise RuntimeError("Controller did not adopt the Core master snapshot catalog: " + json.dumps(last, sort_keys=True))


def ssh_run(lab: Path, core: str, remote_code: str, config: dict, failure_path: Path) -> dict:
    command = "python3 -c 'import base64;exec(base64.b64decode(\"" + base64.b64encode(remote_code.encode()).decode() + "\"))'"
    ssh_args = ssh_run_args(lab, core)
    try:
        result = subprocess.run(ssh_args + [command], input=json.dumps(config),
                                capture_output=True, text=True, timeout=config["timeout_seconds"] + 30)
    except subprocess.TimeoutExpired as exc:
        def output(value):
            return value.decode(errors="replace") if isinstance(value, bytes) else value
        append_private(failure_path, {"timeout": True, "stdout": output(exc.stdout), "stderr": output(exc.stderr)})
        raise RuntimeError("Core SSH operation exceeded its bounded timeout; private output was retained") from exc
    if result.returncode != 0:
        append_private(failure_path, {"returncode": result.returncode, "stdout": result.stdout, "stderr": result.stderr})
        raise RuntimeError("Core SSH operation failed; private output was retained")
    return json.loads(result.stdout)


def ssh_run_lab(lab: Path) -> Path:
    helper = __import__("runpy").run_path(str(ROOT / "tests/integration/recovery_s0_interrupt.py"))
    return helper["validate_lab"](lab)


def ssh_run_args(lab: Path, core: str) -> list[str]:
    helper = __import__("runpy").run_path(str(ROOT / "tests/integration/recovery_s0_interrupt.py"))
    return helper["ssh_args"](helper["validate_lab"](lab), core)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lab-dir", type=Path, required=True)
    parser.add_argument("--core", choices=("core1",), default="core1")
    parser.add_argument("--prep-dir", type=Path, required=True)
    parser.add_argument("--expected-core-binary-sha256", required=True)
    parser.add_argument("--expected-installed-package-version", required=True)
    parser.add_argument("--controller-pid", type=int, help="exact PID of the owned ephemeral Controller")
    parser.add_argument("--timeout-seconds", type=int, default=180)
    parser.add_argument("--request-file", type=Path,
                        help="resume the exact operation recorded in a private request envelope")
    parser.add_argument("--attempt-label", default="attempt1",
                        help="unique safe label for this immutable result attempt")
    parser.add_argument("--execute", action="store_true", help="required because this submits a native snapshot request")
    args = parser.parse_args()
    raw_prep = args.prep_dir.expanduser()
    if raw_prep.is_symlink():
        raise RuntimeError("snapshot API prep directory must not be a symlink")
    lab, prep = args.lab_dir.resolve(), raw_prep.resolve(strict=True)
    if prep.parent != (ROOT / ".cache").resolve() or not prep.name.startswith("recovery-corea-snapshot-api-prep-"):
        raise RuntimeError("prep directory is outside the selected private CoreA fixture")
    if prep.stat().st_mode & 0o077:
        raise RuntimeError("snapshot API prep directory must be private and not a symlink")
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,31}", args.attempt_label):
        parser.error("attempt label must use lowercase letters, digits, and hyphens")
    evidence, request_evidence, failure_path = attempt_evidence_paths(prep, args.attempt_label)
    if not args.execute:
        parser.error("pass --execute only after a reviewed candidate handoff and disposable VM preflight")
    if not re.fullmatch(r"[0-9a-f]{64}", args.expected_core_binary_sha256):
        parser.error("expected Core binary SHA-256 must be lowercase hex")
    if args.timeout_seconds < 30 or args.timeout_seconds > 900:
        parser.error("timeout must be between 30 and 900 seconds")
    vm = load_json(prep / "managed-vm.json")
    marker_evidence = load_json(prep / "managed-vm-native-inspection.json")
    ids = load_json(prep / "corea-ids.json")
    controller = load_json(prep / "controller-prepared.json")
    if vm.get("vm_id") is None or vm.get("host_id") != ids.get("host_id") or vm.get("pool_id") != ids.get("pool_id"):
        raise RuntimeError("managed VM identity does not match the enrolled Core fixture")
    if marker_evidence.get("vm_id") != vm["vm_id"] or marker_evidence.get("state") != "shut off" or len(marker_evidence.get("current_markers", [])) != 2:
        raise RuntimeError("managed VM marker evidence is incomplete or belongs to another VM")
    if marker_evidence.get("uuid", "").lower() != vm["vm_id"].lower():
        raise RuntimeError("managed VM inspection UUID differs from its recorded identity")
    preserved_request = load_snapshot_request(args.request_file, prep, vm["vm_id"]) if args.request_file else None
    resources = load_json(prep / "resources.json")
    storage_root = Path(resources.get("storage_path", "")).resolve()
    expected_vm_dir = storage_root / "nex-vms" / vm["vm_id"]
    for index, marker in enumerate(marker_evidence["current_markers"]):
        if Path(marker.get("path", "")).resolve() != expected_vm_dir / f"disk-{index}.qcow2":
            raise RuntimeError("marker evidence does not bind to the selected VM disk directory")
    listen = controller.get("listen", "")
    if not re.fullmatch(r"127\.0\.0\.1:[0-9]{1,5}", listen):
        raise RuntimeError("ephemeral Controller is not bound to loopback")
    port = int(listen.rsplit(":", 1)[1])
    if port < 1 or port > 65535:
        raise RuntimeError("invalid Controller loopback port")
    core_password = read_private(prep / "root-password")
    controller_password = read_private(prep / "password")
    if len(core_password) > 1024 or any(ch in core_password for ch in ":\r\n\x00"):
        raise RuntimeError("selected Core password file contains invalid chpasswd input")
    cert_path = prep / "tls.crt"
    cert_digest = hashlib.sha256(cert_path.read_bytes()).hexdigest()
    if cert_digest != controller.get("ca_sha256"):
        raise RuntimeError("ephemeral Controller TLS certificate differs from the recorded preparation")
    controller_base = "https://127.0.0.1:" + str(port)

    # Check the installed Core candidate before interrupting Controller connectivity.
    identity = {
        "package_version": args.expected_installed_package_version,
        "binary_sha256": args.expected_core_binary_sha256,
        "timeout_seconds": args.timeout_seconds,
        "vm_id": vm["vm_id"],
        "vm_name": vm["name"],
        "storage_root": str(storage_root),
        "resume": preserved_request is not None,
        "operation_id": preserved_request["operation_id"] if preserved_request else "",
    }
    remote_check = CANDIDATE_CHECK
    # Reuse the same SSH identity and lab boundary without sending any credential.
    check_result = ssh_run(lab, args.core, remote_check, identity, failure_path)
    op_id = preserved_request["operation_id"] if preserved_request else "api-s0-" + uuid.uuid4().hex[:20]
    name = preserved_request["snapshot_name"] if preserved_request else "api-s0-" + uuid.uuid4().hex[:12]
    description = preserved_request["description"] if preserved_request else "Disposable Core local API acceptance"
    config = {
        "root_password": core_password, "host_id": ids["host_id"], "pool_id": ids["pool_id"],
        "vm_id": vm["vm_id"], "vm_name": vm["name"], "operation_id": op_id, "name": name,
        "description": description, "timeout_seconds": args.timeout_seconds,
        "resume": preserved_request is not None, "binary_sha256": args.expected_core_binary_sha256,
        "package_version": args.expected_installed_package_version, "storage_root": str(storage_root),
        "marker_vm_id": marker_evidence["vm_id"],
        "markers": [{"target": target, "pattern": item["pattern"], "offset": item["offset"], "length": item["length"]}
                    for target, item in zip(("vda", "vdb"), marker_evidence["current_markers"], strict=True)],
    }
    stopped = None
    restarted = None
    restart_attempted = False
    result = {"status": "incomplete", "core_candidate": check_result, "controller_stop": None,
              "core_result": None, "controller_restart": None, "adoption": None,
              "core_root_relocked": False}
    failure = None
    password_unlocked = False
    try:
        pre_controller = controller_request(controller_base, cert_path, controller_password, "GET", "/api/v1/hosts")
        registered = next((host for host in pre_controller if host.get("id") == ids["host_id"]), None)
        if not registered or registered.get("pool_id") != ids["pool_id"]:
            raise RuntimeError("ephemeral Controller does not contain the selected Core host")
        if not host_reports_managed_vm(registered, vm["vm_id"]):
            raise RuntimeError("selected VM is absent from the Core-reported managed inventory")
        save_private_result(request_evidence, {
            "operation_id": op_id, "vm_id": vm["vm_id"], "snapshot_name": name,
            "description": config["description"], "resume": preserved_request is not None,
            "body": {"operation_id": op_id, "name": name, "description": config["description"]},
        })
        stopped = stop_owned_controller(prep, listen, args.controller_pid)
        result["controller_stop"] = {"pid": stopped["pid"], "start_time": stopped["start_time"], "graceful": True}
        password_state = ssh_command(lab, args.core, "passwd -S root | awk '{print $2}'")
        if password_state != "L":
            raise RuntimeError("selected Core root account was not locked before local API login")
        password_unlocked = True
        ssh_command(lab, args.core, "chpasswd", "root:" + core_password + "\n")
        if ssh_command(lab, args.core, "passwd -S root | awk '{print $2}'") == "L":
            raise RuntimeError("temporary Core root password did not unlock PAM login")
        core_result = ssh_run(lab, args.core, REMOTE, config, failure_path)
        result["core_result"] = core_result
        restart_attempted = True
        restarted = restart_controller(prep, stopped, cert_path, controller_password)
        result["controller_restart"] = {"pid": restarted.pid, "returncode": restarted.poll()}
        if core_result.get("status") != "passed":
            result["status"] = core_result.get("status", "failed")
            raise RuntimeError("Core snapshot request remains " + str(result["status"]))
        adopted = wait_controller_adoption(prep, listen, cert_path, controller_password,
                                           ids["host_id"], ids["pool_id"], op_id, vm["vm_id"], name,
                                           config["description"], 90)
        result["adoption"] = adopted
        result["status"] = "passed"
    except Exception as exc:
        result["error"] = str(exc)
        failure = exc
    finally:
        if stopped and not restart_attempted:
            restart_attempted = True
            try:
                restarted = restart_controller(prep, stopped, cert_path, controller_password)
                result["controller_restart"] = {"pid": restarted.pid, "returncode": restarted.poll(), "recovered_after_error": True}
            except Exception as restart_error:
                result["controller_restart_error"] = str(restart_error)
        if password_unlocked:
            try:
                ssh_command(lab, args.core, "passwd -l root")
                result["core_root_relocked"] = ssh_command(lab, args.core, "passwd -S root | awk '{print $2}'") == "L"
                if not result["core_root_relocked"]:
                    raise RuntimeError("selected Core root account did not relock")
            except Exception as lock_error:
                result["core_root_relock_error"] = str(lock_error)
                failure = failure or lock_error
                result["status"] = "failed"
        save_private_result(evidence, result)
    if failure is not None:
        raise RuntimeError(str(failure)) from failure
    if result["status"] != "passed" or not result["core_root_relocked"]:
        raise RuntimeError("snapshot API acceptance did not complete its safety checks")
    print(json.dumps({"status": result["status"], "steps": ["candidate_verified", "controller_stopped",
        "exact_retry_and_conflict", "core_operation_and_master_catalog", "controller_restarted", "catalog_adopted"]}))
    return 0


def save_private_result(path: Path, result: dict) -> None:
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    fd = os.open(path, flags, 0o600)
    with os.fdopen(fd, "w") as stream:
        json.dump(result, stream, indent=2, sort_keys=True)
        stream.write("\n")


def stage_binary_for_replace(source, installed, expected_sha256, staged=None):
    """Copy and verify a candidate beside its destination before a service stop."""
    source = Path(source)
    installed = Path(installed)
    staged = Path(staged) if staged is not None else installed.with_name("." + installed.name + ".staged")
    if staged.parent != installed.parent:
        raise RuntimeError("staged binary must be beside its destination")
    if source.is_symlink() or not source.is_file():
        raise RuntimeError("binary candidate is not a regular file")
    if hashlib.sha256(source.read_bytes()).hexdigest() != expected_sha256:
        raise RuntimeError("binary candidate checksum does not match")
    if staged.exists() or staged.is_symlink():
        info = staged.lstat()
        if (not staged.is_file() or staged.is_symlink() or info.st_mode & 0o777 != 0o755 or
                info.st_uid != os.geteuid() or hashlib.sha256(staged.read_bytes()).hexdigest() != expected_sha256):
            raise RuntimeError("existing staged binary does not match the candidate")
        return staged
    fd = os.open(staged, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o700)
    with os.fdopen(fd, "wb") as output, source.open("rb") as candidate:
        while chunk := candidate.read(1024 * 1024):
            output.write(chunk)
        output.flush()
        os.fchmod(output.fileno(), 0o755)
        os.fsync(output.fileno())
    info = staged.stat()
    if (info.st_mode & 0o777 != 0o755 or info.st_uid != os.geteuid() or
            hashlib.sha256(staged.read_bytes()).hexdigest() != expected_sha256):
        raise RuntimeError("staged binary failed mode or checksum verification")
    return staged


def replace_staged_binary(staged, installed, expected_sha256, expected_old_sha256):
    """Atomically install a previously staged candidate after the service stops."""
    staged = Path(staged)
    installed = Path(installed)
    if staged.parent != installed.parent or staged.is_symlink() or not staged.is_file():
        raise RuntimeError("staged binary is not a regular sibling of the destination")
    if (staged.stat().st_mode & 0o777 != 0o755 or
            hashlib.sha256(staged.read_bytes()).hexdigest() != expected_sha256):
        raise RuntimeError("staged binary changed before replacement")
    if installed.is_symlink() or not installed.is_file():
        raise RuntimeError("installed binary is not a regular file")
    if hashlib.sha256(installed.read_bytes()).hexdigest() != expected_old_sha256:
        raise RuntimeError("installed binary changed before replacement")
    os.replace(staged, installed)
    directory_fd = os.open(installed.parent, os.O_RDONLY)
    try:
        os.fsync(directory_fd)
    finally:
        os.close(directory_fd)
    if (installed.stat().st_mode & 0o777 != 0o755 or
            hashlib.sha256(installed.read_bytes()).hexdigest() != expected_sha256):
        raise RuntimeError("installed binary failed mode or checksum verification")
    return installed


def append_private(path: Path, value: dict) -> None:
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
    os.fchmod(fd, 0o600)
    with os.fdopen(fd, "a") as stream:
        json.dump(value, stream, sort_keys=True)
        stream.write("\n")


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(f"snapshot API acceptance failed: {exc}", file=sys.stderr)
        raise SystemExit(1)
