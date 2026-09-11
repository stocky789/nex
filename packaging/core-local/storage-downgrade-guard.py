#!/usr/bin/python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Refuse an APT downgrade that would strand version-2 storage state."""
import argparse
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys


def legacy_archive(path):
    try:
        return 'storage-format-v2' not in subprocess.run(
            ['dpkg-deb', '-c', path], text=True, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, check=True).stdout
    except (OSError, subprocess.CalledProcessError):
        return True


def has_core_v2_state(root):
    directory = root / 'var/lib/nex-core-agent/managed-vms'
    if directory.exists():
        try:
            entries = list(directory.iterdir())
        except OSError:
            return True
        for path in entries:
            if path.suffix != '.json' or path.is_symlink():
                continue
            try:
                data = json.loads(path.read_text())
            except (OSError, ValueError):
                return True
            text = json.dumps(data, separators=(',', ':'))
            if any(key in text for key in ('"applied_storage":', '"storage_lifecycle":',
                                           '"credential_receipt":', '"credential_request_id":',
                                           '"tombstone":')):
                return True
            storage = ((data.get('command') or {}).get('storage') or {})
            if storage.get('kind') or storage.get('generation') or storage.get('source') or storage.get('identity'):
                return True
    credentials = root / 'var/lib/nex-core-agent/storage-credentials'
    if not credentials.exists():
        return False
    try:
        return any(entry.name != 'receipt-key' for entry in credentials.iterdir())
    except OSError:
        return True


def controller_has_v2_core(root):
    database = root / 'var/lib/nex-controller/controller.db'
    if not database.exists():
        return False
    try:
        connection = sqlite3.connect('file:' + str(database) + '?mode=ro', uri=True)
        try:
            return any('storage-repositories-v1' in row[0] for row in connection.execute(
                'SELECT c.capabilities FROM host_capabilities c JOIN hosts h ON h.id=c.host_id WHERE h.revoked=0'))
        finally:
            connection.close()
    except sqlite3.OperationalError as error:
        return 'no such table' not in str(error).lower()
    except sqlite3.DatabaseError:
        return True


def target_archives(package):
    for line in sys.stdin:
        fields = line.split()
        if not fields or fields[0].split(':', 1)[0] != package:
            continue
        for field in reversed(fields[1:]):
            if field.endswith('.deb'):
                yield field
                break
        else:
            yield ''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--core', action='store_true')
    parser.add_argument('--controller', action='store_true')
    parser.add_argument('--check', action='store_true', help='fail when protected state exists')
    args = parser.parse_args()
    if args.core == args.controller:
        parser.error('select exactly one package')
    root = Path(os.environ.get('DPKG_ROOT', '/'))
    package = 'nex-core-local' if args.core else 'nex-controller'
    protected = has_core_v2_state(root) if args.core else controller_has_v2_core(root)
    if not protected:
        return
    if args.check:
        print(f'{package}: refusing removal while version-2 storage state is retained', file=sys.stderr)
        raise SystemExit(1)
    for archive in target_archives(package):
        if not archive or legacy_archive(archive):
            print(f'{package}: refusing APT downgrade while version-2 storage state is retained; upgrade to a storage-format-v2 package instead', file=sys.stderr)
            raise SystemExit(1)


if __name__ == '__main__':
    main()
