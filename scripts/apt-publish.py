#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Publish a signed Debian APT repository layout for Nex packages."""
import argparse
from datetime import datetime, timezone
from email.utils import format_datetime
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile


DEB_NAME = re.compile(r"^[a-z0-9][a-z0-9.+-]+_[A-Za-z0-9.+~+-]+_[a-z0-9]+\.deb$")
KEY_UID = "Nex Archive Signing Key <apt@nex.local>"


class AptPublishError(RuntimeError):
    pass


def _refuse_symlink(path, label):
    path = Path(path)
    if path.is_symlink():
        raise AptPublishError(f"{label} must not be a symlink: {path}")
    return path


def _ensure_real_dir(path, *, create=False, mode=0o755):
    path = Path(path)
    if path.exists() or path.is_symlink():
        if path.is_symlink() or not path.is_dir():
            raise AptPublishError(f"refusing symlink or non-directory: {path}")
        return path
    if not create:
        raise AptPublishError(f"missing directory: {path}")
    parent = path.parent
    if parent != path:
        _ensure_real_dir(parent, create=True, mode=mode)
    path.mkdir(mode=mode, exist_ok=False)
    if path.is_symlink() or not path.is_dir():
        raise AptPublishError(f"refusing symlink or non-directory: {path}")
    return path


def _safe_under(root, relative):
    root = _refuse_symlink(root, "repo root").resolve(strict=False)
    relative = Path(relative)
    if relative.is_absolute() or ".." in relative.parts:
        raise AptPublishError(f"unsafe relative path: {relative}")
    target = root.joinpath(*relative.parts)
    current = root
    for part in relative.parts[:-1]:
        current = current / part
        if current.exists() or current.is_symlink():
            if current.is_symlink() or not current.is_dir():
                raise AptPublishError(f"refusing symlink in repo path: {current}")
    if target.exists() or target.is_symlink():
        if target.is_symlink():
            raise AptPublishError(f"refusing symlink in repo path: {target}")
    return target


def _write_bytes(path, data, mode=0o644):
    path = Path(path)
    if path.is_symlink():
        raise AptPublishError(f"refusing to write through symlink: {path}")
    parent = _ensure_real_dir(path.parent, create=True)
    fd, tmp_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=parent)
    tmp = Path(tmp_name)
    try:
        with os.fdopen(fd, "wb") as handle:
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(tmp, mode)
        os.replace(tmp, path)
    finally:
        tmp.unlink(missing_ok=True)
    if path.is_symlink():
        raise AptPublishError(f"refusing symlink result: {path}")


def _write_text(path, text, mode=0o644):
    _write_bytes(path, text.encode("utf-8"), mode=mode)


def validate_deb_name(name):
    if not DEB_NAME.fullmatch(name):
        raise AptPublishError(f"unsafe or invalid deb filename: {name}")
    return name.split("_", 1)[0]


def collect_debs(paths):
    debs = []
    for raw in paths:
        path = Path(raw)
        if path.is_symlink():
            raise AptPublishError(f"refusing symlink input: {path}")
        if path.is_dir():
            for child in sorted(path.iterdir()):
                if child.is_symlink() or not child.is_file():
                    continue
                if child.name.endswith(".deb"):
                    validate_deb_name(child.name)
                    debs.append(child)
            continue
        if not path.is_file():
            raise AptPublishError(f"deb path not found: {path}")
        validate_deb_name(path.name)
        debs.append(path)
    return debs


def find_scanpackages():
    for candidate in ("/usr/bin/dpkg-scanpackages", shutil.which("dpkg-scanpackages")):
        if candidate and Path(candidate).is_file():
            return candidate
    return None


def copy_deb(deb, repo_root, component):
    package = validate_deb_name(deb.name)
    letter = package[0]
    relative_dir = Path("pool") / component / letter / package
    dest_dir = _safe_under(repo_root, relative_dir)
    _ensure_real_dir(dest_dir, create=True)
    dest = dest_dir / deb.name
    if dest.is_symlink():
        raise AptPublishError(f"refusing symlink destination: {dest}")
    shutil.copy2(deb, dest)
    if dest.is_symlink():
        raise AptPublishError(f"refusing symlink after copy: {dest}")
    return Path("pool") / component / letter / package / deb.name


