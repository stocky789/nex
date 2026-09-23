#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Capture every Controller view against a running Controller with a labeled simulated inventory.

Reads are answered from tests/integration/fixtures/ui-review.json; the sign-in, session, shell
and every write go to the real Controller. Consoles therefore never connect: no host exists
behind the fixture. Each view is captured at 1440x900, 1024x768 and 390x844 in the light and
dark theme; a page error, a missing control or horizontal page scroll fails the run.
Fictional names and RFC 5737 addresses only.
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
            edge = {**fx['members']['members'][0], 'host_id': 'host-4', 'name': 'edge-node-01', 'guests': {'known': True, 'total': 1, 'running': 1, 'names': ['edge-proxy-01']}}
            return send(fx['members'] if m[1] == 'pool-a' else {**fx['members'], 'pool_id': 'pool-b', 'pool_name': 'edge-node-01', 'master_host_id': 'host-4', 'generation': 5, 'members': [edge]})
        m = re.fullmatch(r'pools/([^/]+)/join-candidates', path)
        if m:
            return send(fx['join_candidates'])
        return route.continue_()

    page.route(re.compile(r'/api/v1/'), handle)


def tab(name):
    return lambda page: page.get_by_role('tab', name=name, exact=True).click()


def click(role, name):
    return lambda page: page.get_by_role(role, name=name, exact=True).first.click()


def menu(summary_label, item=None):
    def run(page):
        page.locator(f'summary[aria-label^="{summary_label}"]').click()
        if item:
            page.locator('.menu[open]').get_by_role('button', name=item, exact=True).click()
    return run


def view(name):  # a view button's name carries its count, e.g. "Running 4"
    return lambda page: page.locator('.views').get_by_role('button', name=re.compile('^' + name)).click()


def filters(page):
    toggle = page.get_by_role('button', name='Filters', exact=True)
    if toggle.is_visible():
        toggle.click()


def search(text):  # below 900px the search field sits behind a Search button
    def run(page):
        toggle = page.get_by_role('button', name='Search', exact=True)
        if toggle.is_visible():
            toggle.click()
        page.get_by_role('combobox').fill(text)
    return run


def steps(*actions):
    def run(page):
        for action in actions:
            action(page)
    return run


def check_rows(*names):
    return lambda page: [page.get_by_label(f'Select {n}', exact=True).check() for n in names]


def check(test, message):  # a failed expectation is a finding, like a missing control
    def run(page):
        if not test(page):
            raise AssertionError(message)
    return run


def hash_is(route):
    return check(lambda page: page.evaluate('location.hash') == '#' + route, f'did not land on #{route}')


def headers(*names):
    return check(lambda page: all(page.get_by_role('columnheader', name=n, exact=True).count() for n in names),
                 f'missing column among {", ".join(names)}')


def texts(locator):
    return [t.strip() for t in locator.all_inner_texts()]


def open_about(title):
    return lambda page: page.locator('summary', has_text=title).first.click()


def collapse(title):
    return lambda page: page.get_by_role('button', name=re.compile(f'^Collapse {title}$')).click()


def reload_keeping_tiles(page):  # the init script clears collapsed tiles unless this flag is set
    page.evaluate("sessionStorage.setItem('nex.review.keep', '1')")
    page.reload(); page.wait_for_timeout(900)
    page.evaluate("sessionStorage.removeItem('nex.review.keep')")


def chart_hover(page):
    plot = page.locator('.ov-plot')
    plot.hover()
    if not page.locator('.ov-tip').is_visible():
        raise AssertionError('chart hover shows no reading')


def chart_keys(range_name):
    def run(page):
        page.get_by_role('button', name=range_name, exact=True).click()
        page.wait_for_timeout(800)
        page.locator('.ov-plot').focus()
        page.keyboard.press('ArrowLeft')
        tip = page.locator('.ov-tip')
        if not tip.is_visible() or 'Average of' not in tip.inner_text():
            raise AssertionError(f'{range_name}: Left arrow shows no averaged reading')
        page.keyboard.press('Escape')
        if tip.count():
            raise AssertionError('Escape leaves the chart reading open')
        page.keyboard.press('ArrowRight')
    return run


