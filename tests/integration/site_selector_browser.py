#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Browser regression for the scope menu (site/pool navigation) using explicit API route fixtures.

Serves the real built Controller UI locally; 500 sites, hosts and VMs are simulated
JSON responses. No Controller, host, disk, network configuration or VM is mutated.
Requires the existing Python Playwright/Chromium installation and web/dist build.
"""
import argparse
from datetime import datetime, timezone
from functools import partial
import hashlib
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import json
import re
from pathlib import Path
import threading

from phase1 import ROOT


class QuietHandler(SimpleHTTPRequestHandler):
    def log_message(self, *_):
        pass


def inventory(count):
    sites = [{'id': f'site-{i:03}', 'name': f'Site {i:03}'} for i in range(count)]
    if count > 1:
        sites[1]['name'] = 'Site 001 — Long regional management location with a descriptive operator-visible site name'
    pools = [{'id': f'pool-{i:03}', 'site_id': s['id'], 'name': f'Pool {i:03}', 'master_host_id': f'host-{i:03}'} for i, s in enumerate(sites)]
    hosts = [{'id': f'host-{i:03}', 'pool_id': p['id'], 'hostname': f'host-{i:03}',
              'revoked': False, 'connection': 'fresh', 'last_seen': None, 'inventory': None}
             for i, p in enumerate(pools)]
    vms = [{'id': f'vm-{i:03}', 'pool_id': p['id'], 'host_id': f'host-{i:03}',
            'name': f'Guest {i:03}', 'role': 'guest', 'protected': False,
            'state': 'shut off', 'freshness': 'fresh'} for i, p in enumerate(pools)]
    return {'sites': sites, 'pools': pools, 'hosts': hosts, 'vms': vms,
            'storage': [], 'networks': [], 'media': [], 'operations': [], 'audit': []}


def acceptance(url, evidence):
    from playwright.sync_api import sync_playwright, expect
    checks, captures = [], []
    assets = {name: hashlib.sha256((ROOT / 'web/dist' / name).read_bytes()).hexdigest()
              for name in ('app.js', 'app.css')}
    data = inventory(500)
    data['hosts'][0]['inventory'] = {'hostname': 'host-000', 'cpu_model': 'Example CPU', 'cpu_count': 8,
        'memory_bytes': 8 * 1024 ** 3, 'virtualization': ['kvm'], 'os_version': 'Nex Core 0.4.13', 'agent_version': '0.4.13',
        'disks': [], 'checks': [], 'nics': [
        {'name': 'lo', 'addresses': ['127.0.0.1/8', '::1/128']},
        {'name': 'eno1', 'kind': 'physical', 'mac': '02:00:5e:00:53:01', 'addresses': [], 'state': 'up'},
        {'name': 'br0', 'addresses': ['fe80::1/64', '192.0.2.10/24']},
    ]}
    update_status = {'package': 'nex-controller', 'installed_version': '1.0', 'available_version': '1.1',
                     'update_available': True, 'repo_url': 'https://updates.example.invalid'}
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        page = browser.new_page(viewport={'width': 1440, 'height': 1000})
        # The UI skips the session probe unless this browser signed in before.
        page.add_init_script("localStorage.setItem('nex-signed-in', '1')")
        errors = []
        page.on('pageerror', lambda error: errors.append(str(error)))

        join_candidates = []
        def route_api(route):
            path = route.request.url.split('/api/v1/', 1)[1].split('?', 1)[0]
            if path == 'session':
                route.fulfill(json={'username': 'qa-admin', 'csrf_token': 'explicit-route-fixture'})
            elif path == 'sites' and route.request.method == 'POST':
                site = {'id': 'site-created', 'name': route.request.post_data_json['name']}
                data['sites'].append(site)
                route.fulfill(status=201, json=site)
            elif path == 'hosts/probe' and route.request.method == 'POST':
                route.fulfill(json={'address': route.request.post_data_json['address'], 'fingerprint': 'SHA256:simulated-fixture',
                                    'subject': 'CN=simulated-host', 'not_after': '2030-01-01T00:00:00Z'})
            elif path == 'sites/site-created/hosts' and route.request.method == 'POST':
                route.fulfill(status=409, json={'error': 'already enrolled', 'host_id': 'host-enrolled'})
            elif path == 'updates' and route.request.method == 'GET':
                route.fulfill(json=update_status)
            elif path == 'storage/cluster' and route.request.method == 'GET':
                route.fulfill(json={'status': 'succeeded', 'pool_id': 'pool-000', 'operations': []})
            elif path == 'storage/credentials' and route.request.method == 'GET':
                route.fulfill(json=[])
            elif path == 'updates/apply' and route.request.method == 'POST':
                update_status.update(phase='failed', error='fixture apt failure')
                route.fulfill(status=202, json={})
            elif route.request.method == 'GET' and path.startswith('pools/') and path.endswith('/join-candidates'):
                route.fulfill(json={'candidates': list(join_candidates)})
            elif route.request.method == 'GET' and path.startswith('pools/') and path.endswith('/members'):
                # Host pages load the pool membership report for the restricted-host banner.
                pool_id = path.split('/', 2)[1]
                pool = next((row for row in data.get('pools', []) if row['id'] == pool_id), None)
                host_id = (pool or {}).get('master_host_id') or f'host-{pool_id.removeprefix("pool-")}'
                route.fulfill(json={
                    'pool_id': pool_id,
                    'pool_name': (pool or {}).get('name') or pool_id,
                    'master_host_id': host_id,
                    'master_connection': 'fresh',
                    'generation': 1,
                    'authoritative': True,
                    'members': [{'host_id': host_id, 'name': host_id, 'role': 'master',
                                 'connection': 'fresh', 'status': 'active', 'maintenance': False}],
                })
            elif route.request.method == 'GET' and path.endswith('/stats'):
                # Lists and Overview read each online host's usage history; the fixture has none.
                route.fulfill(status=404, json={'error': 'no usage history in this fixture'})
            elif route.request.method == 'GET' and path in data:
                route.fulfill(json=data[path])
            else:
                raise AssertionError(f'Unexpected API fixture request: {route.request.method} {path}')

        page.route('**/api/v1/**', route_api)
        scope = page.locator('summary[aria-label^="Scope: "]')
        menu = page.locator('details.scope')
        items = menu.locator('.menu-items')

        def capture(name):
            name += '.png'
            page.screenshot(path=str(evidence / name), full_page=page.viewport_size['width'] < 700)
            captures.append(name)

        def passed(message):
            checks.append(message)
            print('PASS:', message, flush=True)

        def selected(number, site_name=None):
            # A site with one pool is that pool, so the scope names both and counts its one host.
            suffix = f'{number:03}'
            expect(scope).to_have_accessible_name(f'Scope: {site_name or "Site " + suffix}, Pool {suffix}. Change scope')
            expect(scope).to_contain_text('1 host')

        def listed(number):
            # The lists show only the chosen site's pool: its master host and its VM.
            suffix = f'{number:03}'
            page.goto(url + '/#/hosts')
            rows = page.locator('table.list tbody tr')
            expect(rows).to_have_count(1)
            expect(rows.get_by_role('link', name=f'host-{suffix}', exact=True)).to_be_visible()
            expect(rows).to_contain_text('Master')
            page.goto(url + '/#/vms')
            rows = page.locator('table.list tbody tr[data-vm]')
            expect(rows).to_have_count(1)
            expect(rows.get_by_role('link', name=f'Guest {suffix}', exact=True)).to_be_visible()

        def open_menu():
            scope.click()
            expect(menu).to_have_attribute('open', '')

        def pick(name):
            open_menu()
            items.get_by_role('button', name=name, exact=True).click()
            expect(menu).not_to_have_attribute('open', '')

        page.goto(url)
        selected(0)
        expect(menu).not_to_have_attribute('open', '')
        listed(0)
        capture('desktop-500-sites')
        page.goto(url + '/#/hosts')
        expect(page.locator('table.list tbody tr')).to_contain_text('192.0.2.10')
        expect(page.locator('table.list tbody tr')).not_to_contain_text('127.0.0.1')
        page.get_by_role('link', name='host-000', exact=True).click()
        expect(page.get_by_role('region', name='host-000')).to_contain_text('192.0.2.10')
        capture('host-management-ip')
        passed('Hosts list and panel show the reported management IPv4 address, ignoring loopback and IPv6 link-local addresses')
        data['hosts'][0]['inventory']['managed'] = {'iso_downloads': [{'operation_id': 'iso-task', 'received_bytes': 50_000_000, 'total_bytes': 100_000_000}]}
        data['media'].append({'id': 'iso-1', 'pool_id': 'pool-000', 'host_id': 'host-000', 'storage_id': 'storage-1',
                              'name': 'guest.iso', 'operation_id': 'iso-task', 'status': 'dispatched', 'size_bytes': 0, 'sha256': ''})
        data['operations'].append({'id': 'iso-task', 'host_id': 'host-000', 'kind': 'iso-import', 'request_id': 'iso-request',
                                   'status': 'dispatched', 'actor': 'operator', 'created_at': '2026-09-25T00:00:00Z', 'updated_at': '2026-09-25T00:00:00Z'})
        page.goto(url + '/#/storage')
        page.reload()
        expect(page.get_by_role('progressbar', name='Download progress for guest.iso')).to_have_attribute('value', '50')
        capture('iso-url-download-progress')
        page.goto(url + '/#/tasks')
        expect(page.get_by_role('progressbar', name='Download progress for guest.iso')).to_have_attribute('value', '50')
        capture('iso-url-download-task')
        data['hosts'][0]['inventory']['managed']['iso_downloads'][0]['total_bytes'] = 0
        page.reload()
        expect(page.get_by_role('progressbar', name='Download progress for guest.iso')).not_to_have_attribute('value', re.compile('.+'))
        expect(page.get_by_text('size unknown')).to_be_visible()
        data['hosts'][0]['connection'] = 'stale'
        page.reload()
        expect(page.get_by_role('progressbar', name='Download progress for guest.iso')).to_have_count(0)
        passed('ISO URL download shows known and unknown size progress in Storage and Tasks, but no live meter for a stale host')
        data['hosts'][0]['connection'] = 'fresh'
        data['hosts'][0]['inventory'].pop('managed')
        data['media'].clear()
        data['operations'].clear()
        page.goto(url + '/#/vms')
        page.reload()
        open_menu()
        expect(items.get_by_role('button', name=re.compile(r' · all pools$'))).to_have_count(500)
        expect(items.locator('button.indent')).to_have_count(500)
        assert items.evaluate('(e) => e.scrollHeight > e.clientHeight && e.clientHeight <= innerHeight * 0.7 + 1'), '500-site menu must scroll within a bounded height'
        capture('dropdown-500-sites')
        # Keyboard: the menu items are buttons in tab order; Escape closes and returns focus.
        page.keyboard.press('Tab')
        expect(items.get_by_role('button').first).to_be_focused()
        page.keyboard.press('Escape')
        expect(menu).not_to_have_attribute('open', '')
        expect(scope).to_be_focused()
        selected(0)
        scope.press('Enter')
        expect(menu).to_have_attribute('open', '')
        page.keyboard.press('Tab')
        page.keyboard.press('Tab')
        focused = page.evaluate('document.activeElement.textContent')
        assert focused == 'Pool 000', focused
        page.keyboard.press('Tab')
        page.keyboard.press('Enter')
        selected(1, data['sites'][1]['name'])
        expect(menu).not_to_have_attribute('open', '')
        passed('One closed scope control opens a menu listing all 500 sites and their pools, scrolling within a bounded height; Tab/Enter choose a site and Escape closes with focus back on the control')

        open_menu()
        outside = page.get_by_role('combobox', name='Search VMs, hosts, IP addresses and settings')
        outside.click()
        expect(menu).not_to_have_attribute('open', '')
        expect(outside).to_be_focused()
        passed('Outside click dismisses the menu without trapping or stealing keyboard focus')
        pick('Site 499 · all pools')
        selected(499)
        open_menu()
        expect(items.get_by_role('button', name='Pool 499', exact=True)).to_have_attribute('aria-current', 'true')
        items.get_by_role('button', name='Pool 499', exact=True).scroll_into_view_if_needed()
        capture('last-site-reopened')
        page.keyboard.press('Escape')
        pick('Pool 123')
        selected(123)
        listed(123)
        vm_search = page.get_by_role('searchbox', name='Search name, IP address or tag', exact=True)
        vm_search.fill('Guest 123')
        expect(page.locator('table.list tbody tr[data-vm]')).to_have_count(1)
        vm_search.fill('Guest 000')
        expect(page.get_by_text('No VMs match this view and these filters.', exact=True)).to_be_visible()
        expect(page.locator('table.list tbody tr[data-vm]')).to_have_count(0)
        vm_search.fill('')
        page.goto(url + '/#/tasks')
        selected(123)
        expect(page.get_by_role('heading', name='Tasks', level=1)).to_be_attached()
        page.reload()
        selected(123)
        passed('Explicit scope changes filter the host and VM lists and VM search to that site; global task navigation and reload preserve the chosen scope')

        page.goto(url + '/#/pools/pool-222')
        selected(222)
        expect(page).to_have_url(url + '/#/')
        page.goto(url + '/#/sites/site-321')
        selected(321)
        page.reload()
        selected(321)
        # VM and host links open their panel over the list without changing the chosen scope.
        for path, name in [('hosts/host-333', 'host-333'), ('vms/vm-444', 'Guest 444')]:
            page.goto(url + '/#/' + path)
            expect(page.get_by_role('region', name=name, exact=True)).to_be_visible()
            selected(321)
        page.evaluate("location.hash = '/pools/pool-055'")
        selected(55)
        capture('deep-link')
        passed('Site and pool deep links choose their scope on navigation/reload and in-page route change; host and VM links open their panel without changing scope')
        # Add host to pool only joins a host that has enrolled. Operators took it for the place
        # to enter a fresh host's address, so an empty dialog must say what it is for, offer no
        # address field, and lead to the site's Add host form.
        page.goto(url + '/#/settings/members')
        selected(55)
        members = page.get_by_role('region', name='Members', exact=True)
        members.get_by_role('button', name='Add host to pool', exact=True).first.click()
        dialog = page.get_by_role('dialog', name='Add host to pool', exact=True)
        enroll = dialog.get_by_role('button', name='Enroll a new host…', exact=True)
        expect(enroll).to_be_visible()  # the candidates answer has arrived: this is the empty state, not loading
        expect(dialog).to_contain_text('This pool grows by joining a host already enrolled in this site.')
        expect(dialog).to_contain_text('There is no other enrolled host at this site yet.')
        expect(dialog.get_by_label('Address', exact=True)).to_have_count(0)
        expect(dialog.get_by_role('button', name='Add host to pool', exact=True)).to_have_count(0)
        capture('join-dialog-no-candidates')
        enroll.click()
        expect(dialog).to_have_count(0)
        add_host = page.get_by_role('dialog', name='Add host', exact=True)
        expect(add_host.get_by_role('heading', name='Add host to Site 055', exact=True)).to_be_visible()
        capture('join-dialog-to-add-host')
        passed('An empty Add host to pool dialog states the model, shows no address field or confirm button, and Enroll a new host opens the site Add host form')
        add_host.get_by_role('button', name='Close', exact=True).click()
        join_candidates.append({'host_id': 'host-056', 'name': 'host-056', 'connection': 'fresh', 'eligible': True,
                                'peer_address': '192.0.2.56', 'peer_port': 9444,
                                'rows': [{'check': 'connection', 'subject': 'host-056', 'result': 'compatible', 'detail': 'reporting now'}]})
        members.get_by_role('button', name='Add host to pool', exact=True).first.click()
        candidate = dialog.get_by_role('radio', name=re.compile('host-056'))
        expect(candidate).to_be_visible()
        expect(dialog.get_by_label('Address', exact=True)).to_have_count(0)
        candidate.check()
        # The host reports its own address, so the operator normally types nothing; the
        # scheme and port are never shown, and a pasted origin is refused rather than guessed at.
        address = dialog.get_by_label('Address', exact=True)
        expect(address).to_have_value('192.0.2.56')
        confirm = dialog.get_by_role('button', name='Add host to pool', exact=True)
        expect(confirm).to_be_enabled()
        capture('join-dialog-host-selected')
        address.fill('https://192.0.2.56:9444')
        confirm.click()
        expect(dialog.get_by_role('alert')).to_contain_text('Enter the address only')
        address.fill('')
        expect(confirm).to_be_disabled()
        dialog.get_by_role('button', name='Cancel', exact=True).click()
        expect(dialog).to_have_count(0)
        passed('Choosing a host prefills its reported address and enables the confirm button; a pasted origin is refused and an empty address disables it')

        # Enrolling a member first creates its own pool. The original implicit selection must
        # survive even when the new pool sorts first in the next inventory report.
        page.evaluate("localStorage.removeItem('nex.scope')")
        page.goto(url + '/#/settings/overview')
        page.reload()
        selected(0)
        data['pools'].insert(0, {'id': 'pool-new', 'site_id': 'site-000', 'name': 'A Pool', 'master_host_id': 'host-new'})
        data['hosts'].append({'id': 'host-new', 'pool_id': 'pool-new', 'hostname': 'host-new',
                              'revoked': False, 'connection': 'fresh', 'last_seen': None, 'inventory': None})
        page.evaluate("document.dispatchEvent(new Event('visibilitychange'))")
        open_menu()
        expect(items.get_by_role('button', name='A Pool', exact=True)).to_be_visible()
        page.keyboard.press('Escape')
        selected(0)
        expect(page.get_by_role('region', name='Members', exact=True)).to_be_visible()
        capture('first-pool-after-enrollment')
        pick('Site 000 · all pools')
        expect(scope).to_have_accessible_name('Scope: Site 000, all pools. Change scope')
        expect(scope).to_contain_text('2 hosts')
        page.reload()
        expect(scope).to_have_accessible_name('Scope: Site 000, all pools. Change scope')
        pick('A Pool')
        expect(scope).to_have_accessible_name('Scope: Site 000, A Pool. Change scope')
        data['pools'][:] = [pool for pool in data['pools'] if pool['id'] != 'pool-new']
        data['hosts'][:] = [host for host in data['hosts'] if host['id'] != 'host-new']
        page.evaluate("document.dispatchEvent(new Event('visibilitychange'))")
        open_menu()
        expect(items.get_by_role('button', name='A Pool', exact=True)).to_have_count(0)
        page.keyboard.press('Escape')
        selected(0)
        passed('Enrollment retains the original pool when a new pool sorts first, explicit All pools remains available, and a removed pool falls back to the first pool')
        assert not errors, f'Browser page errors: {errors}'


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--evidence', type=Path, default=ROOT / '.cache/site-selector-browser')
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer(('127.0.0.1', 0), partial(QuietHandler, directory=str(ROOT / 'web/dist')))
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        acceptance(f'http://127.0.0.1:{server.server_port}', args.evidence)
    finally:
        server.shutdown()
        server.server_close()
