#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Three booted image hosts, one pool, one rollout: the nested lab for the pool rollout coordinator.

Needs releases built by image/build.sh from one nex-core-local deb (sequence 1 and 2 at `up`, more later with
`publish`; the same pinned update trust, NEX_PACKAGE_DIRS=<image-edition deb dir>) and the development keys of
scripts/update-publish.py keygen. Three QEMU/KVM guests (-smp 2 -m 2048 each; the host that carries the managed
guest gets more) boot release 1 from three fresh single disks. Each has two NICs: QEMU user-mode networking
(10.0.2.15 in every guest; the workstation is 10.0.2.2: update server, Controller, lab management controller, and
the host-local API reached through a loopback forward) and a second NIC on an L2 hub the script runs in userspace
(QEMU `-netdev stream`, no root, no tap), 10.77.0.11-13, which carries the pool. The default route is on the hub
NIC, so Core takes that address as the management/pool-peer address.

The Controller is the real nex-controller binary run on the workstation as a process (the Controller guest image
is not built); it enrols the three hosts and joins hosts 2 and 3 to host 1's pool. Every rollout, promotion and VM
action goes through the Controller's routes; the master's host-local API is only read.

  pool_rollout_lab.py up --rel1 D/release --rel2 D/release --keys DIR --trust FILE --hostimage F   install, boot, pin
  pool_rollout_lab.py login | enroll | layer | fence   passwords, Controller enrolment and pool join, nex-cluster layer, BMC profiles
  pool_rollout_lab.py publish --release D/release   add one more release to the update tree (the channel moves to it)
  pool_rollout_lab.py guest GUEST.raw              a Nex-managed VM on host 2 that answers ACPI shutdown
  pool_rollout_lab.py rollout [--running-vms refuse|shutdown] [--expect finalized|paused|aborted|any] [--kill-controller] [--cut-master-at b:soak]
  pool_rollout_lab.py promote N                    preflight, then promote host N through the Controller (the master is gone)
  pool_rollout_lab.py control pause|resume|abort [--rollout-id ID]   pause, resume or abort the rollout through the Controller
  pool_rollout_lab.py boot N | cut N               start / power-cut host N's VM
  pool_rollout_lab.py watch | api N METHOD PATH [JSON] | capi METHOD PATH [JSON] | sh N CMD
  pool_rollout_lab.py resume | down                start the same disks again | kill everything and verify with pgrep

