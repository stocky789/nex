#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Install and select immutable static Nex website releases."""
import argparse
from contextlib import contextmanager
import fcntl
import os
from pathlib import Path
import re
import shutil
import sys
import tempfile
from urllib.error import URLError
from urllib.request import urlopen


RELEASE_ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}\Z")
REQUIRED_FILES = {"index.html", "LICENSE.txt", "THIRD-PARTY-NOTICES.txt"}
ALLOWED_SUFFIXES = {".css", ".gif", ".html", ".ico", ".jpeg", ".jpg", ".js",
                    ".map", ".mjs", ".png", ".svg", ".txt", ".webp", ".woff", ".woff2"}
FORBIDDEN_NAMES = {".npmrc", "id_ed25519", "id_rsa"}
FORBIDDEN_SUFFIXES = {".env", ".key", ".pem"}
SECRET = re.compile(rb"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----|\b(?:gh[pousr]_|github_pat_|AKIA)")


class ReleaseError(RuntimeError):
    pass


def validate_release_id(release_id):
    if not RELEASE_ID.fullmatch(release_id):
        raise ReleaseError("release id must contain only letters, digits, dot, underscore, or dash")


def validate_artifact(source):
    source = Path(source)
    if not source.is_dir() or source.is_symlink():
        raise ReleaseError("artifact must be a real directory")
    present = set()
    for path in source.rglob("*"):
        relative = path.relative_to(source)
        if path.is_symlink() or not (path.is_dir() or path.is_file()):
            raise ReleaseError(f"artifact contains unsafe path: {relative}")
        if path.is_dir():
            continue
        present.add(relative.as_posix())
        if path.name in FORBIDDEN_NAMES or path.suffix.lower() in FORBIDDEN_SUFFIXES:
            raise ReleaseError(f"artifact contains forbidden file: {relative}")
        if path.suffix.lower() not in ALLOWED_SUFFIXES:
            raise ReleaseError(f"artifact contains unsupported file: {relative}")
        if path.stat().st_mode & 0o111:
            raise ReleaseError(f"artifact contains executable file: {relative}")
        if path.suffix.lower() not in {".gif", ".jpeg", ".jpg", ".png", ".webp"}:
            if SECRET.search(path.read_bytes()):
                raise ReleaseError(f"artifact appears to contain a credential: {relative}")
    missing = REQUIRED_FILES - present
    if missing:
        raise ReleaseError("artifact is missing: " + ", ".join(sorted(missing)))


def health_check(url, expected):
    try:
        with urlopen(url, timeout=10) as response:
            body = response.read(1024 * 1024).decode("utf-8", "replace")
            return response.status == 200 and expected in body
    except (OSError, URLError):
        return False


def _root_owned_read_only(path):
    for item in [path, *path.rglob("*")]:
        os.chown(item, 0, 0, follow_symlinks=False)
        os.chmod(item, 0o755 if item.is_dir() else 0o644, follow_symlinks=False)


def _switch(current, target):
    link = current.with_name(f".{current.name}.new")
    link.unlink(missing_ok=True)
    link.symlink_to(target)
    os.replace(link, current)


def _release_path(releases, release_id):
    validate_release_id(release_id)
    path = releases / release_id
    if not path.is_dir() or path.is_symlink():
        raise ReleaseError("release does not exist or is not a directory")
    return path


def publish(source, release_id, root, check=None):
    root = Path(root)
    releases = root / "releases"
    validate_release_id(release_id)
    validate_artifact(source)
    releases.mkdir(mode=0o755, parents=True, exist_ok=True)
    target = releases / release_id
    if target.exists() or target.is_symlink():
        raise ReleaseError("release id already exists")
    previous = current_release(root)
    with tempfile.TemporaryDirectory(prefix=f".{release_id}.", dir=releases) as staging:
        staged = Path(staging) / release_id
        shutil.copytree(source, staged, symlinks=True)
        validate_artifact(staged)
        _root_owned_read_only(staged)
        os.replace(staged, target)
    _switch(root / "current", target)
    if check and not check():
        if previous is None:
            (root / "current").unlink(missing_ok=True)
        else:
            _switch(root / "current", previous)
        raise ReleaseError("health check failed; restored previous release")
    return target


def current_release(root):
    current = Path(root) / "current"
    if not current.is_symlink():
        return None
    target = current.resolve(strict=False)
    releases = (Path(root) / "releases").resolve(strict=False)
    if target.parent != releases or not target.is_dir() or target.is_symlink():
        raise ReleaseError("current selector is unsafe")
    return target


def rollback(release_id, root, check=None):
    root = Path(root)
    target = _release_path(root / "releases", release_id)
    previous = current_release(root)
    _switch(root / "current", target)
    if check and not check():
        if previous is None:
            (root / "current").unlink(missing_ok=True)
        else:
            _switch(root / "current", previous)
        raise ReleaseError("health check failed; restored previous release")
    return target


@contextmanager
def website_lock(root):
    # ponytail: global release lock; per-site locks if one tool manages multiple sites.
    root = Path(root)
    root.mkdir(mode=0o755, parents=True, exist_ok=True)
    with (root / ".release.lock").open("w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        yield


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", default="/srv/nex-website")
    parser.add_argument("--health-url")
    parser.add_argument("--expected", default="Nex")
    parser.add_argument("--no-health", action="store_true",
                        help="only for initial installation before the web service is running")
    commands = parser.add_subparsers(dest="command", required=True)
    publish_parser = commands.add_parser("publish")
    publish_parser.add_argument("artifact")
    publish_parser.add_argument("release_id")
    rollback_parser = commands.add_parser("rollback")
    rollback_parser.add_argument("release_id")
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error("run as root; releases must be root-owned")
    if args.no_health and args.health_url:
        parser.error("choose --no-health or --health-url")
    if not args.no_health and not args.health_url:
        parser.error("--health-url is required after initial installation")
    check = None if args.no_health else lambda: health_check(args.health_url, args.expected)
    try:
        with website_lock(args.root):
            if args.no_health and (args.command != "publish" or
                                   os.path.lexists(Path(args.root) / "current")):
                raise ReleaseError("--no-health is only allowed for the first publish before current exists")
            target = (publish(args.artifact, args.release_id, args.root, check)
                      if args.command == "publish" else rollback(args.release_id, args.root, check))
    except ReleaseError as error:
        print(f"website release: {error}", file=sys.stderr)
        return 1
    print(target)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
