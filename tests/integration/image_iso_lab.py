#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Build the installation ISO for the local QEMU lab (development evidence only).

The full release build (installer/build.sh) needs the Controller system image build and a release image;
none of that is under test here. This script assembles a small ISO with the same layout from the pinned
Debian netinst ISO:

  /nex/image/<release>, /nex/bin/nex-hostimage, /nex/nex-image-install.sh, /nex/root-account.sh,
  /nex/payload/<sha256>/<file>      (small lab fixtures, NOT the Controller system image or VirtIO ISO)

and appends one initrd overlay on the Nex GUI/text initrd of the netinst ISO with the real installer pieces
(`nex-config` compiled from installer/gui, `00nex-recheck`, `prepare`, `S66nex-wizard`, the text templates).

The only lab-specific part is the wizard: `usr/lib/nex-installer/wizard-text` is replaced by a stub that
writes the same /run/nex-installer/config.ini handoff from a control record on a 1 MiB virtio disk with serial
NEXLABCTL (mode, disk serials, hostname). Everything after the handoff (nex-config validate/apply/recheck,
partman hook, nex-image-install.sh, nex-hostimage) is the product code. The real text wizard is covered by
installer-text-wizard.py against a fixture, and the graphical one by the Xvfb run; neither is driven here.

  tests/integration/image_iso_lab.py --release DIR --hostimage BIN --out ISO [--cc-image IMAGE]