Resource rules (shared machine): run `up` under one systemd-run --user --scope (CPUQuota=600% MemoryMax=8G
MemorySwapMax=0, nice -n 19 ionice -c3); all QEMU processes stay in that scope. At most 3 VMs, never a fourth.
"""
import argparse
import http.cookiejar
import json
import os
import secrets
import shutil
import signal
import socket
import ssl
import struct
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import image_boot as lab  # noqa: E402
import image_update as upd  # noqa: E402  (Ctl, Server, publish, check, info)

REPO = lab.REPO
LAB = os.path.join(REPO, ".cache/lab4")
WORK = os.path.join(LAB, "run")
HOSTS = [
    {"n": 1, "name": "nexlab-a", "ip": "10.77.0.11", "loop": "127.0.0.11"},
    # nexlab-b hosts the guest: Core starts a VM only with guest RAM + 2 GiB host reserve available, so it gets 3.25 GiB (owner-approved
    # deviation: 3 GiB leaves MemAvailable below guest + 2 GiB), and it creates one only with the disk + 4 GiB free on /var, so 24 GiB
    {"n": 2, "name": "nexlab-b", "ip": "10.77.0.12", "loop": "127.0.0.12", "mem": 3328, "disk": 24 * 1024**3},
    {"n": 3, "name": "nexlab-c", "ip": "10.77.0.13", "loop": "127.0.0.13"},
]
CTRL_PORT = 18443
RUN = time.strftime("r%m%d%H%M%S")

SEED = r"""
set -eu
mkdir -p /seed/nex /seed/systemd/system/multi-user.target.wants /seed/systemd/system-preset
printf '%(name)s\n' > /seed/hostname
printf 'base_url=http://10.0.2.2:%(port)s\nchannel=dev\n' > /seed/nex/updates.conf
# the six-hour step timeout cannot be watched in a lab: a drop-in, in the image's own /etc overlay, makes it %(step_timeout)s
mkdir -p /seed/systemd/system/nex-core-local.service.d
printf '[Service]\nEnvironment=NEX_POOL_UPDATE_STEP_TIMEOUT=%(step_timeout)s\n' > /seed/systemd/system/nex-core-local.service.d/lab.conf
printf 'enable nex-lab-shell.service\n' > /seed/systemd/system-preset/00-nex-lab.preset
ln -s /etc/systemd/system/nex-lab-shell.service /seed/systemd/system/multi-user.target.wants/nex-lab-shell.service
cat > /seed/nex-lab-net.sh <<'EOT'
#!/bin/sh
# Lab network, static: user-mode NIC (10.0.2.15, workstation 10.0.2.2) and the L2 hub NIC (pool, default route).
U=; L=
for d in /sys/class/net/*; do
  case "$(cat $d/address)" in 52:54:00:55:*) U=${d##*/};; 52:54:00:77:*) L=${d##*/};; esac
done
ip link set "$U" up; ip addr add 10.0.2.15/24 dev "$U" 2> /dev/null || true
ip link set "$L" up; ip addr add %(ip)s/24 dev "$L" 2> /dev/null || true
ip route replace default via 10.77.0.1 dev "$L" onlink
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
"""


def path(*p):
    return os.path.join(WORK, *p)


def state():
    with open(path("lab.json")) as f:
        return json.load(f)


def save(st):
    tmp = path("lab.json.new")
    with open(tmp, "w") as f:
        json.dump(st, f, indent=1)
    os.chmod(tmp, 0o600)
    os.replace(tmp, path("lab.json"))


# ---- the L2 hub: QEMU `-netdev stream` frames (4-byte big-endian length + frame), flooded to every other peer ----

def run_hub(sock_path):
    if os.path.exists(sock_path):
        os.unlink(sock_path)
    srv = socket.socket(socket.AF_UNIX)
    srv.bind(sock_path)
    srv.listen(8)
    peers, lock = [], threading.Lock()

    def recvall(c, n):
        buf = b""
        while len(buf) < n:
            chunk = c.recv(n - len(buf))
            if not chunk:
                raise EOFError
            buf += chunk
        return buf

    def pump(c, mine):
        try:
            while True:
                hdr = recvall(c, 4)
                frame = recvall(c, struct.unpack(">I", hdr)[0])
                with lock:
                    others = [p for p in peers if p[0] is not c]
                for o, olock in others:
                    try:
                        with olock:
                            o.sendall(hdr + frame)
                    except OSError:
                        pass
        except (EOFError, OSError):
            pass
        finally:
            with lock:
                if mine in peers:
                    peers.remove(mine)
            c.close()

    while True:
        c, _ = srv.accept()
        mine = (c, threading.Lock())
        with lock:
            peers.append(mine)
        threading.Thread(target=pump, args=(c, mine), daemon=True).start()


def run_serve(root, port):
    server = upd.Server(root)
    server.server_close()
    import http.server
    http.server.HTTPServer.__init__(server, ("127.0.0.1", int(port)), upd.RangeHandler)
    server.serve_forever()


# ---- a lab management controller: Redfish for the three machines, and a ForceOff that really kills the VM ----

BMC_PORT = 18444
BMC_USER, BMC_SECRET = "root", "lab-bmc-secret"


def run_bmc(port):
    """Redfish ComputerSystem for sys-1..3 (the SMBIOS UUIDs the VMs are started with). GET reports On while the host's
    QEMU process lives; POST .../ComputerSystem.Reset {"ResetType":"ForceOff"} kills it (kill -9) like a BMC cutting power."""
    import base64
    import http.server
    want = "Basic " + base64.b64encode(f"{BMC_USER}:{BMC_SECRET}".encode()).decode()

    def pid(n):
        return read_pidfile(path(f"h{n}/qemu.pid"), "qemu-system")

    class H(http.server.BaseHTTPRequestHandler):
        def log_message(self, fmt, *args):
            sys.stderr.write(time.strftime("%H:%M:%S ") + (fmt % args) + "\n")
            sys.stderr.flush()

        def reply(self, code, body=None):
            raw = json.dumps(body or {}).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def system(self):
            parts = self.path.strip("/").split("/")
            if parts[:3] != ["redfish", "v1", "Systems"] or len(parts) < 4 or not parts[3].startswith("sys-"):
                return None, parts
            n = int(parts[3][4:])
            return (n if 1 <= n <= 3 else None), parts

        def do_GET(self):
            if self.headers.get("Authorization") != want:
                return self.reply(401)
            n, parts = self.system()
            if n is None or len(parts) != 4:
                return self.reply(404)
            self.reply(200, {"Id": parts[3], "UUID": f"5e1ec7ed-1ab0-4000-8000-0000000000{n:02x}", "PowerState": "On" if pid(n) else "Off"})

        def do_POST(self):
            self.rfile.read(int(self.headers.get("Content-Length") or 0))
            if self.headers.get("Authorization") != want:
                return self.reply(401)
            n, parts = self.system()
            if n is None or parts[4:] != ["Actions", "ComputerSystem.Reset"]:
                return self.reply(404)
            p = pid(n)
            if p and kill_qemu(path(f"h{n}/qemu.pid")):
                sys.stderr.write(f"ForceOff sys-{n}: killed QEMU {p}\n")
            self.reply(204)

    bdir = path("bmc")
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", port), H)
    import ssl as _ssl
    ctx = _ssl.SSLContext(_ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(f"{bdir}/tls.crt", f"{bdir}/tls.key")
    srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
    srv.serve_forever()


def start_bmc(st):
    bdir = path("bmc")
    if not os.path.exists(f"{bdir}/tls.crt"):
        os.makedirs(bdir, exist_ok=True)
        subprocess.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-days", "2",
                        "-subj", "/CN=nexlab-bmc", "-keyout", f"{bdir}/tls.key", "-out", f"{bdir}/tls.crt"], check=True, capture_output=True)
    p = spawn([sys.executable, os.path.abspath(__file__), "bmc-serve", str(BMC_PORT)], path("bmc.log"))
    st["pids"].append(p.pid)
    st["bmc_pid"] = p.pid
    save(st)
    for _ in range(50):
        try:
            socket.create_connection(("127.0.0.1", BMC_PORT), 1).close()
            return
        except OSError:
            time.sleep(0.2)
    raise RuntimeError("the lab BMC did not listen")


def cmd_fence(a):
    """Start the lab BMC and record a Redfish fence profile for every host through the Controller (the product's route)."""
    st = state()
    if not st.get("bmc_pid"):
        start_bmc(st)
    c = ctrl_api(st)
    c.login()
    pool = st["hosts"][0]["pool_id"]
    for h in st["hosts"]:
        status, r = c.call("POST", f"/api/v1/pools/{pool}/members/{h['host_id']}/fence-profile", {
            "request_id": f"fence-{h['name']}-{RUN}",
            "fence": {"kind": "redfish", "endpoint": f"10.0.2.2:{BMC_PORT}", "target_id": f"sys-{h['n']}", "username": BMC_USER},
            "credential": {"secret": BMC_SECRET}}, timeout=200)
        upd.check(status in (200, 202), f"fence profile recorded for {h['name']}", (status, r))
        print(json.dumps(r)[:400])


LAYER_BOOTSTRAP = r"""
import urllib.request,json,os
P=%(port)d
b=[l.split('=',1)[1].strip().strip('"') for l in open('/usr/lib/os-release') if l.startswith('BUILD_ID=')][0]
seq=b.rsplit('-',1)[1]
d='/var/lib/nex/layers/releases/'+b
os.makedirs(d,exist_ok=True)
def get(u,t): urllib.request.urlretrieve('http://10.0.2.2:%%d/%%s'%%(P,u),t)
get('releases/%%s/manifest.json'%%seq,d+'/manifest.json'); get('releases/%%s/manifest.sig'%%seq,d+'/manifest.sig')
m=json.load(open(d+'/manifest.json'))
c=[x for x in m['components'] if x['kind']=='layer' and x['id']=='nex-cluster'][0]
os.makedirs('/var/lib/nex/layers/store',exist_ok=True)
t='/var/lib/nex/layers/store/sha256-%%s.raw'%%c['sha256']
get('blobs/sha256-'+c['sha256'],t+'.part'); os.rename(t+'.part',t)
print('stored',b,c['sha256'][:12],c['bytes'])
"""


def put_text(host, remote, text):
    import base64
    b64 = base64.b64encode(text.encode()).decode()
    host.shell().sh(f": > {remote}.b64", 30)
    for i in range(0, len(b64), 1200):
        host.shell().sh(f"printf %s '{b64[i:i + 1200]}' >> {remote}.b64", 30)
    host.shell().sh(f"base64 -d {remote}.b64 > {remote} && rm {remote}.b64", 30)


def cmd_layer(a):
    """Lay the signed release manifest and the layer file of the running release into each host's layer store (what an
    engine stage leaves) and live-merge nex-cluster with `nex-core-local layers ensure`."""
    st = state()
    for h in st["hosts"]:
        host = Host(h, st)
        put_text(host, "/tmp/layer-bootstrap.py", LAYER_BOOTSTRAP % {"port": st["http_port"]})
        rc, out = host.shell().sh("python3 /tmp/layer-bootstrap.py", 300)
        print(h["name"], rc, out[-300:])
        rc, out = host.shell().sh("/usr/lib/nex/nex-core-local layers ensure nex-cluster; echo rc=$?; systemd-sysext status | tail -4; corosync -v | head -1", 300)
        print(h["name"], out[-600:])


# ---- HTTPS clients ----

class Api:
    def __init__(self, base, user, password, login_path):
        self.base, self.user, self.password, self.login_path = base, user, password, login_path
        ctx = ssl.create_default_context()
        ctx.check_hostname, ctx.verify_mode = False, ssl.CERT_NONE
        self.opener = urllib.request.build_opener(urllib.request.HTTPSHandler(context=ctx),
                                                  urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.csrf = None

    def call(self, method, p, body=None, timeout=30, _retry=True):
        headers = {"Content-Type": "application/json", "Origin": self.base}
        if self.csrf:
            headers["X-CSRF-Token"] = self.csrf
        req = urllib.request.Request(self.base + p, method=method, headers=headers,
                                     data=None if body is None else json.dumps(body).encode())
        try:
            with self.opener.open(req, timeout=timeout) as r:
                raw, status = r.read(), r.status
        except urllib.error.HTTPError as e:
            raw, status = e.read(), e.code
        except (urllib.error.URLError, socket.timeout, ConnectionError, OSError) as e:
            return 0, {"transport_error": str(e)}
        try:
            out = json.loads(raw) if raw else None
        except ValueError:
            out = raw.decode(errors="replace")
        if status == 401 and p != self.login_path and _retry:
            self.login()
            return self.call(method, p, body, timeout, False)
        return status, out

    def login(self):
        status, body = self.call("POST", self.login_path, {"username": self.user, "password": self.password}, _retry=False)
        if status != 200:
            raise RuntimeError(f"login {self.base}: {status} {body}")
        self.csrf = body.get("csrf_token")
        return body


def host_api(st, n):
    h = st["hosts"][n - 1]
    return Api(f"https://{h['loop']}:9443", "root", h["password"], "/api/local/v1/session")


def ctrl_api(st):
    return Api(f"https://127.0.0.1:{CTRL_PORT}", "root", st["controller_password"], "/api/v1/session")


# ---- VM ----

class Host:
    def __init__(self, h, st):
        self.h, self.dir = h, path(f"h{h['n']}")
        self.ctl = None

    def qemu_cmd(self):
        h, d = self.h, self.dir
        n = h["n"]
        return ["nice", "-n", "19", "ionice", "-c3", "qemu-system-x86_64", "-enable-kvm", "-cpu", "host", "-machine", "q35",
                "-m", str(h.get("mem", 2048)), "-smp", "2", "-display", "none", "-monitor", "none",
                "-uuid", f"5e1ec7ed-1ab0-4000-8000-0000000000{n:02x}",
                "-serial", f"file:{d}/console.log", "-serial", f"unix:{d}/ctl.sock,server=on,wait=off",
                "-qmp", f"unix:{d}/qmp.sock,server=on,wait=off",
                "-drive", f"if=pflash,format=raw,unit=0,file={lab.OVMF_CODE},readonly=on",
                "-drive", f"if=pflash,format=raw,unit=1,file={d}/vars.fd",
                "-drive", f"if=none,id=d0,format=raw,file={d}/d0.img,cache=none",
                "-device", "virtio-blk-pci,drive=d0,serial=nexdisk0,bootindex=1",
                "-netdev", f"user,id=u0,hostfwd=tcp:{h['loop']}:9443-{h['ip']}:9443",
                "-device", f"virtio-net-pci,netdev=u0,mac=52:54:00:55:00:{n:02x}",
                "-netdev", f"stream,id=l0,addr.type=unix,addr.path={path('hub.sock')},server=off,reconnect-ms=1000",
                "-device", f"virtio-net-pci,netdev=l0,mac=52:54:00:77:00:{n:02x}",
                "-device", "virtio-rng-pci", "-pidfile", f"{d}/qemu.pid"]

    def start(self):
        d = self.dir
        for stale in (f"{d}/ctl.sock", f"{d}/qmp.sock"):
            if os.path.exists(stale):
                os.unlink(stale)
        with open(f"{d}/qemu.err", "ab") as err:
            subprocess.Popen(self.qemu_cmd(), stdin=subprocess.DEVNULL, stdout=err, stderr=err, start_new_session=True)
        self.ctl = None

    def shell(self):
        if self.ctl is None:
            self.ctl = upd.Ctl(f"{self.dir}/ctl.sock", open(f"{self.dir}/control.log", "a"))
        return self.ctl

    def console(self):
        try:
            with open(f"{self.dir}/console.log", errors="replace") as f:
                return f.read()
        except OSError:
            return ""

    def pid(self):
        return read_pidfile(f"{self.dir}/qemu.pid", "qemu-system")

    def alive(self):
        return self.pid() is not None

    def booted(self):
        """The kernel has started since the firmware last did. The lab shell is a second serial port that the firmware also reads
        as keyboard, so a probe sent while systemd-boot shows its menu is a keystroke: it picked "Reboot Into Firmware Interface"
        and held host a in a boot loop until `up` gave up."""
        log = self.console()
        return log.rfind("Linux version") > log.rfind("BdsDxe: starting")

    def wait_shell(self, timeout, seen):
        end = time.time() + timeout
        while time.time() < end:
            c = self.shell() if os.path.exists(f"{self.dir}/ctl.sock") and self.booted() else None
            if c is not None:
                try:
                    c.send("echo BOOTID=$(cat /proc/sys/kernel/random/boot_id)")
                except OSError:
                    self.ctl = None
                    time.sleep(1)
                    continue
                c._read(time.time() + 2)
                import re
                for found in re.findall(r"BOOTID=([0-9a-f-]{36})", c.buf):
                    if found not in seen:
                        seen.add(found)
                        c.drain()
                        return True
                c.buf = c.buf[-4000:]
            time.sleep(1)
        return False


def install_disk(h, port, release, hostimage, timeout, step_timeout):
    script = (SEED % {"name": h["name"], "ip": h["ip"], "port": port, "step_timeout": step_timeout}) + """
set -eu
l=$(losetup -fP --show /w/d0.img)
trap 'losetup -d $l' EXIT
nex-hostimage install --layout single --disks $l --image-dir /rel --etc-seed /seed
"""
    lab.run_install(path(f"h{h['n']}"), release, hostimage, script, timeout, f"pool-lab-{h['n']}")


def spawn(args, log, **kw):
    with open(log, "ab") as f:
        return subprocess.Popen(args, stdin=subprocess.DEVNULL, stdout=f, stderr=f, start_new_session=True, **kw)


def cmd_up(a):
    check = upd.check
    if os.path.exists(path("lab.json")):
        sys.exit("a lab already exists in " + WORK + "; run `down` first")
    for need in (lab.OVMF_CODE, shutil.which("docker"), shutil.which("qemu-system-x86_64"), shutil.which("openssl")):
        if not need:
            sys.exit("missing a prerequisite (OVMF, docker, qemu, openssl)")
    rel1, rel2 = os.path.abspath(a.rel1), os.path.abspath(a.rel2)
    hostimage = os.path.abspath(a.hostimage)
    shutil.rmtree(WORK, ignore_errors=True)
    os.makedirs(WORK)
    st = {"started": time.time(), "pids": [], "hosts": [dict(h, password=secrets.token_urlsafe(16)) for h in HOSTS],
          "controller_password": secrets.token_urlsafe(16)}
    save(st)

    # 1. the release tree and the update server
    source = path("source.tar.gz")
    subprocess.run(["tar", "czf", source, "-C", REPO, "internal/nexupdate", "scripts/update-publish.py"], check=True)
    pub = publish_releases(WORK, [rel1, rel2], a.keys, os.path.abspath(a.trust), source)
    st.update(keys=os.path.abspath(a.keys), trust=os.path.abspath(a.trust), source=source)
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]
    st["http_port"] = port
    start_services(st)
    save(st)
    upd.info(f"update tree on 127.0.0.1:{port} (guests see 10.0.2.2:{port}); hub {path('hub.sock')}")

    # 2. the Controller (workstation process)
    cdir = path("controller")
    os.makedirs(cdir)
    subprocess.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-days", "2",
                    "-subj", "/CN=nexlab-controller", "-addext", "subjectAltName=IP:127.0.0.1,IP:10.0.2.2",
                    "-keyout", f"{cdir}/tls.key", "-out", f"{cdir}/tls.crt"], check=True, capture_output=True)
    with open(f"{cdir}/admin.pw", "w") as f:
        f.write(st["controller_password"])
    os.chmod(f"{cdir}/admin.pw", 0o600)
    os.makedirs(f"{cdir}/ui")
    open(f"{cdir}/ui/index.html", "w").write("<html></html>")
    start_controller(st)
    save(st)

    # 3. disks, installs, boots
    hosts = []
    for h in st["hosts"]:
        d = path(f"h{h['n']}")
        os.makedirs(d)
        with open(f"{d}/d0.img", "wb") as f:
            f.truncate(h.get("disk", lab.DISK_BYTES))
        shutil.copy(lab.OVMF_VARS, f"{d}/vars.fd")
        t0 = time.time()
        install_disk(h, port, rel1, hostimage, a.budget, a.step_timeout)
        upd.info(f"{h['name']}: install {time.time() - t0:.0f}s")
        hosts.append(Host(h, st))
    for host in hosts:
        host.start()
    seen = set()
    for host in hosts:
        ok = host.wait_shell(a.budget, seen)
        check(ok, f"{host.h['name']} reached the lab shell")
        if not ok:
            sys.exit("a host did not boot; console tail:\n" + host.console()[-1500:])
        host.shell().sh(f"echo 'root:{host.h['password']}' | chpasswd")
    for host in hosts:
        end = time.time() + 240
        while time.time() < end:
            s, _ = host_api(st, host.h["n"]).call("GET", "/healthz", timeout=5)
            if s == 200:
                break
            time.sleep(3)
        check(s == 200, f"{host.h['name']}: /healthz answers through the loopback forward")
    save(st)
    print("OK up")


