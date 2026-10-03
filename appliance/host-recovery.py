#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Root-only manual Controller recovery through the local libvirt guest agent."""
import argparse
import base64
import fcntl
import json
import os
import re
from pathlib import Path
import shlex
import subprocess
import time
import uuid
import xml.etree.ElementTree as ET


def virsh(uri, *args):
    # Feed commands on stdin: recovery archive contents must never appear in argv.
    result = subprocess.run(['virsh', '--quiet', '--connect', uri], input=shlex.join(args) + '\n',
                            text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60, env=dict(os.environ, LC_ALL='C'))
    if result.returncode or result.stderr.strip():
        raise RuntimeError('libvirt command failed; inspect the host locally (output withheld)')
    lines = result.stdout.splitlines()
    if args[0] == 'qemu-agent-command':
        # Readline may truncate/wrap long echoed commands; QGA replies are one JSON line.
        replies = []
        for line in lines:
            try:
                reply = json.loads(line)
            except ValueError:
                continue
            if isinstance(reply, dict) and set(reply) in ({'return'}, {'error'}):
                replies.append(line)
        if len(replies) != 1:
            raise RuntimeError('ambiguous guest-agent response; output withheld')
        return replies[0]
    expected = 'virsh # ' + shlex.join(args)
    if not lines or lines[0].strip() != expected or lines[-1].strip() != 'virsh #':
        raise RuntimeError('unexpected libvirt response framing; output withheld')
    return '\n'.join(lines[1:-1]).strip()


class Guest:
    def __init__(self, uri, target):
        self.uri, self.target = uri, target

    def call(self, command, **arguments):
        reply = json.loads(virsh(self.uri, 'qemu-agent-command', self.target,
                                json.dumps({'execute': command, 'arguments': arguments})))
        if 'error' in reply:
            raise RuntimeError('guest agent rejected recovery command (details withheld)')
        return reply['return']

    def execute(self, path, *args):
        pid = self.call('guest-exec', path=path, arg=list(args), **{'capture-output': False})['pid']
        deadline = time.monotonic() + 300
        while time.monotonic() < deadline:
            status = self.call('guest-exec-status', pid=pid)
            if status.get('exited'):
                if status.get('exitcode') != 0:
                    raise RuntimeError('guest recovery command failed; services remain stopped')
                return
            time.sleep(.2)
        raise RuntimeError('guest command outcome unknown; inspect before retrying')

    def download(self, remote, output):
        handle = self.call('guest-file-open', path=remote, mode='rb')
        try:
            while True:
                data = self.call('guest-file-read', handle=handle, count=49152)
                output.write(base64.b64decode(data.get('buf-b64', ''), validate=True))
                if data.get('eof'):
                    break
                if not data.get('count'):
                    raise RuntimeError('guest archive read made no progress')
        finally:
            self.call('guest-file-close', handle=handle)

    def upload(self, remote, source):
        handle = self.call('guest-file-open', path=remote, mode='wb')
        try:
            while data := source.read(49152):
                result = self.call('guest-file-write', handle=handle, **{'buf-b64': base64.b64encode(data).decode()})
                if result.get('count') != len(data):
                    raise RuntimeError('incomplete guest archive write')
            self.call('guest-file-flush', handle=handle)
        finally:
            self.call('guest-file-close', handle=handle)


