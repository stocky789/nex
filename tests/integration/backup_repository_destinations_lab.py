#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Smoke-check private backup destination services in a selected cluster-ha-v1 fixture.

This checks storage services only. It does not exercise Nex backup or restore.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import secrets
import shlex
import signal
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[2]
CACHE = ROOT / ".cache"
FIXTURE = {"disposable": True, "fixture": "cluster-ha-v1"}
SERVER = "198.18.90.10"
MINIO_SOURCE = "github.com/minio/minio@RELEASE.2025-10-15T17-29-55Z"
MINIO_SHA256 = "79e4efe5196c57938d76ebf4587eeaf8f009565451e352ec10ec6bdd4b21bbb6"
MC_SOURCE = "github.com/minio/mc@RELEASE.2025-08-13T08-35-41Z"
MC_SHA256 = "d0082b7ef1cffbb105b64ac0d506afc5417fa0575484c7fb83b72ad98e70c61c"
MINIO_POLICY = {"Version": "2012-10-17", "Statement": [
    {"Effect": "Allow", "Action": ["s3:ListBucket", "s3:ListBucketMultipartUploads"],
     "Resource": ["arn:aws:s3:::nex-backup-smoke"]},
    {"Effect": "Allow", "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject",
                                    "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"],
     "Resource": ["arn:aws:s3:::nex-backup-smoke/*"]}]}
sys.path.insert(0, str(ROOT))
from tests.integration import cluster_ha_lab as fixture


def selected_lab(path):
    if path.is_symlink():
        raise ValueError("lab path is a symlink")
    lab = path.resolve(strict=True)
    if lab.parent != CACHE.resolve() or not lab.name.startswith("nex-cluster-ha-lab."):
        raise ValueError("select a cluster-ha-v1 lab directly under this worktree's .cache")
    if json.loads((lab / "lab.json").read_text()) != FIXTURE or not (lab / "ready.json").is_file():
        raise ValueError("selected disposable lab is not ready")
    return lab


def owned_guests(lab):
    lab = selected_lab(lab)
    ports = json.loads((lab / "ports.json").read_text())
    for name in ("storage", "core1", "core2"):
        command = json.loads((lab / f"{name}-command.json").read_text())
        metadata = json.loads((lab / f"{name}-qemu.process.json").read_text())
        if (not command or command[0] != "qemu-system-x86_64" or
                metadata.get("command") != command or
                f"file={lab / (name + '.qcow2')},format=qcow2,if=virtio" not in command or
                f"unix:{lab / (name + '-qmp.sock')},server=on,wait=off" not in command or
                not any(command[index] == "-netdev" and command[index + 1].startswith("user,id=nat,") and
                        f"hostfwd=tcp:127.0.0.1:{ports[name]}-:22" in command[index + 1].split(",")
                        for index in range(len(command) - 1)) or
                (lab / f"{name}.qcow2").is_symlink() or
                not fixture.owned_process(metadata)):
            raise RuntimeError(f"{name} is not the selected fixture's current QEMU process")
    return ports


def remote(lab, host, script, stdin=""):
    if host not in ("storage", "core1", "core2"):
        raise ValueError("unexpected fixture guest")
    ports = owned_guests(lab)
    command = ["ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
               "-o", "StrictHostKeyChecking=yes", "-o", f"UserKnownHostsFile={lab / 'known_hosts'}",
               "-i", str(lab / "id_ed25519"), "-p", str(ports[host]), "root@127.0.0.1",
               "sh -ec " + shlex.quote(script)]
    result = subprocess.run(command, input=stdin, text=True, capture_output=True, timeout=120)
    if result.returncode:
        raise RuntimeError(f"{host} smoke command failed ({result.returncode}): {result.stderr[-800:]}")
    return result.stdout.strip()


def require_mount(source, kind):
    expected = (f"{SERVER}:/srv/nex-backup-smoke/nfs nfs4" if kind == "nfs"
                else f"//{SERVER}/nexbackupsmoke cifs")
    if source != expected:
        raise RuntimeError(f"{kind} mount source or filesystem differs from selected fixture")


def kill_owned_storage_now(lab):
    """Immediately stop only the selected fixture's recorded storage QEMU process."""
    lab = selected_lab(lab)
    owned_guests(lab)
    command = json.loads((lab / "storage-command.json").read_text())
    metadata = json.loads((lab / "storage-qemu.process.json").read_text())
    if (not command or command[0] != "qemu-system-x86_64" or
            metadata.get("command") != command or
            f"file={lab / 'storage.qcow2'},format=qcow2,if=virtio" not in command or
            f"unix:{lab / 'storage-qmp.sock'},server=on,wait=off" not in command or
            not fixture.owned_process(metadata)):
        raise RuntimeError("refusing to signal a foreign or changed storage QEMU process")
    fd = os.pidfd_open(metadata["pid"])
    try:
        if not fixture.owned_process(metadata):
            raise RuntimeError("storage QEMU identity changed before signal")
        signal.pidfd_send_signal(fd, signal.SIGKILL)
    finally:
        os.close(fd)
    for _ in range(50):
        if not fixture.process_exists(metadata["pid"]):
            return
        time.sleep(.1)
    raise RuntimeError("storage QEMU did not exit after immediate SIGKILL")