def publish_releases(work, rels, keys, trust, source):
    """Publish each release dir (D/release, with D/nex-cluster.raw[.json] beside it) to one channel, the layer as a
    component of every release. The compliance file is the build's package list."""
    pub = os.path.join(work, "pub")
    script = os.path.join(REPO, "scripts/update-publish.py")
    os.makedirs(pub, exist_ok=True)
    for name in ("trust.json", "trust.sig"):
        shutil.copy(os.path.join(os.path.dirname(trust), name), os.path.join(pub, name))
    for rel in rels:
        d = os.path.dirname(rel)
        layers = []
        for raw in sorted(f for f in os.listdir(d) if f.endswith(".raw")):
            layers += ["--layer", os.path.join(d, raw)]
        subprocess.run([sys.executable, script, "publish", "--base", pub, "--release-dir", rel, "--channel", "dev",
                        "--release-key", os.path.join(keys, "release.key"), "--source", source,
                        "--compliance", os.path.join(d, "base.dpkg-list"), *layers], check=True)
    return pub


def cmd_login(a):
    """Set each host's random root password from the lab shell and wait for /healthz (the tail of `up`)."""
    st = state()
    seen = set()
    for h in st["hosts"]:
        host = Host(h, st)
        upd.check(host.wait_shell(a.budget, seen), f"{h['name']} reached the lab shell")
        host.shell().sh(f"echo 'root:{h['password']}' | chpasswd")
    for h in st["hosts"]:
        end = time.time() + 240
        while time.time() < end:
            s_, _ = host_api(st, h["n"]).call("GET", "/healthz", timeout=5)
            if s_ == 200:
                break
            time.sleep(3)
        upd.check(s_ == 200, f"{h['name']}: /healthz answers through the loopback forward")


