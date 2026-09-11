#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Check one existing disposable storage repository through the Controller API.

This runner never creates a lab, repository, VM, export, share, or target. ``inspect`` is
read-only. ``recheck`` and the bounded ``nfs-outage`` fixture require both an explicitly
selected disposable lab and ``--execute``. The outage always restores a service it stopped.
"""
import argparse
from datetime import datetime
import ipaddress
import json
from pathlib import Path
import shlex
import sys
import time
import uuid


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))
from storage_repositories_guest import API  # noqa: E402
from storage_repositories_lab_boot import check_lab, ssh  # noqa: E402


def operation(api, operation_id, timeout=120):
    until = time.monotonic() + timeout
    while time.monotonic() < until:
        for item in api.request("GET", "/api/v1/operations"):
            if item["id"] == operation_id and item["status"] not in ("queued", "dispatched", "running"):
                return item
        time.sleep(1)
    raise RuntimeError(f"operation {operation_id} did not settle within {timeout}s")


def repository(api, repository_id):
    rows = api.request("GET", "/api/v1/storage")
    return next((row for row in rows if row["id"] == repository_id), None)


def save_result(path, result):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(result, indent=2) + "\n")


def journal_result(lab, operation_id):
    root = "/var/lib/nex-storage-lab/managed-vms"
    found = ssh(lab, f"grep -l -- {shlex.quote(operation_id)} {root}/*.json 2>/dev/null | tail -1", check=False).stdout.strip()
    if not found:
        return {"found": False}
    program = (
        "import json,os,sys; j=json.load(open(sys.argv[1])); r=j.get('result',{}); "
        "print(json.dumps({'found':True,'file':os.path.basename(sys.argv[1]),'operation_id':r.get('operation_id'),"
        "'status':r.get('status'),'error':r.get('error'),'observed_at':r.get('observed_at')}))"
    )
    raw = ssh(lab, "python3 -c " + shlex.quote(program) + " " + shlex.quote(found), check=False).stdout
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return {"found": False, "read_error": "managed journal did not return a safe result summary"}


def core_now(lab):
    raw = ssh(lab, "date --iso-8601=ns", check=False).stdout.strip()
    try:
        return datetime.fromisoformat(raw.replace("Z", "+00:00"))
    except ValueError:
        raise RuntimeError(f"Core did not return a usable UTC timestamp: {raw!r}") from None


def validate_journal(journal, terminal, after_restore):
    if not journal.get("found") or journal.get("operation_id") != terminal["id"] or journal.get("status") != terminal["status"]:
        raise RuntimeError(f"managed journal does not match the terminal Core task: {journal}")
    try:
        observed = datetime.fromisoformat(journal["observed_at"].replace("Z", "+00:00"))
    except (AttributeError, ValueError):
        raise RuntimeError(f"managed journal has no usable observation time: {journal}") from None
    if observed < after_restore:
        raise RuntimeError(f"managed journal did not settle after the recorded recovery: {journal}")


def validate_down_journal(journal, terminal, stopped_at, restore_started):
    if not journal.get("found") or journal.get("operation_id") != terminal["id"] or journal.get("status") != terminal["status"]:
        raise RuntimeError(f"managed journal does not match the terminal down-state task: {journal}")
    try:
        observed = datetime.fromisoformat(journal["observed_at"].replace("Z", "+00:00"))
    except (AttributeError, ValueError):
        raise RuntimeError(f"managed journal has no usable down-state observation time: {journal}") from None
    if not stopped_at <= observed <= restore_started:
        raise RuntimeError(f"managed journal observation escaped the outage interval: {journal}")


def refreshed_host(api, host_id, last_seen, timeout=30):
    until = time.monotonic() + timeout
    while time.monotonic() < until:
        host = next((item for item in api.request("GET", "/api/v1/hosts") if item["id"] == host_id), None)
        if host and host.get("last_seen") and host.get("last_seen") != last_seen:
            return host
        time.sleep(1)
    raise RuntimeError("agent heartbeat did not advance during the bounded outage")


def healthy_access(api, repository_id, timeout=30):
    until = time.monotonic() + timeout
    while time.monotonic() < until:
        row = repository(api, repository_id)
        access = row.get("access", {}) if row else {}
        if access.get("status") == "healthy" and access.get("readable") and access.get("writable") and access.get("observed_at"):
            return access
        time.sleep(1)
    raise RuntimeError("repository did not report a verified healthy, writable access observation")


def validate_task(op, host_id, request_id, phase):
    if op.get("host_id") != host_id or op.get("kind") != "storage-recheck" or op.get("request_id") != request_id:
        raise RuntimeError(f"{phase} recheck task lost its Core identity: {op}")


def recheck(api, host, repo, request_id, want_success):
    reply, queued = enqueue_recheck(api, host, repo, request_id)
    op = operation(api, queued["id"])
    validate_task(op, host["id"], request_id, "terminal")
    if want_success and op["status"] != "succeeded":
        raise RuntimeError(f"recheck failed: {op.get('error', op)}")
    if not want_success and op["status"] == "succeeded":
        raise RuntimeError("recheck succeeded while the selected NFS service was stopped")
    return {"enqueue": reply, "terminal": op}


def enqueue_recheck(api, host, repo, request_id):
    body = {"request_id": request_id, "pool_id": host["pool_id"], "host_id": host["id"], "expected_generation": repo["generation"]}
    reply = api.request("POST", f"/api/v1/storage/{repo['id']}/recheck", body)
    queued = reply.get("operation") if isinstance(reply, dict) else None
    if not isinstance(queued, dict) or not isinstance(queued.get("id"), str):
        raise RuntimeError(f"recheck enqueue response has no operation ID: {reply!r}")
    validate_task(queued, host["id"], request_id, "enqueue")
    return reply, queued


def observed_operation(api, operation_id, seconds=20):
    until, latest = time.monotonic() + seconds, None
    while time.monotonic() < until:
        latest = next((item for item in api.request("GET", "/api/v1/operations") if item["id"] == operation_id), None)
        if latest and latest["status"] not in ("queued", "dispatched", "running"):
            return latest
        time.sleep(1)
    if not latest:
        raise RuntimeError("queued recheck disappeared during the bounded outage observation")
    return latest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lab-dir", type=Path, required=True, help="explicitly disposable nex-storage-repositories-lab.* directory")
    parser.add_argument("--ids", type=Path, required=True, help="explicit disposable-fixture JSON containing storage and optional host IDs")
    parser.add_argument("--stage", choices=("inspect", "heartbeat", "recheck", "nfs-outage"), required=True)
    parser.add_argument("--controller", default="https://127.0.0.1:44443")
    parser.add_argument("--password-file", type=Path, default=ROOT / ".cache/storage-lab/runtime/password")
    parser.add_argument("--nfs-service", default="nfs-server", help="fixture service; used only by nfs-outage")
    parser.add_argument("--nfs-server", default="127.0.0.1", help="selected Core-local NFS server; used only by nfs-outage")
    parser.add_argument("--nfs-export", help="exact disposable NFS export; required by nfs-outage")
    parser.add_argument("--result", type=Path, default=ROOT / ".cache/storage-native/result.json")
    parser.add_argument("--execute", action="store_true", help="permit a Core recheck or the bounded NFS fixture outage")
    args = parser.parse_args()
    lab, ids = check_lab(args.lab_dir), json.loads(args.ids.read_text())
    if args.stage not in ("inspect", "heartbeat") and not args.execute:
        raise SystemExit("--execute is required for recheck and nfs-outage")
    api = API(args.controller, args.password_file.read_text().strip())
    hosts = api.request("GET", "/api/v1/hosts")
    host = next((item for item in hosts if item["id"] == ids.get("host")), hosts[0] if len(hosts) == 1 else None)
    if not host:
        raise RuntimeError("selected host is absent or ids must name it")
    repo = repository(api, ids["storage"])
    if not repo or repo["pool_id"] != host["pool_id"] or repo["host_id"] != host["id"]:
        raise RuntimeError("selected repository is not adopted by the selected host")
    before = {key: repo.get(key) for key in ("id", "pool_id", "host_id", "kind", "source", "identity", "generation")}
    run_id = uuid.uuid4().hex
    result = {"stage": args.stage, "run_id": run_id, "repository_before": before, "completed": False}
    if args.stage == "heartbeat":
        heartbeat = refreshed_host(api, host["id"], host.get("last_seen"), timeout=10)
        result["heartbeat"] = {"last_seen": heartbeat.get("last_seen"), "connection": heartbeat.get("connection")}
    elif args.stage == "recheck":
        result["recheck"] = recheck(api, host, repo, "native-recheck-" + run_id, True)
    elif args.stage == "nfs-outage":
        if repo.get("kind") != "nfs":
            raise RuntimeError("nfs-outage requires an NFS repository")
        source = repo.get("source", {}).get("nfs", {})
        outage_error = None
        recovery_error = None
        try:
            loopback = ipaddress.ip_address(source.get("server", "")).is_loopback
        except ValueError:
            loopback = False
        if not args.nfs_export or not loopback or source.get("server") != args.nfs_server or source.get("export") != args.nfs_export:
            raise RuntimeError("nfs-outage requires the explicitly selected loopback fixture server and export")
        service = shlex.quote(args.nfs_service)
        initial = ssh(lab, f"systemctl is-active {service}", check=False).stdout.strip()
        if initial != "active":
            raise RuntimeError(f"fixture service is not active: {initial}")
        watchdog = "nex-storage-acceptance-restore-" + repo["id"]
        result["outage_before"] = {"host_last_seen": host.get("last_seen"), "service": initial, "source": source}
        baseline = refreshed_host(api, host["id"], host.get("last_seen"), timeout=10)
        result["outage_baseline"] = {"last_seen": baseline.get("last_seen"), "connection": baseline.get("connection")}
        result["outage_access_baseline"] = healthy_access(api, repo["id"])
        host = baseline
        try:
            # This recovery is owned by the disposable lab's systemd, so it still runs if the
            # client dies during the deliberately bounded outage.
            arm = f"systemd-run --collect --unit={shlex.quote(watchdog)} --on-active=180s /usr/bin/systemctl start {service}"
            armed = ssh(lab, arm, check=False)
            result["outage_watchdog"] = {"unit": watchdog, "delay_seconds": 180, "armed": armed.returncode == 0}
            if armed.returncode:
                raise RuntimeError("could not arm the disposable NFS fixture recovery watchdog")
            if ssh(lab, f"systemctl stop {service}", check=False).returncode:
                raise RuntimeError("could not stop the selected disposable NFS fixture service")
            stopped = ssh(lab, f"systemctl is-active {service}", check=False).stdout.strip()
            result["outage_service_after_stop"] = stopped
            if stopped == "active":
                raise RuntimeError("selected NFS fixture service remained active")
            stopped_at = core_now(lab)
            result["outage_stopped_at"] = stopped_at.isoformat()
            down_baseline = next((item for item in api.request("GET", "/api/v1/hosts") if item["id"] == host["id"]), None)
            if not down_baseline:
                raise RuntimeError("host disappeared while the selected NFS fixture was down")
            result["outage_down_baseline"] = {"last_seen": down_baseline.get("last_seen"), "connection": down_baseline.get("connection")}
            heartbeat = refreshed_host(api, host["id"], down_baseline.get("last_seen"), timeout=90)
            result["outage_heartbeat"] = heartbeat.get("last_seen")
            if ssh(lab, f"systemctl is-active {service}", check=False).stdout.strip() == "active":
                raise RuntimeError("NFS fixture recovered before the required down-heartbeat")
            outage_request_id = "native-outage-" + run_id
            result["outage_request_id"] = outage_request_id
            reply, queued = enqueue_recheck(api, host, repo, outage_request_id)
            result["outage_recheck_enqueue"] = reply
            result["outage_recheck_during_outage"] = observed_operation(api, queued["id"])
            validate_task(result["outage_recheck_during_outage"], host["id"], outage_request_id, "outage")
            if result["outage_recheck_during_outage"]["status"] == "succeeded":
                raise RuntimeError("recheck reported success while the selected NFS fixture was down")
            down_repo = repository(api, repo["id"])
            result["outage_access"] = down_repo.get("access", {}) if down_repo else {}
            if result["outage_access"].get("status") == "healthy" and result["outage_access"].get("writable"):
                raise RuntimeError("repository remained healthy and writable during the bounded outage")
        except Exception as exc:
            outage_error = exc
        finally:
            result["outage_restore_started_at"] = core_now(lab).isoformat()
            restored = ssh(lab, f"systemctl start {service}", check=False)
            if restored.returncode:
                result["outage_recovery_error"] = "NFS fixture recovery command failed"
                recovery_error = RuntimeError(result["outage_recovery_error"])
            active = ssh(lab, f"systemctl is-active {service}", check=False).stdout.strip()
            result["outage_service_after_restore"] = active
            if active != "active":
                result["outage_recovery_error"] = f"NFS fixture did not return active: {active}"
                recovery_error = RuntimeError(result["outage_recovery_error"])
            else:
                result["outage_restored_at"] = core_now(lab).isoformat()
            cancelled = ssh(lab, f"systemctl stop {shlex.quote(watchdog)}.timer", check=False)
            result["outage_watchdog_cancelled"] = cancelled.returncode == 0
            if cancelled.returncode:
                recovery_error = RuntimeError("NFS fixture watchdog cleanup failed")
        if outage_error or recovery_error:
            result["error"] = str(outage_error or recovery_error)
            save_result(args.result, result)
            raise outage_error or recovery_error
        original = operation(api, result["outage_recheck_enqueue"]["operation"]["id"])
        result["outage_recheck_after_restore"] = original
        validate_task(original, host["id"], result["outage_request_id"], "recovered")
        result["outage_journal"] = journal_result(lab, original["id"])
        down = result["outage_recheck_during_outage"]
        if down["status"] in ("failed", "needs_attention"):
            if original["status"] != down["status"]:
                raise RuntimeError(f"terminal down-state task changed after recovery: {original}")
            result["outage_outcome"] = "terminal while down; recovery uses a new recheck"
            validate_down_journal(result["outage_journal"], original, stopped_at, datetime.fromisoformat(result["outage_restore_started_at"]))
        else:
            if original["status"] != "succeeded":
                raise RuntimeError(f"pending recheck did not recover after NFS restoration: {original}")
            result["outage_outcome"] = "pending while down; original recheck settled after recovery"
            validate_journal(result["outage_journal"], original, datetime.fromisoformat(result["outage_restored_at"]))
        result["recovery_recheck"] = recheck(api, host, repository(api, repo["id"]), "native-recovery-" + run_id, True)
        recovered_access = healthy_access(api, repo["id"])
        result["outage_access_after_recovery"] = recovered_access
        try:
            baseline_seen = datetime.fromisoformat(result["outage_access_baseline"]["observed_at"].replace("Z", "+00:00"))
            recovered_seen = datetime.fromisoformat(recovered_access["observed_at"].replace("Z", "+00:00"))
        except (AttributeError, ValueError):
            raise RuntimeError("healthy access observations have no usable timestamps") from None
        if recovered_seen <= baseline_seen:
            raise RuntimeError("repository did not record a newer healthy access observation after recovery")
    after = repository(api, repo["id"])
    if not after:
        raise RuntimeError("repository disappeared during acceptance check")
    for key, value in before.items():
        if after.get(key) != value:
            raise RuntimeError(f"repository {key} changed: {value!r} -> {after.get(key)!r}")
    result["repository_after"] = {key: after.get(key) for key in before}
    result["completed"] = True
    save_result(args.result, result)
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