def run(lab):
    report = {"scope": "disposable NFS/SMB service smoke only; no Nex backup or restore",
              "fixture": lab.name, "status": "failed", "checks": {}}
    result_path = lab / "backup-destination-service-smoke.json"
    if result_path.exists():
        raise ValueError("service smoke already ran in this fixture")
    password = secrets.token_urlsafe(32)
    try:
        report["storage_versions"] = remote(lab, "storage",
            "dpkg-query -W -f='${Package} ${Version}\\n' nfs-kernel-server samba; "
            "sha256sum /usr/sbin/smbd /usr/sbin/rpc.nfsd")
        remote(lab, "storage", """set -eu
test ! -e /srv/nex-backup-smoke
test ! -e /etc/exports.d/nex-backup-smoke.exports
! grep -q '^\\[nexbackupsmoke\\]' /etc/samba/smb.conf
install -d -m 0755 /srv/nex-backup-smoke /etc/exports.d
install -d -o 64055 -g 64055 -m 0770 /srv/nex-backup-smoke/nfs
id nexbackupsmoke >/dev/null 2>&1 || useradd -r -M -s /usr/sbin/nologin nexbackupsmoke
install -d -o nexbackupsmoke -g nexbackupsmoke -m 0770 /srv/nex-backup-smoke/smb
printf '%s\\n' '/srv/nex-backup-smoke/nfs 198.18.90.0/24(rw,sync,no_subtree_check,root_squash,anonuid=64055,anongid=64055)' > /etc/exports.d/nex-backup-smoke.exports
exportfs -ra
systemctl enable --now nfs-server
read -r password
printf '%s\\n%s\\n' "$password" "$password" | smbpasswd -a -s nexbackupsmoke >/dev/null
printf '\\n[nexbackupsmoke]\\n path = /srv/nex-backup-smoke/smb\\n read only = no\\n valid users = nexbackupsmoke\\n' >> /etc/samba/smb.conf
systemctl enable --now smbd
systemctl restart smbd
systemctl is-active nfs-server smbd
""", password + "\n")
        report["checks"]["services_active"] = "passed"
        for host in ("core1", "core2"):
            remote(lab, host, f"""set -eu
test ! -e /mnt/nex-backup-smoke
install -d -m 0755 /mnt/nex-backup-smoke/nfs /mnt/nex-backup-smoke/smb
mount -t nfs4 -o rw,vers=4.2,hard,proto=tcp {SERVER}:/srv/nex-backup-smoke/nfs /mnt/nex-backup-smoke/nfs
read -r password
umask 077
printf 'username=nexbackupsmoke\\npassword=%s\\n' "$password" > /root/.nex-backup-smoke-smb
mount -t cifs //{SERVER}/nexbackupsmoke /mnt/nex-backup-smoke/smb -o credentials=/root/.nex-backup-smoke-smb,vers=3.1.1,seal
findmnt -n -M /mnt/nex-backup-smoke/nfs -o FSTYPE
findmnt -n -M /mnt/nex-backup-smoke/smb -o FSTYPE
""", password + "\n")
        report["checks"]["two_client_mounts"] = "passed"
        for kind in ("nfs", "smb"):
            path = f"/mnt/nex-backup-smoke/{kind}"
            for host in ("core1", "core2"):
                source = remote(lab, host, f"findmnt -n -M {path} -o SOURCE,FSTYPE")
                require_mount(source, kind)
                report["checks"][f"{kind}_{host}_source"] = source
            remote(lab, "core1", f"printf %s backup-smoke > {path}/marker; mv {path}/marker {path}/published")
            if remote(lab, "core2", f"cat {path}/published") != "backup-smoke":
                raise RuntimeError(f"{kind} cross-host publication read failed")
            report["checks"][f"{kind}_write_rename_read"] = "passed"
        report["status"] = "passed"
    finally:
        result_path.write_text(json.dumps(report, indent=2) + "\n")
    return result_path


def run_transfer_services(lab):
    """Exercise the fixture's confined SFTP service with a pinned host key."""
    report_path = lab / "backup-destination-sftp-smoke.json"
    if report_path.exists():
        raise ValueError("SFTP smoke already ran in this fixture")
    report = {"scope": "disposable SFTP service smoke; no Nex backup or restore",
              "fixture": lab.name, "status": "failed", "checks": {}}
    key = lab / "backup-sftp-id_ed25519"
    try:
        if key.exists():
            raise ValueError("private SFTP key already exists in selected fixture")
        subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "backup-smoke",
                        "-f", str(key)], check=True, timeout=30)
        remote(lab, "storage", """set -eu
test -d /srv/nex-backup-smoke
test ! -e /srv/nex-backup-smoke/sftp
install -d -m 0755 /srv/nex-backup-smoke/sftp
useradd -M -d /data -s /usr/sbin/nologin nexsftp
install -d -o nexsftp -g nexsftp -m 0700 /srv/nex-backup-smoke/sftp/data
install -d -m 0755 /etc/ssh/nex-backup-authorized
read -r key
printf '%s\\n' "$key" > /etc/ssh/nex-backup-authorized/nexsftp
chmod 0644 /etc/ssh/nex-backup-authorized/nexsftp
""", key.with_suffix(".pub").read_text())
        remote(lab, "storage", "chpasswd", f"nexsftp:{secrets.token_urlsafe(32)}\n")
        host_key = remote(lab, "storage", "cat /etc/ssh/ssh_host_ed25519_key.pub")
        if not host_key.startswith("ssh-ed25519 "):
            raise RuntimeError("storage guest has no Ed25519 host key")
        known_hosts = f"[{SERVER}]:2222 {host_key}\n"
        report["sftp_host_key_sha256"] = remote(lab, "storage",
            "ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub -E sha256 | awk '{print $2}'")
        remote(lab, "storage", """set -eu
test ! -e /etc/ssh/nex-backup-sftp.conf
install -d -m 0755 /run/sshd
cat > /etc/ssh/nex-backup-sftp.conf <<'EOF'
Port 2222
ListenAddress 198.18.90.10
HostKey /etc/ssh/ssh_host_ed25519_key
PidFile /run/nex-backup-sftp.pid
AuthorizedKeysFile /etc/ssh/nex-backup-authorized/%u
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
AllowUsers nexsftp
ChrootDirectory /srv/nex-backup-smoke/sftp
ForceCommand internal-sftp -d /data
Subsystem sftp internal-sftp
EOF
/usr/sbin/sshd -t -f /etc/ssh/nex-backup-sftp.conf
/usr/sbin/sshd -f /etc/ssh/nex-backup-sftp.conf
""")
        for host in ("core1", "core2"):
            remote(lab, host, "umask 077; cat > /root/nex-backup-sftp-id", key.read_text())
            remote(lab, host, "umask 077; cat > /root/nex-backup-sftp-known-hosts", known_hosts)
        options = ("-q -b - -P 2222 -i /root/nex-backup-sftp-id -o BatchMode=yes "
                   "-o StrictHostKeyChecking=yes -o UserKnownHostsFile=/root/nex-backup-sftp-known-hosts "
                   f"nexsftp@{SERVER}")
        remote(lab, "core1", f"""set -eu
printf %s backup-smoke > /root/nex-backup-sftp-marker
sftp {options} <<'EOF'
put /root/nex-backup-sftp-marker /data/marker
rename /data/marker /data/published
EOF
""")
        remote(lab, "core2", f"""set -eu
sftp {options} <<'EOF'
get /data/published /root/nex-backup-sftp-received
EOF
test "$(cat /root/nex-backup-sftp-received)" = backup-smoke
""")
        report["checks"]["pinned_upload_rename_cross_client_download"] = "passed"
        report["storage_versions"] = remote(lab, "storage",
            "dpkg-query -W -f='${Package} ${Version}\\n' openssh-server openssh-sftp-server; sha256sum /usr/sbin/sshd")
        report["status"] = "passed"
    finally:
        report_path.write_text(json.dumps(report, indent=2) + "\n")
    return report_path