def start_services(st):
    pub = path("pub")
    p = spawn([sys.executable, os.path.abspath(__file__), "serve", pub, str(st["http_port"])], path("http.log"))
    st["pids"].append(p.pid)
    p = spawn([sys.executable, os.path.abspath(__file__), "hub", path("hub.sock")], path("hub.log"))
    st["pids"].append(p.pid)
    for _ in range(50):
        if os.path.exists(path("hub.sock")):
            break
        time.sleep(0.1)


def start_controller(st):
    cdir = path("controller")
    p = spawn([path_bin("nex-controller"), "-data-dir", f"{cdir}/data", "-listen", f"127.0.0.1:{CTRL_PORT}",
               "-tls-cert", f"{cdir}/tls.crt", "-tls-key", f"{cdir}/tls.key", "-admin-password-file", f"{cdir}/admin.pw",
               "-ui-dir", f"{cdir}/ui", "-docs-dir", os.path.join(REPO, "docs/api"),
               "-source-archive", source_archive()], path("controller.log"))
    st["controller_pid"] = p.pid
    for _ in range(50):
        try:
            socket.create_connection(("127.0.0.1", CTRL_PORT), 1).close()
            return
        except OSError:
            time.sleep(0.3)
    raise RuntimeError("the Controller did not listen")