RAIL = ['Overview', 'VMs', 'Containers', 'Hosts', 'Storage', 'Networks', 'Backups', 'Tasks', 'Events', 'Settings']
SETTINGS_NAV = ['Overview', 'Members', 'High availability', 'Backup repositories', 'Updates']


VM_TABS = ['Overview', 'Performance', 'Snapshots', 'Configuration', 'Activity']
HOST_TABS = ['Overview', 'Hardware', 'Performance', 'Networking', 'Storage', 'Console', 'Activity']
SECTIONS = ['overview', 'members', 'ha', 'backups', 'updates']
VIEWS = [  # (name, route, action)
    ('overview', '/', None), ('overview-site-berlin', '/sites/site-ber', None),
    ('overview-collapsed', '/', steps(collapse('.* utilisation'), collapse('Needs attention'), collapse('Placement'),
                                      check(lambda page: page.locator('.ov-tile.collapsed .ov-summary').count() == 3, 'collapsed tiles show no summary line'),
                                      reload_keeping_tiles,
                                      check(lambda page: page.locator('.ov-tile.collapsed').count() == 3, 'collapsed tiles not remembered after reload'))),
    ('overview-chart-hover', '/', chart_hover),
    ('overview-chart-week', '/', chart_keys('1W')),
    ('rail', '/containers', steps(check(lambda page: texts(page.locator('nav.rail a')) == RAIL, 'rail order is not ' + ', '.join(RAIL)),
                                  check(lambda page: page.locator('nav.rail a.rail-sub').count() == 1 and page.locator('nav.rail a.rail-sub').inner_text().strip() == 'Containers',
                                        'Containers is not the rail sub-item'))),
    ('search-rail-page', '/', steps(search('netw'), check(lambda page: page.get_by_role('option', name=re.compile('^Networks')).count() > 0, 'search does not offer the Networks page'))),
    ('scope-menu', '/', menu('Scope')), ('search-results', '/', search('lab')),
    ('account-menu', '/', menu('Account')), ('dialog-add-host', '/', menu('Scope', 'Add host')),
    ('dialog-create-site', '/', menu('Scope', 'Create site')),
    ('vms', '/vms', None),
    ('vms-filtered', '/vms', steps(filters, lambda page: page.locator('.facet', has_text='Tags').get_by_label('production').check(),
                                   view('Running'))),
    ('vms-grouped', '/vms', steps(filters, lambda page: page.get_by_role('switch', name='Group by host').check())),
    ('vms-needs-attention', '/vms', view('Needs attention')),
    ('vms-bulk-bar', '/vms', check_rows('web-01', 'build-runner-01')),
    ('vms-bulk-dialog', '/vms', steps(check_rows('web-01', 'build-runner-01'), lambda page: page.locator('.bulk').get_by_role('button', name='Shut down').click())),
    ('create-vm-wizard', '/vms', click('button', 'Create VM')),
    *[(f'vm-{t.lower()}', '/vms/vm-web', tab(t)) for t in VM_TABS],
    ('vm-more-menu', '/vms/vm-web', lambda page: page.locator('.vm-more summary').click()),
    ('vm-force-stop-confirm', '/vms/vm-web', steps(lambda page: page.locator('.vm-more summary').click(), click('button', 'Force stop'))),
    ('vm-edit-hardware', '/vms/vm-web', steps(tab('Configuration'), click('button', 'Edit hardware'))),
    ('vm-delete-confirm', '/vms/vm-build', steps(tab('Configuration'), click('button', 'Delete VM'))),
    ('vm-shut-off', '/vms/vm-build', None), ('vm-stale', '/vms/vm-legacy', None),
    ('vm-guest-tools', '/vms/vm-win', steps(
        check(lambda page: texts(page.locator('.guest-caps summary > span:first-child')) == ['Guest agent', 'Clipboard', 'Display resize'], 'guest tools grid rows'),
        check(lambda page: page.locator('.guest-caps li', has_text='Clipboard').locator('.badge').inner_text().strip() == 'Not available', 'no-channel clipboard is not Not available'),
        lambda page: page.locator('.guest-caps summary', has_text='Clipboard').click(),
        check(lambda page: page.locator('.guest-caps li', has_text='Clipboard').get_by_text('Channel missing', exact=False).is_visible(), 'Why? does not explain the missing channel'),
        open_about('How to enable guest tools'))),
    ('vm-guest-tools-ready', '/vms/vm-web', check(lambda page: texts(page.locator('.guest-caps .badge')) == ['Ready', 'Ready', 'Ready'], 'a connected guest with ready clipboard is not Ready, Ready, Ready')),
    ('vm-stale-configuration', '/vms/vm-legacy', tab('Configuration')),
    ('hosts', '/hosts', None),
    *[(f'host-{t.lower()}', '/hosts/host-1', tab(t)) for t in HOST_TABS],
    *[(f'host-stale-{t.lower()}', '/hosts/host-3', tab(t)) for t in HOST_TABS],
    ('host-degraded', '/hosts/host-2', None),
    ('dialog-revoke', '/hosts/host-1', click('button', 'Revoke access')),
    *[(f'settings-{s}', f'/settings/{s}', None) for s in SECTIONS],
    ('settings-nav', '/settings/members', check(lambda page: texts(page.locator('nav.snav a')) == SETTINGS_NAV, 'settings nav is not ' + ', '.join(SETTINGS_NAV))),
    ('settings-members-details', '/settings/members', steps(
        check(lambda page: texts(page.locator('.pool-member').first.locator('.pool-member-more > summary .pool-check')) and page.locator('.pool-member-more > summary', has_text='Connection').count() > 0, 'member rows have no status checks'),
        lambda page: page.locator('.pool-member-more > summary').first.click(),
        check(lambda page: page.locator('.pool-member-detail').first.is_visible(), 'Details does not open the row'),
        lambda page: page.locator('.pool-status > summary').click())),
    ('settings-members-actions', '/settings/members', steps(
        lambda page: page.locator('.pool-row-actions summary').first.click(),
        check(lambda page: page.locator('.pool-row-actions[open]').count() == 1, 'Actions menu does not open'),
        lambda page: page.keyboard.press('Escape'),
        check(lambda page: page.locator('.pool-row-actions[open]').count() == 0, 'Escape leaves the Actions menu open'),
        lambda page: page.locator('.pool-row-actions summary').first.click())),
    *[(f'redirect-settings-{old}', f'/settings/{old}', hash_is(new)) for old, new in (('storage', '/storage'), ('iso', '/storage'), ('networks', '/networks'))],
    ('storage', '/storage', steps(headers('Name', 'Type', 'Scope', 'Capacity', 'Health', 'Host access', 'Last check', 'Manage'),
                                  # the stale host's light is the hollow Unknown one on shared storage, never Accessible
                                  check(lambda page: page.locator('tr', has_text='nfs-shared').locator('a[aria-label^="lab-node-03: Unknown"] .dot.unknown').count() == 1, 'stale host is not Unknown on nfs-shared'),
                                  check(lambda page: page.locator('tr', has_text='tank').get_by_text('Degraded').count() > 0, 'degraded ZFS repository not shown Degraded'))),
    ('storage-about', '/storage', open_about('About storage')),
    ('dialog-register-storage', '/storage', click('button', 'Register storage')),
    ('networks', '/networks', headers('Name', 'VLAN', 'Uplink', 'Host access', 'VMs', 'Status', 'Actions')),
    ('networks-about', '/networks', open_about('About changing networks')),
    ('networks-vm-filter', '/networks', steps(lambda page: page.locator('tr', has_text='guest-vlan20').get_by_role('link', name='5', exact=True).click(), lambda page: page.wait_for_timeout(600),
                                              check(lambda page: page.evaluate('location.hash') == '#/vms?network=guest-vlan20', 'VM count does not link to the network filter'),
                                              check(lambda page: page.locator('tbody tr').count() == 5, 'network filter does not show the five VMs on guest-vlan20'))),
    ('dialog-register-network', '/networks', click('button', 'Register network')),
    ('iso-library', '/storage', steps(lambda page: page.get_by_role('heading', name='ISO library').scroll_into_view_if_needed(), open_about('About removing ISOs'))),
    ('dialog-upload-iso', '/storage', click('button', 'Upload ISO')),
    ('backups', '/backups', None), ('containers', '/containers', None),
    ('tasks', '/tasks', None), ('events', '/events', None),
    ('console-vm', '/console/vm-web', None), ('console-host', '/host-console/host-1', None),
    ('invalid-object', '/vms/does-not-exist', None),
]
VIEWPORTS = {'desktop': (1440, 900), 'mid': (1024, 768), 'narrow': (390, 844)}
TALL = {'overview', 'settings-overview', 'host-overview', 'vm-overview', 'vm-configuration', 'storage', 'settings-members-details'}  # also captured 1440x2000, light


