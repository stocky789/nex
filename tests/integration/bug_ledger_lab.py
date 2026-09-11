#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Create two private BIOS/KVM Debian guests for BUG-0093--0096 lab work.

Everything mutable, including generated keys and the SMB fixture credential, is
kept in one ignored ``.cache/nex-bug-ledger-lab.*`` directory.  The guests have
their own QEMU user-mode NAT for SSH and a separate loopback-only point-to-point
network for the storage fixture; no host bridge, mount, libvirt daemon or
existing QEMU process is used.
"""
import argparse
import base64
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time


ROOT = Path(__file__).resolve().parents[2]
BASE = ROOT / ".cache/appliance/debian-13-genericcloud-amd64-20260831-2587.qcow2"


def port():
    with socket.socket() as value:
        value.bind(("127.0.0.1", 0))
        return value.getsockname()[1]


def docker(*args):
    return subprocess.run(["sudo", "-n", "docker", "run", "--rm", "--user", f"{os.getuid()}:{os.getgid()}", *args], check=True,
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def ssh(lab, name, command, check=True):
    ports = json.loads((lab / "ports.json").read_text())
    return subprocess.run(["ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=3",
                           "-o", "StrictHostKeyChecking=accept-new", "-o", f"UserKnownHostsFile={lab / 'known_hosts'}",
                           "-i", str(lab / "id_ed25519"), "-p", str(ports[name]), "root@127.0.0.1", command],
                          check=check, capture_output=True, text=True)


def seed(lab, name, public_key, peer_ip, installer):
    directory = lab / f"seed-{name}"
    directory.mkdir(mode=0o700)
    link_setup = ("install -d /etc/systemd/system; printf '%s\\n' '[Unit]' 'Description=Nex bug ledger private link' "
                  "'After=network.target' '[Service]' 'Type=oneshot' 'RemainAfterExit=yes' 'ExecStart=/usr/sbin/ip link set ens4 up' "
                  f"'ExecStart=/usr/sbin/ip address replace {peer_ip}/24 dev ens4' '[Install]' 'WantedBy=multi-user.target' "
                  "> /etc/systemd/system/nex-lab-link.service; systemctl daemon-reload; systemctl enable --now nex-lab-link; ")
    config = "#cloud-config\n" + "\n".join((
        f"hostname: nex-bug-{name}", "manage_etc_hosts: true", "ssh_pwauth: false", "disable_root: false",
        "ssh_authorized_keys:", f"  - {public_key}",
        "runcmd:", f"  - [ sh, -ec, {json.dumps(link_setup + installer)} ]",
    )) + "\n"
    (directory / "user-data").write_text(config)
    (directory / "meta-data").write_text(f"instance-id: nex-bug-ledger-{name}\nlocal-hostname: nex-bug-{name}\n")
    output = lab / f"{name}-seed.iso"
    docker("-v", f"{lab}:{lab}", "--entrypoint", "xorriso", "nex-iso-builder", "-as", "mkisofs", "-quiet",
           "-output", str(output), "-volid", "cidata", "-joliet", "-rock", str(directory))
    for item in directory.iterdir():
        item.unlink()
    directory.rmdir()


def launch(lab, name, ssh_port, link_port, listen):
    network = (f"socket,id=private,listen=127.0.0.1:{link_port}" if listen
               else f"socket,id=private,connect=127.0.0.1:{link_port}")
    command = ["qemu-system-x86_64", "-accel", "kvm", "-cpu", "host", "-m", "3072", "-smp", "2", "-display", "none", "-monitor", "none",
               "-serial", f"file:{lab / (name + '-serial.log')}", "-qmp", f"unix:{lab / (name + '-qmp.sock')},server=on,wait=off",
               "-drive", f"file={lab / (name + '.qcow2')},format=qcow2,if=virtio",
               "-drive", f"file={lab / (name + '-seed.iso')},format=raw,media=cdrom,readonly=on",
               "-netdev", f"user,id=nat,hostfwd=tcp:127.0.0.1:{ssh_port}-:22", "-device", "virtio-net-pci,netdev=nat,mac=52:54:00:93:10:" + ("01" if name == "a" else "02"),
               "-netdev", network, "-device", "virtio-net-pci,netdev=private,mac=52:54:00:93:11:" + ("01" if name == "a" else "02")]
    (lab / f"{name}-command.json").write_text(json.dumps(command, indent=2) + "\n")
    with (lab / f"{name}-qemu.log").open("w") as log:
        process = subprocess.Popen(command, stdout=log, stderr=log, start_new_session=True)
    (lab / f"{name}-qemu.pid").write_text(str(process.pid) + "\n")
    return process


def provision_fixture(lab):
    """Finish fixture setup after cloud-init without placing its credential in seed media or logs."""
    initiator = ssh(lab, "b", "awk -F= '/^InitiatorName=/{print $2}' /etc/iscsi/initiatorname.iscsi").stdout.strip()
    assert initiator.startswith("iqn."), "node b has no iSCSI initiator identity"
    password = (lab / "smb-password").read_bytes()
    command = """set -eu
