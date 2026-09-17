#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Focused Phase 2 API probes against a deployed fresh-install Controller.

Covers oversized create (queued→failed), dedicated-memory admission, and BUG-0256
running-VM add-only MAC reuse (gate row 11 probe c2).
"""
from __future__ import annotations

import argparse
import http.client
import json
import ssl
import time
import uuid
from pathlib import Path
from urllib.parse import urlparse


def password(config: dict, key: str, base: Path) -> str:
    path = Path(config[key + '_password_file'])
    if not path.is_absolute():
        path = base / path
    assert path.stat().st_mode & 0o077 == 0
    return path.read_text().rstrip('\n')


class Client:
    def __init__(self, url: str):
        parsed = urlparse(url)
        self.host = parsed.hostname
        self.port = parsed.port or (443 if parsed.scheme == 'https' else 80)
        self.ctx = ssl._create_unverified_context()
        self.cookie = None
        self.csrf = None

    def request(self, method, path, body=None, expected=None):
        conn = http.client.HTTPSConnection(self.host, self.port, context=self.ctx, timeout=60)
        headers = {'Accept': 'application/json', 'Content-Type': 'application/json'}
        if self.cookie:
            headers['Cookie'] = self.cookie
        if self.csrf and method not in ('GET', 'HEAD'):
            headers['X-CSRF-Token'] = self.csrf
            headers['Origin'] = f'https://{self.host}:{self.port}'
        raw = None if body is None else json.dumps(body).encode()
        conn.request(method, path, body=raw, headers=headers)
        resp = conn.getresponse()
        data = resp.read()
        set_cookie = resp.getheader('Set-Cookie')
        if set_cookie:
            self.cookie = set_cookie.split(';', 1)[0]
        try:
            parsed = json.loads(data.decode() or 'null')
        except json.JSONDecodeError:
            parsed = {'raw': data.decode(errors='replace')}
        if isinstance(parsed, dict):
            parsed = {**parsed, 'status_code': resp.status}
        elif expected is not None:
            parsed = {'_list': parsed, 'status_code': resp.status}
        if expected is not None and resp.status != expected:
            raise AssertionError(f'{method} {path}: got {resp.status} want {expected}: {parsed}')
        conn.close()
        return parsed if isinstance(parsed, dict) and 'status_code' in (parsed or {}) else parsed

    def get_list(self, path):
        body = self.request('GET', path)
        if isinstance(body, list):
            return body
        if isinstance(body, dict):
            if '_list' in body:
                return body['_list']
            for key in ('hosts', 'storage', 'networks', 'vms', 'operations', 'items'):
                if isinstance(body.get(key), list):
                    return body[key]
        raise AssertionError(f'unexpected list payload for {path}: {body!r}')

    def wait_op(self, op_id, timeout=180):
        deadline = time.time() + timeout
        while time.time() < deadline:
            rows = self.get_list('/api/v1/operations')
            for row in rows:
                if row.get('id') == op_id and row.get('status') in ('succeeded', 'failed', 'needs_attention'):
                    return row
            one = self.request('GET', f'/api/v1/operations/{op_id}')
            if isinstance(one, dict) and one.get('id') == op_id and one.get('status') in (
                    'succeeded', 'failed', 'needs_attention'):
                return one
            time.sleep(1)
        raise TimeoutError(f'operation {op_id} did not settle')


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--config', type=Path, required=True)
    p.add_argument('--evidence', type=Path, required=True)
    args = p.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    cfg = json.loads(args.config.read_text())
    assert cfg.get('disposable') is True
    client = Client(cfg['controller_url'])
    session = client.request('POST', '/api/v1/session', {
        'username': 'root',
        'password': password(cfg, 'controller', args.config.parent),
    }, expected=200)
    client.csrf = session['csrf_token']

    host = client.get_list('/api/v1/hosts')[0]
    pool = host['pool_id']
    host_id = host['id']
    storage = next(
        s for s in client.get_list('/api/v1/storage')
        if s.get('pool_id') == pool and (
            s.get('status') in ('healthy', 'succeeded')
            or (s.get('access') or {}).get('status') == 'healthy'
        )
    )
    network = next(n for n in client.get_list('/api/v1/networks') if n.get('pool_id') == pool)

    results = {'probes': [], 'pool_id': pool, 'host_id': host_id}
    vm_rows = client.get_list('/api/v1/vms')
    non_appliance = [v for v in vm_rows if v.get('role') != 'controller']
    results['non_appliance_vm_count'] = len(non_appliance)
    (args.evidence / 'pool-vm-list.json').write_text(json.dumps({
        'count': len(vm_rows), 'non_appliance': len(non_appliance),
        'names': [v.get('name') for v in vm_rows],
    }, indent=2) + '\n')

    def create(name, *, size_gib=4, memory_mib=1024, nics=1, start=False, memory_mode='dedicated'):
        body = {
            'request_id': str(uuid.uuid4()),
            'pool_id': pool,
            'host_id': host_id,
            'name': name,
            'guest_os': 'linux',
            'cpu': {'count': 1, 'model': 'qemu64'},
            'memory': {'mode': memory_mode, 'size_mib': memory_mib},
            'boot': {'firmware': 'uefi'},
            'start_after_create': start,
            'autostart': False,
            'disks': [{'storage_id': storage['id'], 'name': 'system', 'size_gib': size_gib,
                       'bus': 'virtio', 'thin': True, 'boot_order': 1}],
            'nics': [{'network_id': network['id'], 'model': 'virtio', 'mac': ''} for _ in range(nics)],
        }
        resp = client.request('POST', '/api/v1/vms', body)
        if resp.get('status_code') not in (200, 202):
            print('CREATE_FAIL', name, json.dumps(body), '->', json.dumps(resp), flush=True)
        return resp

    def op_id(resp):
        return resp.get('operation_id') or (resp.get('operation') or {}).get('id')

    # Oversized disk create
    over = create('gate-overcap2', size_gib=2000)
    assert over.get('status_code') in (200, 202), over
    over_final = client.wait_op(op_id(over))
    over_err = (over_final.get('error') or over_final.get('message') or '').lower()
    results['probes'].append({
        'name': 'oversized_create_fails',
        'status': over_final.get('status'),
        'error': over_final.get('error') or over_final.get('message'),
        'pass': over_final.get('status') == 'failed' and 'storage' in over_err,
    })
    (args.evidence / 'oversized-create.json').write_text(json.dumps(over_final, indent=2) + '\n')

    # Dedicated memory admission — 14 GiB on a 16 GiB host with appliance + reserve
    mem = create('gate-memcap2', memory_mib=14 * 1024)
    if mem.get('status_code') not in (200, 202):
        mem_final = mem
        mem_err = (mem.get('error') or '').lower()
        mem_pass = mem.get('status_code') in (400, 409) and any(
            w in mem_err for w in ('memory', 'ram', 'admit', 'insufficient', 'reserve'))
    else:
        mem_final = client.wait_op(op_id(mem))
        mem_err = (mem_final.get('error') or mem_final.get('message') or '').lower()
        mem_pass = mem_final.get('status') == 'failed' and any(
            w in mem_err for w in ('memory', 'ram', 'admit', 'insufficient', 'reserve'))
    results['probes'].append({
        'name': 'dedicated_memory_admission',
        'status': mem_final.get('status') or mem_final.get('status_code'),
        'error': mem_final.get('error') or mem_final.get('message'),
        'pass': bool(mem_pass),
    })
    (args.evidence / 'memory-admission.json').write_text(json.dumps(mem_final, indent=2) + '\n')

    # BUG-0256 probe VM with two NICs
    probe = create('gate-mac-probe2', nics=2)
    assert probe.get('status_code') in (200, 202), probe
    assert client.wait_op(op_id(probe)).get('status') == 'succeeded'
    pvm = probe.get('vm_id') or probe.get('id')
    assert pvm

    start = client.request('POST', f'/api/v1/vms/{pvm}/power', {
        'request_id': str(uuid.uuid4()), 'action': 'start',
    })
    assert start.get('status_code') in (200, 202), start
    assert client.wait_op(op_id(start)).get('status') == 'succeeded'
    time.sleep(3)

    vm = next(v for v in client.get_list('/api/v1/vms') if v['id'] == pvm)
    definition = dict(vm['spec'])
    nics = list(definition.get('nics') or [])
    assert len(nics) >= 2, nics
    for nic in nics:
        if not nic.get('mac') and nic.get('effective_mac'):
            nic['mac'] = nic['effective_mac']

    survivor = dict(nics[1])
    edit = dict(definition)
    edit['nics'] = [survivor, {'network_id': network['id'], 'model': 'virtio', 'mac': ''}]
    edit['request_id'] = str(uuid.uuid4())
    put = client.request('PUT', f'/api/v1/vms/{pvm}', edit)
    (args.evidence / 'mac-remove-add.json').write_text(json.dumps(put, indent=2) + '\n')
    if put.get('status_code') in (200, 202):
        put_final = client.wait_op(op_id(put))
    else:
        put_final = put
    results['probes'].append({
        'name': 'running_remove_plus_add',
        'status': put_final.get('status') or put_final.get('status_code'),
        'error': put_final.get('error') or put_final.get('message'),
        'pass': (put_final.get('status') == 'succeeded') or (
            put_final.get('status') == 'failed' and 'mac' in (put_final.get('error') or '').lower()),
    })

    vm = next(v for v in client.get_list('/api/v1/vms') if v['id'] == pvm)
    definition = dict(vm['spec'])
    nics2 = list(definition.get('nics') or [])
    for nic in nics2:
        if not nic.get('mac') and nic.get('effective_mac'):
            nic['mac'] = nic['effective_mac']
    add_only = dict(definition)
    add_only['nics'] = nics2 + [{'network_id': network['id'], 'model': 'virtio', 'mac': ''}]
    add_only['request_id'] = str(uuid.uuid4())
    put2 = client.request('PUT', f'/api/v1/vms/{pvm}', add_only)
    if put2.get('status_code') in (200, 202):
        put2_final = client.wait_op(op_id(put2))
    else:
        put2_final = put2
    err2 = (put2_final.get('error') or put2_final.get('message') or '').lower()
    # After the fix, Core may refuse a colliding MAC, or succeed with a new index that
    # does not reuse a still-live removed MAC. Either way the live domain must not carry
    # the same MAC on two interfaces.
    import subprocess
    ssh = [
        'ssh', '-o', 'BatchMode=yes',
        '-o', f'UserKnownHostsFile={args.config.parent / "known_hosts"}',
        '-i', str(args.config.parent / 'id_ed25519'),
        '-p', str(cfg['ports']['ssh']), 'root@127.0.0.1',
        f'virsh -c qemu:///system domiflist {pvm}',
    ]
    domif = subprocess.run(ssh, capture_output=True, text=True, timeout=30)
    live_macs = []
    for line in domif.stdout.splitlines():
        parts = line.split()
        if len(parts) >= 5 and parts[-1].count(':') == 5:
            live_macs.append(parts[-1].lower())
    dupes = sorted({m for m in live_macs if live_macs.count(m) > 1})
    refused = (
        put2_final.get('status') == 'failed'
        or put2_final.get('status_code') in (400, 409)
    ) and any(w in err2 for w in ('mac', 'release', 'still attached', 'running', 'guest'))
    c2_pass = (not dupes) and (refused or put2_final.get('status') == 'succeeded')
    results['probes'].append({
        'name': 'bug0256_add_only_c2',
        'status': put2_final.get('status') or put2_final.get('status_code'),
        'error': put2_final.get('error') or put2_final.get('message'),
        'live_macs': live_macs,
        'duplicate_macs': dupes,
        'pass': bool(c2_pass),
    })
    (args.evidence / 'mac-add-only-c2.json').write_text(json.dumps({
        'final': put2_final, 'domiflist': domif.stdout, 'live_macs': live_macs, 'duplicate_macs': dupes,
    }, indent=2) + '\n')

    results['all_pass'] = all(x.get('pass') for x in results['probes'])
    (args.evidence / 'phase2-focused-probes.json').write_text(json.dumps(results, indent=2) + '\n')
    print(json.dumps(results, indent=2))
    return 0 if results['all_pass'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