def run_minio_multipart(lab, key="multipart"):
    report_path = lab / "backup-destination-minio-multipart.json"
    port = 5900
    config_path = "/root/nex-backup-minio-curl.conf"
    if report_path.exists():
        raise ValueError("S3 multipart smoke already ran in this fixture")
    report = {"scope": "disposable MinIO 5 MiB+1 conditional multipart service smoke",
              "fixture": lab.name, "object_key": key, "status": "failed", "checks": {}}
    probe = f"""import hashlib, pathlib, subprocess, urllib.parse, xml.etree.ElementTree as ET
base = 'https://{SERVER}:{port}/nex-backup-smoke/{key}'
config = '{config_path}'
body = pathlib.Path('/root/nex-backup-multipart-body')
headers = pathlib.Path('/root/nex-backup-multipart-headers')
payload = pathlib.Path('/root/nex-backup-multipart-payload')
def call(method, url, data=None, extra=()):
    args = ['curl', '--config', config, '-X', method, '-D', str(headers), '-o', str(body),
            '-w', '%{{http_code}}', *extra]
    if data is not None: args += ['--data-binary', '@' + str(data)]
    args.append(url)
    result = subprocess.run(args, capture_output=True, text=True, check=True, timeout=60)
    return result.stdout.strip(), body.read_bytes(), headers.read_text()
def upload(marker):
    code, xml, _ = call('POST', base + '?uploads', extra=['--data', ''])
    assert code == '200', (code, xml[:200])
    upload_id = ET.fromstring(xml).findtext('.//{{*}}UploadId')
    assert upload_id
    payload.write_bytes(marker * (5 * 1024 * 1024 + 1))
    query = '?partNumber=1&uploadId=' + urllib.parse.quote(upload_id, safe='')
    code, _, response_headers = call('PUT', base + query, extra=['--upload-file', str(payload)])
    assert code == '200', code
    etag = next(line.split(':', 1)[1].strip() for line in response_headers.splitlines()
                if line.lower().startswith('etag:'))
    complete = pathlib.Path('/root/nex-backup-multipart-complete.xml')
    complete.write_text('<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>' +
                        etag + '</ETag></Part></CompleteMultipartUpload>')
    code, xml, _ = call('POST', base + '?uploadId=' + urllib.parse.quote(upload_id, safe=''),
                        data=complete, extra=['-H', 'If-None-Match: *',
                                              '-H', 'Content-Type: application/xml'])
    return code, xml, hashlib.sha256(payload.read_bytes()).hexdigest()
first, first_xml, digest = upload(b'A')
second, second_xml, _ = upload(b'B')
assert first == '200' and b'CompleteMultipartUploadResult' in first_xml, (first, first_xml[:200])
abort_base = 'https://{SERVER}:{port}/nex-backup-smoke/multipart-abort'
created, xml, _ = call('POST', abort_base + '?uploads', extra=['--data', ''])
assert created == '200', created
abort_id = ET.fromstring(xml).findtext('.//{{*}}UploadId')
assert abort_id
active, active_xml, _ = call('GET', 'https://{SERVER}:{port}/nex-backup-smoke?uploads')
query = '?partNumber=1&uploadId=' + urllib.parse.quote(abort_id, safe='')
uploaded, _, _ = call('PUT', abort_base + query, extra=['--upload-file', str(payload)])
assert uploaded == '200', uploaded
part_query = '?uploadId=' + urllib.parse.quote(abort_id, safe='')
parts, parts_xml, _ = call('GET', abort_base + part_query)
aborted, _, _ = call('DELETE', abort_base + part_query)
after_abort, _, _ = call('GET', abort_base + part_query)
abort_check = (active == '200' and b'ListMultipartUploadsResult' in active_xml and
               parts == '200' and b'<PartNumber>1</PartNumber>' in parts_xml and
               aborted == '204' and after_abort == '404')
print(first + ' ' + second + ' ' + digest + ' ' +
      ('complete' if b'CompleteMultipartUploadResult' in second_xml else 'refused') + ' ' +
      ('abort-passed' if abort_check else 'abort-failed'))
"""
    try:
        result = remote(lab, "core1", "python3 -c " + shlex.quote(probe)).split()
        if len(result) != 5:
            raise RuntimeError("multipart probe returned incomplete result")
        first, second, digest, second_result, abort_result = result
        report["checks"]["conditional_complete"] = {"first": first, "repeat": second,
                                                       "repeat_result": second_result}
        report["checks"]["scoped_list_parts_abort"] = abort_result
        verify = f"""import hashlib, pathlib, subprocess
config = '{config_path}'
url = 'https://{SERVER}:{port}/nex-backup-smoke/{key}'
output = pathlib.Path('/root/nex-backup-multipart-downloaded')
subprocess.run(['curl', '--config', config, '--fail', '-o', str(output), url], check=True, timeout=60)
listing = subprocess.check_output(['curl', '--config', config, '--fail',
    'https://{SERVER}:{port}/nex-backup-smoke?list-type=2'], text=True, timeout=60)
assert '<Key>{key}</Key>' in listing
print(hashlib.sha256(output.read_bytes()).hexdigest())
"""
        received = remote(lab, "core2", "python3 -c " + shlex.quote(verify))
        report["checks"]["reconnect_get_list_digest_matches_first"] = received == digest
        report["status"] = ("passed" if second in ("409", "412") and received == digest and
                            abort_result == "abort-passed" else "unsupported")
        if report["status"] == "unsupported":
            report["limit"] = "Conditional multipart completion or scoped list/abort did not pass."
    finally:
        report_path.write_text(json.dumps(report, indent=2) + "\n")
    return report_path