def overflow(page):
    """Horizontal scroll of the document or the page pane; inner table scrollers are fine."""
    return page.evaluate("""() => {
        const doc = document.documentElement.scrollWidth - window.innerWidth;
        const pane = document.querySelector('.page');
        return Math.max(doc, pane ? pane.scrollWidth - pane.clientWidth : 0);
    }""")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--url', required=True)
    ap.add_argument('--username', default='root')
    ap.add_argument('--password-file', required=True)
    ap.add_argument('--captures', required=True)
    ap.add_argument('--views', default='all', help='capture only views whose name starts with this')
    args = ap.parse_args()
    out = Path(args.captures); out.mkdir(parents=True, exist_ok=True)
    fx = resolve(FIXTURE, FIXTURE['hosts'])
    password = Path(args.password_file).read_text().strip()
    failures = []
    with sync_playwright() as pw:
        browser = pw.chromium.launch()
        ctx = browser.new_context(ignore_https_errors=True, viewport={'width': 1440, 'height': 900})
        page = ctx.new_page()
        page.set_default_timeout(8000)
        current = ['sign-in']
        page.on('pageerror', lambda e: failures.append(f'{current[0]}: page error: {e}'))
        page.goto(args.url + '/')
        page.screenshot(path=str(out / '00-login.png'))
        page.get_by_label(re.compile('username', re.I)).fill(args.username)
        page.get_by_label(re.compile('password', re.I)).fill(password)
        page.get_by_role('button', name=re.compile('sign in', re.I)).click()
        page.wait_for_timeout(1200)
        page.screenshot(path=str(out / '01-first-run.png'))  # the real, empty Controller
        install(page, fx)
        # Each view starts from the default scope; a /sites/ route would otherwise leave its scope behind.
        # Collapsed Overview tiles are cleared per view too, except across the reload that checks them.
        page.add_init_script("localStorage.removeItem('nex.scope'); if (!sessionStorage.getItem('nex.review.keep')) localStorage.removeItem('nex.overview.collapsed')")
        runs = [(vp, theme) for theme in ('light', 'dark') for vp in VIEWPORTS]
        for n, (name, route, action) in enumerate(VIEWS, start=2):
            if args.views != 'all' and not name.startswith(args.views):
                continue
            for vp, theme in runs + ([('tall', 'light')] if name in TALL else []):
                current[0] = f'{name} {vp} {theme}'
                w, h = VIEWPORTS.get(vp, (1440, 2000))
                page.set_viewport_size({'width': w, 'height': h})
                page.emulate_media(color_scheme=theme)
                page.goto(args.url + '/#' + route); page.reload(); page.wait_for_timeout(900)
                try:
                    if action:
                        action(page)
                        page.wait_for_timeout(1500 if 'performance' in name else 600)
                except Exception as e:  # a missing control is a finding, not a crash
                    failures.append(f'{current[0]}: action failed: {str(e).splitlines()[0]}')
                if (extra := overflow(page)) > 0:
                    failures.append(f'{current[0]}: horizontal page scroll of {extra}px')
                page.screenshot(path=str(out / f'{n:02d}-{name}-{vp}-{theme}.png'))
            print('captured', name)
        browser.close()
    if failures:
        print('failures:'); print('\n'.join(failures))
        raise SystemExit(1)


if __name__ == '__main__':
    main()
