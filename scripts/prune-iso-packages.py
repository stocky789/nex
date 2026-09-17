#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Remove named Debian packages and refresh an extracted ISO's indexes."""
import argparse
import gzip
import hashlib
from pathlib import Path
import subprocess


def package_name(path):
    return subprocess.check_output(['dpkg-deb', '-f', str(path), 'Package'], text=True).strip()


def filter_packages(data, blocked):
    stanzas = data.decode().strip().split('\n\n') if data.strip() else []
    kept = [stanza for stanza in stanzas
            if not any(line == f'Package: {name}' for name in blocked for line in stanza.splitlines())]
    return ('\n\n'.join(kept) + ('\n' if kept else '')).encode()


def refresh_release(path):
    algorithms = {'MD5Sum': 'md5', 'SHA1': 'sha1', 'SHA256': 'sha256', 'SHA512': 'sha512'}
    algorithm = None
    output = []
    for line in path.read_text().splitlines():
        if line.rstrip(':') in algorithms and line.endswith(':'):
            algorithm = algorithms[line.rstrip(':')]
        elif line and not line[:1].isspace():
            algorithm = None
        if algorithm and line[:1].isspace():
            _, _, relative = line.split()
            artifact = path.parent / relative
            if artifact.is_file():
                digest = hashlib.new(algorithm, artifact.read_bytes()).hexdigest()
                line = f' {digest} {artifact.stat().st_size:8d} {relative}'
        output.append(line)
    path.write_text('\n'.join(output) + '\n')


def prune(tree, blocked):
    tree = Path(tree)
    for path in tree.rglob('*'):
        if path.is_file() and path.suffix in ('.deb', '.udeb') and package_name(path) in blocked:
            path.unlink()
    for path in tree.rglob('Packages*'):
        if not path.is_file():
            continue
        compressed = path.suffix == '.gz'
        data = gzip.decompress(path.read_bytes()) if compressed else path.read_bytes()
        filtered = filter_packages(data, blocked)
        path.write_bytes(gzip.compress(filtered, mtime=0) if compressed else filtered)
    firmware = tree / 'firmware'
    if firmware.is_dir():
        for path in firmware.rglob('*'):
            if path.is_file() and any(name in path.name for name in blocked):
                path.unlink()
        contents = firmware / 'Contents-firmware'
        if contents.is_file():
            contents.write_text(''.join(line for line in contents.read_text().splitlines(True)
                                        if not any(name in line for name in blocked)))
    for release in (tree / 'dists').rglob('Release'):
        refresh_release(release)
    remaining = [path for path in tree.rglob('*')
                 if path.is_file() and path.suffix in ('.deb', '.udeb') and package_name(path) in blocked]
    if remaining:
        raise RuntimeError(f'blocked packages remain: {remaining}')


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--tree', required=True, type=Path)
    parser.add_argument('package', nargs='+')
    args = parser.parse_args(argv)
    prune(args.tree, set(args.package))


if __name__ == '__main__':
    main()