def run_sftp_directory_profile(lab):
    report_path = lab / "backup-destination-sftp-directory-profile.json"
    if report_path.exists():
        raise ValueError("SFTP directory profile already ran in this fixture")
    report = {"scope": "disposable OpenSSH SFTP file/directory sync and crash observations; no Nex backup",
              "fixture": lab.name, "status": "failed", "checks": {}}
    source = lab / "sftp-directory-profile.go"
    binary = lab / "sftp-directory-profile"
    try:
        source.write_text('''package main

import (
    "bytes"
    "fmt"
    "os"

    "github.com/pkg/sftp"
    "golang.org/x/crypto/ssh"
    "golang.org/x/crypto/ssh/knownhosts"
)

func main() {
    key, err := os.ReadFile("/root/nex-backup-sftp-id")
    must(err)
    signer, err := ssh.ParsePrivateKey(key)
    must(err)
    hostKey, err := knownhosts.New("/root/nex-backup-sftp-known-hosts")
    must(err)
    conn, err := ssh.Dial("tcp", "198.18.90.10:2222", &ssh.ClientConfig{
        User: "nexsftp", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
        HostKeyCallback: hostKey,
    })
    must(err)
    defer conn.Close()
    client, err := sftp.NewClient(conn)
    must(err)
    defer client.Close()
    payload := []byte("sftp-directory-sync-before-crash")
    file, err := client.Create("/data/profile-directory.partial")
    must(err)
    _, err = file.Write(payload)
    must(err)
    must(file.Sync())
    must(file.Close())
    must(client.Rename("/data/profile-directory.partial", "/data/profile-directory.final"))
    dir, err := client.Open("/data")
    must(err)
    must(dir.Sync())
    must(dir.Close())
    final, err := client.Open("/data/profile-directory.final")
    must(err)
    actual := make([]byte, len(payload))
    _, err = final.Read(actual)
    must(err)
    must(final.Close())
    if !bytes.Equal(actual, payload) {
        panic("acknowledged bytes differ before crash")
    }
    fmt.Println("file-sync-rename-directory-sync-read: passed")
}

func must(err error) {
    if err != nil { panic(err) }
}
''')
        subprocess.run(["go", "build", "-o", str(binary), str(source)], cwd=ROOT, check=True,
                       capture_output=True, text=True, timeout=120)
        ports = owned_guests(lab)
        subprocess.run(["scp", "-q", "-F", "/dev/null", "-o", "StrictHostKeyChecking=yes",
                        "-o", f"UserKnownHostsFile={lab / 'known_hosts'}", "-i", str(lab / "id_ed25519"),
                        "-P", str(ports["core1"]), str(binary), "root@127.0.0.1:/root/sftp-directory-profile"],
                       check=True, capture_output=True, text=True, timeout=120)
        remote(lab, "core1", "/root/sftp-directory-profile")
        report["checks"]["file_sync_rename_directory_sync"] = "passed"
        options = ("-q -b - -P 2222 -i /root/nex-backup-sftp-id -o BatchMode=yes "
                   "-o StrictHostKeyChecking=yes -o UserKnownHostsFile=/root/nex-backup-sftp-known-hosts "
                   f"nexsftp@{SERVER}")
        def read_final():
            remote(lab, "core2", f"""set -eu
sftp {options} <<'EOF'
get /data/profile-directory.final /root/nex-backup-sftp-directory-read
EOF
test "$(cat /root/nex-backup-sftp-directory-read)" = sftp-directory-sync-before-crash
""")
        read_final()
        report["checks"]["cross_client_acknowledged_bytes"] = "passed"
        kill_owned_storage_now(lab)
        report["crash_method"] = "pidfd SIGKILL of selected storage QEMU; no shutdown request"
        process = fixture.launch(lab, "storage", 2048, 2, ports["storage"])
        for _ in range(90):
            if process.poll() is not None:
                raise RuntimeError("selected storage guest did not restart")
            if fixture.ssh(lab, "storage", "true", check=False).returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError("selected storage guest SSH did not return")
        remote(lab, "storage", "/usr/sbin/sshd -f /etc/ssh/nex-backup-sftp.conf")
        try:
            read_final()
        except RuntimeError:
            report["checks"]["after_storage_qemu_sigkill"] = "file not readable"
            report["status"] = "unsupported"
        else:
            report["checks"]["after_storage_qemu_sigkill"] = "passed"
            report["status"] = "passed"
        report["limit"] = "One OpenSSH/ext4/qcow2 profile; rename no-overwrite exclusivity is not established here."
    finally:
        report_path.write_text(json.dumps(report, indent=2) + "\n")
    return report_path


