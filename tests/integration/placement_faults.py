#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Native fault/recovery acceptance for master-owned VM placement, in an explicitly selected lab.

Topology: two ISO-installed Nex Core hosts on an isolated bridge, host A (pool master, runs
the bundled Controller appliance) and host B (member), plus an NFS storage node exporting the
pool's shared repository. Each host lab is a private fresh-install config.json (see
fresh_install_lab.prepare) with disposable=true, a pinned known_hosts and its SSH key. The
pool, its shared NFS repository and a pool network must already exist and be ready on B.

Cases (--case, repeatable; run in this order):
  lost-reply     create/update replayed with the same request_id after a dropped reply; a
                 changed payload under that request_id is refused with 409
  shared-edit    stopped edit and ISO insert/eject of a VM placed on B on shared storage
  storage-loss   DESTRUCTIVE: stops nfs-server (--storage-fault stop) or changes the export's
                 fsid (--storage-fault fsid) on the storage node; restored in finally
  membership     DESTRUCTIVE: ejects B while a create targets it; B rejoins in finally
  interrupt      DESTRUCTIVE: SIGKILLs nex-core-local on B during a create and on A during an
                 update; restarted in finally
  replacement    ONE-WAY: shuts off and disables the Controller appliance, adopts the pool
                 into the empty Controller at --replacement-url. Not restored: afterwards pass
                 --controller-url/--controller-password-file for the replacement.

Every case proves one durable owner and no second writer: a VM UUID may be defined on at
most one host and running on at most one (virsh list on both hosts), and the master's
pool-vms.json record names that owner with no pending update. VMs this run creates are
named placement-fault-<run>-* and deleted at the end of their case unless --keep; nothing
else is deleted. Evidence is one JSON file per case in a new --evidence directory.
"""
import argparse
import concurrent.futures
import http.client
import http.cookiejar
import json
import re
import secrets
import shlex
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from fresh_install_host import ssh  # noqa: E402
from phase1 import ROOT, eventually  # noqa: E402

AGENT_STATE = '/var/lib/nex-core-agent'
SERVICE = 'nex-core-local'
PREFIX = 'placement-fault-'
CASES = ('lost-reply', 'shared-edit', 'storage-loss', 'membership', 'interrupt', 'replacement')

# Runs on a host: SIGKILL the Core service as soon as work for the named VM is pending there,
# either a target's managed journal entry or the master's reserved pending update.
WATCH = r"""
import json, pathlib, subprocess, sys, time
state, name, service, wait = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3], float(sys.argv[4])
def pending():
    for path in (state / 'managed-vms').glob('*.json'):
        try:
            j = json.loads(path.read_text())
        except (OSError, ValueError):
            continue
        if ((j.get('command') or {}).get('vm') or {}).get('name') == name and j.get('result', {}).get('status') == 'pending':
            return {'journal': path.name, 'stage': j.get('stage')}
    try:
        vms = json.loads((state / 'pool-vms.json').read_text()).get('vms') or {}
    except (OSError, ValueError):
        vms = {}
    for vm_id, record in vms.items():
        update = record.get('pending_update') or {}
        if (update.get('next') or {}).get('name') == name:
            return {'pool_record': vm_id, 'pending_update': update.get('operation_id')}
deadline = time.monotonic() + wait
while time.monotonic() < deadline:
    hit = pending()
    if hit:
        subprocess.run(['systemctl', 'kill', '--signal=SIGKILL', service], check=True)
        print(json.dumps({'killed_at': time.time(), **hit}), flush=True)
        sys.exit(0)
    time.sleep(0.005)
