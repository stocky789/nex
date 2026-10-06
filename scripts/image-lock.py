#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Pin and check the package closure of the Nex Core base image.

  scripts/image-lock.py write [--debs DIRS] [image/out/base.dpkg-list]   record the resolved closure as image/packages.lock
  scripts/image-lock.py check [--debs DIRS] [--snapshot-only] [image/out/base.dpkg-list]
                                                                         fail on drift, a missing requested package or a denylisted one

image/packages.lock holds "name<TAB>version" for every package of the built tree (dpkg-query output saved by
image/mkosi.finalize). With the pinned snapshot (image/snapshot) the same lock must come out of every build;
`check` against a fresh build's list is the drift gate.

Packages built by Nex (nex-core-local, the matched libvirt) come from local .deb files, not the snapshot. DIRS is a
colon-separated list of directories with those files (default: $NEX_PACKAGE_DIRS, what image/build.sh installs). A
built package that has a .deb there is locked as "name<TAB>-<TAB>origin=nex": a release builds these debs afresh, so
neither their version nor their bytes are pinned (the .deb must match what the build installed). With DIRS, `check`
requires a .deb for each locked one, and a .deb that carries the name of a snapshot package still fails.
`--snapshot-only` leaves the packages locked as
origin=nex out of the drift comparison: a build without them, or with a freshly built deb, is still gated on the
denylist, the requested set and every snapshot package, and a .deb that carries the name of a snapshot package fails
(image/build.sh runs it that way after the base step; its release step runs the full check with the release's debs).
"""
import os
import subprocess
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
    """name -> (version, extra fields) of a dpkg-query list or of the lock."""
    pkgs = {}
    with open(path) as f:
        for line in f:
            if line.strip():
                name, version, *extra = line.rstrip("\n").split("\t")
                pkgs[name.split(":")[0]] = (version, extra)
    return pkgs


def local_debs(dirs):
    """package name -> version for every .deb in the directories."""
    debs = {}
    for d in filter(None, dirs.split(":")):
        for entry in sorted(os.listdir(d)):
            if not entry.endswith(".deb"):
                continue
            path = os.path.join(d, entry)
            name, version = subprocess.check_output(
                ["dpkg-deb", "-W", "--showformat=${Package} ${Version}", path], text=True).split()
            if name in debs:
                sys.exit(f"image-lock: two .deb files for {name} in {dirs}")
            debs[name] = version
    return debs


def main(argv):
    if not argv or argv[0] not in ("write", "check"):
        sys.exit(__doc__)
    argv, dirs = argv[:], os.environ.get("NEX_PACKAGE_DIRS", "")
    snapshot_only = "--snapshot-only" in argv
    if snapshot_only:
        argv.remove("--snapshot-only")
    if "--debs" in argv:
        i = argv.index("--debs")
        dirs = argv[i + 1]
        del argv[i:i + 2]
    src = argv[1] if len(argv) > 1 else DEFAULT_LIST
    built = read_list(src)
    debs = local_debs(dirs)
    problems = []
    for name, version in debs.items():
        if name in built and built[name][0] != version:
            problems.append(f"{name}: the .deb is {version}, the build installed {built[name][0]}")
    # name -> (version, extra): a built package with a local .deb is Nex's.
    wanted = {name: ("-", ["origin=nex"]) if name in debs else (version, [])
              for name, (version, _) in built.items()}
    if argv[0] == "write":
        if problems:
            sys.exit("image-lock: " + "\n".join(problems))
        with open(LOCK, "w") as f:
            for name in sorted(wanted):
                f.write("\t".join([name, wanted[name][0], *wanted[name][1]]) + "\n")
        print(f"wrote {LOCK}: {len(wanted)} packages, {sum(bool(e) for _, e in wanted.values())} built by Nex")
        return
    locked = read_list(LOCK) if os.path.exists(LOCK) else {}
    if snapshot_only:
        # Only what the lock records as Nex-built is exempt: a .deb named like a snapshot package is compared
        # (the lock row carries a real version, a Nex build is "-"), whatever version it carries.
        nex = {name for name, (_, extra) in locked.items() if extra}
        wanted = {name: w for name, w in wanted.items() if name not in nex}
        locked = {name: w for name, w in locked.items() if name not in nex}
    for name in names(os.path.join(IMAGE, "base.packages")):
        if name not in built:
            problems.append(f"requested package {name} is not in the build")
    for name in names(os.path.join(IMAGE, "denylist.txt")):
        if name in built:
            problems.append(f"denylisted package {name} is in the build")
    if not locked:
        problems.append("image/packages.lock is empty or missing; run image-lock.py write")
    for name in sorted(set(wanted) | set(locked)):
        have, want = wanted.get(name), locked.get(name)
        if want is not None and "origin=nex" in want[1]:
            if name not in debs:
                problems.append(f"{name}: the locked Nex .deb is missing")
        elif have is None or want is None or have[0] != want[0]:
            problems.append(f"{name}: lock has {want and want[0]}, build has {have and have[0]}")
    if problems:
        sys.exit("image-lock: " + "\n".join(problems[:40]))
    print(f"image-lock: {len(built)} packages match the lock ({sum(bool(e) for _, e in locked.values())} built by Nex)")


if __name__ == "__main__":
    main(sys.argv[1:])