def run_minio_fault_profile(lab):
    report_path = lab / "backup-destination-minio-fault-profile.json"
    if report_path.exists():
        raise ValueError("MinIO fault profile already ran in this fixture")
    report = {"scope": "disposable single-process MinIO concurrent conditional create and storage crash; no Nex backup",
              "fixture": lab.name, "source": MINIO_SOURCE,
              "status": "failed", "checks": {}}
    base = f"https://{SERVER}:5900/nex-backup-smoke"
    config = "/root/nex-backup-minio-curl.conf"
    def run_pair(commands):
        with ThreadPoolExecutor(max_workers=2) as pool:
            futures = [pool.submit(remote, lab, host, command) for host, command in commands]
            return [future.result() for future in futures]
    def get_bytes(key):
        return remote(lab, "core2", f"curl --config {config} --max-time 5 --fail {base}/{key}")
    try:
        stamp = time.time() + 2
        commands = []
        for host, marker in (("core1", "first"), ("core2", "second")):
            commands.append((host, f"printf %s {marker} > /root/nex-backup-minio-race; "
                                   f"python3 -c 'import time; time.sleep(max(0, {stamp} - time.time()))'; "
                                   f"curl --config {config} -H 'If-None-Match: *' "
                                   f"--upload-file /root/nex-backup-minio-race "
                                   f"-o /root/nex-backup-minio-race-response -w '%{{http_code}}' {base}/race"))
        codes = run_pair(commands)
        winner = get_bytes("race")
        report["checks"]["concurrent_conditional_put"] = {"codes": codes, "winner": winner}
        if sorted(codes) not in (["200", "412"], ["201", "412"]) or winner not in ("first", "second"):
            report["status"] = "unsupported"
            report["limit"] = "Concurrent conditional simple PUT did not provide one winner."
            return report_path
        prepare = f"""import pathlib, subprocess, urllib.parse, xml.etree.ElementTree as ET
base = '{base}/race-multipart'
config = '{config}'
name = '{{marker}}'
body = pathlib.Path('/root/nex-backup-minio-' + name + '-body')
headers = pathlib.Path('/root/nex-backup-minio-' + name + '-headers')
payload = pathlib.Path('/root/nex-backup-minio-' + name + '-payload')
def call(method, url, extra=()):
    result = subprocess.run(['curl', '--config', config, '-X', method, '-D', str(headers),
        '-o', str(body), '-w', '%{{http_code}}', *extra, url], check=True,
        capture_output=True, text=True, timeout=60)
    return result.stdout.strip(), body.read_bytes(), headers.read_text()
code, xml, _ = call('POST', base + '?uploads', ['--data', ''])
assert code == '200', code
upload_id = ET.fromstring(xml).findtext('.//{{*}}UploadId')
assert upload_id
payload.write_bytes(name.encode()[:1] * (5 * 1024 * 1024 + 1))
query = '?partNumber=1&uploadId=' + urllib.parse.quote(upload_id, safe='')
code, _, response_headers = call('PUT', base + query, ['--upload-file', str(payload)])
assert code == '200', code
etag = next(line.split(':', 1)[1].strip() for line in response_headers.splitlines()
            if line.lower().startswith('etag:'))
complete = pathlib.Path('/root/nex-backup-minio-' + name + '-complete.xml')
complete.write_text('<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>' +
                    etag + '</ETag></Part></CompleteMultipartUpload>')
print(upload_id)
"""
        upload_ids = []
        for host, marker in (("core1", "Afirst"), ("core2", "Bsecond")):
            upload_ids.append(remote(lab, host, "python3 -c " + shlex.quote(prepare.replace("{marker}", marker))))
        stamp = time.time() + 2
        commands = []
        for (host, marker), upload_id in zip((("core1", "Afirst"), ("core2", "Bsecond")), upload_ids):
            url = base + "/race-multipart?uploadId=" + upload_id
            commands.append((host, f"python3 -c 'import time; time.sleep(max(0, {stamp} - time.time()))'; "
                                   f"curl --config {config} -X POST -H 'If-None-Match: *' "
                                   f"-H 'Content-Type: application/xml' "
                                   f"--data-binary @/root/nex-backup-minio-{marker}-complete.xml "
                                   f"-o /root/nex-backup-minio-{marker}-complete-response "
                                   f"-w '%{{http_code}}' " + shlex.quote(url)))
        complete_codes = run_pair(commands)
        report["checks"]["concurrent_conditional_multipart_complete"] = complete_codes
        if sorted(complete_codes) not in (["200", "412"], ["200", "409"]):
            report["status"] = "unsupported"
            report["limit"] = "Concurrent conditional multipart completion did not provide one winner."
            return report_path
        expected = b"A" if complete_codes[0] == "200" else b"B"
        remote(lab, "core2", f"curl --config {config} --fail -o /root/nex-backup-minio-race-multipart {base}/race-multipart")
        check = remote(lab, "core2", "python3 -c " + shlex.quote(
            f"from pathlib import Path; p=Path('/root/nex-backup-minio-race-multipart').read_bytes(); "
            f"assert p == {expected!r} * (5*1024*1024+1); print('matched')"))
        report["checks"]["concurrent_multipart_exact_bytes"] = check
        kill_owned_storage_now(lab)
        report["crash_method"] = "pidfd SIGKILL of selected storage QEMU; no shutdown request"
        ports = json.loads((lab / "ports.json").read_text())
        process = fixture.launch(lab, "storage", 2048, 2, ports["storage"])
        for _ in range(90):
            if process.poll() is not None:
                raise RuntimeError("selected storage guest did not restart")
            if fixture.ssh(lab, "storage", "true", check=False).returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError("selected storage guest SSH did not return")
        unit = "nex-backup-minio.service"
        for _ in range(60):
            if remote(lab, "storage", f"systemctl is-active {unit} || true") == "active":
                break
            time.sleep(1)
        else:
            raise RuntimeError("MinIO service did not restart after guest crash")
        for _ in range(12):
            try:
                if get_bytes("race") == winner:
                    break
            except RuntimeError:
                pass
            time.sleep(1)
        else:
            report["checks"]["after_storage_qemu_sigkill"] = "acknowledged simple object unreadable"
            report["status"] = "unsupported"
            report["limit"] = "Published simple object not readable after storage guest crash."
            return report_path
        try:
            remote(lab, "core2", f"curl --config {config} --max-time 5 --fail -o /root/nex-backup-minio-race-multipart {base}/race-multipart")
        except RuntimeError:
            report["checks"]["after_storage_qemu_sigkill"] = "acknowledged multipart object unreadable"
            report["status"] = "unsupported"
            return report_path
        check = remote(lab, "core2", "python3 -c " + shlex.quote(
            f"from pathlib import Path; p=Path('/root/nex-backup-minio-race-multipart').read_bytes(); "
            f"assert p == {expected!r} * (5*1024*1024+1); print('matched')"))
        listing = remote(lab, "core2", f"curl --config {config} --fail '{base}?list-type=2'")
        report["checks"]["after_storage_qemu_sigkill"] = ("passed" if check == "matched" and
            "<Key>race</Key>" in listing and "<Key>race-multipart</Key>" in listing else "failed")
        report["status"] = "passed" if report["checks"]["after_storage_qemu_sigkill"] == "passed" else "unsupported"
        report["limit"] = "One MinIO single-node/ext4/qcow2 profile; no Nex product restore performed."
    finally:
        report_path.write_text(json.dumps(report, indent=2) + "\n")
    return report_path