def path_bin(name):
    return os.path.join(LAB, "bin", name)


def source_archive():
    return os.path.join(LAB, "nex-source.tar.gz")


def is_ours(pid, want):
    """True when /proc/<pid>/cmdline still names the process we started (a recorded pid may have been reused)."""
    if not pid:
        return False
    try:
        with open(f"/proc/{pid}/cmdline", "rb") as f:
            return want in f.read().decode(errors="replace")
    except OSError:
        return False


def read_pidfile(file, want):
    """The pid in a pidfile when it is still the process `want`, else None."""
    try:
        with open(file) as f:
            pid = int(f.read())
    except (OSError, ValueError):
        return None
    return pid if is_ours(pid, want) else None


def kill_pid(pid, want, sig=signal.SIGKILL):
    """SIGKILL `pid` only when its cmdline contains `want`; False when it is gone or no longer ours."""
    if not is_ours(pid, want):
        return False
    try:
        os.kill(pid, sig)
    except (ProcessLookupError, PermissionError):
        return False
    return True


def kill_qemu(pidfile):
    """Power-cut the QEMU named by a pidfile, then delete the pidfile. False when there was no live QEMU."""
    pid = read_pidfile(pidfile, "qemu-system")
    ok = kill_pid(pid, "qemu-system")
    try:
        os.unlink(pidfile)
    except OSError:
        pass
    return ok


def cmd_resume(a):
    """Start the services and the three VMs again from the disks `up` made (after `down`, or a killed scope)."""
    st = state()
    had_bmc = st.pop("bmc_pid", None)
    st["pids"] = []
    start_services(st)
    if had_bmc:
        start_bmc(st)
    start_controller(st)
    hosts = [Host(h, st) for h in st["hosts"]]
    for host in hosts:
        host.start()
    seen = set()
    for host in hosts:
        if not host.wait_shell(a.budget, seen):
            sys.exit("a host did not boot; console tail:\n" + host.console()[-1500:])
    save(st)
    print("OK resume")


def cmd_enroll(a):
    """Enrol the three hosts at one Controller site, then join hosts 2 and 3 to host 1's pool through the Controller."""
    check = upd.check
    st = state()
    c = ctrl_api(st)
    c.login()
    status, sites = c.call("GET", "/api/v1/sites")
    if not sites:
        status, site = c.call("POST", "/api/v1/sites", {"name": "pool-rollout-lab"})
        sites = [site]
    site = sites[0]
    controller_url = f"https://10.0.2.2:{CTRL_PORT}"
    for h in st["hosts"]:
        if h.get("host_id"):
            continue
        status, probe = c.call("POST", "/api/v1/hosts/probe", {"address": h["loop"]})
        check(status == 200, f"probe {h['name']}", probe)
        status, r = c.call("POST", f"/api/v1/sites/{site['id']}/hosts", {
            "address": h["loop"], "controller_url": controller_url, "fingerprint": probe["fingerprint"], "root_password": h["password"]}, timeout=120)
        check(status in (200, 201), f"enrol {h['name']}", (status, r))
        if status not in (200, 201):
            sys.exit(1)
        h["host_id"], h["pool_id"] = r["host_id"], r["pool_id"]
        save(st)
    time.sleep(10)
    master = st["hosts"][0]
    for h in st["hosts"][1:]:
        status, r = c.call("POST", f"/api/v1/pools/{master['pool_id']}/members", {
            "request_id": f"join-{h['name']}-{RUN}", "host_id": h["host_id"], "peer_origin": f"https://{h['ip']}:9444"}, timeout=90)
        check(status in (200, 201, 202), f"join {h['name']} to {master['name']}'s pool", (status, r))
    end = time.time() + 180
    members = []
    while time.time() < end:
        status, pools = c.call("GET", f"/api/v1/pools/{master['pool_id']}/members")
        members = pools[0].get("members", []) if status == 200 and isinstance(pools, list) and pools else []
        if len(members) == 3 and all(m.get("status") == "ready" for m in members):
            break
        time.sleep(5)
    check(len(members) == 3 and all(m.get("status") == "ready" for m in members), "the pool has three ready members", members)
    save(st)
    print(json.dumps([{k: m.get(k) for k in ("name", "status", "role", "connection")} for m in members], indent=1))