def owned(uri, target, state, storage):
    if str(uuid.UUID(target)) != target:
        raise ValueError('select a canonical VM UUID')
    journals = [(p.stem, json.loads(p.read_text())) for p in state.glob('*.json')]
    matches = [j for _, j in journals if j.get('vm_id') == target and j.get('kind') == 'deploy']
    # Single-disk appliance: controller.qcow2 is the only disk. Two-disk profile: a read-only system
    # image (swapped by Nex Core) plus the persistent controller-data.qcow2 that holds the state exported here.
    system = matches[0].get('system') if len(matches) == 1 else None
    disk = str(storage / target / ('controller-data.qcow2' if system else 'controller.qcow2'))
    if len(matches) != 1 or matches[0].get('disk') != disk or (system and str(Path(system).parent) != str(storage / target)):
        raise ValueError('VM lacks unique Nex deployment ownership')
    systems = {system} if system else set()
    # A failed restore can leave the verified new image defined while deployment still records the old
    # one. Only a durable swap of this exact deployment may extend the owned read-only image set.
    stages = {
        'running': {'swap_define', 'swap_start', 'swap_probe', 'swap_commit', 'swap_restore',
                    'swap_redefine', 'swap_restart', 'swap_reprobe'},
        'needs_attention': {'swap_restore', 'swap_redefine', 'swap_restart', 'swap_reprobe'},
    }
    for operation_id, journal in journals:
        swap = journal.get('swap')
        if (not system or journal.get('kind') != 'swap' or not isinstance(swap, dict)
                or journal.get('id') != operation_id or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_-]{0,63}', operation_id)
                or journal.get('stage') not in stages.get(journal.get('status'), set())
                or any(journal.get(key) != matches[0].get(key) for key in ('vm_id', 'name', 'disk', 'bridge', 'system'))):
            continue
        sequence, sha = swap.get('sequence'), swap.get('sha256')
        if (type(sequence) is not int or sequence < 1 or not isinstance(sha, str)
                or not re.fullmatch(r'[0-9a-f]{64}', sha)):
            continue
        new = str(storage / target / f'system-{sha}.img')
        if (journal.get('digest') == f'swap:{sequence}:{sha}' and swap.get('old') == system
                and swap.get('new') == new and new != system and swap.get('checkpoint') == disk + f'.pre-{sequence}'
                and type(swap.get('started', False)) is bool):
            systems.add(new)
    for flags in ((), ('--inactive',)):
        xml = ET.fromstring(virsh(uri, 'dumpxml', target, *flags))
        disks = xml.findall('./devices/disk[@device="disk"]')
        data = [d for d in disks if d.find('source').get('file') == disk]
        others = [d for d in disks if d not in data]
        if system:
            system_ok = (len(others) == 1 and others[0].find('source').get('file') in systems
                         and others[0].find('readonly') is not None)
        else:
            system_ok = not others
        if (xml.findtext('uuid') != target or xml.findtext('name') != matches[0]['name'] or len(data) != 1
                or data[0].find('readonly') is not None or not system_ok):
            raise ValueError('VM definition differs from owned appliance')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('export', 'restore'))
    parser.add_argument('--vm', required=True, help='explicit owned appliance UUID')
    parser.add_argument('--archive', required=True, type=Path)
    parser.add_argument('--state-dir', required=True, type=Path)
    parser.add_argument('--storage-dir', required=True, type=Path)
    parser.add_argument('--uri', default='qemu:///system')
    parser.add_argument('--original-vm')
    parser.add_argument('--original-external', action='store_true', help='original is on another host; explicit exclusion attestation replaces local observation')
    parser.add_argument('--original-stopped-and-excluded', action='store_true')
    parser.add_argument('--replace-empty-controller', action='store_true')
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error('run as root on the Nex Core host')
    if args.uri != 'qemu:///system':
        parser.error('recovery requires local system libvirt')
    os.umask(0o077)
    with (args.state_dir / 'operations.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        owned(args.uri, args.vm, args.state_dir, args.storage_dir)
        if args.action == 'restore':
            if not args.original_stopped_and_excluded or not args.original_vm or args.original_vm == args.vm:
                parser.error('select distinct original UUID and attest permanent exclusion before restoring')
            if str(uuid.UUID(args.original_vm)) != args.original_vm:
                parser.error('select the original canonical VM UUID')
            if not args.original_external:
                owned(args.uri, args.original_vm, args.state_dir, args.storage_dir)
                if virsh(args.uri, 'domstate', args.original_vm) != 'shut off':
                    parser.error('original appliance must be observed shut off on this host')
                virsh(args.uri, 'autostart', args.original_vm, '--disable')
        guest = Guest(args.uri, args.vm)
        guest.execute('/usr/bin/systemctl', 'stop', 'nex-appliance-init', 'nex-controller')
        # Remote archives use unique names and root0600 creation; no caller-selected guest paths.
        remote = '/var/lib/nex-recovery-' + str(uuid.uuid4()) + '.tar.gz'
        if args.action == 'export':
            with args.archive.open('xb') as output:
                guest.execute('/usr/local/lib/nex/controller-recovery', 'export', remote)
                guest.download(remote, output)
                output.flush()
                os.fsync(output.fileno())
        else:
            guest.execute('/usr/bin/install', '-m', '0600', '/dev/null', remote)
            with args.archive.open('rb') as source:
                guest.upload(remote, source)
            flags = ['--original-stopped-and-excluded']
            if args.replace_empty_controller:
                flags.append('--replace-empty-controller')
            guest.execute('/usr/local/lib/nex/controller-recovery', 'restore', remote, *flags)
        guest.execute('/usr/bin/rm', '--', remote)
    print('Completed; Controller remains stopped. Archive contains private identity material.')


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, ValueError, OSError, subprocess.SubprocessError):
        raise SystemExit('Recovery did not complete; inspect locally before retrying. Services may remain stopped; partial archives are not valid backups.')