def run_minio_profile(lab):
    report_path = lab / "backup-destination-minio-profile.json"
    if report_path.exists():
        raise ValueError("MinIO profile already ran in this fixture")
    report = {"scope": "disposable single-process MinIO S3 service smoke; no Nex backup or restore",
              "fixture": lab.name, "server_source": MINIO_SOURCE, "client_source": MC_SOURCE,
              "status": "failed", "checks": {}}
    build = lab / "minio-build"
    try:
        for name, expected in (("minio", MINIO_SHA256), ("mc", MC_SHA256)):
            binary = build / name
            if hashlib.file_digest(binary.open("rb"), "sha256").hexdigest() != expected:
                raise RuntimeError(f"pinned {name} binary digest differs")
            info = subprocess.run(["go", "version", "-m", str(binary)], check=True,
                                  capture_output=True, text=True, timeout=30).stdout
            source = MINIO_SOURCE if name == "minio" else MC_SOURCE
            if source.split("@", 1)[0] not in info:
                raise RuntimeError(f"{name} build metadata lacks source module")
            report[f"{name}_sha256"] = expected
            report[f"{name}_module"] = next(line.strip() for line in info.splitlines() if line.startswith("\tmod\t"))
            ports = owned_guests(lab)
            if remote(lab, "storage", f"if test -e /usr/local/bin/nex-backup-{name}; then echo yes; fi") != "yes":
                subprocess.run(["scp", "-q", "-F", "/dev/null", "-o", "StrictHostKeyChecking=yes",
                                "-o", f"UserKnownHostsFile={lab / 'known_hosts'}", "-i", str(lab / "id_ed25519"),
                                "-P", str(ports["storage"]), str(binary), f"root@127.0.0.1:/usr/local/bin/nex-backup-{name}"],
                               check=True, capture_output=True, text=True, timeout=120)
            if remote(lab, "storage", f"sha256sum /usr/local/bin/nex-backup-{name} | cut -d' ' -f1") != expected:
                raise RuntimeError(f"{name} binary changed during transfer")
        existing = remote(lab, "storage", "if test -e /etc/nex-backup-minio.env; then echo yes; fi")
        scoped_id, scoped_secret = "NEXLABSCOPED" + secrets.token_hex(8), secrets.token_urlsafe(32)
        if existing != "yes":
            root_id, root_secret = "NEXLABROOT" + secrets.token_hex(8), secrets.token_urlsafe(32)
            remote(lab, "storage", """set -eu
test ! -e /etc/nex-backup-minio.env
install -d -m 0700 /etc/nex-backup-minio-certs /srv/nex-backup-smoke/minio-data
read -r root_id
read -r root_secret
umask 077
printf 'MINIO_ROOT_USER=%s\\nMINIO_ROOT_PASSWORD=%s\\nMINIO_FS_OSYNC=on\\nMINIO_BROWSER=off\\n' "$root_id" "$root_secret" > /etc/nex-backup-minio.env
openssl req -new -x509 -newkey rsa:2048 -nodes -days 2 -sha256 \
  -subj /CN=backup.example.test -addext 'subjectAltName=IP:198.18.90.10' \
  -keyout /etc/nex-backup-minio-certs/private.key \
  -out /etc/nex-backup-minio-certs/public.crt >/dev/null 2>&1
chmod 0600 /etc/nex-backup-minio-certs/private.key
cat > /etc/systemd/system/nex-backup-minio.service <<'EOF'
[Unit]
Description=Disposable backup destination MinIO service
[Service]
Type=simple
EnvironmentFile=/etc/nex-backup-minio.env
ExecStart=/usr/local/bin/nex-backup-minio server --address 198.18.90.10:5900 --console-address 127.0.0.1:5901 --certs-dir /etc/nex-backup-minio-certs /srv/nex-backup-smoke/minio-data
Restart=no
[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now nex-backup-minio.service >/dev/null
""", root_id + "\n" + root_secret + "\n")
        report["version"] = remote(lab, "storage", "/usr/local/bin/nex-backup-minio --version")
        report["tls_fingerprint"] = remote(lab, "storage",
            "openssl x509 -in /etc/nex-backup-minio-certs/public.crt -noout -fingerprint -sha256")
        cert = remote(lab, "storage", "cat /etc/nex-backup-minio-certs/public.crt") + "\n"
        for host in ("core1", "core2"):
            remote(lab, host, "umask 077; cat > /root/nex-backup-minio-ca.crt", cert)
        for _ in range(60):
            code = remote(lab, "core1", f"curl --cacert /root/nex-backup-minio-ca.crt --silent --output /root/nex-backup-minio-ready -w '%{{http_code}}' https://{SERVER}:5900/minio/health/live 2>/dev/null || true")
            if code == "200":
                break
            time.sleep(1)
        else:
            raise RuntimeError("MinIO HTTPS service did not become ready")
        remote(lab, "storage", "umask 077; cat > /etc/nex-backup-minio-policy.json", json.dumps(MINIO_POLICY) + "\n")
        remote(lab, "storage", """set -eu
. /etc/nex-backup-minio.env
export MC_CONFIG_DIR=/root/.mc-nex-backup
for i in $(seq 1 60); do
  if /usr/local/bin/nex-backup-mc --insecure alias set lab https://198.18.90.10:5900 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null 2>&1; then break; fi
  sleep 1
done
/usr/local/bin/nex-backup-mc --insecure mb --ignore-existing lab/nex-backup-smoke >/dev/null
/usr/local/bin/nex-backup-mc --insecure admin policy create lab nex-backup-smoke /etc/nex-backup-minio-policy.json >/dev/null
read -r scoped_id
read -r scoped_secret
/usr/local/bin/nex-backup-mc --insecure admin user add lab "$scoped_id" "$scoped_secret" >/dev/null
/usr/local/bin/nex-backup-mc --insecure admin policy attach lab nex-backup-smoke --user "$scoped_id" >/dev/null
""", scoped_id + "\n" + scoped_secret + "\n")
        curl_config = (f'user = "{scoped_id}:{scoped_secret}"\n'
                       'aws-sigv4 = "aws:amz:us-east-1:s3"\n'
                       'cacert = "/root/nex-backup-minio-ca.crt"\n'
                       'silent\nshow-error\n')
        for host in ("core1", "core2"):
            remote(lab, host, "umask 077; cat > /root/nex-backup-minio-curl.conf", curl_config)
        denied = remote(lab, "core2", f"curl --config /root/nex-backup-minio-curl.conf -X PUT -o /root/nex-backup-minio-denied -w '%{{http_code}}' https://{SERVER}:5900/other-bucket")
        url = f"https://{SERVER}:5900/nex-backup-smoke/point"
        first = remote(lab, "core1", f"printf %s first > /root/nex-backup-minio-first; curl --config /root/nex-backup-minio-curl.conf -H 'If-None-Match: *' --upload-file /root/nex-backup-minio-first -o /root/nex-backup-minio-first-response -w '%{{http_code}}' {url}")
        second = remote(lab, "core2", f"printf %s second > /root/nex-backup-minio-second; curl --config /root/nex-backup-minio-curl.conf -H 'If-None-Match: *' --upload-file /root/nex-backup-minio-second -o /root/nex-backup-minio-second-response -w '%{{http_code}}' {url}")
        content = remote(lab, "core2", f"curl --config /root/nex-backup-minio-curl.conf --fail {url}")
        report["checks"] = {"scoped_key_other_bucket": denied,
                            "conditional_put": {"first": first, "repeat": second, "read_first": content == "first"}}
        report["status"] = ("passed" if denied in ("401", "403") and first in ("200", "201") and
                            second in ("409", "412") and content == "first" else "unsupported")
        report["limit"] = "Archived community release; simple PUT only, multipart/concurrent/crash tested separately."
    finally:
        report_path.write_text(json.dumps(report, indent=2) + "\n")
    return report_path


