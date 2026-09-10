#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Install a built Nex Core ISO into the website download tree and prune old ones.

Runs on the website host. Packages go to the APT archive through nex-apt-publish;
this handles the ISO, which is only needed for fresh installs. Retention is bounded
because each release is over a gigabyte and the archive already holds ~740 MB of
packages per version.

Layout, all under one root owned by the publishing user (no root privilege needed):

    <root>/0.1.6~alpha1/nex-core-0.1.6~alpha1-amd64.iso
    <root>/0.1.6~alpha1/nex-core-0.1.6~alpha1-amd64.iso.sha256
    <root>/latest -> 0.1.6~alpha1
"""
import argparse
import hashlib
import os
import re
import shutil
import sys
import tempfile
from pathlib import Path

VERSION_DIR = re.compile(r"^[0-9][A-Za-z0-9.+~]*$")
ISO_NAME = re.compile(r"^nex-core-(?P<version>[0-9][A-Za-z0-9.+~]*)-amd64\.iso$")


class ReleasePublishError(RuntimeError):
    pass


def _real_dir(path, create=False):
    path = Path(path)
    if path.is_symlink():
        raise ReleasePublishError(f"refusing symlink: {path}")
    if path.is_dir():
        return path
    if not create:
        raise ReleasePublishError(f"missing directory: {path}")
    path.mkdir(mode=0o755, parents=True)
    return path


def iso_version(iso):
    match = ISO_NAME.match(Path(iso).name)
    if not match:
        raise ReleasePublishError(
            f"unexpected ISO filename {Path(iso).name!r}; "
            "expected nex-core-<version>-amd64.iso")
    return match.group("version")


def sha256(path, chunk=1 << 20):
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for block in iter(lambda: handle.read(chunk), b""):
            digest.update(block)
    return digest.hexdigest()


def debian_sort_key(version):
    """Order versions the way dpkg does for the subset release tags use.

    '~' sorts below everything including the empty string, so 0.1.6~alpha1 comes
    before 0.1.6. Anything else compares numerically per dot-separated field.
    """
    release, _, pre = version.partition("~")
    fields = []
    for part in release.split("."):
        fields.append((0, int(part)) if part.isdigit() else (1, 0, part))
    # No pre-release sorts above any pre-release of the same version.
    return (fields, pre == "", pre)


def existing_releases(root):
    found = []
    for child in sorted(Path(root).iterdir()):
        if child.is_symlink() or not child.is_dir():
            continue
        if VERSION_DIR.match(child.name):
            found.append(child.name)
    return sorted(found, key=debian_sort_key)


def prune(root, keep):
    if keep < 1:
        raise ReleasePublishError("keep must be at least 1")
    releases = existing_releases(root)
    removed = []
    for version in releases[:-keep] if len(releases) > keep else []:
        shutil.rmtree(Path(root) / version)
        removed.append(version)
    return removed


def publish(root, iso, keep):
    root = _real_dir(root, create=True)
    iso = Path(iso)
    if iso.is_symlink() or not iso.is_file():
        raise ReleasePublishError(f"ISO must be a regular file: {iso}")
    version = iso_version(iso)
    digest = sha256(iso)

    # Stage beside the target so the move is atomic on the same filesystem, and a
    # failed upload never leaves a half-written ISO that looks installable.
    target = root / version
    staging = Path(tempfile.mkdtemp(prefix=f".{version}.", dir=root))
    try:
        shutil.copy2(iso, staging / iso.name)
        if sha256(staging / iso.name) != digest:
            raise ReleasePublishError("ISO changed while being copied")
        (staging / (iso.name + ".sha256")).write_text(f"{digest}  {iso.name}\n")
        for entry in staging.iterdir():
            os.chmod(entry, 0o644)
        os.chmod(staging, 0o755)
        if target.exists() or target.is_symlink():
            if target.is_symlink():
                raise ReleasePublishError(f"refusing symlink: {target}")
            shutil.rmtree(target)
        os.rename(staging, target)
        staging = None
    finally:
        if staging is not None:
            shutil.rmtree(staging, ignore_errors=True)

    # Point latest at the newest release present, which is not necessarily the one
    # just published if an older version were ever republished.
    newest = existing_releases(root)[-1]
    link = root / "latest"
    temporary = root / ".latest.tmp"
    if temporary.is_symlink() or temporary.exists():
        temporary.unlink()
    temporary.symlink_to(newest)
    os.replace(temporary, link)
    return version, digest, newest, prune(root, keep)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default="/srv/nex-downloads")
    parser.add_argument("--iso", required=True)
    parser.add_argument("--keep", type=int, default=3,
                        help="number of releases to retain (default 3)")
    args = parser.parse_args(argv)
    try:
        version, digest, newest, removed = publish(args.root, args.iso, args.keep)
    except (ReleasePublishError, OSError) as error:
        print(f"release-publish: {error}", file=sys.stderr)
        return 1
    print(f"published {version} sha256={digest}")
    print(f"latest -> {newest}")
    if removed:
        print("pruned " + ", ".join(removed))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