def _deb_control_fields(deb_path):
    """Read control stanza fields via dpkg-deb, or a minimal ar/tar parse fallback."""
    dpkg_deb = shutil.which("dpkg-deb")
    if dpkg_deb:
        result = subprocess.run(
            [dpkg_deb, "-f", str(deb_path),
             "Package", "Version", "Architecture", "Maintainer", "Depends",
             "Section", "Priority", "Description"],
            check=False,
            capture_output=True,
            text=True,
        )
        if result.returncode == 0 and result.stdout.strip():
            fields = {}
            key = None
            for line in result.stdout.splitlines():
                if not line:
                    continue
                if line.startswith(" ") and key:
                    fields[key] = fields[key] + "\n" + line
                    continue
                if ":" not in line:
                    continue
                key, value = line.split(":", 1)
                key = key.strip()
                fields[key] = value.strip()
            if "Package" in fields and "Version" in fields:
                return fields
    # Minimal fallback: extract control.tar.* from the ar archive.
    with open(deb_path, "rb") as handle:
        if handle.read(8) != b"!<arch>\n":
            raise AptPublishError(f"not a deb archive: {deb_path}")
        control_data = None
        control_name = ""
        while True:
            header = handle.read(60)
            if not header:
                break
            if len(header) < 60:
                raise AptPublishError(f"truncated deb archive: {deb_path}")
            name = header[0:16].decode("ascii", "replace").strip()
            size = int(header[48:58].decode("ascii").strip())
            payload = handle.read(size)
            if size % 2 == 1:
                handle.read(1)
            if name.startswith("control.tar"):
                control_data = payload
                control_name = name
                break
        if control_data is None:
            raise AptPublishError(f"control.tar missing in {deb_path}")
    mode = "r:gz" if "gz" in control_name else "r:xz" if "xz" in control_name else "r:"
    with tarfile.open(fileobj=io.BytesIO(control_data), mode=mode) as archive:
        names = archive.getnames()
        member = archive.getmember("./control") if "./control" in names else archive.getmember("control")
        text = archive.extractfile(member).read().decode("utf-8")
    fields = {}
    key = None
    for line in text.splitlines():
        if not line:
            break
        if line.startswith(" ") and key:
            fields[key] = fields[key] + "\n" + line
            continue
        if ":" not in line:
            continue
        key, value = line.split(":", 1)
        fields[key.strip()] = value.strip()
    if "Package" not in fields or "Version" not in fields:
        raise AptPublishError(f"incomplete control in {deb_path}")
    return fields


def _packages_stanza(deb_path, filename_rel):
    fields = _deb_control_fields(deb_path)
    data = Path(deb_path).read_bytes()
    size = len(data)
    md5 = hashlib.md5(data).hexdigest()
    sha1 = hashlib.sha1(data).hexdigest()
    sha256 = hashlib.sha256(data).hexdigest()
    order = ["Package", "Version", "Architecture", "Maintainer", "Depends",
             "Section", "Priority", "Description"]
    lines = []
    for key in order:
        if key in fields:
            lines.append(f"{key}: {fields[key]}")
    for key, value in fields.items():
        if key not in order:
            lines.append(f"{key}: {value}")
    lines.append(f"Filename: {filename_rel.as_posix()}")
    lines.append(f"Size: {size}")
    lines.append(f"MD5sum: {md5}")
    lines.append(f"SHA1: {sha1}")
    lines.append(f"SHA256: {sha256}")
    return "\n".join(lines) + "\n\n"


