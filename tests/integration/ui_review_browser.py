#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Capture every Controller view against a running Controller with a labeled simulated inventory.

Reads are answered from tests/integration/fixtures/ui-review.json; the sign-in, session, shell
and every write go to the real Controller. Console tabs therefore report "not found": no host
exists behind the fixture. Fictional names and RFC 5737 addresses only.
"""
import argparse, json, math, re
from datetime import datetime, timedelta, timezone
from pathlib import Path
from playwright.sync_api import sync_playwright

ROOT = Path(__file__).resolve().parents[2]
FIXTURE = json.loads((ROOT / 'tests/integration/fixtures/ui-review.json').read_text())
UNITS = {'s': 1, 'm': 60, 'h': 3600, 'd': 86400}
NOW = datetime.now(timezone.utc)


def resolve(value, hosts):
    """Rewrite "@-7m" timestamps and "@same-as-host-1" references."""
    if isinstance(value, str):
        m = re.fullmatch(r'@-(\d+)([smhd])', value)
        if m:
            return (NOW - timedelta(seconds=int(m[1]) * UNITS[m[2]])).isoformat().replace('+00:00', 'Z')
        return value
    if isinstance(value, list):
        return [resolve(v, hosts) for v in value]
    if isinstance(value, dict):
        out = {}
        for k, v in value.items():
            if v == '@same-as-host-1':
                out[k] = hosts[0][k] if k in hosts[0] else hosts[0]['inventory'][k]
            else:
                out[k] = resolve(v, hosts)
        return out
    return value


def series(obj, id_, rng):
    n, step = {'live': (24, 5), '10m': (120, 5), '24h': (288, 300), '30d': (720, 3600), '1y': (365, 86400)}.get(rng, (120, 5))
    def wave(base, amp, period):
        return [max(0.0, base + amp * math.sin(i / period) + amp * 0.15 * math.sin(i * 1.7)) for i in range(n)]
    def s(key, group, label, unit, values):
        return {'key': key, 'group': group, 'label': label, 'unit': unit, 'values': values}
    gib, mib = 1024 ** 3, 1024 ** 2
    out = []
    if obj == 'host':
        out += [s(f'cpu.core{c}', 'cpu', f'Core {c}', 'percent', wave(8 + c * 3, 6, 9 + c)) for c in range(4)]
        out += [s('cpu.total', 'cpu', 'Total', 'percent', wave(14, 8, 11)),
                s('memory.used', 'memory', 'Used', 'bytes', wave(150 * gib, 12 * gib, 30)),
                s('memory.total', 'memory', 'Total', 'bytes', [256 * gib] * n),
                s('network.eno1.rx', 'network', 'eno1 receive', 'bytes_per_second', wave(42 * mib, 30 * mib, 7)),
                s('network.eno1.tx', 'network', 'eno1 transmit', 'bytes_per_second', wave(18 * mib, 12 * mib, 8)),
                s('disk.nvme0n1.read', 'disk', 'nvme0n1 read', 'bytes_per_second', wave(25 * mib, 20 * mib, 5)),
                s('disk.nvme0n1.write', 'disk', 'nvme0n1 write', 'bytes_per_second', wave(9 * mib, 8 * mib, 6)),
                s('load.1', 'load', '1 min', 'load', wave(3.2, 1.1, 10)), s('load.5', 'load', '5 min', 'load', wave(3.0, 0.6, 14)),
                s('load.15', 'load', '15 min', 'load', wave(2.8, 0.3, 20)),
                s('temperature.k10temp.tctl', 'temperature', 'Tctl', 'celsius', wave(58, 6, 12)),
                s('temperature.nvme.composite', 'temperature', 'NVMe composite', 'celsius', wave(41, 3, 15))]
    else:
        out += [s(f'cpu.vcpu{c}', 'cpu', f'vCPU {c}', 'percent', wave(10 + c * 4, 8, 9 + c)) for c in range(4)]
        out += [s('cpu.total', 'cpu', 'Total', 'percent', wave(14, 8, 11)),
                s('memory.used', 'memory', 'Used', 'bytes', wave(5.5 * gib, 0.8 * gib, 30)),
                s('network.rx', 'network', 'Receive', 'bytes_per_second', wave(4 * mib, 3 * mib, 7)),
                s('network.tx', 'network', 'Transmit', 'bytes_per_second', wave(9 * mib, 6 * mib, 8)),
                s('disk.read', 'disk', 'Read', 'bytes_per_second', wave(2 * mib, 2 * mib, 5)),
                s('disk.write', 'disk', 'Write', 'bytes_per_second', wave(1 * mib, 1 * mib, 6))]
    if id_ in ('host-3', 'vm-legacy'):  # a stale host stops producing samples
        for x in out:
            x['values'] = [v if i < n - 80 else None for i, v in enumerate(x['values'])]
    start = (NOW - timedelta(seconds=n * step)).isoformat().replace('+00:00', 'Z')
    return {'object': obj, 'id': id_, 'range': rng, 'step_seconds': step, 'start': start, 'series': out}


def host_network(host):
    br0 = next(n for n in host['inventory']['nics'] if n['name'] == 'br0')
    address = br0['addresses'][0]
    return {'host_id': host['id'], 'hostname': host['hostname'], 'connection': host['connection'], 'supported': True, 'state': 'confirmed',
            'management_network': {'revision': 3, 'stage': 'none', 'spec': {'interfaces': [{'name': 'br0', 'mac': br0['mac'], 'mode': 'static', 'address': address, 'gateway': address.rsplit('.', 1)[0] + '.1', 'management': True}], 'dns': ['192.0.2.2', '192.0.2.3']}, 'observed_at': host['inventory']['observed_at']},
            'nics': host['inventory']['nics'], 'check': {'name': 'management-network', 'status': 'healthy', 'detail': 'default route via br0'}, 'last_operation': None}


def install(page, fx):
    lists = {k: fx[k] for k in ('sites', 'pools', 'hosts', 'vms', 'storage', 'networks', 'media', 'operations', 'audit')}

    def handle(route):
        req = route.request
        if req.method != 'GET':
            return route.continue_()
        url = req.url
        path = url.split('/api/v1/', 1)[1].split('?', 1)[0]
        query = dict(p.split('=', 1) for p in url.split('?', 1)[1].split('&')) if '?' in url else {}
        def send(body):
            route.fulfill(status=200, content_type='application/json', body=json.dumps(body))
        if path in lists:
            return send(lists[path])
        if path == 'updates':
            return send(fx['updates'])
        if path == 'storage/credentials':
            return send([])
        if path == 'storage/cluster':
            return send({'status': 'succeeded', 'pool_id': query.get('pool_id'), 'observed_at': NOW.isoformat(), 'operations': []})
        m = re.fullmatch(r'hosts/([^/]+)/hardware', path)
        if m:
            return send({**fx['hardware'], 'host_id': m[1]})
        m = re.fullmatch(r'hosts/([^/]+)/network', path)
        if m:
            host = next((h for h in fx['hosts'] if h['id'] == m[1]), None)
            return send(host_network(host)) if host else route.continue_()
        m = re.fullmatch(r'(hosts|vms)/([^/]+)/stats', path)
        if m:
            return send(series('host' if m[1] == 'hosts' else 'vm', m[2], query.get('range', '10m')))
        m = re.fullmatch(r'pools/([^/]+)/members', path)
        if m:
            return send(fx['members'] if m[1] == 'pool-a' else {**fx['members'], 'pool_id': 'pool-b', 'pool_name': 'edge-node-01', 'master_host_id': 'host-4', 'generation': 5, 'members': fx['members']['members'][:1]})
        m = re.fullmatch(r'pools/([^/]+)/join-candidates', path)
        if m:
            return send(fx['join_candidates'])
        return route.continue_()

    page.route(re.compile(r'/api/v1/'), handle)


VIEWS = [  # (name, route, tab or action, viewport)
    ('workspace-overview', '/', None, 'desktop'), ('site-overview', '/sites/site-ams', None, 'desktop'),
    ('pool-general', '/pools/pool-a', None, 'desktop'), ('pool-hosts', '/pools/pool-a', 'Hosts', 'tall'),
    ('pool-virtual-machines', '/pools/pool-a', 'Virtual machines', 'desktop'), ('pool-storage', '/pools/pool-a', 'Storage', 'tall'),
    ('pool-networking', '/pools/pool-a', 'Networking', 'desktop'), ('pool-iso-library', '/pools/pool-a', 'ISO library', 'desktop'),
    ('pool-activity', '/pools/pool-a', 'Activity', 'desktop'),
    ('dialog-add-storage', '/pools/pool-a', 'Storage>Register storage', 'tall'), ('dialog-add-network', '/pools/pool-a', 'Networking>Register network', 'tall'),
    ('dialog-iso-upload', '/pools/pool-a', 'ISO library>Upload ISO', 'tall'), ('dialog-new-vm', '/pools/pool-a', '>New VM', 'tall'), ('dialog-add-host', '/pools/pool-a', '>Add host', 'tall'),
    ('host-general', '/hosts/host-1', None, 'tall'), ('host-stats', '/hosts/host-1', 'Stats', 'tall'), ('host-console', '/hosts/host-1', 'Console', 'desktop'),
    ('host-networking', '/hosts/host-1', 'Networking', 'tall'), ('host-storage', '/hosts/host-1', 'Storage', 'desktop'), ('host-activity', '/hosts/host-1', 'Activity', 'desktop'),
    ('host-stale', '/hosts/host-3', None, 'desktop'), ('host-degraded', '/hosts/host-2', None, 'tall'),
    ('vm-general', '/vms/vm-web', None, 'tall'), ('vm-stats', '/vms/vm-web', 'Stats', 'tall'), ('vm-console', '/vms/vm-web', 'Console', 'tall'),
    ('vm-activity', '/vms/vm-web', 'Activity', 'desktop'), ('vm-edit', '/vms/vm-web', '>Edit', 'tall'), ('dialog-power-confirm', '/vms/vm-web', '>Force stop', 'desktop'),
    ('vm-shut-off', '/vms/vm-build', None, 'desktop'), ('vm-stale', '/vms/vm-legacy', None, 'desktop'), ('vm-windows-console', '/vms/vm-win', 'Console', 'desktop'),
    ('task-history', '/tasks', None, 'tall'), ('updates', '/updates', None, 'desktop'), ('invalid-object', '/vms/does-not-exist', None, 'desktop'),
    ('narrow-overview', '/', None, 'narrow'), ('narrow-pool', '/pools/pool-a', None, 'narrow'), ('narrow-vm', '/vms/vm-web', None, 'narrow'),
    ('mid-host', '/hosts/host-1', None, 'mid'),
]
VIEWPORTS = {'desktop': (1440, 900), 'tall': (1440, 2000), 'mid': (1024, 768), 'narrow': (390, 844)}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--url', required=True)
    ap.add_argument('--username', default='root')
    ap.add_argument('--password-file', required=True)
    ap.add_argument('--captures', required=True)
    ap.add_argument('--views', default='all')
    args = ap.parse_args()
    out = Path(args.captures); out.mkdir(parents=True, exist_ok=True)
    fx = resolve(FIXTURE, FIXTURE['hosts'])
    password = Path(args.password_file).read_text().strip()
    with sync_playwright() as pw:
        browser = pw.chromium.launch()
        ctx = browser.new_context(ignore_https_errors=True, viewport={'width': 1440, 'height': 900})
        page = ctx.new_page()
        page.set_default_timeout(8000)
        errors = []
        page.on('pageerror', lambda e: errors.append(str(e)))
        page.goto(args.url + '/')
        page.screenshot(path=str(out / '00-login.png'))
        page.get_by_label(re.compile('username', re.I)).fill(args.username)
        page.get_by_label(re.compile('password', re.I)).fill(password)
        page.get_by_role('button', name=re.compile('sign in', re.I)).click()
        page.wait_for_timeout(1200)
        page.screenshot(path=str(out / '01-first-run.png'))
        install(page, fx)
        for n, (name, route, action, vp) in enumerate(VIEWS, start=2):
            if args.views != 'all' and not name.startswith(args.views):
                continue
            w, h = VIEWPORTS[vp]
            page.set_viewport_size({'width': w, 'height': h})
            page.goto(args.url + '/#' + route); page.reload(); page.wait_for_timeout(900)
            if action:
                tab, _, button = action.partition('>')
                if tab:
                    page.get_by_role('navigation', name='Resource views').get_by_role('button', name=tab, exact=True).click()
                if button:
                    page.get_by_role('button', name=button, exact=True).first.click()
                page.wait_for_timeout(1500 if tab == 'Stats' else 700)
            page.screenshot(path=str(out / f'{n:02d}-{name}.png'))
            print('captured', name)
        browser.close()
    if errors:
        print('page errors:'); print('\n'.join(errors))
        raise SystemExit(1)


if __name__ == '__main__':
    main()
