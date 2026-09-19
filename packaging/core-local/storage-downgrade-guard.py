#!/usr/bin/python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Refuse an APT downgrade that would strand retained storage state."""
import argparse
import json
import os
from pathlib import Path
import posixpath
import sqlite3
import subprocess
import sys

# Where the packages install their storage-format markers. Matching the exact
# path matters: any other member merely containing the marker name (a doc, a
# receipt, an attacker-chosen filename) must not pass for the marker itself.
MARKER_DIRS = ('usr/share/nex/core-local', 'usr/local/share/nex')


def archive_paths(text):
    """The member paths in `dpkg-deb -c` output, normalized without a leading /."""
    for line in text.splitlines():
        fields = line.split()
        # perms owner/group size date time path [-> link target]
        if len(fields) < 6:
            continue
        yield posixpath.normpath(fields[5]).lstrip('/')


def legacy_archive(path, marker):
    try:
        listing = subprocess.run(
            ['dpkg-deb', '-c', path], text=True, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, check=True).stdout
    except (OSError, subprocess.CalledProcessError):
        return True
    wanted = {posixpath.join(directory, marker) for directory in MARKER_DIRS}
    return not any(entry in wanted for entry in archive_paths(listing))


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


def core_storage_format(root):
    # Any durable ZFS record means this host's storage is v3. A provisioned zpool
    # outlives its receipts, so the directory counts when it holds anything at
    # all, and an unreadable record is treated as present rather than absent:
    # downgrading under a live Nex-created pool is the unrecoverable outcome.
    agent = root / 'var/lib/nex-core-agent'
    for name in ('zfs-pool.json', 'zfs-lifecycle.json'):
        path = agent / name
        try:
            path.lstat()
            return 3
        except FileNotFoundError:
            pass
        except OSError:
            return 3
    receipts = agent / 'zfs-provision'
    try:
        if receipts.is_symlink() or any(receipts.iterdir()):
            return 3
    except FileNotFoundError:
        pass
    except OSError:
        return 3
    return 2 if has_core_v2_state(root) else 0


def controller_storage_format(root):
    database = root / 'var/lib/nex-controller/controller.db'
    if not database.exists():
        return 0
    try:
        connection = sqlite3.connect('file:' + str(database) + '?mode=ro', uri=True)
        try:
            version = 0
            for row in connection.execute(
                    'SELECT c.capabilities FROM host_capabilities c JOIN hosts h ON h.id=c.host_id WHERE h.revoked=0'):
                if 'storage-zfs-provision-v1' in row[0]:
                    return 3
                if 'storage-repositories-v1' in row[0]:
                    version = 2
            return version
        finally:
            connection.close()
    except sqlite3.OperationalError as error:
        return 3 if 'no such table' not in str(error).lower() else 0
    except sqlite3.DatabaseError:
        return 3


# APT names one line per dpkg action. The unpack line carries the .deb it is about to
# install; the matching configure of that same package, and a removal, carry these
# pseudo-filenames instead. They install no archive, so they are not a downgrade, and
# refusing them blocked every upgrade on a host with storage state.
APT_PSEUDO_FILES = ('**CONFIGURE**', '**REMOVE**')


def target_archives(package):
    for line in sys.stdin:
        fields = line.split()
        if not fields or fields[0].split(':', 1)[0] != package:
            continue
        if fields[-1] in APT_PSEUDO_FILES:
            continue
        for field in reversed(fields[1:]):
            if field.endswith('.deb'):
                yield field
                break
        else:
            # A line naming neither an archive nor a known pseudo-action is not
            # understood; refuse rather than guess what is being installed.
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
    storage_format = core_storage_format(root) if args.core else controller_storage_format(root)
    if not storage_format:
        return
    marker = f'storage-format-v{storage_format}'
    if args.check:
        print(f'{package}: refusing removal while {marker} storage state is retained', file=sys.stderr)
        raise SystemExit(1)
    for archive in target_archives(package):
        if not archive or legacy_archive(archive, marker):
            print(f'{package}: refusing APT downgrade while {marker} storage state is retained; upgrade to a {marker} package instead', file=sys.stderr)
            raise SystemExit(1)


if __name__ == '__main__':
    main()
