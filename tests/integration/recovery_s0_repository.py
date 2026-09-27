#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Check NFS repository primitives between two explicitly disposable Core guests.

This is a native feasibility experiment, not Nex repository or restore-pin code.
Requires the recovery S0 lab and its two prepared NFS mount aliases on Core A/C.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
from pathlib import Path
import selectors
import shlex
import subprocess
import uuid


MOUNT = "/mnt/recovery-s0-nfs"
ALIAS = "/mnt/recovery-s0-nfs-alias"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lab", required=True, type=Path)
    args = parser.parse_args()
    cache = Path(__file__).resolve().parents[2] / ".cache"
    lab = args.lab.resolve(strict=True)
    if lab.parent != cache.resolve() or not lab.name.startswith("recovery-s0-core."):
        parser.error("select the newly created recovery S0 lab under repository .cache")
    if json.loads((lab / "lab.json").read_text()) != {"disposable": True, "fixture": "cluster-ha-v1"}:
        parser.error("disposable fixture marker does not match")
    ports = json.loads((lab / "ports.json").read_text())
    directory = "repository-probe-" + uuid.uuid4().hex
    evidence = lab / (directory + ".json")

    def command(host, code, *values):
        return ["ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
                "-o", "StrictHostKeyChecking=yes", "-o", f"UserKnownHostsFile={lab / 'known_hosts'}",
                "-i", str(lab / "id_ed25519"), "-p", str(ports[host]), "root@127.0.0.1",
                "python3 -c " + shlex.quote(code) + " " + shlex.join(values)]

    def remote(host, code, *values):
        result = subprocess.run(command(host, code, *values), check=True, capture_output=True,
                                text=True, timeout=30)
        return result.stdout.strip()

    mounts = {}
    for host in ("core1", "core3"):
        mounts[host] = json.loads(remote(host, """import json, subprocess, sys
mounts = []
for path in sys.argv[1:]:
    data = json.loads(subprocess.check_output(
        ['findmnt', '-J', '-M', path, '-o', 'TARGET,SOURCE,FSTYPE,OPTIONS'], text=True))
    mount, = data['filesystems']
    assert mount['target'] == path and mount['fstype'] == 'nfs4', 'exact mount must be NFSv4 before any write'
    assert 'vers=4.2' in mount['options'].split(','), 'this experiment requires NFSv4.2'
    mounts.append(mount)
assert mounts[0]['source'] == mounts[1]['source'], 'aliases must select the same lab export'
print(json.dumps(mounts))
""", MOUNT, ALIAS))
    assert mounts['core1'][0]['source'] == mounts['core3'][0]['source'], 'clients must select the same lab export'

    root = MOUNT + "/" + directory
    alias = ALIAS + "/" + directory
    remote("core1", "import os, sys; os.mkdir(sys.argv[1], 0o777); os.chmod(sys.argv[1], 0o777)", root)
    report = {"scope": "NFS filesystem primitives only; no Core recovery API or fencing proof",
              "status": "failed", "mounts": mounts, "scratch": [root, alias], "checks": {}}
    try:
        claim = """import json, os, sys
path, owner = sys.argv[1:]
try:
    fd = os.open(path + '/owner.json', os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o644)
except FileExistsError:
    print('existing')
else:
    with os.fdopen(fd, 'w') as f:
        json.dump({'repository': 'repository-a', 'pool': owner}, f)
        f.flush(); os.fsync(f.fileno())
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try: os.fsync(fd)
    finally: os.close(fd)
    print('created')
"""
        with ThreadPoolExecutor(max_workers=2) as executor:
            futures = [executor.submit(remote, host, claim, path, owner) for host, path, owner in
                       (("core1", root, "pool-a"), ("core3", alias, "pool-b"))]
            outcomes = [future.result() for future in futures]
        assert sorted(outcomes) == ["created", "existing"], outcomes
        read_owner = "import pathlib, sys; print((pathlib.Path(sys.argv[1]) / 'owner.json').read_text())"
        owners = [json.loads(remote(host, read_owner, path)) for host, path in
                  (("core1", root), ("core1", alias), ("core3", root), ("core3", alias))]
        assert all(owner == owners[0] for owner in owners)
        assert owners[0] == {"repository": "repository-a", "pool": "pool-a" if outcomes[0] == "created" else "pool-b"}
        report["checks"]["exclusive_claim_and_alias_identity"] = "passed"

        hold = """import fcntl, select, sys
with open(sys.argv[1] + '/coordination.lock', 'a') as f:
    fcntl.flock(f, fcntl.LOCK_EX)
    print('held', flush=True)
    assert select.select([sys.stdin], [], [], 20)[0], 'lab lock release timed out'
    assert sys.stdin.readline().strip() == 'release'
"""
        attempt = """import fcntl, sys
with open(sys.argv[1] + '/coordination.lock', 'a') as f:
    try: fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError: print('blocked')
    else: print('acquired')
"""
        holder = subprocess.Popen(command("core1", hold, root), stdin=subprocess.PIPE,
                                  stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(holder.stdout, selectors.EVENT_READ)
                assert selector.select(10), "remote lock holder did not become ready"
            assert holder.stdout.readline().strip() == "held"
            assert remote("core3", attempt, alias) == "blocked"
            stdout, stderr = holder.communicate("release\n", timeout=10)
            assert holder.returncode == 0, stderr
        finally:
            if holder.poll() is None:
                holder.kill()
                holder.communicate(timeout=10)
        assert remote("core3", attempt, alias) == "acquired"
        assert json.loads(remote("core3", read_owner, alias)) == owners[0]
        report["checks"]["cross_host_lock_and_persistent_owner"] = "passed"

        publish = """import os, pathlib, sys
root = pathlib.Path(sys.argv[1]); staging = root / 'staging'
staging.mkdir()
with (staging / 'manifest.json').open('x') as f:
    f.write('{"complete": true}'); f.flush(); os.fsync(f.fileno())
for directory in (staging, root):
    fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
    try: os.fsync(fd)
    finally: os.close(fd)
os.rename(staging, root / 'point')
fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY)
try: os.fsync(fd)
finally: os.close(fd)
"""
        remote("core1", publish, root)
        remote("core3", """import json, pathlib, sys
root = pathlib.Path(sys.argv[1])
assert not (root / 'staging').exists()
assert json.loads((root / 'point/manifest.json').read_text()) == {'complete': True}
""", alias)
        report["checks"]["synced_directory_publication_visible_to_peer"] = "passed"
        report["status"] = "passed"
    finally:
        evidence.write_text(json.dumps(report, indent=2) + "\n")
        print(evidence)
    print("NFS primitive checks passed; crash, partition, fencing and durable restore pins remain untested")


if __name__ == "__main__":
    main()
