#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Turn the mkosi base tree into a release set. Runs inside the nex-image-tools container (image/build.sh).

  release.py base      erofs + dm-verity hash tree + roothash from /out/base       -> /out/root.erofs, root.verity, roothash
  release.py release   UKI, ESP tree and manifest.json into /out/release/          (what nex-hostimage installs)

Every input that reaches the output is fixed: timestamps (SOURCE_DATE_EPOCH), the erofs UUID, the
verity UUID and salt, file order. The same tree gives the same roothash.
"""
import hashlib
import json
import os
import shutil
import subprocess
import sys

OUT = "/out"
TREE = f"{OUT}/base"
SDE = os.environ.get("SOURCE_DATE_EPOCH") or "1790000000"
LABEL = os.environ.get("NEX_LABEL", "dev")
SEQ = int(os.environ.get("NEX_SEQUENCE", "0"))
EROFS_UUID = "5e1ec7ed-0000-4000-8000-0000000000e1"
VERITY_UUID = "5e1ec7ed-0000-4000-8000-0000000000e2"
VERITY_SALT = "5e1ec7ed000040008000000000000e3a5e1ec7ed000040008000000000000e3a"
# /var is the data volume at run time; the dpkg database was saved by mkosi.finalize for the layer builds.
EXCLUDES = ["var/lib/dpkg", "var/lib/apt", "var/cache", "var/log", "boot", "efi", "dev", "proc", "sys", "run", "tmp"]  # contents only; the directories stay as mount points
CMDLINE = "ro console=tty0 console=ttyS0,115200n8 panic=10 systemd.firstboot=off systemd.ssh_auto=no"


def run(*args, **kw):
    print("+", " ".join(map(str, args)), flush=True)
    return subprocess.run(args, check=True, **kw)


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def base():
    for name in ("root.erofs", "root.verity", "roothash"):
        if os.path.exists(f"{OUT}/{name}"):
            os.unlink(f"{OUT}/{name}")
    opts = ["-zlz4hc,12", f"-T{SDE}", f"-U{EROFS_UUID}"]
    for e in EXCLUDES:
        opts.append(f"--exclude-regex=^{e}/.")
    run("mkfs.erofs", *opts, f"{OUT}/root.erofs", TREE)
    out = subprocess.run(
        ["veritysetup", "format", "--hash=sha256", "--data-block-size=4096", "--hash-block-size=4096", "--format=1",
         f"--uuid={VERITY_UUID}", f"--salt={VERITY_SALT}", f"{OUT}/root.erofs", f"{OUT}/root.verity"],
        check=True, capture_output=True, text=True).stdout
    os.chmod(f"{OUT}/root.verity", 0o644)
    roothash = [l.split()[-1] for l in out.splitlines() if l.startswith("Root hash:")][0]
    with open(f"{OUT}/roothash", "w") as f:
        f.write(roothash + "\n")
    print(f"roothash {roothash}")
    print(f"root.erofs {os.path.getsize(OUT + '/root.erofs')} bytes, root.verity {os.path.getsize(OUT + '/root.verity')} bytes")


def initrd_overlay(dst):
    """newc cpio of image/initrd (units and config appended to the initrd), reproducibly."""
    src = "/nex/image/initrd"
    stage = "/tmp/nex-initrd"
    shutil.rmtree(stage, ignore_errors=True)
    shutil.copytree(src, stage, symlinks=True)
    for root, dirs, files in os.walk(stage):
        for n in dirs + files:
            os.utime(os.path.join(root, n), (int(SDE), int(SDE)), follow_symlinks=False)
    os.utime(stage, (int(SDE), int(SDE)))
    names = subprocess.run(["find", ".", "-mindepth", "1", "-print0"], cwd=stage, capture_output=True, check=True).stdout
    names = b"\0".join(sorted(n for n in names.split(b"\0") if n)) + b"\0"
    with open(dst, "wb") as f:
        subprocess.run(["cpio", "--null", "-o", "-H", "newc", "--reproducible", "--owner=0:0", "--quiet"],
                       cwd=stage, input=names, stdout=f, check=True)


def boot_files(tree, esp):
    """Firmware -> Microsoft-signed shim -> Debian-signed sd-boot -> Nex-signed UKI (trusted through the MOK).

    shim starts the loader named grubx64.efi in its own directory, so the removable path holds shim as
    BOOTX64.EFI and sd-boot as grubx64.efi, with MokManager beside them for enrolment. With Secure Boot off
    shim starts sd-boot the same way. The sd-boot copy under EFI/systemd is the firmware boot entry's target.
    Signed binaries only: an unsigned loader would not start on a Secure Boot host.
    """
    shim, boot = f"{tree}/usr/lib/shim", f"{tree}/usr/lib/systemd/boot/efi"
    sdboot = f"{boot}/systemd-bootx64.efi.signed"
    for src, dst in ((f"{shim}/shimx64.efi.signed", "EFI/BOOT/BOOTX64.EFI"), (sdboot, "EFI/BOOT/grubx64.efi"),
                     (f"{shim}/mmx64.efi.signed", "EFI/BOOT/mmx64.efi"), (sdboot, "EFI/systemd/systemd-bootx64.efi")):
        shutil.copy(src, f"{esp}/{dst}")


def release():
    rel = f"{OUT}/release"
    shutil.rmtree(rel, ignore_errors=True)
    os.makedirs(f"{rel}/esp/EFI/BOOT")
    os.makedirs(f"{rel}/esp/EFI/systemd")
    os.makedirs(f"{rel}/esp/loader")
    for name in ("root.erofs", "root.verity", "roothash"):
        shutil.copy(f"{OUT}/{name}", f"{rel}/{name}")
    roothash = open(f"{OUT}/roothash").read().strip()

    kver = sorted(os.listdir(f"{TREE}/usr/lib/modules"))[-1]
    uki = f"nex-core_{LABEL}_{SEQ}.efi"
    osrel = "/tmp/uki-os-release"
    with open(osrel, "w") as f:
        # sd-boot sorts entries by this version; the sequence is zero-padded so it sorts numerically.
        f.write(f'NAME="Nex Core"\nID=nex-core\nPRETTY_NAME="Nex Core {LABEL} ({SEQ})"\n'
                f'VERSION_ID="{LABEL}"\nVERSION="{SEQ:010d}"\nIMAGE_ID=nex-core\nIMAGE_VERSION="{LABEL}.{SEQ}"\n'
                f'BUILD_ID="{LABEL}-{SEQ}"\n')
    overlay = "/tmp/nex-initrd.cpio"
    initrd_overlay(overlay)
    ukify = ["ukify", "build", f"--linux={OUT}/base.vmlinuz", f"--initrd={OUT}/base.initrd", f"--initrd={overlay}",
             f"--cmdline=roothash={roothash} {CMDLINE}", f"--os-release=@{osrel}", f"--uname={kver}",
             f"--output={rel}/{uki}"]
    key, cert = os.environ.get("NEX_UKI_KEY"), os.environ.get("NEX_UKI_CERT")
    if key and cert:
        ukify += [f"--secureboot-private-key={key}", f"--secureboot-certificate={cert}"]
    env = dict(os.environ, SOURCE_DATE_EPOCH=SDE)
    run(*ukify, env=env)

    boot_files(TREE, f"{rel}/esp")
    with open(f"{rel}/esp/loader/loader.conf", "w") as f:
        f.write("timeout 3\nconsole-mode keep\neditor no\n")

    files = {}
    for root, _, names in os.walk(rel):
        for n in names:
            p = os.path.join(root, n)
            name = os.path.relpath(p, rel)
            files[name] = {"sha256": sha256(p), "size": os.path.getsize(p)}
    manifest = {"label": LABEL, "sequence": SEQ, "build_id": f"{LABEL}-{SEQ}", "roothash": roothash,
                "kernel": kver, "files": dict(sorted(files.items()))}
    with open(f"{rel}/manifest.json", "w") as f:
        json.dump(manifest, f, indent=1, sort_keys=True)
        f.write("\n")
    # Directories and manifest.json carry the wall-clock time otherwise; every entry gets the build timestamp.
    for root, dirs, names in os.walk(rel, topdown=False):
        for n in dirs + names:
            os.utime(os.path.join(root, n), (int(SDE), int(SDE)), follow_symlinks=False)
    os.utime(rel, (int(SDE), int(SDE)))
    print(f"release {rel}: {uki}, roothash {roothash}")


if __name__ == "__main__":
    {"base": base, "release": release}[sys.argv[1]]() if len(sys.argv) == 2 and sys.argv[1] in ("base", "release") else sys.exit("usage: release.py base|release")
