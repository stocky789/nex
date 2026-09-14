#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Create a private three-Core shared-block lab.

The fixture owns only one new ``.cache/nex-cluster-ha-lab.*`` directory.  It
uses regular-file overlays, a lab-local VDE UNIX switch and QEMU user NAT;
it does not use libvirt, a host bridge, host mounts, or existing QEMU guests.
The guest packages are prerequisites only.  A cluster-aware Core backend must
still configure the GFS2 resource, fencing and workloads before this lab can
claim shared-block acceptance.
"""
import argparse
import base64
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import uuid


ROOT = Path(__file__).resolve().parents[2]
CACHE = ROOT / ".cache"
BASE = CACHE / "appliance/debian-13-genericcloud-amd64-20260831-2587.qcow2"
PREFIX = "nex-cluster-ha-lab."
CORE_NAMES = ("core1", "core2", "core3")
ALL_NAMES = (*CORE_NAMES, "storage")
MIN_START_AVAILABLE = 6 * 1024**3
PRESSURE_AVAILABLE = 3 * 1024**3
MAX_SWAPOUT_PAGES = 128 * 1024
PRISTINE_HELPER = ROOT / "packaging/core-local/corosync-pristine"


def memory_snapshot():
    values = {}
    for line in Path("/proc/meminfo").read_text().splitlines():
        key, value = line.split(":", 1)
        values[key] = int(value.split()[0]) * 1024
    vmstat = dict(line.split() for line in Path("/proc/vmstat").read_text().splitlines())
    return {"available": values["MemAvailable"], "swap_free": values["SwapFree"],
            "pswpin": int(vmstat["pswpin"]), "pswpout": int(vmstat["pswpout"])}


def require_start_budget():
    snapshot = memory_snapshot()
    assert snapshot["available"] >= MIN_START_AVAILABLE, (
        f"need at least {MIN_START_AVAILABLE // 1024**3} GiB MemAvailable; got "
        f"{snapshot['available'] // 1024**2} MiB")
    return snapshot


def under_real_pressure(before):
    now = memory_snapshot()
    churn = now["pswpout"] - before["pswpout"]
    return now["available"] < PRESSURE_AVAILABLE and churn > MAX_SWAPOUT_PAGES, now


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def check_lab(path):
    assert not path.is_symlink(), "lab path must not be a symlink"
    lab = path.resolve(strict=True)
    assert lab.parent == CACHE.resolve(), "lab must be directly below .cache"
    assert lab.name.startswith(PREFIX) and not lab.is_symlink(), "not this fixture's lab"
    config = json.loads((lab / "lab.json").read_text())
    assert config == {"disposable": True, "fixture": "cluster-ha-v1"}, "not this fixture's lab"
    return lab


def docker(*args):
    return subprocess.run(["sudo", "-n", "docker", "run", "--rm", "--user", f"{os.getuid()}:{os.getgid()}", *args],
                          check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def ssh(lab, name, command, check=True):
    ports = json.loads((lab / "ports.json").read_text())
    return subprocess.run([
        "ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
        "-o", "StrictHostKeyChecking=accept-new", "-o", f"UserKnownHostsFile={lab / 'known_hosts'}",
        "-i", str(lab / "id_ed25519"), "-p", str(ports[name]), "root@127.0.0.1", command],
        check=check, capture_output=True, text=True)


def seed(lab, name, address, public_key, packages):
    directory = lab / f"seed-{name}"
    directory.mkdir(mode=0o700)
    pristine = base64.b64encode(PRISTINE_HELPER.read_bytes()).decode()
    network = (
        "install -d /etc/systemd/system; "
        "printf '%s\\n' '[Unit]' 'Description=Nex cluster lab private VDE link' 'After=network.target' "
        "'[Service]' 'Type=oneshot' 'RemainAfterExit=yes' 'ExecStart=/usr/sbin/ip link set ens4 up' "
        f"'ExecStart=/usr/sbin/ip address replace {address}/24 dev ens4' '[Install]' 'WantedBy=multi-user.target' "
        "> /etc/systemd/system/nex-cluster-lab-link.service; "
        "systemctl daemon-reload; systemctl enable --now nex-cluster-lab-link; "
        "apt-get update -qq; DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends " + packages + "; "
        "install -d -m 0755 /usr/lib/nex; printf '%s' '" + pristine + "' | base64 -d > /usr/lib/nex/corosync-pristine; "
        "chmod 0755 /usr/lib/nex/corosync-pristine; "
        # Fence agents are optional until this profile chooses a standard adapter.  A missing
        # package must not conceal a failed base cluster prerequisite installation.
        "if apt-cache show fence-agents >/dev/null 2>&1; then DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends fence-agents; fi"
    )
    user_data = "#cloud-config\n" + "\n".join((
        f"hostname: nex-cluster-{name}", "manage_etc_hosts: true", "ssh_pwauth: false", "disable_root: false",
        "ssh_authorized_keys:", f"  - {public_key}", "runcmd:", f"  - [ sh, -ec, {json.dumps(network)} ]")) + "\n"
    (directory / "user-data").write_text(user_data)
    (directory / "meta-data").write_text(f"instance-id: nex-cluster-ha-{name}\nlocal-hostname: nex-cluster-{name}\n")
    docker("-v", f"{lab}:{lab}", "--entrypoint", "xorriso", "nex-iso-builder", "-as", "mkisofs", "-quiet",
           "-output", str(lab / f"{name}-seed.iso"), "-volid", "cidata", "-joliet", "-rock", str(directory))
    shutil.rmtree(directory)


def prepare_pristine_corosync(lab, name):
    """Record only the shipped Debian conffile digest before Nex owns clustering."""
    lab = check_lab(lab)
    assert name in CORE_NAMES, "pristine marker is only valid for a disposable Core"
    command = (
        "set -eu; "
        "if virsh -c qemu:///system list --name | grep -q .; then echo running-nested-vm >&2; exit 1; fi; "
        "test -z \"$(findmnt -rn -t gfs2 -o TARGET)\"; "
        "pcs resource status 2>&1 | grep -qx 'NO resources configured'; "
        # Debian owns the daemon as dlm.service.  It must be disabled, not masked:
        # Pacemaker's OCF agent starts dlm_controld directly after Nex has rendered
        # the cluster, while a boot-time systemd instance races that ordering.
        "systemctl stop nex-cluster-local.service corosync.service pacemaker.service dlm.service; "
        "sleep 1; "
        "for unit in nex-cluster-local.service corosync.service pacemaker.service dlm.service; do "
        "if systemctl is-active --quiet \"$unit\"; then echo \"$unit still active\" >&2; exit 1; fi; done; "
        "systemctl unmask dlm.service; systemctl disable dlm.service; "
        "test \"$(systemctl is-enabled dlm.service)\" = disabled; "
        "/usr/lib/nex/corosync-pristine; "
        "source=/var/lib/nex-core-agent/storage-cluster/corosync-unconfigured; "
        "target=/var/lib/nex-cluster-agent/storage-cluster/corosync-unconfigured; "
        "test \"$(cat \"$source\")\" = \"$(sha256sum /etc/corosync/corosync.conf | cut -d ' ' -f 1)\"; "
        "install -D -m 0600 \"$source\" \"$target\"; "
        "test \"$(cat \"$target\")\" = \"$(cat \"$source\")\""
    )
    return ssh(lab, name, command)


def launch(lab, name, memory_mib, vcpus, ssh_port):
    command = [
        "qemu-system-x86_64", "-accel", "kvm", "-cpu", "host", "-m", str(memory_mib), "-smp", str(vcpus),
        "-display", "none", "-monitor", "none", "-serial", f"file:{lab / (name + '-serial.log')}",
        "-qmp", f"unix:{lab / (name + '-qmp.sock')},server=on,wait=off",
        "-drive", f"file={lab / (name + '.qcow2')},format=qcow2,if=virtio",
        "-drive", f"file={lab / (name + '-seed.iso')},format=raw,media=cdrom,readonly=on",
        "-netdev", f"user,id=nat,hostfwd=tcp:127.0.0.1:{ssh_port}-:22",
        "-device", f"virtio-net-pci,netdev=nat,mac=52:54:00:96:10:{ALL_NAMES.index(name)+1:02x}",
        "-netdev", f"vde,id=private,sock={lab / 'vde'}",
        "-device", f"virtio-net-pci,netdev=private,mac=52:54:00:96:11:{ALL_NAMES.index(name)+1:02x}",
    ]
    (lab / f"{name}-command.json").write_text(json.dumps(command, indent=2) + "\n")
    with (lab / f"{name}-qemu.log").open("w") as log:
        proc = subprocess.Popen(command, stdout=log, stderr=log, start_new_session=True)
    (lab / f"{name}-qemu.pid").write_text(str(proc.pid) + "\n")
    (lab / f"{name}-qemu.process.json").write_text(json.dumps(
        {"pid": proc.pid, "starttime": process_starttime(proc.pid), "command": command}) + "\n")
    return proc


def process_starttime(pid):
    # /proc/PID/stat's second field may contain spaces, so split after its final ')'.
    return int(Path(f"/proc/{pid}/stat").read_text().rsplit(") ", 1)[1].split()[19])


def owned_process(metadata):
    pid = metadata["pid"]
    try:
        command = Path(f"/proc/{pid}/cmdline").read_bytes().rstrip(b"\0").split(b"\0")
        command = [item.decode() for item in command]
        return (process_starttime(pid) == metadata["starttime"] and command == metadata["command"]
                and os.getpgid(pid) == pid)
    except (FileNotFoundError, ProcessLookupError):
        return False


def process_exists(pid):
    return Path(f"/proc/{pid}").exists()


def quit_owned(lab, name):
    """Terminate only a named QEMU process whose recorded command is entirely in this lab."""
    command = json.loads((lab / f"{name}-command.json").read_text())
    assert all(str(lab) in item or not item.startswith(("file=", "unix:", "sock=")) for item in command), "unsafe target"
    metadata = json.loads((lab / f"{name}-qemu.process.json").read_text())
    if not process_exists(metadata["pid"]):
        return
    assert metadata["command"] == command and owned_process(metadata), "refusing a reused or foreign PID"
    os.killpg(metadata["pid"], signal.SIGTERM)
    for _ in range(20):
        if not process_exists(metadata["pid"]):
            return
        time.sleep(.25)
    # Revalidate immediately before escalation: PID reuse must never turn this into a broad kill.
    if not process_exists(metadata["pid"]):
        return
    assert owned_process(metadata), "refusing a reused or foreign PID"
    os.killpg(metadata["pid"], signal.SIGKILL)


def cleanup(lab):
    lab = check_lab(lab)
    for name in ALL_NAMES:
        if (lab / f"{name}-qemu.pid").is_file():
            quit_owned(lab, name)
    pidfile = lab / "vde-switch.pid"
    if pidfile.is_file():
        metadata = json.loads((lab / "vde-switch.process.json").read_text())
        if not process_exists(metadata["pid"]):
            return
        assert owned_process(metadata), "refusing a reused or foreign VDE PID"
        os.killpg(metadata["pid"], signal.SIGTERM)


def domain_xml(lab, name, domain_id, ssh_port):
    index = ALL_NAMES.index(name) + 1
    qemu = [
        ("-netdev", f"user,id=nat,hostfwd=tcp:127.0.0.1:{ssh_port}-:22"),
        ("-device", f"virtio-net-pci,netdev=nat,mac=52:54:00:96:10:{index:02x},addr=0x10"),
        ("-netdev", f"vde,id=private,sock=/lab/vde"),
        ("-device", f"virtio-net-pci,netdev=private,mac=52:54:00:96:11:{index:02x},addr=0x11"),
    ]
    args = "".join(f'<qemu:arg value="{value}"/><qemu:arg value="{argument}"/>' for value, argument in qemu)
    memory = 3072 if name in CORE_NAMES else 2048
    return f'''<domain type="kvm" xmlns:qemu="http://libvirt.org/schemas/domain/qemu/1.0">
  <name>nex-cluster-{name}</name><uuid>{domain_id}</uuid><memory unit="MiB">{memory}</memory><vcpu>2</vcpu>
  <os><type arch="x86_64">hvm</type></os><features><acpi/><apic/></features><cpu mode="host-passthrough"/>
  <devices><emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type="file" device="disk"><driver name="qemu" type="qcow2"/><source file="/lab/{name}.qcow2"/><target dev="vda" bus="virtio"/></disk>
    <serial type="file"><source path="/lab/{name}-libvirt-serial.log"/><target port="0"/></serial>
    <video><model type="vga"/></video>
  </devices><qemu:commandline>{args}</qemu:commandline></domain>'''


def libvirt_container(lab):
    """Own only this lab's outer Core domains in a private, host-network container."""
    lab = check_lab(lab)
    ports = json.loads((lab / "ports.json").read_text())
    name = "nex-cluster-ha-libvirt-" + lab.name.rsplit(".", 1)[1]
    profile_path = lab / "fence-profile.json"
    if profile_path.is_file():
        return json.loads(profile_path.read_text())["domains"]
    fence_port = free_port()
    fence_key = lab / "fence_id_ed25519"
    if not fence_key.is_file():
        subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "nex-cluster-ha-fence", "-f", str(fence_key)], check=True)
    exists = subprocess.run(["sudo", "-n", "docker", "inspect", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0
    if exists:
        # A terminal timeout can interrupt apt output after the container is safely created.
        # Reuse only this exact labelled, lab-mounted container.
        labels = subprocess.run(["sudo", "-n", "docker", "inspect", "--format", "{{index .Config.Labels \"nex.test\"}}", name],
                                check=True, capture_output=True, text=True).stdout.strip()
        assert labels == "cluster-ha", "foreign libvirt container"
    else:
        subprocess.run(["sudo", "-n", "docker", "run", "--init", "--detach", "--name", name, "--label", "nex.test=cluster-ha",
                        "--network", "host", "--device", "/dev/kvm", "--security-opt", "apparmor=unconfined",
                        "--mount", f"type=bind,src={lab},dst=/lab",
                        "--mount", f"type=bind,src={BASE},dst={BASE},readonly", "debian:13-slim", "sleep", "infinity"], check=True,
                       stdout=subprocess.DEVNULL)
    try:
        setup = ("apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "
                 "libvirt-daemon-system libvirt-clients qemu-system-x86 openssh-server && "
                 "mkdir -p /run/sshd /root/.ssh /etc/libvirt && printf '%s\\n' 'security_driver = \"none\"' 'dynamic_ownership = 0' 'remember_owner = 0' 'user = \"1000\"' 'group = \"993\"' 'cgroup_controllers = []' > /etc/libvirt/qemu.conf && "
                 "pkill -x libvirtd || true; sleep 1; chmod 700 /root/.ssh && "
                 "cat /lab/fence_id_ed25519.pub > /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys && "
                 f"printf '%s\\n' 'Port {fence_port}' 'ListenAddress 127.0.0.1' 'PermitRootLogin prohibit-password' 'PasswordAuthentication no' > /etc/ssh/sshd_config.d/nex-lab.conf && "
                 "pgrep -x virtlogd >/dev/null || virtlogd -d; "
                 "pgrep -x libvirtd >/dev/null || libvirtd -d; pgrep -x sshd >/dev/null || /usr/sbin/sshd")
        subprocess.run(["sudo", "-n", "docker", "exec", name, "sh", "-ec", setup], check=True)
        domains = {}
        for core in CORE_NAMES:
            domain_name = f"nex-cluster-{core}"
            prior = subprocess.run(["sudo", "-n", "docker", "exec", name, "virsh", "-c", "qemu:///system", "domuuid", domain_name],
                                   capture_output=True, text=True)
            if prior.returncode == 0:
                domain_id = prior.stdout.strip()
            else:
                domain_id = str(uuid.uuid4())
                xml = domain_xml(lab, core, domain_id, ports[core])
                (lab / f"{core}.xml").write_text(xml)
                subprocess.run(["sudo", "-n", "docker", "exec", name, "virsh", "-c", "qemu:///system", "define", f"/lab/{core}.xml"], check=True)
            state = subprocess.run(["sudo", "-n", "docker", "exec", name, "virsh", "-c", "qemu:///system", "domstate", domain_name],
                                   check=True, capture_output=True, text=True).stdout.strip()
            if state != "running":
                subprocess.run(["sudo", "-n", "docker", "exec", name, "virsh", "-c", "qemu:///system", "start", domain_name], check=True)
            domains[core] = domain_id
        # The lab key is copied only into the newly restarted disposable Cores.
        for core in CORE_NAMES:
            for _ in range(90):
                if ssh(lab, core, "true", check=False).returncode == 0:
                    break
                time.sleep(1)
            else:
                raise RuntimeError(f"{core} did not return after private libvirt restart")
            subprocess.run(["scp", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new",
                            "-o", f"UserKnownHostsFile={lab / 'known_hosts'}", "-i", str(lab / "id_ed25519"), "-P", str(ports[core]),
                            str(fence_key), f"root@127.0.0.1:/root/.ssh/nex-cluster-fence"], check=True)
            ssh(lab, core, "chmod 600 /root/.ssh/nex-cluster-fence")
        (lab / "fence-profile.json").write_text(json.dumps({"endpoint": "10.0.2.2", "port": fence_port,
            "login": "root", "identity_file": "/root/.ssh/nex-cluster-fence", "domains": domains,
            "container": name, "disposable": True}, indent=2) + "\n")
        return domains
    except BaseException:
        # Retain the labelled container and its logs for inspection/retry.
        raise


def resume_storage(lab):
    """Restart only this fixture's storage VM and its private VDE switch."""
    lab = check_lab(lab)
    shutil.rmtree(lab / "vde", ignore_errors=True)
    switch_command = ["vde_switch", "-s", str(lab / "vde"), "-m", "0700", "--dirmode", "0700", "--nostdin"]
    with (lab / "vde-switch.log").open("a") as log:
        switch = subprocess.Popen(switch_command, stdout=log, stderr=log, start_new_session=True)
    (lab / "vde-switch.pid").write_text(str(switch.pid) + "\n")
    (lab / "vde-switch.process.json").write_text(json.dumps(
        {"pid": switch.pid, "starttime": process_starttime(switch.pid), "command": switch_command}) + "\n")
    ports = json.loads((lab / "ports.json").read_text())
    proc = launch(lab, "storage", 2048, 2, ports["storage"])
    for _ in range(90):
        if proc.poll() is not None:
            raise RuntimeError("storage QEMU exited; inspect storage-qemu.log")
        if ssh(lab, "storage", "true", check=False).returncode == 0:
            print(lab)
            return
        time.sleep(1)
    raise RuntimeError("storage did not return")


def prepare():
    assert BASE.is_file() and not BASE.is_symlink(), "missing cached Debian cloud image"
    require_start_budget()
    lab = Path(tempfile.mkdtemp(prefix=PREFIX, dir=CACHE))
    lab.chmod(0o700)
    (lab / "lab.json").write_text(json.dumps({"disposable": True, "fixture": "cluster-ha-v1"}) + "\n")
    subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "nex-cluster-ha-lab", "-f", str(lab / "id_ed25519")], check=True)
    key = (lab / "id_ed25519.pub").read_text().strip()
    ports = {name: free_port() for name in ALL_NAMES}
    (lab / "ports.json").write_text(json.dumps(ports, indent=2) + "\n")
    core_packages = "corosync pacemaker dlm-controld gfs2-utils open-iscsi qemu-system-x86 qemu-utils nfs-common cifs-utils busybox-static cpio e2fsprogs"
    storage_packages = "nfs-kernel-server samba targetcli-fb open-iscsi e2fsprogs"
    for index, name in enumerate(CORE_NAMES, 11):
        seed(lab, name, f"198.18.90.{index}", key, core_packages)
    seed(lab, "storage", "198.18.90.10", key, storage_packages)
    for name in ALL_NAMES:
        docker("-v", f"{BASE}:{BASE}:ro", "-v", f"{lab}:{lab}", "--entrypoint", "qemu-img", "nex-appliance-builder",
               "create", "-f", "qcow2", "-F", "qcow2", "-b", str(BASE), str(lab / f"{name}.qcow2"), "32G")
    with (lab / "vde-switch.log").open("w") as log:
        switch_command = ["vde_switch", "-s", str(lab / "vde"), "-m", "0700", "--dirmode", "0700", "--nostdin"]
        switch = subprocess.Popen(switch_command,
                                  stdout=log, stderr=log, start_new_session=True)
    (lab / "vde-switch.pid").write_text(str(switch.pid) + "\n")
    (lab / "vde-switch.process.json").write_text(json.dumps(
        {"pid": switch.pid, "starttime": process_starttime(switch.pid), "command": switch_command}) + "\n")
    time.sleep(.2)
    if switch.poll() is not None:
        raise RuntimeError((lab / "vde-switch.log").read_text(errors="replace"))
    before = memory_snapshot()
    processes = {name: launch(lab, name, 3072 if name in CORE_NAMES else 2048, 2, ports[name]) for name in ALL_NAMES}
    try:
        for _ in range(360):
            if any(proc.poll() is not None for proc in processes.values()):
                raise RuntimeError("new fixture QEMU exited; inspect its qemu.log")
            pressured, observed = under_real_pressure(before)
            if pressured:
                raise RuntimeError(f"real host memory pressure: {observed}")
            ready = [ssh(lab, name, "test -f /var/lib/cloud/instance/boot-finished && ip -4 addr show ens4 | grep -q 198.18.90", check=False).returncode == 0 for name in ALL_NAMES]
            if all(ready):
                break
            time.sleep(1)
        else:
            raise RuntimeError("cluster fixture did not finish cloud-init")
        # Verify only the disposable private fabric and nested KVM prerequisite here.
        for name in CORE_NAMES:
            assert ssh(lab, name, "test -c /dev/kvm; ping -c1 -W1 198.18.90.10 >/dev/null").returncode == 0
            prepare_pristine_corosync(lab, name)
        (lab / "ready.json").write_text(json.dumps({
            "disposable": True, "cores": list(CORE_NAMES), "core_memory_mib": 3072,
            "storage_memory_mib": 2048, "private_network": "lab-local VDE UNIX socket",
            "workload_plan": "six VMs per backend: NFS, SMB, then one GFS2 iSCSI LUN",
            "fencing": "pending standard fence_virsh/fence_virt profile selection",
            "memory_before": before, "memory_after": memory_snapshot()}, indent=2) + "\n")
        print(lab)
        return lab
    except BaseException:
        cleanup(lab)
        raise


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prepare", action="store_true")
    parser.add_argument("--cleanup", type=Path)
    parser.add_argument("--libvirt-container", type=Path)
    parser.add_argument("--resume-storage", type=Path)
    args = parser.parse_args()
    assert sum(bool(value) for value in (args.prepare, args.cleanup, args.libvirt_container, args.resume_storage)) == 1, "choose exactly one action"
    if args.prepare:
        prepare()
    elif args.cleanup:
        cleanup(args.cleanup)
    else:
        if args.libvirt_container:
            print(json.dumps(libvirt_container(args.libvirt_container), indent=2))
        else:
            resume_storage(args.resume_storage)