def run_minio_degraded_profile(lab, metadata=False):
    report_path = lab / ("backup-destination-minio-metadata-fault-profile.json" if metadata else
                         "backup-destination-minio-degraded-profile.json")
    if report_path.exists():
        raise ValueError("MinIO degraded profile already ran in this fixture")
    report = {"scope": "disposable MinIO object-metadata fault preconditions; no Nex backup" if metadata else
                       "disposable MinIO drive-unavailable conditional preconditions; no Nex backup",
              "fixture": lab.name, "server_source": MINIO_SOURCE, "status": "failed", "checks": {}}
    base = f"https://{SERVER}:5900/nex-backup-smoke"
    config = "/root/nex-backup-minio-curl.conf"
    prepare = f"""import pathlib, subprocess, urllib.parse, xml.etree.ElementTree as ET
base = '{base}/multipart'
config = '{config}'
body = pathlib.Path('/root/nex-backup-minio-degraded-body')
headers = pathlib.Path('/root/nex-backup-minio-degraded-headers')
payload = pathlib.Path('/root/nex-backup-minio-degraded-payload')
def call(method, url, extra=()):
    result = subprocess.run(['curl', '--config', config, '-X', method, '-D', str(headers),
        '-o', str(body), '-w', '%{{http_code}}', *extra, url], check=True,
        capture_output=True, text=True, timeout=60)
    return result.stdout.strip(), body.read_bytes(), headers.read_text()
code, xml, _ = call('POST', base + '?uploads', ['--data', ''])
assert code == '200', code
upload_id = ET.fromstring(xml).findtext('.//{{*}}UploadId')
assert upload_id
payload.write_bytes(b'B' * (5 * 1024 * 1024 + 1))
query = '?partNumber=1&uploadId=' + urllib.parse.quote(upload_id, safe='')
code, _, response_headers = call('PUT', base + query, ['--upload-file', str(payload)])
assert code == '200', code
etag = next(line.split(':', 1)[1].strip() for line in response_headers.splitlines()
            if line.lower().startswith('etag:'))
complete = pathlib.Path('/root/nex-backup-minio-degraded-complete.xml')
complete.write_text('<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>' +
                    etag + '</ETag></Part></CompleteMultipartUpload>')
print(upload_id)
"""
    fault_started = False
    try:
        if remote(lab, "core2", f"curl --config {config} --fail {base}/point") != "first":
            raise RuntimeError("baseline object differs before degraded probe")
        upload_id = remote(lab, "core1", "python3 -c " + shlex.quote(prepare))
        fault_started = True
        remote(lab, "storage", """set -eu
test ! -e /root/nex-backup-minio-point-meta.saved
test ! -e /root/nex-backup-minio-multipart-meta.saved
test -f /srv/nex-backup-smoke/minio-data/nex-backup-smoke/point/xl.meta
test -f /srv/nex-backup-smoke/minio-data/nex-backup-smoke/multipart/xl.meta
cp /srv/nex-backup-smoke/minio-data/nex-backup-smoke/point/xl.meta /root/nex-backup-minio-point-meta.saved
cp /srv/nex-backup-smoke/minio-data/nex-backup-smoke/multipart/xl.meta /root/nex-backup-minio-multipart-meta.saved
sync
printf %s invalid-metadata > /srv/nex-backup-smoke/minio-data/nex-backup-smoke/point/xl.meta
printf %s invalid-metadata > /srv/nex-backup-smoke/minio-data/nex-backup-smoke/multipart/xl.meta
sync
""" if metadata else """set -eu
test -d /srv/nex-backup-smoke/minio-data
test ! -e /srv/nex-backup-smoke/minio-data.paused
test ! -e /srv/nex-backup-smoke/minio-data.transient
mv /srv/nex-backup-smoke/minio-data /srv/nex-backup-smoke/minio-data.paused
""")
        read_code = remote(lab, "core2", f"curl --config {config} --max-time 5 -o /root/nex-backup-minio-degraded-read -w '%{{http_code}}' {base}/point 2>/dev/null || true")
        report["checks"]["drive_unavailable_read_code"] = read_code
        if read_code in ("200", "206"):
            report["status"] = "indeterminate"
            report["limit"] = "Selected fault did not make the object read unavailable."
            return report_path
        simple = remote(lab, "core2", f"printf %s replacement > /root/nex-backup-minio-degraded-replacement; curl --config {config} --max-time 5 -H 'If-None-Match: *' --upload-file /root/nex-backup-minio-degraded-replacement -o /root/nex-backup-minio-degraded-put -w '%{{http_code}}' {base}/point 2>/dev/null || true")
        complete = remote(lab, "core1", f"curl --config {config} --max-time 5 -X POST -H 'If-None-Match: *' -H 'Content-Type: application/xml' --data-binary @/root/nex-backup-minio-degraded-complete.xml -o /root/nex-backup-minio-degraded-complete-response -w '%{{http_code}}' " + shlex.quote(base + "/multipart?uploadId=" + upload_id) + " 2>/dev/null || true")
        report["checks"]["conditional_during_drive_outage"] = {"simple": simple, "multipart": complete}
    finally:
        if fault_started:
            remote(lab, "storage", """set -eu
point=/root/nex-backup-minio-point-meta.saved
multipart=/root/nex-backup-minio-multipart-meta.saved
if test -e "$point" || test -e "$multipart"; then
  systemctl stop nex-backup-minio.service
  if test -e "$point"; then
    cp "$point" /srv/nex-backup-smoke/minio-data/nex-backup-smoke/point/xl.meta
  fi
  if test -e "$multipart"; then
    cp "$multipart" /srv/nex-backup-smoke/minio-data/nex-backup-smoke/multipart/xl.meta
  fi
  sync
  systemctl start nex-backup-minio.service
fi
""" if metadata else """set -eu
if test -e /srv/nex-backup-smoke/minio-data.paused; then
  systemctl stop nex-backup-minio.service
  if test -e /srv/nex-backup-smoke/minio-data; then
    test ! -e /srv/nex-backup-smoke/minio-data.transient
    mv /srv/nex-backup-smoke/minio-data /srv/nex-backup-smoke/minio-data.transient
  fi
  mv /srv/nex-backup-smoke/minio-data.paused /srv/nex-backup-smoke/minio-data
  systemctl start nex-backup-minio.service
fi
""")
        report_path.write_text(json.dumps(report, indent=2) + "\n")
    for _ in range(30):
        code = remote(lab, "core2", f"curl --config {config} --max-time 5 -o /root/nex-backup-minio-after-degraded -w '%{{http_code}}' {base}/point 2>/dev/null || true")
        if code == "200":
            break
        time.sleep(1)
    else:
        report["status"] = "unsupported"
        report["limit"] = "Original object not readable after restoring the selected disposable drive path."
        report_path.write_text(json.dumps(report, indent=2) + "\n")
        return report_path
    point = remote(lab, "core2", "cat /root/nex-backup-minio-after-degraded")
    remote(lab, "core2", f"curl --config {config} --fail -o /root/nex-backup-minio-multipart-after-degraded {base}/multipart")
    digest_ok = remote(lab, "core2", "python3 -c " + shlex.quote(
        "from pathlib import Path; import hashlib; p=Path('/root/nex-backup-minio-multipart-after-degraded').read_bytes(); "
        "print(p == b'A' * (5*1024*1024+1))"))
    listing = remote(lab, "core2", f"curl --config {config} --fail '{base}?list-type=2'")
    report["checks"]["old_bytes_and_names_after_restore"] = (point == "first" and digest_ok == "True" and
        "<Key>point</Key>" in listing and "<Key>multipart</Key>" in listing)
    refused = all(code not in ("000", "200", "201", "204") and code.isdigit() and int(code) >= 400
                  for code in (simple, complete))
    report["status"] = "passed" if refused and report["checks"]["old_bytes_and_names_after_restore"] else "unsupported"
    report["limit"] = ("Malformed xl.meta simulates per-object metadata read failure, not a physical EIO or multi-node quorum." if metadata else
                       "A renamed drive path simulates whole-drive unavailability, not an EIO at one metadata read or multi-node quorum.")
    report_path.write_text(json.dumps(report, indent=2) + "\n")
    return report_path


