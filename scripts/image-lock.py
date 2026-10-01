#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Pin and check the package closure of the Nex Core base image.

  scripts/image-lock.py write [image/out/base.dpkg-list]   record the resolved closure as image/packages.lock
  scripts/image-lock.py check [image/out/base.dpkg-list]   fail on drift, a missing requested package or a denylisted one

image/packages.lock holds "name<TAB>version" for every package of the built tree (dpkg-query output saved by
image/mkosi.finalize). With the pinned snapshot (image/snapshot) the same lock must come out of every build;
`check` against a fresh build's list is the drift gate.
"""
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
IMAGE = os.path.join(os.path.dirname(HERE), "image")
LOCK = os.path.join(IMAGE, "packages.lock")
DEFAULT_LIST = os.path.join(IMAGE, "out", "base.dpkg-list")


def names(path):
    out = []
    with open(path) as f:
        for line in f:
            line = line.split("#", 1)[0].strip()
            if line:
                out.append(line)
    return out


def read_list(path):
    pkgs = {}
    with open(path) as f:
        for line in f:
            if line.strip():
                name, _, version = line.rstrip("\n").partition("\t")
                pkgs[name.split(":")[0]] = version
    return pkgs


def main(argv):
    if not argv or argv[0] not in ("write", "check"):
        sys.exit(__doc__)
    src = argv[1] if len(argv) > 1 else DEFAULT_LIST
    built = read_list(src)
    if argv[0] == "write":
        with open(LOCK, "w") as f:
            for name in sorted(built):
                f.write(f"{name}\t{built[name]}\n")
        print(f"wrote {LOCK}: {len(built)} packages")
        return
    problems = []
    locked = read_list(LOCK) if os.path.exists(LOCK) else {}
    for name in names(os.path.join(IMAGE, "base.packages")):
        if name not in built:
            problems.append(f"requested package {name} is not in the build")
    for name in names(os.path.join(IMAGE, "denylist.txt")):
        if name in built:
            problems.append(f"denylisted package {name} is in the build")
    if not locked:
        problems.append("image/packages.lock is empty or missing; run image-lock.py write")
    for name in sorted(set(built) | set(locked)):
        if built.get(name) != locked.get(name):
            problems.append(f"{name}: lock has {locked.get(name)}, build has {built.get(name)}")
    if problems:
        sys.exit("image-lock: " + "\n".join(problems[:40]))
    print(f"image-lock: {len(built)} packages match the lock")


if __name__ == "__main__":
    main(sys.argv[1:])
