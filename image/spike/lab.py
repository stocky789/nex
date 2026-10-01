#!/usr/bin/env python3
"""Throwaway lab helpers for the Phase I-0 spikes: container tool runner, release/ESP/disk helpers.

Everything runs against disposable raw images under .cache/spike/lab; nothing touches a real disk.
"""
import json, os, re, subprocess

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, "..", ".."))
CACHE = os.path.join(ROOT, ".cache", "spike")
LAB = os.path.join(CACHE, "lab")
BUILD = os.path.join(CACHE, "build1", "output")
ESP_OFF = 2048 * 512


def sh(cmd, check=False, timeout=900, stdin=None):
    r = subprocess.run(cmd, shell=True, capture_output=True, text=True, timeout=timeout, input=stdin)
    out = (r.stdout + r.stderr).strip()
    if check and r.returncode:
        raise RuntimeError(f"{cmd}\n{out}")
    return r.returncode, out


def T(script, env=None, check=True, extra=""):
    """Run a shell snippet in the pinned Debian trixie tools container (root, /lab, /o, /s mounted)."""
    envs = " ".join(f"-e {k}='{v}'" for k, v in (env or {}).items())
    cmd = (f"docker run -i --rm {envs} {extra} -v {LAB}:/lab -v {BUILD}:/o:ro -v {HERE}/scripts:/s:ro "
           f"nex-spike-tools sh -s")
    return sh(cmd, check=check, stdin=script)


def esp(img, op, *args):
    rel = os.path.relpath(img, LAB)
    rc, out = T(f"/s/esp.sh /lab/{rel} {op} " + " ".join(f"'{a}'" for a in args), check=False)
    if rc and op in ("put", "rm", "mv"):
        raise RuntimeError(f"esp {op} {args}: {out}")
    return rc, out


def uuids(roothash):
    return f"{roothash[:8]}-{roothash[8:12]}-{roothash[12:16]}-{roothash[16:20]}-{roothash[20:32]}", \
           f"{roothash[32:40]}-{roothash[40:44]}-{roothash[44:48]}-{roothash[48:52]}-{roothash[52:64]}"


def release(label, mod="", erofs_opts=""):
    """Build /lab/rel/<label>/{root.erofs,root.verity,roothash}."""
    _, out = T(f"/s/mkrelease.sh '{label}' '{mod}'", env={"EROFS_OPTS": erofs_opts})
    return out.splitlines()[-1]


def roothash(label):
    return open(f"{LAB}/rel/{label}/roothash").read().strip()


def parts(img):
    rel = os.path.relpath(img, LAB)
    _, out = T(f"sfdisk -J /lab/{rel}")
    return {p["name"]: p for p in json.loads(out)["partitiontable"]["partitions"]}


def flash(img, slot, label):
    """Host-side slot write: erofs -> nex-root-<slot>, hash tree -> nex-verity-<slot>, UUIDs from the roothash."""
    rel = os.path.relpath(img, LAB)
    p = parts(img)
    r, v = p[f"nex-root-{slot}"], p[f"nex-verity-{slot}"]
    ru, vu = uuids(roothash(label))
    T(f"dd if=/lab/rel/{label}/root.erofs of=/lab/{rel} bs=512 seek={r['start']} conv=notrunc status=none && "
      f"dd if=/lab/rel/{label}/root.verity of=/lab/{rel} bs=512 seek={v['start']} conv=notrunc status=none && "
      f"sfdisk --part-uuid /lab/{rel} {p_index(r)} {ru} && sfdisk --part-uuid /lab/{rel} {p_index(v)} {vu}")


def p_index(part):
    return int(re.search(r"(\d+)$", part["node"]).group(1))


def uki(out, label, extra_cmdline="", extra_initrds=(), sign=False):
    rel = os.path.relpath(out, LAB)
    T(f"/s/mkuki.sh /lab/{rel} '{label}' {roothash(label)} {extra_cmdline}",
      env={"EXTRA_INITRDS": " ".join(extra_initrds), "SIGN": "1" if sign else "0"})