def scan_packages(repo_root, codename, component, arch):
    binary_rel = Path("dists") / codename / component / f"binary-{arch}"
    binary_dir = _safe_under(repo_root, binary_rel)
    _ensure_real_dir(binary_dir, create=True)
    scan = find_scanpackages()
    if scan:
        result = subprocess.run(
            [scan, "-m", f"pool/{component}", "/dev/null"],
            cwd=repo_root,
            check=False,
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            raise AptPublishError(
                f"dpkg-scanpackages failed: {result.stderr.strip() or result.stdout}")
        packages_text = result.stdout
    else:
        print("apt-publish: dpkg-scanpackages missing; using built-in indexer",
              file=sys.stderr)
        pool = repo_root / "pool" / component
        stanzas = []
        if pool.is_dir():
            for deb in sorted(pool.rglob("*.deb")):
                if deb.is_symlink() or not deb.is_file():
                    continue
                validate_deb_name(deb.name)
                rel = deb.relative_to(repo_root)
                stanzas.append(_packages_stanza(deb, rel))
        packages_text = "".join(stanzas)
    packages_path = binary_dir / "Packages"
    _write_text(packages_path, packages_text)
    compressed = gzip.compress(packages_text.encode("utf-8"), mtime=0)
    _write_bytes(binary_dir / "Packages.gz", compressed)
    return packages_path, binary_dir / "Packages.gz", packages_text


def _file_digest(path):
    data = Path(path).read_bytes()
    return len(data), hashlib.md5(data).hexdigest(), hashlib.sha256(data).hexdigest()


def write_release(repo_root, codename, component, arch, packages_path, packages_gz_path):
    suite_dir = _safe_under(repo_root, Path("dists") / codename)
    _ensure_real_dir(suite_dir, create=True)
    entries = []
    for path in (packages_path, packages_gz_path):
        size, md5, sha256 = _file_digest(path)
        rel = path.relative_to(suite_dir).as_posix()
        entries.append((rel, size, md5, sha256))
    date = format_datetime(datetime.now(timezone.utc))
    lines = [
        "Origin: Nex",
        "Label: Nex",
        f"Suite: {codename}",
        f"Codename: {codename}",
        f"Architectures: {arch}",
        f"Components: {component}",
        f"Date: {date}",
        "MD5Sum:",
    ]
    for rel, size, md5, _sha in entries:
        lines.append(f" {md5} {size} {rel}")
    lines.append("SHA256:")
    for rel, size, _md5, sha256 in entries:
        lines.append(f" {sha256} {size} {rel}")
    lines.append("")
    release_path = suite_dir / "Release"
    _write_text(release_path, "\n".join(lines))
    return release_path


def gpg_has_secret(gpg_home):
    gpg_home = Path(gpg_home)
    if not gpg_home.is_dir() or gpg_home.is_symlink():
        return False
    result = subprocess.run(
        ["gpg", "--homedir", str(gpg_home), "--batch", "--list-secret-keys", "--with-colons"],
        check=False,
        capture_output=True,
        text=True,
    )
    return result.returncode == 0 and "sec:" in result.stdout


def export_pubkey(repo_root, gpg_home):
    result = subprocess.run(
        ["gpg", "--homedir", str(gpg_home), "--batch", "--export", "--armor"],
        check=False,
        capture_output=True,
    )
    if result.returncode != 0 or not result.stdout:
        raise AptPublishError("gpg export of pubkey.gpg failed")
    _write_bytes(_safe_under(repo_root, "pubkey.gpg"), result.stdout)


def sign_and_export(repo_root, release_path, gpg_home):
    suite_dir = release_path.parent
    for name in ("Release.gpg", "InRelease"):
        target = suite_dir / name
        if target.is_symlink():
            raise AptPublishError(f"refusing symlink: {target}")
    detach = subprocess.run(
        ["gpg", "--homedir", str(gpg_home), "--batch", "--yes", "--pinentry-mode", "loopback",
         "--passphrase", "", "--detach-sign", "--armor",
         "-o", str(suite_dir / "Release.gpg"), str(release_path)],
        check=False,
        capture_output=True,
        text=True,
    )
    if detach.returncode != 0:
        raise AptPublishError(f"gpg detach-sign failed: {detach.stderr.strip()}")
    clear = subprocess.run(
        ["gpg", "--homedir", str(gpg_home), "--batch", "--yes", "--pinentry-mode", "loopback",
         "--passphrase", "", "--clearsign",
         "-o", str(suite_dir / "InRelease"), str(release_path)],
        check=False,
        capture_output=True,
        text=True,
    )
    if clear.returncode != 0:
        raise AptPublishError(f"gpg clearsign failed: {clear.stderr.strip()}")
    export_pubkey(repo_root, gpg_home)


def parse_packages(packages_text):
    records = []
    current = {}
    for line in packages_text.splitlines():
        if not line.strip():
            if current:
                records.append(current)
                current = {}
            continue
        if line.startswith(" ") or ":" not in line:
            continue
        key, value = line.split(":", 1)
        current[key.strip()] = value.strip()
    if current:
        records.append(current)
    return records


def debian_newer(left, right):
    if left == right:
        return False
    result = subprocess.run(
        ["dpkg", "--compare-versions", left, "gt", right],
        check=False,
        capture_output=True,
    )
    if result.returncode == 0:
        return True
    if result.returncode == 1:
        return False
    # Fallback when dpkg is unavailable: lexical compare is enough for lab versions.
    return left > right


def newest_packages(packages_text):
    """Summarise the newest version of each published package."""
    newest = {}
    for record in parse_packages(packages_text):
        name = record.get("Package")
        version = record.get("Version")
        filename = record.get("Filename")
        sha256 = record.get("SHA256")
        size = record.get("Size")
        if not all((name, version, filename, sha256, size)):
            continue
        existing = newest.get(name)
        if existing is None or debian_newer(version, existing["version"]):
            newest[name] = {
                "version": version,
                "filename": filename,
                "sha256": sha256,
                "size": int(size),
            }
    return {"repo": "Nex", "packages": dict(sorted(newest.items()))}


def write_updates_json(repo_root, payload):
    """Publish the legacy index, for hosts that predate apt-based updates.

    Current hosts read the signed Packages/Release index through apt and ignore this
    file. Hosts released before that switch poll {repo}/updates.json instead, and a
    frozen copy would hide every later release from them — stranding them on their
    installed version with no in-product way forward. Keep publishing it until no
    supported host still reads it; see docs/plans/2026-09-09-package-updates.md.
    """
    _write_text(_safe_under(repo_root, "updates.json"),
                json.dumps(payload, indent=2, sort_keys=False) + "\n")
    return payload


def init_key(gpg_home):
    gpg_home = Path(gpg_home)
    if gpg_home.exists() or gpg_home.is_symlink():
        if gpg_home.is_symlink() or not gpg_home.is_dir():
            raise AptPublishError(f"gpg home must be a real directory: {gpg_home}")
    else:
        gpg_home.mkdir(mode=0o700, parents=True)
    os.chmod(gpg_home, 0o700)
    if gpg_has_secret(gpg_home):
        print(f"apt-publish: signing key already present in {gpg_home}", file=sys.stderr)
        return 0
    for algo in ("ed25519", "rsa4096"):
        result = subprocess.run(
            ["gpg", "--homedir", str(gpg_home), "--batch", "--pinentry-mode", "loopback",
             "--passphrase", "", "--quick-generate-key", KEY_UID, algo, "default", "never"],
            check=False,
            capture_output=True,
            text=True,
        )
        if result.returncode == 0 and gpg_has_secret(gpg_home):
            print(f"apt-publish: created {algo} signing key in {gpg_home}")
            return 0
        last_error = result.stderr.strip() or result.stdout.strip()
    raise AptPublishError(f"failed to create signing key: {last_error}")


def publish(repo_root, codename, component, arch, deb_paths, gpg_home):
    repo_root = _ensure_real_dir(repo_root, create=True)
    _ensure_real_dir(_safe_under(repo_root, Path("pool") / component), create=True)
    collect = collect_debs(deb_paths) if deb_paths else []
    for deb in collect:
        copy_deb(deb, repo_root, component)
    packages_path, packages_gz_path, packages_text = scan_packages(
        repo_root, codename, component, arch)
    release_path = write_release(
        repo_root, codename, component, arch, packages_path, packages_gz_path)
    published = write_updates_json(repo_root, newest_packages(packages_text))
    if gpg_has_secret(gpg_home):
        sign_and_export(repo_root, release_path, gpg_home)
    else:
        print("apt-publish: warning: no signing key in "
              f"{gpg_home}; wrote unsigned Release (lab use only)", file=sys.stderr)
    return published


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo-root", default="/srv/nex-apt")
    parser.add_argument("--codename", default="nex")
    parser.add_argument("--component", default="main")
    parser.add_argument("--arch", default="amd64")
    parser.add_argument("--gpg-home", default="/etc/nex-apt/gnupg")
    parser.add_argument("--init-key", action="store_true",
                        help="create a non-interactive archive signing key and exit")
    parser.add_argument("debs", nargs="*",
                        help=".deb paths or a directory of .deb files")
    args = parser.parse_args(argv)
    try:
        if args.init_key:
            if args.debs:
                parser.error("--init-key does not take package paths")
            return init_key(args.gpg_home)
        publish(args.repo_root, args.codename, args.component, args.arch, args.debs, args.gpg_home)
    except AptPublishError as error:
        print(f"apt-publish: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