Needs docker (images nex-iso-builder for xorriso and a gcc/glib image for nex-config) and
.cache/installer-lab/debian-13.6.0-amd64-netinst.iso.
"""
import argparse
import gzip
import hashlib
import os
import shutil
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
NETINST = os.path.join(REPO, ".cache/installer-lab/debian-13.6.0-amd64-netinst.iso")
LAB_PASSWORD = "nexlab-pass-1"

STUB = r'''#!/bin/sh
# LAB ONLY: stands in for the interactive wizard. Writes the config.ini handoff the real wizards write.
set -u
STATE=/run/nex-installer
umask 077
mkdir -p "$STATE"
echo 'LAB-HOOK: lab wizard stub started' > /dev/ttyS0
[ -f "$STATE/prepared" ] || /usr/lib/nex-installer/prepare || true
[ -f "$STATE/prepared" ] || exit 1
ctl=
for b in /sys/block/vd*; do
    [ "$(tr -d ' \n' < "$b/serial" 2>/dev/null)" = NEXLABCTL ] && ctl=/dev/${b##*/}
done
[ -n "$ctl" ] || { echo 'lab control disk missing' >&2; exit 1; }
mode=install; serials=; hostname=nex-lab-host
eval "$(dd if="$ctl" bs=512 count=1 2>/dev/null | tr -d '\000' | grep -E '^[a-z_]+=[A-Za-z0-9,._-]*$')"
disks=
for s in $(printf '%s' "$serials" | tr ',' ' '); do
    for b in /sys/block/vd*; do
        [ "$(tr -d ' \n' < "$b/serial" 2>/dev/null)" = "$s" ] && disks="$disks ${b##*/}"
    done
done
[ -n "$disks" ] || { echo 'no lab disk matches the control record' >&2; exit 1; }
if [ "${hold:-0}" = 1 ]; then
    # LAB ONLY: hold the selected disks' mirror open read-only (assembled and mounted, nothing written to the members),
    # the way an operator with a shell would, so the installer has to refuse to re-lay them.
    anna-install mdadm-udeb md-modules ext4-modules >/dev/null 2>&1 || true
    depmod -a 2>/dev/null; modprobe md_mod 2>/dev/null; modprobe raid1 2>/dev/null; modprobe ext4 2>/dev/null
    members=
    for d in $disks; do members="$members /dev/${d}6"; done
    mdadm --assemble --readonly --run /dev/md/nex-data $members > /dev/ttyS0 2>&1
    mkdir -p /mnt/hold && mount -o ro,noload /dev/md/nex-data /mnt/hold > /dev/ttyS0 2>&1
    echo "LAB-HOLD: $(grep -E '^md' /proc/mdstat | head -1 | tr -s ' ') mounted=$(grep -c ' /mnt/hold ' /proc/mounts)" > /dev/ttyS0
fi
nics=
for n in /sys/class/net/*; do
    [ -e "$n/device" ] && nics="$nics ${n##*/}"
done
set -- $nics
{
    [ "$mode" != repair ] || printf '[Install]\nmode=repair\n\n'
    printf '[Host]\nhostname=%s\ntimezone=UTC\nkeymap=us\npassword=%s\n' "$hostname" "NEXLAB_PASSWORD"
    for key in disk disk_size disk_serial disk_major_minor; do
        printf '%s=' "$key"
        for name in $disks; do
            case $key in
                disk) printf '/dev/%s;' "$name" ;;
                disk_size) printf '%s;' "$(($(cat /sys/class/block/$name/size) * 512))" ;;
                disk_serial) printf '%s;' "$(tr -d ' \n' < /sys/class/block/$name/serial)" ;;
                disk_major_minor) printf '%s;' "$(cat /sys/class/block/$name/dev)" ;;
            esac
        done
        printf '\n'
    done
    printf '\n[Network]\nmanagement=%s\ndns=\n' "${1:-}"
    for nic in $nics; do
        printf '\n[NIC:%s]\nmac=%s\nmode=dhcp\naddress=\ngateway=\nvlan=0\n' "$nic" "$(cat /sys/class/net/$nic/address)"
    done
} > "$STATE/config.ini.new"
mv "$STATE/config.ini.new" "$STATE/config.ini"
if detail=$(/usr/lib/nex-installer/validate-config "$STATE/config.ini" 2>&1 >/dev/null); then
    exit 0
fi
rm -f "$STATE/config.ini"
echo "nex-config rejected the lab settings: $detail" >&2
echo "LAB-VALIDATE-REFUSED: $detail" > /dev/ttyS0
exit 1
'''

LAB_SEED = r'''#!/bin/sh
# LAB ONLY: a control shell on the second serial port for the installed host, added to the staged /etc seed after
# nex-config --apply and before the image install (partman init hooks run in name order; this one sorts first).
T=/run/nex-installer/target/etc
[ -d "$T" ] || exit 0
mkdir -p "$T/systemd/system/multi-user.target.wants" "$T/systemd/system-preset"
cat > "$T/systemd/system/nex-lab-shell.service" <<'EOT'
[Unit]
Description=Lab control shell on the second serial port (test only)
After=local-fs.target
[Service]
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
ln -sf /etc/systemd/system/nex-lab-shell.service "$T/systemd/system/multi-user.target.wants/nex-lab-shell.service"
printf 'enable nex-lab-shell.service\n' > "$T/systemd/system-preset/00-nex-lab.preset"
'''

TTY_LOG = '''# LAB ONLY: mirror the installer log to the serial console so the lab can follow it.
if [ -c /dev/ttyS0 ]; then
    ( tail -n +1 -F /var/log/syslog /var/log/nex-installer-prepare.log > /dev/ttyS0 2>&1 & )
    echo 'LAB-HOOK: S65nexlab-log ran' > /dev/ttyS0
    ( sleep 60; { echo "LAB-DIAG xkb=$(debconf-get keyboard-configuration/xkb-keymap 2>&1) prio=$(debconf-get debconf/priority 2>&1) run=$(ls /run/nex-installer | tr '\\n' ,) seen=$(debconf-fget keyboard-configuration/xkb-keymap seen 2>&1) env=$(cat /run/nex-installer/image.env 2>&1 | tr '\\n' ,)"; } > /dev/ttyS0 2>&1 & )
fi
'''


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def run(*cmd, **kw):
    print("+", " ".join(cmd)[:200], flush=True)
    subprocess.run(cmd, check=True, **kw)


def docker(image, script, mounts, user=True):
    cmd = ["docker", "run", "--rm", "--entrypoint", "sh"]
    if user:
        cmd += ["-u", f"{os.getuid()}:{os.getgid()}"]
    for m in mounts:
        cmd += ["-v", m]
    run(*cmd, image, "-c", script)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--release", required=True)
    ap.add_argument("--hostimage", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--cc-image", default="nex-lab-gtk:phase6")
    ap.add_argument("--work", default=os.path.join(REPO, ".cache/phase6/iso-work"))
    a = ap.parse_args()
    work = os.path.realpath(a.work)
    stage = os.path.join(work, "stage")
    overlay = os.path.join(work, "overlay")
    rel = os.path.realpath(a.release)
    out = os.path.realpath(a.out)
    if os.path.commonpath([rel, out]) in (rel, out):
        sys.exit(f"--out {out} is, contains or lies inside --release {rel}")
    if os.path.commonpath([rel, work]) in (rel, work):
        sys.exit(f"--work {work} contains or lies inside --release {rel}")
    if os.path.commonpath([out, work]) in (out, work):
        sys.exit(f"--out {out} is, contains or lies inside --work {work}")
    marker = os.path.join(work, ".image_iso_lab")
    if os.path.isdir(work) and os.listdir(work) and not os.path.exists(marker):
        sys.exit(f"{work} exists and was not created by this script; pass an empty or new --work")
    shutil.rmtree(work, ignore_errors=True)
    os.makedirs(work)
    open(marker, "w").close()
    run(os.path.abspath(a.hostimage), "verify", rel)

    # 1. extract the pinned netinst ISO, nothing else is taken from it
    os.makedirs(stage)
    docker("nex-iso-builder", f"xorriso -osirrox on -indev /iso/{os.path.basename(NETINST)} -extract / /stage >/dev/null",
           [f"{os.path.dirname(NETINST)}:/iso:ro", f"{stage}:/stage"])
    for root, dirs, files in os.walk(stage):
        for n in dirs + files:
            os.chmod(os.path.join(root, n), 0o755 if os.path.isdir(os.path.join(root, n)) else 0o644)
    os.chmod(stage, 0o755)

    # 2. /nex on the media: the image set, the static tool, the installer script, lab payload fixtures
    nex = os.path.join(stage, "nex")
    os.makedirs(os.path.join(nex, "bin"))
    shutil.copytree(rel, os.path.join(nex, "image"), copy_function=shutil.copyfile)
    shutil.copyfile(a.hostimage, os.path.join(nex, "bin/nex-hostimage"))
    for src, dst in (("installer/nex-image-install.sh", "nex-image-install.sh"), ("installer/root-account.sh", "root-account.sh")):
        shutil.copyfile(os.path.join(REPO, src), os.path.join(nex, dst))
    fixture = os.path.join(work, "controller-system.img")
    with open(fixture, "wb") as f:
        f.write(b"nex lab fixture, not a Controller system image\n" * 4096)
    digest = sha256(fixture)
    pdir = os.path.join(nex, "payload", digest)
    os.makedirs(pdir)
    shutil.copyfile(fixture, os.path.join(pdir, "controller-system.img"))
    print("INFO  lab payload fixture sha256", digest, flush=True)

    # 3. initrd overlay: real installer files, compiled nex-config, lab wizard stub
    for d in ("usr/bin", "usr/lib/nex-installer", "usr/lib/debian-installer.d", "usr/lib/partman/init.d", "usr/share/nex-installer"):
        os.makedirs(os.path.join(overlay, d))
    docker(a.cc_image,
           "cc -O2 -Wall -Wextra $(pkg-config --cflags glib-2.0) /w/installer/gui/nex-config.c /w/installer/gui/nex-network.c "
           "-o /o/usr/bin/nex-config $(pkg-config --libs glib-2.0) -lcrypt",
           [f"{REPO}:/w:ro", f"{overlay}:/o"])
    os.symlink("/usr/bin/nex-config", os.path.join(overlay, "usr/lib/nex-installer/validate-config"))
    for src, dst, mode in (("installer/gui/recheck.sh", "usr/lib/partman/init.d/00nex-recheck", 0o755),
                           ("installer/gui/prepare.sh", "usr/lib/nex-installer/prepare", 0o755),
                           ("installer/gui/wizard-text.templates", "usr/share/nex-installer/wizard-text.templates", 0o644),
                           ("installer/gui/start.sh", "usr/lib/debian-installer.d/S66nex-wizard", 0o644),
                           ("installer/gui/netinst-network.sh", "usr/lib/nex-installer/netinst-network", 0o755),
                           ("installer/gui/dhcp-preview.sh", "usr/lib/nex-installer/dhcp-preview", 0o755)):
        shutil.copyfile(os.path.join(REPO, src), os.path.join(overlay, dst))
        os.chmod(os.path.join(overlay, dst), mode)
    with open(os.path.join(overlay, "usr/lib/nex-installer/wizard-text"), "w") as f:
        f.write(STUB.replace("NEXLAB_PASSWORD", LAB_PASSWORD))
    os.chmod(os.path.join(overlay, "usr/lib/nex-installer/wizard-text"), 0o755)
    with open(os.path.join(overlay, "usr/lib/partman/init.d/000nexlab-seed"), "w") as f:
        f.write(LAB_SEED)
    os.chmod(os.path.join(overlay, "usr/lib/partman/init.d/000nexlab-seed"), 0o755)
    with open(os.path.join(overlay, "usr/lib/debian-installer.d/S65nexlab-log"), "w") as f:
        f.write(TTY_LOG)
    with open(os.path.join(overlay, "usr/share/nex-installer/timezones"), "w") as f:
        f.write("UTC\n")
    shutil.copyfile(os.path.join(REPO, "installer/preseed.cfg"), os.path.join(overlay, "preseed.cfg"))
    listing = subprocess.run(["find", ".", "-print0"], cwd=overlay, check=True, capture_output=True).stdout
    cpio = subprocess.run(["cpio", "--null", "-o", "-H", "newc", "--quiet", "-R", "0:0"], cwd=overlay, input=listing,
                          check=True, capture_output=True).stdout
    initrd = os.path.join(stage, "install.amd/gtk/initrd.gz")
    with open(initrd, "ab") as f:
        f.write(gzip.compress(cpio, mtime=0))

    # 4. boot menus: BIOS and UEFI both start the same entry at once (the firmware decides whether it may proceed)
    append = "DEBIAN_FRONTEND=newt console=ttyS0,115200n8 ---"
    with open(os.path.join(stage, "isolinux/isolinux.cfg"), "w") as f:
        f.write(f"default nex\nprompt 0\ntimeout 10\nlabel nex\n  kernel /install.amd/vmlinuz\n  append initrd=/install.amd/gtk/initrd.gz {append}\n")
    with open(os.path.join(stage, "boot/grub/grub.cfg"), "w") as f:
        f.write(f"set default=0\nset timeout=1\nmenuentry 'Nex Core (lab)' {{\n"
                f"  linux /install.amd/vmlinuz {append}\n  initrd /install.amd/gtk/initrd.gz\n}}\n")
    for p in ("md5sum.txt",):
        if os.path.exists(os.path.join(stage, p)):
            os.remove(os.path.join(stage, p))

    # 5. pack, replaying the netinst boot structure
    os.makedirs(os.path.dirname(out), exist_ok=True)
    if os.path.exists(out):
        os.remove(out)
    docker("nex-iso-builder",
           f"xorriso -indev /iso/{os.path.basename(NETINST)} -outdev /o/{os.path.basename(out)} -boot_image any replay "
           f"-update_r /stage / -volid NEX_CORE_LAB -commit -end >/dev/null 2>&1",
           [f"{os.path.dirname(NETINST)}:/iso:ro", f"{stage}:/stage:ro", f"{os.path.dirname(out)}:/o"])
    shutil.rmtree(work, ignore_errors=True)
    print(f"ISO {out} {os.path.getsize(out)} bytes sha256 {sha256(out)}")


if __name__ == "__main__":
    sys.exit(main())