def cmd_publish(a):
    """Publish one more release (D/release, its layer files beside it) into the running update tree: the dev channel moves to it,
    so the hosts see it as the release a new rollout may start."""
    st = state()
    publish_releases(WORK, [os.path.abspath(a.release)], st["keys"], st["trust"], st["source"])


def cmd_guest(a):
    """A Nex-managed VM on host 2 that really answers ACPI shutdown. Created through the Controller (BIOS, 512 MiB, one 1 GiB
    sata disk on the host's local storage, no network), its disk filled with the tiny guest of pool_lab_guest.sh by `qemu-img
    convert` on the host, and started through the Controller's power route, so Core's own start admission records it."""
    check = upd.check
    st = state()
    c = ctrl_api(st)
    c.login()
    h = st["hosts"][1]
    status, stores = c.call("GET", "/api/v1/storage")
    mine = [s for s in (stores if isinstance(stores, list) else []) if s.get("host_id") == h["host_id"] and s.get("status") == "succeeded"]
    check(status == 200 and mine, f"{h['name']} has a registered local storage", stores)
    # the pool is the master's: host 2 was enrolled into a pool of its own and joined host 1's afterwards
    spec = {"request_id": f"guest-{RUN}", "pool_id": st["hosts"][0]["pool_id"], "host_id": h["host_id"], "name": "lab-guest", "description": "",
            "guest_os": "linux", "cpu": {"count": 1, "sockets": 0, "cores": 0, "threads": 0, "model": "host-model"},
            "memory": {"mode": "dedicated", "size_mib": 512},
            "disks": [{"storage_id": mine[0]["id"], "name": "disk-0", "size_gib": 1, "bus": "sata", "thin": True, "boot_order": 1}],
            "nics": [], "boot": {"firmware": "bios", "machine": "q35"}, "media_id": "", "start_after_create": False, "autostart": False,
            "revision": 0}
    if not st.get("guest_id"):
        status, r = c.call("POST", "/api/v1/vms", spec, timeout=120)
        check(status in (200, 201, 202), "the Controller accepts the managed VM", (status, r))
        if status not in (200, 201, 202):
            sys.exit(1)
    vm = None
    end = time.time() + 180
    while time.time() < end and not vm:
        status, vms = c.call("GET", "/api/v1/vms")
        vm = next((v for v in (vms if isinstance(vms, list) else []) if v.get("name") == "lab-guest" and v.get("host_id") == h["host_id"]), None)
        time.sleep(3)
    check(vm is not None, "the VM is listed on host 2", vms)
    # the list shows the operation at once; the domain exists on the host only when Core has defined it (a refused create is listed
    # for a moment too), so the id is kept only after that
    host = Host(h, st)
    end = time.time() + 180
    while time.time() < end and host.shell().sh("virsh domstate %s" % vm["id"], 20)[0] != 0:
        time.sleep(3)
    check(host.shell().sh("virsh domstate %s" % vm["id"], 20)[0] == 0, "Core defined the VM on host 2")
    st["guest_id"] = vm["id"]
    save(st)
    os.makedirs(path("pub/lab"), exist_ok=True)
    shutil.copy(a.disk, path("pub/lab/guest.raw"))
    sh = Host(h, st).shell().sh
    rc, out = sh("python3 -c \"import urllib.request;urllib.request.urlretrieve('http://10.0.2.2:%d/lab/guest.raw','/var/tmp/guest.raw')\" && "
                 "d=$(virsh domblklist %s | awk '$2 ~ /qcow2$/ {print $2; exit}') && test -n \"$d\" && "
                 "qemu-img convert -n -f raw -O qcow2 /var/tmp/guest.raw \"$d\" && rm /var/tmp/guest.raw && echo filled $d" % (st["http_port"], vm["id"]), 240)
    check(rc == 0 and "filled" in out, "the guest disk is in the VM's qcow2", out)
    status, r = c.call("POST", f"/api/v1/vms/{vm['id']}/power", {"request_id": f"guest-start-{RUN}", "action": "start"}, timeout=60)
    check(status in (200, 202), "the Controller accepts the start", (status, r))
    end = time.time() + 120
    while time.time() < end:
        rc, out = sh("virsh list --uuid", 20)
        if vm["id"] in out:
            break
        time.sleep(3)
    check(vm["id"] in out, "the guest is running on host 2", out)
    print("guest", vm["id"])


def cmd_promote(a):
    """The master is gone: ask host N, through the Controller, whether it may take the pool over (the preflight, nothing is
    stored), then promote it with the verdict's own fence list and the pool's typed name. Waits until host N reports itself master."""
    check = upd.check
    st = state()
    c = ctrl_api(st)
    c.login()
    cand = st["hosts"][a.n - 1]
    pool = st["hosts"][0]["pool_id"]  # the pool every host joined; a joined host's own record still names the pool it was enrolled into
    status, pools = c.call("GET", "/api/v1/pools")
    name = next(p["name"] for p in pools if p["id"] == pool)
    status, pre = c.call("POST", f"/api/v1/pools/{pool}/master/promote/preflight", {"host_id": cand["host_id"]}, timeout=90)
    print("preflight:", status, json.dumps(pre)[:1800])
    verdict = (pre or {}).get("promotion") or {}
    check(status == 200 and verdict.get("ready_to_fence"), "the preflight says the candidate is ready to fence", verdict.get("blocking"))
    status, r = c.call("POST", f"/api/v1/pools/{pool}/master/promote", {
        "request_id": f"promote-{RUN}", "host_id": cand["host_id"], "confirmation": name, "fence_confirmed": True,
        "fence_host_ids": verdict["fence_host_ids"]}, timeout=60)
    check(status in (200, 202), "the Controller accepts the promotion", (status, r))
    api = host_api(st, a.n)
    end, last = time.time() + a.seconds, None
    while time.time() < end:
        s_, body = api.call("GET", "/api/local/v1/pool", timeout=10)
        now = (s_, (body or {}).get("master_host_id") if isinstance(body, dict) else body)
        if now != last:
            print(time.strftime("%H:%M:%S"), "pool as host", a.n, "reports:", now, flush=True)
            last = now
        if s_ == 200 and now[1] == cand["host_id"]:
            return
        time.sleep(3)
    sys.exit(f"host {a.n} did not become the pool master in {a.seconds}s")


