#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Publish Nex Core installer artifacts together and prune old releases.

Runs on the website host. Packages go to the APT archive through nex-apt-publish;
this handles fresh-install ISOs and their hash-pinned netinst payloads. Retention is bounded
because each release is over a gigabyte and the archive already holds ~740 MB of
packages per version.

Layout, all under one root owned by the publishing user (no root privilege needed):

    <root>/0.1.6~alpha1/nex-core-0.1.6~alpha1-amd64.iso
    <root>/0.1.6~alpha1/nex-core-0.1.6~alpha1-amd64.iso.sha256
    <root>/0.1.6~alpha1/nex-core-0.1.6~alpha1-source.tar.gz
    <root>/0.1.6~alpha1/nex-core-0.1.6~alpha1-compliance-manifest.json
    <root>/latest -> 0.1.6~alpha1
    <root>/latest.json   (version, build date, and file paths/sizes/hashes of `latest`, for the website)
"""
import argparse
from datetime import datetime, timezone
from functools import cmp_to_key
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

VERSION_DIR = re.compile(r"^[0-9][A-Za-z0-9.+~-]*$")
ISO_NAME = re.compile(r"^nex-core-(?P<version>[0-9][A-Za-z0-9.+~-]*)-amd64\.iso$")
SOURCE_NAME = re.compile(r"^nex-core-(?P<version>[0-9][A-Za-z0-9.+~-]*)-source\.tar\.gz$")
NETINST_ISO_NAME = re.compile(r"^nex-core-(?P<version>[0-9][A-Za-z0-9.+~-]*)-amd64-netinst\.iso$")
NETINST_MANIFEST_NAME = re.compile(r"^nex-core-(?P<version>[0-9][A-Za-z0-9.+~-]*)-netinst-compliance-manifest\.json$")
PAYLOAD_NAME = re.compile(r"^(?:nex-core-local|dlm-controld|libvirt[a-z0-9.+-]*|libnss-libvirt)-[0-9a-f]{64}\.deb$")
MANIFEST_NAME = re.compile(r"^nex-core-(?P<version>[0-9][A-Za-z0-9.+~-]*)-compliance-manifest\.json$")
# Release names are readable (0.4.23-dev.251); dpkg orders the Debian form (0.4.23~~dev.251).
PRERELEASE = re.compile(r"^(?P<release>\d+\.\d+\.\d+)-(?P<pre>[0-9A-Za-z.]+)$")


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


def release_asset(path, pattern, label, version):
    path = Path(path)
    if path.is_symlink() or not path.is_file():
        raise ReleasePublishError(f"{label} must be a regular file: {path}")
    match = pattern.match(path.name)
    if not match or match.group("version") != version:
        raise ReleasePublishError(
            f"{label} must be named for ISO version {version}: {path.name}")
    return path


def compliance_manifest(path, version, pattern=MANIFEST_NAME):
    path = release_asset(path, pattern, "compliance manifest", version)
    try:
        manifest = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError) as error:
        raise ReleasePublishError(f"invalid compliance manifest: {path}") from error
    if not isinstance(manifest, dict) or manifest.get("version") != version:
        raise ReleasePublishError(
            f"compliance manifest version must be {version}: {path}")
    return path, manifest


def sha256(path, chunk=1 << 20):
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for block in iter(lambda: handle.read(chunk), b""):
            digest.update(block)
    return digest.hexdigest()


def debian_version(version):
    match = PRERELEASE.match(version)
    if not match:
        return version
    prefix = "~~" if match["pre"].startswith("dev.") else "~"
    return f"{match['release']}{prefix}{match['pre']}"


def compare_versions(left, right):
    if left == right:
        return 0
    for relation, result in (("lt", -1), ("gt", 1)):
        try:
            compared = subprocess.run(
                ["dpkg", "--compare-versions", debian_version(left), relation, debian_version(right)],
                capture_output=True, text=True)
        except OSError as error:
            raise ReleasePublishError("dpkg is required to order releases") from error
        if compared.returncode == 0:
            return result
        if compared.returncode != 1:
            raise ReleasePublishError(f"cannot compare release versions: {compared.stderr.strip()}")
    return 0


def existing_releases(root):
    found = []
    for child in sorted(Path(root).iterdir()):
        if child.is_symlink() or not child.is_dir():
            continue
        if VERSION_DIR.match(child.name):
            found.append(child.name)
    return sorted(found, key=cmp_to_key(compare_versions))


def prune(root, keep):
    if keep < 1:
        raise ReleasePublishError("keep must be at least 1")
    releases = existing_releases(root)
    removed = []
    for version in releases[:-keep] if len(releases) > keep else []:
        shutil.rmtree(Path(root) / version)
        removed.append(version)
    return removed


def write_latest_manifest(root):
    """Describe the newest release so the website can show it without a redeploy."""
    root = Path(root)
    newest = existing_releases(root)[-1]
    directory = root / newest
    names = {"iso": f"nex-core-{newest}-amd64.iso",
             "netinst": f"nex-core-{newest}-amd64-netinst.iso",
             "source": f"nex-core-{newest}-source.tar.gz"}
    files = {}
    for key, name in names.items():
        path = directory / name
        if path.is_file():
            digest = (directory / (name + ".sha256")).read_text().split()[0]
            files[key] = {"path": f"{newest}/{name}", "size": path.stat().st_size, "sha256": digest}
    manifest = {"version": newest.replace("~~", "-").replace("~", "-"), "files": files}
    if "iso" in files:
        built = (directory / names["iso"]).stat().st_mtime
        manifest["published"] = datetime.fromtimestamp(built, timezone.utc).date().isoformat()
    temporary = root / ".latest.json.tmp"
    temporary.write_text(json.dumps(manifest, indent=2) + "\n")
    os.chmod(temporary, 0o644)
    os.replace(temporary, root / "latest.json")


def write_download_page(directory, version):
    images = (f"nex-core-{version}-amd64.iso", f"nex-core-{version}-amd64-netinst.iso")
    documents = (f"nex-core-{version}-source.tar.gz",
                 f"nex-core-{version}-compliance-manifest.json",
                 f"nex-core-{version}-netinst-compliance-manifest.json")
    lines = ["<!doctype html>", '<html lang="en">', '<meta charset="utf-8">',
             '<meta name="viewport" content="width=device-width, initial-scale=1">',
             f"<title>Nex Core {version} downloads</title>",
             f"<h1>Nex Core {version}</h1>",
             "<p>Development channel build of the next release, for testing only. It is replaced "
             "by newer builds without notice.</p>" if "~dev." in debian_version(version)
             else "<p>Development build for disposable evaluation.</p>"]
    for heading, names in (("Installer images", images), ("Source and compliance", documents)):
        lines.extend((f"<h2>{heading}</h2>", "<ul>"))
        for name in names:
            if (directory / name).is_file():
                checksum = (f' (<a href="{name}.sha256">SHA-256</a>)'
                            if (directory / (name + ".sha256")).is_file() else "")
                lines.append(f'<li><a href="{name}">{name}</a>{checksum}</li>')
        lines.append("</ul>")
    lines.append("</html>")
    temporary = directory / ".index.html.tmp"
    temporary.write_text("\n".join(lines) + "\n")
    os.chmod(temporary, 0o644)
    os.replace(temporary, directory / "index.html")


def write_release_index(root):
    root = Path(root)
    lines = ["<!doctype html>", '<html lang="en">', '<meta charset="utf-8">',
             '<meta name="viewport" content="width=device-width, initial-scale=1">',
             "<title>Nex Core downloads</title>", "<h1>Nex Core downloads</h1>",
             '<nav><a href="/downloads/">Stable</a> · '
             '<a href="/downloads/dev/">Development</a></nav>',
             '<h2>Releases</h2>', '<ul>',
             '<li><a href="latest/">Latest release</a></li>']
    for version in reversed(existing_releases(root)):
        lines.append(f'<li><a href="{version}/">{version}</a></li>')
    lines.extend(("</ul>", "</html>"))
    temporary = root / ".index.html.tmp"
    temporary.write_text("\n".join(lines) + "\n")
    os.chmod(temporary, 0o644)
    os.replace(temporary, root / "index.html")


def publish(root, iso, source, manifest, keep, netinst_iso=None, netinst_manifest=None, payloads=(), check=False):
    root = _real_dir(root, create=True)
    iso = Path(iso)
    if iso.is_symlink() or not iso.is_file():
        raise ReleasePublishError(f"ISO must be a regular file: {iso}")
    version = iso_version(iso)
    source = release_asset(source, SOURCE_NAME, "source bundle", version)
    manifest, manifest_data = compliance_manifest(manifest, version)
    assets = [iso, source, manifest]
    bindings = [(manifest_data, "iso", iso), (manifest_data, "source_bundle", source)]
    if netinst_iso or netinst_manifest or payloads:
        if not netinst_iso or not netinst_manifest or not payloads:
            raise ReleasePublishError("netinst requires ISO, compliance manifest and payloads together")
        netinst_iso = release_asset(netinst_iso, NETINST_ISO_NAME, "netinst ISO", version)
        netinst_manifest, netinst_data = compliance_manifest(
            netinst_manifest, version, NETINST_MANIFEST_NAME)
        assets.extend((netinst_iso, netinst_manifest))
        bindings.extend(((netinst_data, "iso", netinst_iso),
                         (netinst_data, "source_bundle", source)))
        expected = netinst_data.get("netinst_payloads")
        if not isinstance(expected, list) or not expected:
            raise ReleasePublishError("netinst manifest requires netinst_payloads")
        supplied = {}
        for payload in map(Path, payloads):
            if payload.is_symlink() or not payload.is_file() or not PAYLOAD_NAME.fullmatch(payload.name):
                raise ReleasePublishError(f"invalid netinst payload: {payload}")
            if payload.name in supplied:
                raise ReleasePublishError(f"duplicate netinst payload: {payload.name}")
            supplied[payload.name] = payload
        if any(not isinstance(item, dict) for item in expected) or {item.get("file") for item in expected} != set(supplied):
            raise ReleasePublishError("netinst payload set does not match manifest")
        for item in expected:
            payload = supplied[item["file"]]
            digest = sha256(payload)
            if item.get("sha256") != digest or not payload.name.endswith(f"-{digest}.deb"):
                raise ReleasePublishError(f"netinst payload binding does not match {payload.name}")
        assets.extend(supplied.values())
    digests = {asset.name: sha256(asset) for asset in assets}
    for data, key, asset in bindings:
        binding = data.get(key)
        if not isinstance(binding, dict) or binding.get("file") != asset.name or binding.get("sha256") != digests[asset.name]:
            raise ReleasePublishError(
                f"compliance manifest {key} binding does not match {asset.name}")

    if check:
        return version, digests[iso.name], version, []

    # Stage beside the target so the move is atomic on the same filesystem, and a
    # failed upload never leaves a half-written ISO that looks installable.
    target = root / version
    staging = Path(tempfile.mkdtemp(prefix=f".{version}.", dir=root))
    try:
        for asset in assets:
            installed = staging / asset.name
            shutil.copy2(asset, installed)
            if sha256(installed) != digests[asset.name]:
                raise ReleasePublishError(f"{asset.name} changed while being copied")
            (staging / (asset.name + ".sha256")).write_text(
                f"{digests[asset.name]}  {asset.name}\n")
        for entry in staging.iterdir():
            os.chmod(entry, 0o644)
        write_download_page(staging, version)
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
    removed = prune(root, keep)
    write_latest_manifest(root)
    for old_version in existing_releases(root):
        if old_version != version:
            write_download_page(root / old_version, old_version)
    write_release_index(root)
    return version, digests[iso.name], newest, removed


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default="/srv/nex-downloads")
    parser.add_argument("--iso", required=True, help="built ISO path")
    parser.add_argument("--source", required=True,
                        help="matching whole-release source bundle")
    parser.add_argument("--compliance-manifest", required=True,
                        help="matching release compliance manifest")
    parser.add_argument("--netinst-iso", help="matching network installer ISO")
    parser.add_argument("--netinst-manifest", help="network installer compliance manifest")
    parser.add_argument("--netinst-payload", action="append", default=[],
                        help="hash-pinned Nex download; repeat for each payload")
    parser.add_argument("--check", action="store_true", help="verify bindings without publishing")
    parser.add_argument("--keep", type=int, default=3,
                        help="number of releases to retain (default 3)")
    args = parser.parse_args(argv)
    try:
        version, digest, newest, removed = publish(
            args.root, args.iso, args.source, args.compliance_manifest, args.keep,
            args.netinst_iso, args.netinst_manifest, args.netinst_payload, args.check)
    except (ReleasePublishError, OSError) as error:
        print(f"release-publish: {error}", file=sys.stderr)
        return 1
    if args.check:
        print(f"verified {version} sha256={digest}")
        return 0
    print(f"published {version} sha256={digest}")
    print(f"latest -> {newest}")
    if removed:
        print("pruned " + ", ".join(removed))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
