#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Manual quiesced Controller state export/restore. Archives contain private keys."""
import argparse
from contextlib import closing
import fcntl
import hashlib
import io
import json
import os
import pwd
import sqlite3
from pathlib import Path, PurePosixPath
import subprocess
import tarfile

PATHS = ('var/lib/nex-controller', 'etc/nex-controller')
REQUIRED = {'var/lib/nex-controller/' + name for name in ('controller.db', 'host-ca.pem', 'host-ca-key.pem', 'appliance-initialized')} | {'etc/nex-controller/tls.crt', 'etc/nex-controller/tls.key'}
SOURCE = Path('/usr/local/share/nex/nex-source.tar.gz')


def checked_members(archive):
    members = archive.getmembers()
    names = set()
    for member in members:
        if member.name in names:
            raise ValueError('duplicate recovery archive path')
        names.add(member.name)
        path = PurePosixPath(member.name)
        if path.is_absolute() or '..' in path.parts or not (member.isfile() or member.isdir()):
            raise ValueError('unsafe recovery archive member')
        if member.name == 'var/lib/nex-controller/controller.lock':
            raise ValueError('recovery must not replace the live ownership lock')
        if member.name != 'recovery.json' and not any(member.name == root or member.name.startswith(root + '/') for root in PATHS):
            raise ValueError('unexpected recovery archive path')
    return members


def verify_archive(archive, version):
    members = checked_members(archive)
    metadata = json.load(archive.extractfile('recovery.json'))
    files = {m.name for m in members if m.isfile() and m.name != 'recovery.json'}
    if metadata['source_sha256'] != version or not REQUIRED <= files or set(metadata['files']) != files:
        raise ValueError('incomplete recovery archive or mismatched Nex source version')
    for name in files:
        with archive.extractfile(name) as stream:
            if hashlib.file_digest(stream, 'sha256').hexdigest() != metadata['files'][name]:
                raise ValueError('recovery archive checksum mismatch')
    return members


def require_empty_controller(state, config):
    if any((config / name).exists() for name in ('bootstrap.json', 'admin-password')):
        raise ValueError('replacement has pending bootstrap credentials')
    with closing(sqlite3.connect((state / 'controller.db').as_uri() + '?mode=ro', uri=True)) as database:
        if database.execute('PRAGMA quick_check').fetchone()[0] != 'ok':
            raise ValueError('replacement database is not healthy')
        tables = [row[0] for row in database.execute("SELECT name FROM sqlite_master WHERE type='table'")]
        if not {'admins', 'sites', 'pools', 'hosts', 'operations', 'appliance_bootstraps', 'appliances'} <= set(tables):
            raise ValueError('replacement schema is incomplete')
        for table in tables:
            if table in ('schema_version', 'admins', 'sessions', 'sqlite_sequence'):
                continue
            quoted = '"' + table.replace('"', '""') + '"'
            if database.execute('SELECT count(*) FROM ' + quoted).fetchone()[0]:
                raise ValueError('replacement contains management state; overwrite refused')


def restore_ownership(directories, uid, gid):
    for directory in directories:
        for path in [directory, *directory.rglob('*')]:
            path.chmod(0o700 if path.is_dir() else 0o600)
            os.chown(path, uid, gid)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('export', 'restore'))
    parser.add_argument('archive', type=Path)
    parser.add_argument('--original-stopped-and-excluded', action='store_true', help='explicitly attest original controller cannot run or regain host access')
    parser.add_argument('--replace-empty-controller', action='store_true', help='explicitly overwrite only a verified empty initialized replacement')
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error('run as root on the appliance')
    os.umask(0o077)
    for service in ('nex-controller', 'nex-appliance-init'):
        status = subprocess.check_output(['systemctl', 'show', '-p', 'ActiveState', '--value', service], text=True).strip()
        if status not in ('inactive', 'failed'):
            parser.error('stop Controller and appliance initialization first; both remain stopped')
    state = Path('/var/lib/nex-controller')
    state.mkdir(mode=0o700, exist_ok=True)
    lock = (state / 'controller.lock').open('a')
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        parser.error('another Controller owns this state; stop it before recovery')
    version = hashlib.sha256(SOURCE.read_bytes()).hexdigest()
    if args.action == 'export':
        if not Path('/var/lib/nex-controller/appliance-initialized').exists():
            parser.error('only an initialized Controller can be exported')
        # Exclusive creation prevents overwriting an earlier recovery copy.
        with args.archive.open('xb') as output, tarfile.open(fileobj=output, mode='w:gz') as archive:
            files = {}
            for root in PATHS:
                for path in (Path('/') / root).rglob('*'):
                    if path.is_symlink():
                        raise ValueError('symlinks refused in recovery state')
                    if path.is_file() and path.name != 'controller.lock':
                        with path.open('rb') as stream:
                            files[str(path.relative_to('/'))] = hashlib.file_digest(stream, 'sha256').hexdigest()
            if not REQUIRED <= files.keys():
                raise ValueError('Controller recovery state is incomplete')
            metadata = json.dumps({'source_sha256': version, 'files': files}).encode()
            info = tarfile.TarInfo('recovery.json')
            info.size, info.mode = len(metadata), 0o600
            archive.addfile(info, io.BytesIO(metadata))
            for path in PATHS:
                archive.add('/' + path, arcname=path, filter=lambda member: None if member.name == 'var/lib/nex-controller/controller.lock' else member)
    else:
        if not args.original_stopped_and_excluded:
            parser.error('confirm original Controller is stopped and excluded before restoring identity')
        with tarfile.open(args.archive) as archive:
            members = verify_archive(archive, version)
            if any((Path('/etc/nex-controller') / name).exists() for name in ('bootstrap.json', 'admin-password')):
                parser.error('replacement has pending bootstrap credentials')
            if (state / 'controller.db').exists():
                if not args.replace_empty_controller:
                    parser.error('initialized replacement requires --replace-empty-controller')
                require_empty_controller(state, Path('/etc/nex-controller'))
                # No process can own the database while this command holds its lock.
                for name in ('controller.db', 'controller.db-wal', 'controller.db-shm'):
                    (state / name).unlink(missing_ok=True)
            archive.extractall('/', members=[m for m in members if m.name != 'recovery.json'], filter='data')
        user = pwd.getpwnam('nex-controller')
        restore_ownership([Path('/') / root for root in PATHS], user.pw_uid, user.pw_gid)
        Path('/etc/cloud/cloud-init.disabled').touch(mode=0o600)
    print('Completed; archive contains secrets. Controller remains stopped.')


if __name__ == '__main__':
    main()
