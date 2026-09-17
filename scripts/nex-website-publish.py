#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Publish or roll back a static website release on the website host.

Runs as root. Reads the private health URL from /etc/nex-website/health.env and
delegates to website-release.py so CI only needs passwordless sudo for this
one command (same pattern as nex-apt-publish).
"""
from __future__ import annotations

import argparse
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys


HEALTH_ENV = Path("/etc/nex-website/health.env")
RELEASE_TOOL = Path("/usr/local/lib/nex-website/website-release.py")
DEFAULT_KEEP = 10
RELEASE_ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}\Z")


class PublishError(RuntimeError):
    pass


def load_health_env(path: Path = HEALTH_ENV) -> tuple[str, str]:
    if not path.is_file() or path.is_symlink():
        raise PublishError(f"missing health env: {path}")
    values: dict[str, str] = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key.strip()] = value.strip().strip("'\"")
    url = values.get("NEX_WEBSITE_URL", "")
    if not url:
        raise PublishError("NEX_WEBSITE_URL is required in health.env")
    expected = values.get("NEX_WEBSITE_EXPECTED", "Nex") or "Nex"
    return url, expected


def prune_releases(site_root: Path, keep: int, current_id: str) -> None:
    if keep < 1:
        raise PublishError("--keep must be at least 1")
    releases = site_root / "releases"
    if not releases.is_dir() or releases.is_symlink():
        return
    entries = []
    for path in releases.iterdir():
        if path.is_symlink() or not path.is_dir():
            continue
        if not RELEASE_ID.fullmatch(path.name):
            continue
        entries.append(path)
    # Newest first by mtime, then name for stability.
    entries.sort(key=lambda p: (p.stat().st_mtime_ns, p.name), reverse=True)
    retained = 0
    for path in entries:
        if path.name == current_id or retained < keep:
            retained += 1
            continue
        shutil.rmtree(path)


def run_release(args: list[str]) -> None:
    if not RELEASE_TOOL.is_file() or RELEASE_TOOL.is_symlink():
        raise PublishError(f"missing release tool: {RELEASE_TOOL}")
    if os.geteuid() != 0:
        raise PublishError("run as root")
    completed = subprocess.run(
        [sys.executable, str(RELEASE_TOOL), *args],
        check=False,
    )
    if completed.returncode != 0:
        raise PublishError("website-release.py failed")


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Publish or roll back the Nex static website as root.",
    )
    parser.add_argument(
        "--keep",
        type=int,
        default=DEFAULT_KEEP,
        help=f"retain this many releases including current (default {DEFAULT_KEEP})",
    )
    parser.add_argument(
        "--root",
        default="/srv/nex-website",
        help="website release root (default /srv/nex-website)",
    )
    commands = parser.add_subparsers(dest="command", required=True)
    publish = commands.add_parser("publish")
    publish.add_argument("artifact")
    publish.add_argument("release_id")
    rollback = commands.add_parser("rollback")
    rollback.add_argument("release_id")
    args = parser.parse_args()

    try:
        url, expected = load_health_env(HEALTH_ENV)
        site_root = Path(args.root)
        if not RELEASE_ID.fullmatch(args.release_id):
            raise PublishError("invalid release id")
        if args.command == "publish":
            artifact = Path(args.artifact)
            if not artifact.is_dir() or artifact.is_symlink():
                raise PublishError("artifact must be a real directory")
            run_release([
                "--root", str(site_root),
                "--health-url", url,
                "--expected", expected,
                "publish", str(artifact), args.release_id,
            ])
            prune_releases(site_root, args.keep, args.release_id)
        else:
            run_release([
                "--root", str(site_root),
                "--health-url", url,
                "--expected", expected,
                "rollback", args.release_id,
            ])
    except PublishError as error:
        print(f"nex-website-publish: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
