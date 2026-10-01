#!/usr/bin/env python3
"""Throwaway QEMU+KVM+OVMF driver for the Phase I-0 spikes (serial console shell)."""
import os, re, socket, subprocess, time

FW_DIR = "/usr/share/OVMF/x64"


class VM:
    def __init__(self, name, disks, workdir, vars_src=None, secboot=False, mem=3072, extra=None, bootindex=None):
        self.name, self.disks, self.workdir = name, disks, workdir
        self.secboot = secboot
        self.vars = os.path.join(workdir, name + ".vars.fd")
        if vars_src:
            subprocess.run(["cp", "--sparse=always", vars_src, self.vars], check=True)
        elif not os.path.exists(self.vars):
            subprocess.run(["cp", f"{FW_DIR}/OVMF_VARS.4m.fd", self.vars], check=True)
        self.sock = os.path.join(workdir, name + ".sock")
        self.mon = os.path.join(workdir, name + ".mon")
        self.logf = os.path.join(workdir, name + ".log")
        self.mem, self.extra, self.proc, self.s = mem, extra or [], None, None
        self.bootindex = bootindex

    def cmd(self):
        code = "OVMF_CODE.secboot.4m.fd" if self.secboot else "OVMF_CODE.4m.fd"
        c = ["qemu-system-x86_64", "-enable-kvm", "-cpu", "host", "-machine", "q35,smm=on" if self.secboot else "q35",
             "-m", str(self.mem), "-smp", "2", "-display", "none", "-monitor", f"unix:{self.mon},server=on,wait=off"]
        c += ["-global", "driver=cfi.pflash01,property=secure,value=on"] if self.secboot else []
        c += ["-drive", f"if=pflash,format=raw,unit=0,file={FW_DIR}/{code},readonly=on",
              "-drive", f"if=pflash,format=raw,unit=1,file={self.vars}"]
        for i, d in enumerate(self.disks):
            c += ["-drive", f"if=none,id=d{i},format=raw,file={d}",
                  "-device", f"virtio-blk-pci,drive=d{i},serial=nexdisk{i},bootindex={self.bootindex[i] if self.bootindex else i + 1}"]
        c += ["-netdev", "user,id=n0", "-device", "virtio-net-pci,netdev=n0", "-device", "virtio-rng-pci",
              "-chardev", f"socket,id=ser0,path={self.sock}0,server=on,wait=off,logfile={self.logf},logappend=on",
              "-serial", "chardev:ser0",
              "-chardev", f"socket,id=ser1,path={self.sock},server=on,wait=off,logfile={self.logf}.shell,logappend=on",
              "-serial", "chardev:ser1"]
        return c + self.extra

    def start(self):
        for f in (self.sock, self.sock + "0", self.mon):
            if os.path.exists(f):
                os.unlink(f)
        self.proc = subprocess.Popen(self.cmd(), stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        for _ in range(100):
            if os.path.exists(self.sock):
                break
            time.sleep(0.1)
        self.s = socket.socket(socket.AF_UNIX)
        self.s.connect(self.sock)
        self.s.settimeout(0.5)
        self.buf = ""
        return self

    def _read(self):
        try:
            d = self.s.recv(65536)
        except socket.timeout:
            return False
        if not d:
            return False
        self.buf += d.decode(errors="replace")
        return True

    def expect(self, pat, timeout=120):
        end = time.time() + timeout
        rx = re.compile(pat)
        while time.time() < end:
            m = rx.search(self.buf)
            if m:
                out, self.buf = self.buf[:m.end()], self.buf[m.end():]
                return out
            self._read()
            if self.proc.poll() is not None and not self._read():
                raise RuntimeError("qemu exited: " + self.proc.stderr.read().decode())
        raise TimeoutError(f"{pat!r} not seen; tail: {self.buf[-600:]!r}")

    def login(self, timeout=240):
        """Wait for the spike control shell on ttyS1."""
        end = time.time() + timeout
        while time.time() < end:
            self.s.sendall(b"\necho __UP_$((40+2))__\n")
            try:
                self.expect(r"__UP_42__", 5)
                self.s.sendall(b"stty -echo; export PS1=''; export TERM=dumb; echo __SYNC_$((1+1))__\n")
                self.expect(r"__SYNC_2__\r?\n", 10)
                return self
            except TimeoutError:
                continue
        raise TimeoutError("no shell; tail: " + self.buf[-800:])

    def monitor(self, command):
        m = socket.socket(socket.AF_UNIX)
        m.connect(self.mon)
        m.settimeout(2)
        try:
            m.recv(4096)
            m.sendall((command + "\n").encode())
            time.sleep(0.5)
            return m.recv(65536).decode(errors="replace")
        except socket.timeout:
            return ""
        finally:
            m.close()

    def run(self, command, timeout=120, check=False):
        self.buf = ""
        self.s.sendall((command + "\necho __RC_$?__\n").encode())
        out = self.expect(r"__RC_(\d+)__", timeout)
        rc = int(re.search(r"__RC_(\d+)__", out).group(1))
        out = out[:out.rindex("__RC_")].replace("\r", "").strip("\n")
        if check and rc:
            raise RuntimeError(f"{command!r} rc={rc}\n{out}")
        return rc, out

    def wait_exit(self, timeout=60):
        self.proc.wait(timeout)

    def kill(self):
        if self.proc and self.proc.poll() is None:
            self.proc.kill()
            self.proc.wait()
        if self.s:
            self.s.close()

    def poweroff(self):
        try:
            self.s.sendall(b"systemctl poweroff\n")
            self.proc.wait(60)
        except Exception:
            self.kill()
