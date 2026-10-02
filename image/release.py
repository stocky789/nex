#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Turn the mkosi base tree into a release set. Runs inside the nex-image-tools container (image/build.sh).

  release.py base      erofs + dm-verity hash tree + roothash from /out/base       -> /out/root.erofs, root.verity, roothash
  release.py release   UKI, ESP tree and manifest.json into /out/release/          (what nex-hostimage installs)
  release.py controller  the Controller guest: erofs + verity of /out/controller, UKI, and the raw GPT system disk
                         /out/controller-release/system.img (read-only on the VM; Core's two-disk Controller profile)

Every input that reaches the output is fixed: timestamps (SOURCE_DATE_EPOCH), the erofs UUID, the
verity UUID and salt, file order. The same tree gives the same roothash.
"""
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys

OUT = "/out"
TREE = f"{OUT}/base"
SDE = os.environ.get("SOURCE_DATE_EPOCH") or "1790000000"
LABEL = os.environ.get("NEX_LABEL", "dev")
SEQ = int(os.environ.get("NEX_SEQUENCE", "0"))
# The label is part of the UKI file name and nex-hostimage (release.go ukiNameRE) refuses any other characters;
# '+' would also read as a boot counter.
LABEL_RE = r"[A-Za-z0-9.~_-]+"
if not re.fullmatch(LABEL_RE, LABEL) or SEQ < 0:
    sys.exit(f"NEX_LABEL {LABEL!r} must match {LABEL_RE} and NEX_SEQUENCE {SEQ} must not be negative")
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


def uki_os_release(name, image_id):
    """os-release embedded in a UKI. sd-boot orders the entries of one IMAGE_ID by IMAGE_VERSION (VERSION only
    when that is absent), compared as version strings, newest first. Both carry the zero-padded sequence alone,
    so that order is the numeric sequence order nex-hostimage assumes whatever the label looks like."""
    return (f'NAME="{name}"\nID={image_id}\nPRETTY_NAME="{name} {LABEL} ({SEQ})"\nVERSION_ID="{LABEL}"\n'
            f'VERSION="{SEQ:010d}"\nIMAGE_ID={image_id}\nIMAGE_VERSION="{SEQ:010d}"\nBUILD_ID="{LABEL}-{SEQ}"\n')


def base(tree=TREE, dest=OUT):
    for name in ("root.erofs", "root.verity", "roothash"):
        if os.path.exists(f"{dest}/{name}"):
            os.unlink(f"{dest}/{name}")
    opts = ["-zlz4hc,12", f"-T{SDE}", f"-U{EROFS_UUID}"]
    for e in EXCLUDES:
        opts.append(f"--exclude-regex=^{e}/.")
    run("mkfs.erofs", *opts, f"{dest}/root.erofs", tree)
    out = subprocess.run(
        ["veritysetup", "format", "--hash=sha256", "--data-block-size=4096", "--hash-block-size=4096", "--format=1",
         f"--uuid={VERITY_UUID}", f"--salt={VERITY_SALT}", f"{dest}/root.erofs", f"{dest}/root.verity"],
        check=True, capture_output=True, text=True).stdout
    os.chmod(f"{dest}/root.verity", 0o644)
    roothash = [l.split()[-1] for l in out.splitlines() if l.startswith("Root hash:")][0]
    with open(f"{dest}/roothash", "w") as f:
        f.write(roothash + "\n")
    print(f"roothash {roothash}")
    print(f"root.erofs {os.path.getsize(dest + '/root.erofs')} bytes, root.verity {os.path.getsize(dest + '/root.verity')} bytes")


def initrd_overlay(dst, only=None):
    """newc cpio of image/initrd (units and config appended to the initrd), reproducibly. `only` limits it to
    the listed paths below image/initrd (the Controller guest takes the /etc overlay and the paced reboot only: no md RAID parts)."""
    src = "/nex/image/initrd"
    stage = "/tmp/nex-initrd"
    shutil.rmtree(stage, ignore_errors=True)
    if only is None:
        shutil.copytree(src, stage, symlinks=True)
    else:
        os.makedirs(stage)
        for rel in only:
            os.makedirs(os.path.dirname(f"{stage}/{rel}"), exist_ok=True)
            shutil.copy(f"{src}/{rel}", f"{stage}/{rel}", follow_symlinks=False)  # the .wants entry is a link
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
        f.write(uki_os_release("Nex Core", "nex-core"))
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


# Controller guest (controller profile). The system disk is a GPT image: ESP with systemd-boot and the UKI, the erofs root and
# its verity tree typed and identified so that systemd-gpt-auto and the roothash= command line find them. UEFI only.
CTREE = f"{OUT}/controller"
CREL = f"{OUT}/controller-release"
CESP_SIZE = 128 << 20
CMDLINE_CONTROLLER = "ro console=tty0 console=ttyS0,115200n8 panic=10 systemd.firstboot=off"
GPT_LABEL_ID = "5e1ec7ed-0000-4000-8000-0000000000c0"
GPT_ESP_ID = "5e1ec7ed-0000-4000-8000-0000000000c1"
GPT_ESP, GPT_ROOT, GPT_VERITY = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b", "4f68bce3-e8cd-4db1-96e7-fbcaf984b709", "2c7357ed-ebd2-46d9-aec1-23d437ec2bf5"
MIB = 1 << 20


def mib_up(n):
    return (n + MIB - 1) // MIB * MIB


def uuid_text(h):
    return f"{h[:8]}-{h[8:12]}-{h[12:16]}-{h[16:20]}-{h[20:]}"


def controller():
    shutil.rmtree(CREL, ignore_errors=True)
    os.makedirs(CREL)
    base(CTREE, CREL)
    roothash = open(f"{CREL}/roothash").read().strip()
    kver = sorted(os.listdir(f"{CTREE}/usr/lib/modules"))[-1]
    esp = f"{CREL}/esp"
    os.makedirs(f"{esp}/EFI/BOOT")
    os.makedirs(f"{esp}/EFI/Linux")
    osrel = "/tmp/uki-os-release-controller"
    with open(osrel, "w") as f:
        f.write(uki_os_release("Nex Controller", "nex-controller"))
    overlay = "/tmp/nex-controller-initrd.cpio"
    # The guest's UKI has no boot counter: a failed initrd step reboots after a pause (nex-initrd-reboot-paced.service),
    # never to a locked shell. The counted-reboot unit is skipped there (no counter) but the shared drop-ins name it.
    initrd_overlay(overlay, only=["usr/lib/systemd/system/nex-etc-overlay.service",
                                  "usr/lib/systemd/system/initrd-fs.target.wants/nex-etc-overlay.service",
                                  "usr/lib/systemd/system/nex-initrd-reboot.service",
                                  "usr/lib/systemd/system/nex-initrd-reboot-paced.service",
                                  "usr/lib/systemd/system/emergency.service.d/10-nex-reboot.conf",
                                  "usr/lib/systemd/system/rescue.service.d/10-nex-reboot.conf"])
    uki = f"{esp}/EFI/Linux/nex-controller_{LABEL}_{SEQ}.efi"
    env = dict(os.environ, SOURCE_DATE_EPOCH=SDE)
    run("ukify", "build", f"--linux={OUT}/controller.vmlinuz", f"--initrd={OUT}/controller.initrd", f"--initrd={overlay}",
        f"--cmdline=roothash={roothash} {CMDLINE_CONTROLLER}", f"--os-release=@{osrel}", f"--uname={kver}", f"--output={uki}", env=env)
    shutil.copy(f"{CTREE}/usr/lib/systemd/boot/efi/systemd-bootx64.efi.signed", f"{esp}/EFI/BOOT/BOOTX64.EFI")
    for root, dirs, names in os.walk(esp):
        for n in dirs + names:
            os.utime(os.path.join(root, n), (int(SDE), int(SDE)), follow_symlinks=False)
    os.utime(esp, (int(SDE), int(SDE)))

    espimg = f"{CREL}/esp.img"
    with open(espimg, "wb") as f:
        f.truncate(CESP_SIZE)
    mt = dict(os.environ, SOURCE_DATE_EPOCH=SDE, MTOOLS_SKIP_CHECK="1")  # mkfs.vfat --invariant rejects an empty SOURCE_DATE_EPOCH
    run("mkfs.vfat", "-F", "32", "-n", "NEXESP", "-i", "5E1EC7ED", "--invariant", espimg, env=mt)
    run("mcopy", "-i", espimg, "-s", "-m", f"{esp}/EFI", "::", env=mt)

    erofs, verity = f"{CREL}/root.erofs", f"{CREL}/root.verity"
    esize, vsize = os.path.getsize(erofs), os.path.getsize(verity)
    start = 2048  # 1 MiB in 512-byte sectors
    esp_sectors, root_sectors, ver_sectors = CESP_SIZE // 512, mib_up(esize) // 512, mib_up(vsize) // 512
    root_start, ver_start = start + esp_sectors, start + esp_sectors + root_sectors
    total = (ver_start + ver_sectors) * 512 + MIB
    image = f"{CREL}/system.img"
    with open(image, "wb") as f:
        f.truncate(total)
    script = (f"label: gpt\nlabel-id: {GPT_LABEL_ID}\n"
              f"start={start}, size={esp_sectors}, type={GPT_ESP}, uuid={GPT_ESP_ID}, name=nex-esp\n"
              f"start={root_start}, size={root_sectors}, type={GPT_ROOT}, uuid={uuid_text(roothash[:32])}, name=nex-controller-root\n"
              f"start={ver_start}, size={ver_sectors}, type={GPT_VERITY}, uuid={uuid_text(roothash[32:])}, name=nex-controller-verity\n")
    run("sfdisk", "--no-reread", "--no-tell-kernel", image, input=script.encode())
    for src, sector in ((espimg, start), (erofs, root_start), (verity, ver_start)):
        run("dd", f"if={src}", f"of={image}", "bs=1M", f"seek={sector * 512 // MIB}", "conv=notrunc", "status=none")
    os.unlink(espimg)
    files = {}
    for name in ("system.img", "root.erofs", "root.verity", "roothash"):
        p = f"{CREL}/{name}"
        files[name] = {"sha256": sha256(p), "size": os.path.getsize(p)}
    manifest = {"label": LABEL, "sequence": SEQ, "build_id": f"{LABEL}-{SEQ}", "roothash": roothash, "kernel": kver,
                "profile": "controller", "files": files}
    with open(f"{CREL}/manifest.json", "w") as f:
        json.dump(manifest, f, indent=1, sort_keys=True)
        f.write("\n")
    print(f"controller release {CREL}: system.img {files['system.img']['sha256']} roothash {roothash}")


COMMANDS = {"base": base, "release": release, "controller": controller}

if __name__ == "__main__":
    COMMANDS[sys.argv[1]]() if len(sys.argv) == 2 and sys.argv[1] in COMMANDS else sys.exit("usage: release.py base|release|controller")
