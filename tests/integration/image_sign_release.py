#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Sign the UKI of a built release directory with a development key and rewrite its manifest (lab use).

image/release.py signs while building when NEX_UKI_KEY and NEX_UKI_CERT are set. The lab releases of earlier
phases were built without them and rebuilding a base tree is far more work than the signature, so this applies
the same Authenticode signature (sbsign, as ukify does) to the finished UKI and recomputes the manifest hashes in
the same format as release.py. The root hash, the erofs and the verity tree are copied unchanged.

  image_sign_release.py --release DIR --out DIR --key KEY --cert CRT [--tamper]

--tamper flips one byte inside the signed UKI's kernel section instead (a negative-test copy: the signature stays
attached but no longer verifies).
"""
import argparse
import hashlib
import json
import os
import shutil
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


SUFFIX = ".image_sign_release"


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--release", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--key", required=True)
    ap.add_argument("--cert", required=True)
    ap.add_argument("--tamper", action="store_true")
    ap.add_argument("--image", default="nex-image-tools")
    a = ap.parse_args()
    rel, out = os.path.abspath(a.release), os.path.abspath(a.out)
    manifest = json.load(open(os.path.join(rel, "manifest.json")))
    if os.path.commonpath([rel, out]) in (rel, out):
        sys.exit(f"--out {out} is, contains or lies inside --release {rel}")
    marker = out.rstrip("/") + SUFFIX  # beside the release: a file inside it would be unlisted and refused by update-publish
    if os.path.exists(out):
        if not os.path.exists(marker):
            sys.exit(f"{out} exists and was not created by this script; remove it or pass a new --out")
        shutil.rmtree(out)
    shutil.copytree(rel, out, copy_function=shutil.copyfile)
    open(marker, "w").close()
    uki = next(n for n in os.listdir(out) if n.endswith(".efi"))
    key, cert = os.path.abspath(a.key), os.path.abspath(a.cert)
    tmp = os.path.join(out, uki + ".signed")
    subprocess.run(["docker", "run", "--rm", "--cpus", "2", "--memory", "4g", "-u", f"{os.getuid()}:{os.getgid()}",
                    "-v", f"{out}:/o", "-v", f"{key}:/k.key:ro", "-v", f"{cert}:/k.crt:ro", a.image,
                    "sbsign", "--key", "/k.key", "--cert", "/k.crt", "--output", f"/o/{uki}.signed", f"/o/{uki}"], check=True)
    os.replace(tmp, os.path.join(out, uki))
    if a.tamper:
        # flip a byte well inside the image (the embedded initrd), leaving the signature table in place
        path = os.path.join(out, uki)
        with open(path, "r+b") as f:
            f.seek(os.path.getsize(path) // 2)
            b = f.read(1)
            f.seek(-1, 1)
            f.write(bytes([b[0] ^ 0xFF]))
    files = {}
    for root, _, names in os.walk(out):
        for n in names:
            if n == "manifest.json" and root == out:
                continue
            p = os.path.join(root, n)
            files[os.path.relpath(p, out)] = {"sha256": sha256(p), "size": os.path.getsize(p)}
    manifest["files"] = dict(sorted(files.items()))
    with open(os.path.join(out, "manifest.json"), "w") as f:
        json.dump(manifest, f, indent=1, sort_keys=True)
        f.write("\n")
    print(f"signed {uki} in {out}{' (TAMPERED copy)' if a.tamper else ''}")


if __name__ == "__main__":
    sys.exit(main())