def run_sftp_exclusive_profile(lab):
    report_path = lab / "backup-destination-sftp-exclusive-profile.json"
    if report_path.exists():
        raise ValueError("SFTP exclusive profile already ran in this fixture")
    report = {"scope": "disposable OpenSSH SFTP exclusive mkdir and rename collisions; no Nex backup",
              "fixture": lab.name, "status": "failed", "checks": {}}
    source = lab / "sftp-exclusive-profile.go"
    binary = lab / "sftp-exclusive-profile"
    try:
        source.write_text('''package main

import (
    "fmt"
    "io"
    "os"
    "strconv"
    "time"

    "github.com/pkg/sftp"
    "golang.org/x/crypto/ssh"
    "golang.org/x/crypto/ssh/knownhosts"
)

func must(err error) { if err != nil { panic(err) } }
func put(c *sftp.Client, path, value string) {
    f, err := c.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
    must(err)
    _, err = f.Write([]byte(value)); must(err)
    must(f.Sync()); must(f.Close())
}
func read(c *sftp.Client, path string) string {
    f, err := c.Open(path); must(err); defer f.Close()
    b, err := io.ReadAll(f); must(err)
    return string(b)
}
func main() {
    key, err := os.ReadFile("/root/nex-backup-sftp-id"); must(err)
    signer, err := ssh.ParsePrivateKey(key); must(err)
    hostKey, err := knownhosts.New("/root/nex-backup-sftp-known-hosts"); must(err)
    conn, err := ssh.Dial("tcp", "198.18.90.10:2222", &ssh.ClientConfig{
        User: "nexsftp", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: hostKey,
    }); must(err); defer conn.Close()
    c, err := sftp.NewClient(conn); must(err); defer c.Close()
    switch os.Args[1] {
    case "prepare":
        put(c, "/data/collision-source", "source")
        put(c, "/data/collision-target", "target")
    case "collision":
        if err := c.Rename("/data/collision-source", "/data/collision-target"); err == nil {
            panic("rename replaced existing target")
        }
        if read(c, "/data/collision-source") != "source" || read(c, "/data/collision-target") != "target" {
            panic("rename collision changed source or target")
        }
        fmt.Println("refused-preserved")
    case "mkdir-race":
        timestamp, err := strconv.ParseFloat(os.Args[2], 64); must(err)
        time.Sleep(time.Until(time.Unix(0, int64(timestamp*1e9))))
        if err := c.Mkdir("/data/exclusive-point"); err != nil { fmt.Println("refused")
        } else { fmt.Println("created") }
    case "rename-prepare":
        put(c, "/data/race-source-"+os.Args[2], os.Args[2])
    case "rename-race":
        timestamp, err := strconv.ParseFloat(os.Args[3], 64); must(err)
        time.Sleep(time.Until(time.Unix(0, int64(timestamp*1e9))))
        if err := c.Rename("/data/race-source-"+os.Args[2], "/data/race-final"); err != nil {
            fmt.Println("refused")
        } else {
            dir, err := c.Open("/data"); must(err); must(dir.Sync()); must(dir.Close())
            fmt.Println("published")
        }
    case "read-final":
        fmt.Println(read(c, "/data/race-final"))
    }
}
''')
        subprocess.run(["go", "build", "-o", str(binary), str(source)], cwd=ROOT,
                       check=True, capture_output=True, text=True, timeout=120)
        ports = owned_guests(lab)
        for host in ("core1", "core2"):
            subprocess.run(["scp", "-q", "-F", "/dev/null", "-o", "StrictHostKeyChecking=yes",
                            "-o", f"UserKnownHostsFile={lab / 'known_hosts'}", "-i", str(lab / "id_ed25519"),
                            "-P", str(ports[host]), str(binary), "root@127.0.0.1:/root/sftp-exclusive-profile"],
                           check=True, capture_output=True, text=True, timeout=120)
        remote(lab, "storage", "/usr/sbin/sshd -f /etc/ssh/nex-backup-sftp.conf")
        remote(lab, "core1", "/root/sftp-exclusive-profile prepare")
        report["checks"]["rename_existing_target"] = remote(lab, "core2", "/root/sftp-exclusive-profile collision")
        stamp = str(time.time() + 2)
        with ThreadPoolExecutor(max_workers=2) as pool:
            results = [pool.submit(remote, lab, host, f"/root/sftp-exclusive-profile mkdir-race {stamp}")
                       for host in ("core1", "core2")]
            mkdir = [item.result() for item in results]
        report["checks"]["concurrent_mkdir"] = mkdir
        for host, marker in (("core1", "first"), ("core2", "second")):
            remote(lab, host, f"/root/sftp-exclusive-profile rename-prepare {marker}")
        stamp = str(time.time() + 2)
        with ThreadPoolExecutor(max_workers=2) as pool:
            results = [pool.submit(remote, lab, host,
                                   f"/root/sftp-exclusive-profile rename-race {marker} {stamp}")
                       for host, marker in (("core1", "first"), ("core2", "second"))]
            rename = [item.result() for item in results]
        report["checks"]["concurrent_rename"] = rename
        winner = remote(lab, "core2", "/root/sftp-exclusive-profile read-final")
        report["checks"]["final_value"] = winner
        report["status"] = ("passed" if report["checks"]["rename_existing_target"] == "refused-preserved" and
                            sorted(mkdir) == ["created", "refused"] and
                            sorted(rename) == ["published", "refused"] and winner in ("first", "second")
                            else "unsupported")
        report["limit"] = "One OpenSSH/ext4/qcow2 profile; not a Nex backup/restore proof."
    finally:
        report_path.write_text(json.dumps(report, indent=2) + "\n")
    return report_path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lab-dir", required=True, type=Path)
    parser.add_argument("--transfer-services", action="store_true")
    parser.add_argument("--sftp-directory-profile", action="store_true")
    parser.add_argument("--sftp-exclusive-profile", action="store_true")
    parser.add_argument("--minio-profile", action="store_true")
    parser.add_argument("--minio-multipart", action="store_true")
    parser.add_argument("--minio-fault-profile", action="store_true")
    parser.add_argument("--minio-degraded-profile", action="store_true")
    parser.add_argument("--minio-metadata-fault-profile", action="store_true")
    args = parser.parse_args()
    lab = selected_lab(args.lab_dir)
    if sum((args.transfer_services, args.sftp_directory_profile,
            args.sftp_exclusive_profile, args.minio_profile, args.minio_multipart,
            args.minio_fault_profile, args.minio_degraded_profile,
            args.minio_metadata_fault_profile)) > 1:
        parser.error("choose one service smoke")
    print(run_minio_degraded_profile(lab, True) if args.minio_metadata_fault_profile else
          run_minio_degraded_profile(lab) if args.minio_degraded_profile else
          run_minio_fault_profile(lab) if args.minio_fault_profile else
          run_minio_multipart(lab) if args.minio_multipart else
          run_minio_profile(lab) if args.minio_profile else
          run_sftp_exclusive_profile(lab) if args.sftp_exclusive_profile else
          run_sftp_directory_profile(lab) if args.sftp_directory_profile else
          run_transfer_services(lab) if args.transfer_services else run(lab))


if __name__ == "__main__":
    main()