id nexlab >/dev/null 2>&1 || useradd -r -M -s /usr/sbin/nologin nexlab
install -d -o 64055 -g 64055 -m 0770 /srv/nex-lab/nfs
install -d -o nexlab -g nexlab -m 0770 /srv/nex-lab/smb
install -d -m 0700 /srv/nex-lab/iscsi /etc/exports.d
printf '%s\\n' '/srv/nex-lab/nfs 198.18.77.0/24(rw,sync,no_subtree_check,root_squash,anonuid=64055,anongid=64055)' > /etc/exports.d/nex-lab.exports
exportfs -ra
systemctl enable --now nfs-server
{ cat /root/.nexlab-smb-password; cat /root/.nexlab-smb-password; } | smbpasswd -a -s nexlab
rm -f /root/.nexlab-smb-password
grep -q '^\\[nexlab\\]' /etc/samba/smb.conf || printf '\\n[nexlab]\\n path = /srv/nex-lab/smb\\n read only = no\\n valid users = nexlab\\n' >> /etc/samba/smb.conf
systemctl enable --now smbd
truncate -s 8G /srv/nex-lab/iscsi/lun.img
mkfs.ext4 -F -q /srv/nex-lab/iscsi/lun.img
targetcli /backstores/fileio create nexlab /srv/nex-lab/iscsi/lun.img 8G
targetcli /iscsi create iqn.2026-09.example.test:nexlab
targetcli /iscsi/iqn.2026-09.example.test:nexlab/tpg1 set attribute generate_node_acls=0 demo_mode_write_protect=1
targetcli /iscsi/iqn.2026-09.example.test:nexlab/tpg1/luns create /backstores/fileio/nexlab
targetcli /iscsi/iqn.2026-09.example.test:nexlab/tpg1/acls create '""" + initiator + """'
targetcli saveconfig
systemctl is-active nfs-server smbd
targetcli ls /iscsi/iqn.2026-09.example.test:nexlab
"""
    ports = json.loads((lab / "ports.json").read_text())
    connection = ["ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
                  "-o", "StrictHostKeyChecking=accept-new", "-o", f"UserKnownHostsFile={lab / 'known_hosts'}",
                  "-i", str(lab / "id_ed25519"), "-p", str(ports["a"]), "root@127.0.0.1"]
    staged = subprocess.run(connection + ["base64", "-d", ">", "/root/.nexlab-smb-password"],
                             input=base64.b64encode(password), capture_output=True)
    if staged.returncode:
        raise RuntimeError(staged.stderr.decode(errors="replace")[-2000:])
    result = subprocess.run(connection + ["sh", "-s"], input=command, text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError(result.stderr[-2000:])


def prepare():
    assert BASE.is_file() and not BASE.is_symlink(), "missing cached Debian 13 cloud image"
    lab = Path(tempfile.mkdtemp(prefix="nex-bug-ledger-lab.", dir=ROOT / ".cache"))
    lab.chmod(0o700)
    subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "nex-bug-ledger-lab", "-f", str(lab / "id_ed25519")], check=True)
    public_key = (lab / "id_ed25519.pub").read_text().strip()
    secret = secrets.token_urlsafe(24)
    (lab / "smb-password").write_text(secret + "\n")
    (lab / "smb-password").chmod(0o600)
    server = "set -eu; apt-get update -qq; DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends nfs-kernel-server samba targetcli-fb nfs-common cifs-utils open-iscsi e2fsprogs"
    client = "set -eu; apt-get update -qq; DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends nfs-common libnfs-utils cifs-utils smbclient open-iscsi targetcli-fb libvirt-daemon-system libvirt-clients"
    seed(lab, "a", public_key, "198.18.77.10", server)
    seed(lab, "b", public_key, "198.18.77.11", client)
    for name in ("a", "b"):
        docker("-v", f"{BASE}:{BASE}:ro", "-v", f"{lab}:{lab}", "--entrypoint", "qemu-img", "nex-appliance-builder",
               "create", "-f", "qcow2", "-F", "qcow2", "-b", str(BASE), str(lab / f"{name}.qcow2"), "32G")
    ports = {"a": port(), "b": port(), "private_link": port()}
    (lab / "ports.json").write_text(json.dumps(ports, indent=2) + "\n")
    a = launch(lab, "a", ports["a"], ports["private_link"], True)
    time.sleep(.5)
    b = launch(lab, "b", ports["b"], ports["private_link"], False)
    for _ in range(240):
        if a.poll() is not None or b.poll() is not None:
            raise RuntimeError("QEMU exited: " + (lab / "a-qemu.log").read_text(errors="replace")[-1000:] + (lab / "b-qemu.log").read_text(errors="replace")[-1000:])
        ready = [ssh(lab, name, "test -f /var/lib/cloud/instance/boot-finished && ip -4 addr show ens4 | grep -q 198.18.77", check=False).returncode == 0 for name in ("a", "b")]
        if all(ready):
            break
        time.sleep(1)
    else:
        raise RuntimeError("guests did not finish cloud-init")
    # The fixture services may still be installing after cloud-init's marker; wait for usable protocol endpoints.
    for _ in range(180):
        reach = ssh(lab, "b", "ping -c1 -W1 198.18.77.10 >/dev/null", check=False)
        if reach.returncode == 0:
            provision_fixture(lab)
            (lab / "ready.json").write_text(json.dumps({"disposable": True, "bios": True, "memory_mib_each": 3072,
                "guests": {"a": "Core candidate plus NFS/SMB/iSCSI fixture", "b": "Core candidate/member"},
                "storage_network": "198.18.77.0/24 private QEMU socket link", "base_image": BASE.name}, indent=2) + "\n")
            print(lab)
            return
        time.sleep(1)
    raise RuntimeError("storage fixture did not become reachable; inspect ignored QEMU serial logs")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prepare", action="store_true")
    args = parser.parse_args()
    assert args.prepare
    prepare()
