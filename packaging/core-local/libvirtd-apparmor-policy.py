#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Maintain Nex's exact stanza in Debian's administrator-owned libvirtd include."""
import os
from pathlib import Path
import stat
import sys
import tempfile

BEGIN = b"# BEGIN Nex storage guard\n"
RULE = b"/usr/lib/nex/nex-core-local rix,\n"
END = b"# END Nex storage guard\n"
BLOCK = BEGIN + RULE + END


def policy_path(root: str) -> Path:
    return Path(root or "/") / "etc/apparmor.d/local/usr.sbin.libvirtd"


def marker_indexes(lines: list[bytes], marker: bytes) -> list[int]:
    return [index for index, line in enumerate(lines)
            if line.rstrip(b"\r\n") == marker.rstrip(b"\n")]


def block_range(data: bytes) -> tuple[int, int] | None:
    lines = data.splitlines(keepends=True)
    begins = marker_indexes(lines, BEGIN)
    ends = marker_indexes(lines, END)
    if not begins and not ends:
        return None
    if len(begins) != 1 or len(ends) != 1:
        raise ValueError("malformed or foreign Nex AppArmor block")
    index = begins[0]
    if lines[index:index + 3] != [BEGIN, RULE, END] or ends[0] != index + 2:
        raise ValueError("malformed or foreign Nex AppArmor block")
    start = sum(len(line) for line in lines[:index])
    return start, start + len(BLOCK)


def replace(path: Path, content: bytes) -> None:
    mode = stat.S_IMODE(path.stat().st_mode)
    owner = path.stat().st_uid, path.stat().st_gid
    descriptor, temporary = tempfile.mkstemp(prefix=f".{path.name}.nex.", dir=path.parent)
    try:
        with os.fdopen(descriptor, "wb") as output:
            output.write(content)
        os.chmod(temporary, mode)
        os.chown(temporary, *owner)
        os.replace(temporary, path)
    except BaseException:
        os.unlink(temporary)
        raise


def main() -> None:
    if len(sys.argv) != 3 or sys.argv[2] not in {"install", "remove"}:
        raise SystemExit(f"usage: {sys.argv[0]} ROOT install|remove")
    root, action = sys.argv[1], sys.argv[2]
    path = policy_path(root)
    path.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
    try:
        metadata = path.lstat()
    except FileNotFoundError:
        if action == "remove":
            return
        path.touch(mode=0o644)
    else:
        if not stat.S_ISREG(metadata.st_mode):
            raise SystemExit(f"refusing non-regular administrator policy file {path}")
    data = path.read_bytes()
    try:
        located = block_range(data)
    except ValueError as error:
        raise SystemExit(f"refusing {error} in {path}")
    if action == "install":
        if located is None:
            replace(path, BLOCK + data)
        return
    if located is not None:
        start, end = located
        replace(path, data[:start] + data[end:])


if __name__ == "__main__":
    main()