def cmd_boot(a):
    """Start host N's VM from its disk again (after `cut`) and wait for its lab shell."""
    st = state()
    host = Host(st["hosts"][a.n - 1], st)
    host.start()
    upd.check(host.wait_shell(a.budget, set()), f"{host.h['name']} reached the lab shell")


def cmd_cut(a):
    """Power cut: kill -9 host N's QEMU, as the lab management controller does on a ForceOff."""
    st = state()
    if not kill_qemu(path(f"h{a.n}/qemu.pid")):
        sys.exit(f"host {a.n}: no running QEMU to cut")
    print(time.strftime("%H:%M:%S"), f"host {a.n}: power cut")


SESSIONS = {}


def snapshot(st):
    """One line: the rollout record and every member's maintenance flag, read from whichever host answers as the pool master
    now (a member answers 409 to the rollout route, a dead host not at all), so it follows a master change."""
    names = {h["host_id"]: h["name"][-1] for h in st["hosts"]}
    out = {"maint": [], "stage": []}
    for n in (1, 2, 3):
        api = SESSIONS.setdefault(n, host_api(st, n))
        try:
            status, ru = api.call("GET", "/api/local/v1/pool/updates", timeout=5)
            if status != 200:
                continue
            status, pool = api.call("GET", "/api/local/v1/pool", timeout=5)
        except RuntimeError:
            continue
        if status != 200:
            continue
        out["maint"] = sorted(names.get(m["host_id"], "?") for m in pool.get("members", []) if not m.get("maintenance_since", "").startswith("0001"))
        out["master"] = names.get(pool.get("master_host_id"), "?")
        r = (ru or {}).get("rollout")
        if r:
            out["state"] = f"{r['state']}/{r.get('phase', '')}" + (f"/{r['pause']['code']}: {r['pause'].get('message', '')}" if r.get("pause") else "")
            out["stage"] = [f"{names.get(h['host_id'], '?')}:{h['stage']}" for h in r["hosts"]]
        break
    return out


def cmd_watch(a):
    st = state()
    end, last = time.time() + a.seconds, None
    while time.time() < end:
        cur = snapshot(st)
        line = json.dumps(cur, sort_keys=True)
        if line != last:
            print(time.strftime("%H:%M:%S"), line, flush=True)
            last = line
            if a.until and a.until in cur.get("state", ""):
                return
        time.sleep(3)


def cmd_rollout(a):
    """Start a rollout through the Controller relay and watch it. Optionally kill the Controller process while a member updates
    (--kill-controller), or power-cut the master VM, without restarting it, once the record shows host:stage (--cut-master-at).
    Prints every change of the record; the number of hosts in maintenance is read from the master's membership at every poll
    (about every 3 s)."""
    check = upd.check
    st = state()
    pool = st["hosts"][0]["pool_id"]
    rid = a.rollout_id
    c = ctrl_api(st)
    c.login()
    if not a.attach:
        status, body = c.call("POST", f"/api/v1/pools/{pool}/updates/rollouts", {"request_id": rid, "sequence": a.sequence, "running_vms": a.running_vms})
        check(status == 202, "the Controller relays the rollout to the master", (status, body))
    killed = False
    last, maxmaint, end, seen_running = None, 0, time.time() + a.seconds, False
    while time.time() < end:
        cur = snapshot(st)
        maxmaint = max(maxmaint, len(cur.get("maint", [])))
        line = json.dumps(cur, sort_keys=True)
        if line != last:
            print(time.strftime("%H:%M:%S"), line, flush=True)
            last = line
        stage = " ".join(cur.get("stage", []))
        if a.kill_controller and not killed and ("b:soak" in stage or "a:drain" in stage):
            kill_pid(st["controller_pid"], "nex-controller")
            killed = time.time()
            print(time.strftime("%H:%M:%S"), "CONTROLLER KILLED", flush=True)
        if killed and not st.get("ctrl_restarted") and time.time() > killed + 25:
            start_controller(st)
            st["ctrl_restarted"] = True
            save(st)
            print(time.strftime("%H:%M:%S"), "CONTROLLER RESTARTED (same data directory)", flush=True)
        if a.cut_master_at and a.cut_master_at in stage.split():
            kill_qemu(path("h1/qemu.pid"))
            print(time.strftime("%H:%M:%S"), f"MASTER VM POWER CUT (kill -9 QEMU) at {a.cut_master_at}", flush=True)
            return
        state_ = cur.get("state", "")
        seen_running = seen_running or state_.startswith("running")
        if seen_running and state_.split("/")[0] in ("paused", "finalized", "aborted"):
            break
        time.sleep(3)
    print("final:", last)
    check(maxmaint <= 1, "never more than one host in maintenance at a poll", maxmaint)
    if a.expect != "any":
        final = (json.loads(last).get("state", "") if last else "").split("/")[0]
        check(final == a.expect, f"the rollout ended {a.expect}", last)
    status, body = ctrl_api(st).call("GET", f"/api/v1/pools/{pool}/updates")
    print("Controller GET:", status, json.dumps((body or {}).get("rollout", body))[:600])