sys.exit('no pending work for ' + name + ' was observed before the deadline; not evidence')
"""


class CaseFailed(Exception):
    pass


class Controller:
    """Session + CSRF + Origin client for one Controller. The lab Controller serves a
    self-signed certificate on a loopback forward, so it is not verified (as the Playwright
    harnesses do with ignore_https_errors)."""

    def __init__(self, url, username, password):
        self.url = url.rstrip('/')
        self.tls = ssl.create_default_context()
        self.tls.check_hostname, self.tls.verify_mode = False, ssl.CERT_NONE
        self.jar = http.cookiejar.CookieJar()
        self.http = urllib.request.build_opener(urllib.request.HTTPSHandler(context=self.tls),
                                                urllib.request.HTTPCookieProcessor(self.jar))
        self.csrf = ''
        self.csrf = self.api('POST', '/api/v1/session', {'username': username, 'password': password})['csrf_token']

    def headers(self):
        return {'Content-Type': 'application/json', 'Origin': self.url, 'X-CSRF-Token': self.csrf}

    def call(self, method, path, body=None):
        request = urllib.request.Request(self.url + path, method=method, headers=self.headers(),
                                         data=None if body is None else json.dumps(body).encode())
        try:
            response = self.http.open(request, timeout=60)
        except urllib.error.HTTPError as exc:
            response = exc
        with response:
            data = response.read()
            return response.status, json.loads(data) if data else None

    def api(self, method, path, body=None, expected=200):
        status, data = self.call(method, path, body)
        assert status == expected, f'{method} {path}: {status} {data}'
        return data

    def lose_reply(self, method, path, body):
        """Send one complete request and close the connection without reading the reply."""
        parts = urllib.parse.urlsplit(self.url)
        payload = json.dumps(body).encode()
        conn = http.client.HTTPSConnection(parts.hostname, parts.port, context=self.tls, timeout=10)
        conn.request(method, path, body=payload, headers={**self.headers(), 'Content-Length': str(len(payload)),
                     'Cookie': '; '.join(f'{c.name}={c.value}' for c in self.jar)})
        conn.close()


class Case:
    def __init__(self, name):
        self.name, self.checks, self.facts, self.created = name, [], {}, []

    def check(self, ok, message, **facts):
        self.checks.append({'ok': bool(ok), 'message': message, **facts})
        print(('PASS' if ok else 'FAIL') + f': [{self.name}] {message}', flush=True)
        if not ok:
            raise CaseFailed(message)

    def note(self, key, value):
        self.facts[key] = value


def with_fsid(exports, path, fsid):
    """Set fsid= on every client of one export line.

    >>> with_fsid('/srv/x a(rw,fsid=1) b(ro)\\n/other c(rw)\\n', '/srv/x', 'u')
    '/srv/x a(rw,fsid=u) b(ro,fsid=u)\\n/other c(rw)\\n'
    """
    def options(match):
        kept = [o for o in match.group(1).split(',') if o and not o.startswith('fsid=')]
        return '(' + ','.join(kept + [f'fsid={fsid}']) + ')'
    lines = [re.sub(r'\(([^)]*)\)', options, line) if line.split()[:1] == [path] else line for line in exports.splitlines()]
    return '\n'.join(lines) + '\n'


class Lab:
    def __init__(self, args):
        self.args = args
        self.run = secrets.token_hex(3)
        configs = {}
        for key, path in (('a', args.config_a), ('b', args.config_b)):
            config = json.loads(path.read_text())
            assert config.get('disposable') is True, f'{path}: explicit disposable lab selection required'
            configs[key] = (path, config)
        self.labs = {key: config['lab_dir'] for key, (_, config) in configs.items()}
        self.addresses = {'a': args.address_a, 'b': args.address_b}
        path_a, config_a = configs['a']
        self.root_password = {key: self.secret(path, config['root_password_file']) for key, (path, config) in configs.items()}
        password = self.secret(path_a, args.controller_password_file or config_a['controller_password_file'])
        self.ctl = Controller(args.controller_url or config_a['controller_url'], args.username, password)
        self.host = {key: json.loads(self.sh(key, f'cat {AGENT_STATE}/state.json'))['host_id'] for key in self.labs}
        self.pool = next(p for p in self.ctl.api('GET', '/api/v1/pools') if p['master_host_id'] == self.host['a'])
        members = self.ctl.api('GET', f'/api/v1/pools/{self.pool["id"]}/members')['members']
        assert any(m['host_id'] == self.host['b'] and m['status'] == 'ready' for m in members), 'host B must be a ready member of host A\'s pool'
        self.storage = self.pick('/api/v1/storage', args.storage_id, lambda s: s.get('kind') == 'nfs')
        self.network = self.pick('/api/v1/networks', args.network_id, lambda n: True)

    @staticmethod
    def secret(config_path, name):
        path = Path(name)
        path = path if path.is_absolute() else config_path.parent / path
        assert path.stat().st_mode & 0o077 == 0, f'{path}: private credential permissions required'
        return path.read_text().rstrip('\n')

    def pick(self, path, wanted, kind):
        rows = [r for r in self.ctl.api('GET', path) if r.get('pool_id') == self.pool['id'] and r.get('status') == 'succeeded' and kind(r)]
        rows = [r for r in rows if r['id'] == wanted] if wanted else rows
        assert len(rows) == 1, f'{path}: need exactly one ready pool resource (pass its ID): {[r["id"] for r in rows]}'
        return rows[0]

    def sh(self, key, command, timeout=120):
        return ssh(self.labs[key], command, text=True, timeout=timeout).stdout

    def storage_sh(self, command, stdin=None):
        return subprocess.run(['ssh', '-F', '/dev/null', '-o', 'StrictHostKeyChecking=yes', '-o', 'BatchMode=yes',
                               '-o', 'ConnectTimeout=5', '-o', f'UserKnownHostsFile={self.args.storage_known_hosts}',
                               '-i', str(self.args.storage_key), f'root@{self.args.storage_host}', command],
                              check=True, capture_output=True, text=True, timeout=120, input=stdin).stdout

    def rid(self, case, label):
        return f'{PREFIX}{self.run}-{case.name}-{label}'

    def spec(self, case, label, host='b', start=False):
        return {'pool_id': self.pool['id'], 'host_id': self.host[host], 'name': self.rid(case, label),
                'description': 'placement fault acceptance', 'guest_os': 'linux',
                'cpu': {'count': 1, 'sockets': 0, 'cores': 0, 'threads': 0, 'model': 'host-model'},
                'memory': {'mode': 'maximum', 'size_mib': 512},
                'disks': [{'storage_id': self.storage['id'], 'name': 'system', 'size_gib': 1, 'bus': 'virtio', 'thin': True, 'boot_order': 1}],
                'nics': [{'network_id': self.network['id'], 'model': 'virtio', 'mac': ''}],
                'boot': {'firmware': 'bios', 'machine': '', 'media_boot_order': 2},
                'media_id': '', 'start_after_create': start, 'autostart': False}

    # Observations.
    def wait_op(self, op_id, timeout=600):
        """The operation once succeeded/failed, or as last seen when the timeout expires."""
        deadline = time.monotonic() + timeout
        while True:
            op = next((o for o in self.ctl.api('GET', '/api/v1/operations') if o['id'] == op_id), {'id': op_id, 'status': 'missing'})
            if op['status'] in ('succeeded', 'failed') or time.monotonic() > deadline:
                return op
            time.sleep(2)

    def vm(self, vm_id):
        return next((v for v in self.ctl.api('GET', '/api/v1/vms') if v['id'] == vm_id), None)

    def wait_vm(self, vm_id, ready, what, timeout=180):
        return eventually(lambda: (lambda v: v if v and v['freshness'] == 'fresh' and ready(v) else None)(self.vm(vm_id)), what, timeout)

    def domains(self, vm_id):
        out = {}
        for key in self.labs:
            defined = self.sh(key, 'virsh -c qemu:///system list --all --uuid').split()
            running = self.sh(key, 'virsh -c qemu:///system list --uuid').split()
            out[key] = {'defined': vm_id in defined, 'running': vm_id in running}
        return out

    def master_record(self, vm_id):
        return (json.loads(self.sh('a', f'cat {AGENT_STATE}/pool-vms.json')).get('vms') or {}).get(vm_id)

    def one_owner(self, case, vm_id, owner):
        """owner is 'a', 'b' or None (no host may hold the VM)."""
        domains = self.domains(vm_id)
        defined = [k for k, d in domains.items() if d['defined']]
        running = [k for k, d in domains.items() if d['running']]
        record = self.master_record(vm_id)
        case.note(f'owner-{vm_id}-{len(case.checks)}', {'domains': domains, 'master_record': record})
        case.check(defined == ([owner] if owner else []) and len(running) <= 1,
                   f'{vm_id[:8]} defined on {defined or "no host"}, running on {running or "no host"}; expected {owner or "none"}')
        if owner:
            case.check(record and record['host_id'] == self.host[owner] and not record.get('pending_update'),
                       f'master record names host {owner.upper()} as the only owner with no pending update')
        else:
            case.check(record is None, f'master holds no record for {vm_id[:8]}')
        return record

    # Requests.
    def create(self, case, label, host='b', start=False, expected=202):
        body = {**self.spec(case, label, host, start), 'request_id': self.rid(case, label)}
        created = self.ctl.api('POST', '/api/v1/vms', body, expected=expected)
        case.created.append(created['vm_id'])
        return body, created

    def created_vm(self, case, label, host='b', start=False):
        body, created = self.create(case, label, host, start)
        op = self.wait_op(created['operation']['id'])
        case.check(op['status'] == 'succeeded', f'{label} created on host {host.upper()}', operation=op)
        self.wait_vm(created['vm_id'], lambda v: v['state'] == ('running' if start else 'shut off'), f'{label} observed')
        return body, created['vm_id']

    def edit_body(self, vm_id, request_id, **changes):
        spec = self.vm(vm_id)['spec']
        body = {k: spec[k] for k in ('pool_id', 'host_id', 'name', 'description', 'guest_os', 'cpu', 'memory', 'disks', 'nics', 'boot', 'media_id', 'autostart')}
        return {**body, 'start_after_create': False, **changes, 'request_id': request_id}

    def power(self, case, vm_id, action):
        r = self.ctl.api('POST', f'/api/v1/vms/{vm_id}/power', {'request_id': self.rid(case, f'{action}-{vm_id[:8]}-{secrets.token_hex(2)}'), 'action': action}, expected=202)
        return self.wait_op(r['operation']['id'], timeout=300)

    def remove(self, case, vm_id):
        v = self.vm(vm_id)
        if not v:
            return
        assert v['name'].startswith(PREFIX + self.run), f'refusing to delete {vm_id}: not created by this run'
        if v['state'] != 'shut off':
            self.power(case, vm_id, 'force-stop')
            self.wait_vm(vm_id, lambda v: v['state'] == 'shut off', 'VM observed shut off before delete')
        r = self.ctl.api('DELETE', f'/api/v1/vms/{vm_id}', {'request_id': self.rid(case, f'delete-{vm_id[:8]}'), 'delete_disks': True}, expected=202)
        case.note(f'cleanup-{vm_id}', self.wait_op(r['operation']['id'], timeout=300))

    def recheck_storage(self, case, label):
        generation = next(s for s in self.ctl.api('GET', '/api/v1/storage') if s['id'] == self.storage['id'])['generation']
        body = {'request_id': self.rid(case, label), 'pool_id': self.pool['id'], 'host_id': self.storage['host_id'], 'expected_generation': generation}
        r = self.ctl.api('POST', f'/api/v1/storage/{self.storage["id"]}/recheck', body, expected=202)
        return self.wait_op(r['operation']['id'], timeout=300)


def lost_reply(lab, case):
    body = {**lab.spec(case, 'vm'), 'request_id': lab.rid(case, 'create')}
    lab.ctl.lose_reply('POST', '/api/v1/vms', body)
    time.sleep(2)
    status, first = lab.ctl.call('POST', '/api/v1/vms', body)
    case.check(status in (200, 202), f'exact create retry after the dropped reply answered {status}')
    case.created.append(first['vm_id'])
    again = lab.ctl.api('POST', '/api/v1/vms', body, expected=200)
    case.check(again['vm_id'] == first['vm_id'] and again['operation']['id'] == first['operation']['id'],
               'every exact create retry returns the same VM identity and operation')
    changed = lab.ctl.api('POST', '/api/v1/vms', {**body, 'memory': {'mode': 'maximum', 'size_mib': 1024}}, expected=409)
    case.note('changed-create', changed)
    case.check(True, 'a changed create reusing the request_id is refused with 409')
    vm_id = first['vm_id']
    op = lab.wait_op(first['operation']['id'])
    case.check(op['status'] == 'succeeded', 'the one create succeeded', operation=op)
    lab.wait_vm(vm_id, lambda v: v['state'] == 'shut off', 'created VM observed')
    case.check(sum(v['name'] == body['name'] for v in lab.ctl.api('GET', '/api/v1/vms')) == 1, 'exactly one VM carries the name')
    lab.one_owner(case, vm_id, 'b')

    edit = lab.edit_body(vm_id, lab.rid(case, 'update'), name=body['name'] + '-r1')
    lab.ctl.lose_reply('PUT', f'/api/v1/vms/{vm_id}', edit)
    time.sleep(2)
    status, first = lab.ctl.call('PUT', f'/api/v1/vms/{vm_id}', edit)
    case.check(status in (200, 202), f'exact update retry after the dropped reply answered {status}')
    again = lab.ctl.api('PUT', f'/api/v1/vms/{vm_id}', edit, expected=200)
    case.check(again['operation']['id'] == first['operation']['id'], 'every exact update retry returns the same operation')
    lab.ctl.api('PUT', f'/api/v1/vms/{vm_id}', {**edit, 'description': 'changed'}, expected=409)
    case.check(True, 'a changed update reusing the request_id is refused with 409')
    op = lab.wait_op(first['operation']['id'])
    case.check(op['status'] == 'succeeded', 'the one update succeeded', operation=op)
    v = lab.wait_vm(vm_id, lambda v: v['spec']['revision'] == 1, 'revision 1 reported')
    record = lab.one_owner(case, vm_id, 'b')
    case.check(v['name'] == edit['name'] and record['spec']['revision'] == 1 and record['spec']['name'] == edit['name'],
               'Controller and master record both hold revision 1 with the new name')


def shared_edit(lab, case):
    media = lab.pick('/api/v1/media', lab.args.media_id, lambda m: m.get('storage_id') == lab.storage['id'])
    _, vm_id = lab.created_vm(case, 'vm')
    edit = lab.edit_body(vm_id, lab.rid(case, 'edit'), name=lab.rid(case, 'vm-edited'), memory={'mode': 'maximum', 'size_mib': 1024})
    op = lab.wait_op(lab.ctl.api('PUT', f'/api/v1/vms/{vm_id}', edit, expected=202)['operation']['id'])
    case.check(op['status'] == 'succeeded', 'stopped definition edit succeeded', operation=op)
    lab.wait_vm(vm_id, lambda v: v['spec']['revision'] == 1, 'revision 1 reported')
    record = lab.one_owner(case, vm_id, 'b')
    xml = lab.sh('b', f'virsh -c qemu:///system dumpxml --inactive {vm_id}')
    case.check(record['spec']['name'] == edit['name'] and record['spec']['memory']['size_mib'] == 1024 and f'<name>{edit["name"]}</name>' in xml,
               'master record and the member\'s libvirt definition carry the edit')
    for action in ('insert', 'eject'):
        body = {'request_id': lab.rid(case, action), 'action': action, 'media_id': media['id'] if action == 'insert' else ''}
        op = lab.wait_op(lab.ctl.api('POST', f'/api/v1/vms/{vm_id}/media', body, expected=202)['operation']['id'])
        case.check(op['status'] == 'succeeded', f'ISO {action} succeeded', operation=op)
        blk = lab.sh('b', f'virsh -c qemu:///system domblklist --inactive {vm_id}')
        case.note(f'domblklist-{action}', blk)
        case.check(('.iso' in blk) == (action == 'insert'), f'member definition shows the ISO {"attached" if action == "insert" else "ejected"}')
        record = lab.one_owner(case, vm_id, 'b')
        v = lab.wait_vm(vm_id, lambda v: True, 'VM observed')
        case.check(record['spec'].get('media_id', '') == v['spec'].get('media_id', ''), 'master record media matches the host-reported definition')


def storage_loss(lab, case):
    _, running = lab.created_vm(case, 'running', start=True)
    _, stopped = lab.created_vm(case, 'stopped')
    export = lab.args.storage_export
    exports = lab.storage_sh('cat /etc/exports')
    case.check(any(line.split()[:1] == [export] for line in exports.splitlines()), f'storage node exports {export}')
    case.note('fault', lab.args.storage_fault)
    try:
        if lab.args.storage_fault == 'stop':
            lab.storage_sh('systemctl stop nfs-server')
        else:
            lab.storage_sh('cat > /etc/exports && exportfs -ra', stdin=with_fsid(exports, export, uuid.uuid4()))
        case.note('recheck-during-fault', lab.recheck_storage(case, 'recheck-fault'))
        body, created = lab.create(case, 'during-fault')
        op = lab.wait_op(created['operation']['id'], timeout=300)
        case.note('create-during-fault', op)
        case.check(op['status'] != 'succeeded' and not any(d['defined'] for d in lab.domains(created['vm_id']).values()),
                   f'create on the lost repository did not succeed ({op["status"]}) and defined no domain')
        op = lab.power(case, stopped, 'start')
        case.note('start-during-fault', op)
        case.check(op['status'] != 'succeeded' and not any(d['running'] for d in lab.domains(stopped).values()),
                   f'start on the lost repository did not succeed ({op["status"]}) and nothing runs it')
        state = lab.vm(running)['state']
        domstate = lab.sh('b', f'virsh -c qemu:///system domstate {running}').strip()
        case.note('running-during-fault', {'reported': state, 'domstate': domstate})
        case.check(state != 'shut off' and domstate in ('running', 'paused'), f'running VM is not declared stopped (reported {state}, libvirt {domstate})')
        lab.one_owner(case, running, 'b')
        lab.one_owner(case, stopped, 'b')
    finally:
        if lab.args.storage_fault == 'stop':
            lab.storage_sh('systemctl start nfs-server')
        else:
            lab.storage_sh('cat > /etc/exports && exportfs -ra', stdin=exports)
        case.note('recheck-restored', lab.recheck_storage(case, 'recheck-restored'))
    op = lab.wait_op(created['operation']['id'])
    case.note('create-after-restore', op)
    lab.one_owner(case, created['vm_id'], 'b' if op['status'] == 'succeeded' else None)
    lab.one_owner(case, running, 'b')


def membership(lab, case):
    pool, b = lab.pool['id'], lab.host['b']
    member = next(m for m in lab.ctl.api('GET', f'/api/v1/pools/{pool}/members')['members'] if m['host_id'] == b)
    hostname = next(h for h in lab.ctl.api('GET', '/api/v1/hosts') if h['id'] == b)['hostname']
    body = {**lab.spec(case, 'vm'), 'request_id': lab.rid(case, 'create')}
    eject = {'request_id': lab.rid(case, 'eject'), 'confirmation': hostname}

    def is_member():
        return any(m['host_id'] == b and m['status'] != 'ejected' for m in lab.ctl.api('GET', f'/api/v1/pools/{pool}/members')['members'])
    try:
        with concurrent.futures.ThreadPoolExecutor(2) as pool_exec:
            create = pool_exec.submit(lab.ctl.call, 'POST', '/api/v1/vms', body)
            ejecting = pool_exec.submit(lab.ctl.call, 'DELETE', f'/api/v1/pools/{pool}/members/{b}', eject)
            (create_status, created), (eject_status, ejected) = create.result(), ejecting.result()
        case.note('responses', {'create': [create_status, created], 'eject': [eject_status, ejected]})
        vm_id = created.get('vm_id') if create_status == 202 else None
        if vm_id:
            case.created.append(vm_id)
        create_op = lab.wait_op(created['operation']['id']) if vm_id else {'status': f'refused {create_status}'}
        eject_op = lab.wait_op(ejected['operation']['id']) if eject_status == 202 else {'status': f'refused {eject_status}'}
        if eject_op['status'] == 'succeeded':
            eventually(lambda: not is_member(), 'eject reflected in the master\'s membership report', 120)
        still = is_member()
        case.note('outcomes', {'create': create_op, 'eject': eject_op, 'still_member': still})
        case.check(not (create_op['status'] == 'succeeded' and not still),
                   f'create {create_op["status"]}, eject {eject_op["status"]}: never both a new shared VM and an ejected owner')
        if vm_id:
            lab.one_owner(case, vm_id, 'b' if create_op['status'] == 'succeeded' else None)
        if still:
            # The race kept B; cover the other order too: an emptied B ejected before a create.
            for created_id in case.created:
                lab.remove(case, created_id)
                # The master prunes a deleted VM's record from the owner's next inventory.
                eventually(lambda: lab.master_record(created_id) is None, 'master pruned the deleted VM', 300)
            eject = {**eject, 'request_id': lab.rid(case, 'eject-sequential')}
            eject_op = lab.wait_op(lab.ctl.api('DELETE', f'/api/v1/pools/{pool}/members/{b}', eject, expected=202)['operation']['id'])
            case.check(eject_op['status'] == 'succeeded', 'emptied member ejected', operation=eject_op)
            eventually(lambda: not is_member(), 'eject reflected in the master\'s membership report', 120)
            still = False
        if not still:
            status, late = lab.ctl.call('POST', '/api/v1/vms', {**lab.spec(case, 'after-eject'), 'request_id': lab.rid(case, 'after-eject')})
            late_op = lab.wait_op(late['operation']['id'], timeout=300) if status == 202 else {'status': f'refused {status}', 'body': late}
            case.note('create-after-eject', late_op)
            case.check(late_op['status'] != 'succeeded', f'create targeting the ejected host fails closed ({late_op["status"]})')
            if status == 202:
                lab.one_owner(case, late['vm_id'], None)
    finally:
        if not is_member():
            rejoin = {'request_id': lab.rid(case, 'rejoin'), 'host_id': b, 'peer_origin': member['peer_origin']}
            op = lab.wait_op(lab.ctl.api('POST', f'/api/v1/pools/{pool}/members', rejoin, expected=202)['operation']['id'])
            case.note('rejoin', op)
            eventually(lambda: any(m['host_id'] == b and m['status'] == 'ready' for m in lab.ctl.api('GET', f'/api/v1/pools/{pool}/members')['members']),
                       'host B ready in the pool again', 300)


def interrupt(lab, case):
    def killed_during(key, name, submit):
        """Watch host key for pending work for name, submit, and return (kill record, response)."""
        with concurrent.futures.ThreadPoolExecutor(1) as watcher:
            watching = watcher.submit(_watch, lab, key, name)
            time.sleep(3)  # let the watcher start before the request can reach the host
            response = submit()
            return watching.result(), response

    try:
        # Create targeting the member, killed on the member (the target).
        body = {**lab.spec(case, 'vm'), 'request_id': lab.rid(case, 'create')}
        kill, created = killed_during('b', body['name'], lambda: lab.ctl.api('POST', '/api/v1/vms', body, expected=202))
        case.created.append(created['vm_id'])
        case.note('create-kill', kill)
        case.check(kill and kill.get('killed_at'), f'{SERVICE} on host B was killed while the create was pending ({kill})')
        lab.sh('b', f'systemctl start {SERVICE}')
        op = lab.wait_op(created['operation']['id'])
        case.note('create-result', op)
        case.check(op['status'] in ('succeeded', 'failed', 'needs_attention'), f'interrupted create resolved as {op["status"]}: {op.get("error", "")}')
        vm_id = created['vm_id']
        defined = [k for k, d in lab.domains(vm_id).items() if d['defined']]
        case.note('create-domains', defined)
        case.check(defined == (['b'] if op['status'] == 'succeeded' else []), f'interrupted create defined the VM on {defined or "no host"}')
        retry = lab.ctl.api('POST', '/api/v1/vms', body, expected=200)
        case.check(retry['vm_id'] == vm_id and retry['operation']['id'] == created['operation']['id'], 'exact retry after recovery replays the same create')
        if op['status'] == 'succeeded':
            lab.one_owner(case, vm_id, 'b')
        else:
            # The owner's next inventory proves the create failed; the master releases its record.
            eventually(lambda: lab.master_record(vm_id) is None, 'master released the failed create', 300)
            lab.one_owner(case, vm_id, None)
            body, vm_id = lab.created_vm(case, 'vm2')

        # Update of the member VM, killed on the master while it holds the pending update.
        lab.wait_vm(vm_id, lambda v: v['state'] == 'shut off', 'created VM observed')
        edit = lab.edit_body(vm_id, lab.rid(case, 'update'), name=body['name'] + '-r1')
        kill, updated = killed_during('a', edit['name'], lambda: lab.ctl.api('PUT', f'/api/v1/vms/{vm_id}', edit, expected=202))
        case.note('update-kill', kill)
        case.check(kill and kill.get('killed_at'), f'{SERVICE} on host A (master) was killed while the update was pending ({kill})')
        lab.sh('a', f'systemctl start {SERVICE}')
        op = lab.wait_op(updated['operation']['id'])
        case.note('update-result', op)
        case.check(op['status'] in ('succeeded', 'failed'), f'interrupted update resolved as {op["status"]}')
        want = edit['name'] if op['status'] == 'succeeded' else body['name']
        eventually(lambda: not (lab.master_record(vm_id) or {}).get('pending_update'), 'master settled the interrupted update', 300)
        record = lab.one_owner(case, vm_id, 'b')
        xml = lab.sh('b', f'virsh -c qemu:///system dumpxml --inactive {vm_id}')
        case.check(record['spec']['name'] == want and f'<name>{want}</name>' in xml, f'master record and libvirt agree on {want}')
        retry = lab.ctl.api('PUT', f'/api/v1/vms/{vm_id}', edit, expected=200)
        case.check(retry['operation']['id'] == updated['operation']['id'], 'exact update retry after recovery replays the same operation')
    finally:
        for key in ('a', 'b'):
            lab.sh(key, f'systemctl start {SERVICE}')


def _watch(lab, key, name):
    try:
        out = lab.sh(key, shlex.join(['python3', '-c', WATCH, AGENT_STATE, name, SERVICE, str(lab.args.interrupt_wait)]),
                     timeout=lab.args.interrupt_wait + 30)
        return json.loads(out.strip().splitlines()[-1])
    except subprocess.CalledProcessError as exc:
        return {'error': (exc.stderr or '')[-400:]}


def replacement(lab, case):
    args = lab.args
    assert args.replacement_url and args.replacement_password_file and args.replacement_host_url, \
        'replacement needs --replacement-url, --replacement-password-file and --replacement-host-url'
    body, member_vm = lab.created_vm(case, 'vm')
    before = {v['id']: {k: v[k] for k in ('name', 'host_id', 'pool_id')} for v in lab.ctl.api('GET', '/api/v1/vms') if v['role'] == 'guest'}
    appliance = next(v for v in lab.ctl.api('GET', '/api/v1/vms') if v['role'] == 'controller' and v['host_id'] == lab.host['a'])
    case.note('before', {'pool': lab.pool, 'hosts': lab.host, 'vms': before, 'appliance': appliance['id']})

    # Exclude the old Controller exactly as the guide says: autostart off, shut down, observed shut off.
    lab.sh('a', f'virsh -c qemu:///system autostart --disable {appliance["id"]}; virsh -c qemu:///system shutdown {appliance["id"]} || true')
    eventually(lambda: lab.sh('a', f'virsh -c qemu:///system domstate {appliance["id"]}').strip() == 'shut off', 'old Controller appliance shut off', 300)
    case.check(True, 'old Controller appliance excluded: autostart disabled and observed shut off')

    new = Controller(args.replacement_url, args.username, Lab.secret(args.replacement_password_file, args.replacement_password_file.name))
    case.check(new.api('GET', '/api/v1/hosts') == [], 'replacement Controller starts with no hosts')
    lab.ctl = new
    site = new.api('POST', '/api/v1/sites', {'name': 'Placement fault replacement'}, expected=201)
    for key in ('a', 'b'):
        probe = new.api('POST', '/api/v1/hosts/probe', {'address': lab.addresses[key]})
        add = {'address': lab.addresses[key], 'controller_url': args.replacement_host_url, 'fingerprint': probe['fingerprint'], 'root_password': lab.root_password[key]}
        # A member is refused (409, retryable) until its master has reported it.
        added = eventually(lambda: (lambda r: r[1] if r[0] == 200 else None)(new.call('POST', f'/api/v1/sites/{site["id"]}/hosts', add)), f'host {key.upper()} added', 300)
        case.check(added['host_id'] == lab.host[key] and added['pool_id'] == lab.pool['id'], f'host {key.upper()} returned with its host ID and pool ID')
        eventually(lambda: any(h['id'] == lab.host[key] and h['connection'] == 'fresh' for h in new.api('GET', '/api/v1/hosts')), f'host {key.upper()} fresh', 300)
    pool = next(p for p in new.api('GET', '/api/v1/pools') if p['id'] == lab.pool['id'])
    case.check(pool['name'] == lab.pool['name'] and pool['master_host_id'] == lab.host['a'], 'pool returned with its name and master')

    def adopted():
        now = {v['id']: {k: v[k] for k in ('name', 'host_id', 'pool_id')} for v in new.api('GET', '/api/v1/vms') if v['role'] == 'guest'}
        return now if all(now.get(i) == v for i, v in before.items()) else None
    after = eventually(adopted, 'every guest VM adopted with its identity, name and placement', 300)
    case.note('after', after)
    case.check(True, f'all {len(before)} guest VMs returned with the same IDs, names, hosts and pool')
    lab.one_owner(case, member_vm, 'b')

    # The replacement has never seen this request_id; the host must still refuse a duplicate.
    status, retry = new.call('POST', '/api/v1/vms', body)
    retry_op = lab.wait_op(retry['operation']['id']) if status == 202 else {'status': f'refused {status}', 'body': retry}
    case.note('retry-after-replacement', retry_op)
    if status == 202 and retry['vm_id'] != member_vm:
        case.created.append(retry['vm_id'])
        lab.one_owner(case, retry['vm_id'], 'b' if retry_op['status'] == 'succeeded' else None)
    names = [v['id'] for v in new.api('GET', '/api/v1/vms') if v['name'] == body['name']]
    case.check(names == [member_vm], f'retry after replacement did not duplicate the VM ({retry_op["status"]})')
    print('NOTE: the pool now belongs to the replacement Controller; the old appliance stays shut off and excluded.', flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--config-a', type=Path, required=True, help='private config.json of host A, the pool master')
    parser.add_argument('--config-b', type=Path, required=True, help='private config.json of host B, the member')
    parser.add_argument('--address-a', default='10.0.2.15')
    parser.add_argument('--address-b', default='10.0.2.16')
    parser.add_argument('--storage-host', default='10.0.2.30')
    parser.add_argument('--storage-key', type=Path, required=True)
    parser.add_argument('--storage-known-hosts', type=Path, required=True, help='pinned host key of the storage node')
    parser.add_argument('--storage-export', default='/srv/nex-shared')
    parser.add_argument('--storage-fault', choices=('stop', 'fsid'), default='stop')
    parser.add_argument('--storage-id', help='shared NFS repository ID when the pool has more than one')
    parser.add_argument('--network-id', help='pool network ID when the pool has more than one')
    parser.add_argument('--media-id', help='ISO on the shared repository for shared-edit')
    parser.add_argument('--controller-url', help='defaults to config A controller_url')
    parser.add_argument('--controller-password-file', help='defaults to config A controller_password_file')
    parser.add_argument('--username', default='root')
    parser.add_argument('--replacement-url', help='empty replacement Controller as this workstation reaches it')
    parser.add_argument('--replacement-host-url', help='the same Controller as the hosts reach it, e.g. https://10.0.2.1:8443')
    parser.add_argument('--replacement-password-file', type=Path)
    parser.add_argument('--interrupt-wait', type=float, default=120)
    parser.add_argument('--case', action='append', choices=CASES, required=True)
    parser.add_argument('--evidence', type=Path, required=True, help='new directory, never under docs/qa')
    parser.add_argument('--keep', action='store_true', help='keep the VMs this run created')
    args = parser.parse_args()
    evidence = args.evidence.resolve()
    assert not evidence.is_relative_to(ROOT / 'docs'), 'evidence must not be written into committed docs'
    evidence.mkdir(parents=True, exist_ok=False)

    lab = Lab(args)
    print(f'run {lab.run}: pool {lab.pool["name"]}, storage {lab.storage["id"]}, network {lab.network["id"]}', flush=True)
    results = {}
    for name in [c for c in CASES if c in args.case]:
        case, error = Case(name), None
        started = time.time()
        try:
            globals()[name.replace('-', '_')](lab, case)
        except CaseFailed as exc:
            error = str(exc)
        except Exception as exc:  # a broken step is a failed case, recorded, not a crashed run
            error = f'{type(exc).__name__}: {exc}'
            print(f'FAIL: [{name}] {error}', flush=True)
        finally:
            if not args.keep:
                for vm_id in case.created:
                    try:
                        lab.remove(case, vm_id)
                    except Exception as exc:
                        case.note(f'cleanup-error-{vm_id}', str(exc))
                        print(f'WARN: [{name}] cleanup of {vm_id} failed: {exc}', flush=True)
        results[name] = error is None
        (evidence / f'{name}.json').write_text(json.dumps({
            'case': name, 'run': lab.run, 'passed': error is None, 'error': error, 'started': started, 'finished': time.time(),
            'checks': case.checks, 'facts': case.facts, 'created': case.created, 'kept': args.keep,
            'environment': 'ISO-installed two-host lab with NFS storage node; Controller API over loopback forward; no mocking'},
            indent=2, default=str) + '\n')
    print(json.dumps(results, indent=2))
    return 0 if all(results.values()) else 1


if __name__ == '__main__':
    sys.exit(main())