def cmd_control(a):
    """pause | resume | abort the rollout through the Controller's control route."""
    st = state()
    c = ctrl_api(st)
    c.login()
    pool = st["hosts"][0]["pool_id"]
    body = {"request_id": f"{a.rollout_id}-{a.action}-{time.strftime('%H%M%S')}", "action": a.action}
    status, r = c.call("POST", f"/api/v1/pools/{pool}/updates/rollouts/{a.rollout_id}/control", body, timeout=60)
    print(status, json.dumps(r)[:500])


def cmd_sh(a):
    st = state()
    host = Host(st["hosts"][a.n - 1], st)
    rc, out = host.shell().sh(a.command, a.timeout)
    print(out)
    sys.exit(rc)


def cmd_down(a):
    try:
        st = state()
    except OSError:
        st = {"pids": []}
    for n in (1, 2, 3):
        kill_qemu(path(f"h{n}/qemu.pid"))
    for pid in st.get("pids", []):
        kill_pid(pid, "pool_rollout_lab.py")
    kill_pid(st.get("controller_pid", 0), "nex-controller")
    time.sleep(1)
    out = subprocess.run(["pgrep", "-af", "qemu-system|nex-controller|pool_rollout_lab.py (serve|hub)|nex-pool-lab"], capture_output=True, text=True).stdout
    left = [l for l in out.splitlines() if "pgrep" not in l and str(os.getpid()) != l.split()[0]]
    print("left over:", left or "none")
    docker = subprocess.run(["docker", "ps", "-q", "--filter", "name=nex-pool-lab"], capture_output=True, text=True).stdout.split()
    for c in docker:
        subprocess.run(["docker", "kill", c], capture_output=True)
    print("containers killed:", docker or "none")


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    up = sub.add_parser("up")
    up.add_argument("--rel1", required=True)
    up.add_argument("--rel2", required=True)
    up.add_argument("--keys", required=True)
    up.add_argument("--trust", required=True)
    up.add_argument("--hostimage", required=True)
    up.add_argument("--budget", type=int, default=480)
    up.add_argument("--step-timeout", default="120s", help="NEX_POOL_UPDATE_STEP_TIMEOUT seeded into every host")
    s = sub.add_parser("serve")
    s.add_argument("root")
    s.add_argument("port")
    s = sub.add_parser("hub")
    s.add_argument("sock")
    sub.add_parser("down")
    s = sub.add_parser("bmc-serve")
    s.add_argument("port")
    s = sub.add_parser("login")
    s.add_argument("--budget", type=int, default=240)
    sub.add_parser("fence")
    sub.add_parser("layer")
    sub.add_parser("enroll")
    r = sub.add_parser("rollout")
    r.add_argument("--rollout-id", default="lab-rollout-1")
    r.add_argument("--sequence", type=int, default=2)
    r.add_argument("--running-vms", default="refuse")
    r.add_argument("--seconds", type=int, default=480)
    r.add_argument("--kill-controller", action="store_true")
    r.add_argument("--expect", choices=["finalized", "paused", "aborted", "any"], default="finalized",
                   help="the state the record must end in (default finalized; any for runs steered by hand)")
    r.add_argument("--attach", action="store_true", help="do not start a rollout; watch the one that exists (after a pause and resume)")
    r.add_argument("--cut-master-at", default="", help="power-cut the master VM when the record shows this host:stage, e.g. b:soak")
    r = sub.add_parser("watch")
    r.add_argument("--seconds", type=int, default=420)
    r.add_argument("--until", default="")
    r = sub.add_parser("control")
    r.add_argument("action", choices=["pause", "resume", "abort"])
    r.add_argument("--rollout-id", default="lab-rollout-1")
    r = sub.add_parser("publish")
    r.add_argument("--release", required=True)
    r = sub.add_parser("guest")
    r.add_argument("disk", help="the raw disk pool_lab_guest.sh made")
    r = sub.add_parser("promote")
    r.add_argument("n", type=int)
    r.add_argument("--seconds", type=int, default=420)
    r = sub.add_parser("boot")
    r.add_argument("n", type=int)
    r.add_argument("--budget", type=int, default=240)
    r = sub.add_parser("cut")
    r.add_argument("n", type=int)
    r = sub.add_parser("resume")
    r.add_argument("--budget", type=int, default=480)
    r = sub.add_parser("sh")
    r.add_argument("n", type=int)
    r.add_argument("command")
    r.add_argument("--timeout", type=int, default=60)
    s = sub.add_parser("api")
    s.add_argument("n", type=int)
    s.add_argument("method")
    s.add_argument("path")
    s.add_argument("body", nargs="?")
    s = sub.add_parser("capi")
    s.add_argument("method")
    s.add_argument("path")
    s.add_argument("body", nargs="?")
    a = ap.parse_args()
    if a.cmd == "serve":
        run_serve(a.root, a.port)
    elif a.cmd == "hub":
        run_hub(a.sock)
    elif a.cmd == "up":
        cmd_up(a)
    elif a.cmd == "down":
        cmd_down(a)
    elif a.cmd == "login":
        cmd_login(a)
    elif a.cmd == "bmc-serve":
        run_bmc(int(a.port))
    elif a.cmd == "fence":
        cmd_fence(a)
    elif a.cmd == "layer":
        cmd_layer(a)
    elif a.cmd == "control":
        cmd_control(a)
    elif a.cmd == "publish":
        cmd_publish(a)
    elif a.cmd == "guest":
        cmd_guest(a)
    elif a.cmd == "promote":
        cmd_promote(a)
    elif a.cmd == "boot":
        cmd_boot(a)
    elif a.cmd == "cut":
        cmd_cut(a)
    elif a.cmd == "watch":
        cmd_watch(a)
    elif a.cmd == "rollout":
        cmd_rollout(a)
    elif a.cmd == "enroll":
        cmd_enroll(a)
    elif a.cmd == "resume":
        cmd_resume(a)
    elif a.cmd == "sh":
        cmd_sh(a)
    elif a.cmd in ("api", "capi"):
        st = state()
        c = host_api(st, a.n) if a.cmd == "api" else ctrl_api(st)
        c.login()
        status, body = c.call(a.method, a.path, json.loads(a.body) if a.body else None)
        print(status, json.dumps(body, indent=1))
    if upd.check.failed:
        sys.exit(1)


if __name__ == "__main__":
    main()
