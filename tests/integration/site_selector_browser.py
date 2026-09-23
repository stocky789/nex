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
    update_status = {'package': 'nex-controller', 'installed_version': '1.0', 'available_version': '1.1',
                     'update_available': True, 'repo_url': 'https://updates.example.invalid'}
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        page = browser.new_page(viewport={'width': 1440, 'height': 1000})
        # The UI skips the session probe unless this browser signed in before.
        page.add_init_script("localStorage.setItem('nex-signed-in', '1')")
        errors = []
        page.on('pageerror', lambda error: errors.append(str(error)))

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
            elif path == 'updates/apply' and route.request.method == 'POST':
                update_status.update(phase='failed', error='fixture apt failure')
                route.fulfill(status=202, json={})
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
                    'members': [{'host_id': host_id, 'status': 'active', 'maintenance': False}],
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

        page.set_viewport_size({'width': 360, 'height': 800})
        page.goto(url + '/#/')
        open_menu()
        long_name = data['sites'][1]['name']
        long_button = items.get_by_role('button', name=long_name + ' · all pools', exact=True)
        long_button.scroll_into_view_if_needed()
        expect(long_button).to_be_visible()
        box = items.bounding_box()
        assert box['x'] >= 0 and box['x'] + box['width'] <= 360, ('scope menu outside the viewport', box)
        capture('narrow-long-name')
        long_button.click()
        expect(menu).not_to_have_attribute('open', '')
        selected(1, long_name)
        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), 'long selected site overflows'
        open_menu()
        items.get_by_role('button', name='Pool 079', exact=True).scroll_into_view_if_needed()
        capture('narrow-dropdown')
        items.get_by_role('button', name='Pool 079', exact=True).click()
        selected(79)
        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), 'narrow viewport overflows'
        capture('narrow-selected-site')
        passed('The scope menu, long names and selection remain reachable at 360px without horizontal page overflow')
        page.set_viewport_size({'width': 1440, 'height': 1000})

        # Simulated removal on a global page must not leave an orphaned selection.
        page.goto(url + '/#/tasks')
        data['sites'] = [s for s in data['sites'] if s['id'] != 'site-079']
        page.reload()
        selected(0)
        passed('An absent saved site falls back to the first available site on reload')

        data.clear()
        data.update(inventory(0))
        page.goto(url + '/#/')
        page.reload()
        expect(page.get_by_role('heading', name='Start with a site', exact=True)).to_be_visible()
        expect(scope).to_have_accessible_name('Scope: no site, all pools. Change scope')
        open_menu()
        expect(items.locator('button.indent')).to_have_count(0)
        expect(items.get_by_role('button', name='Add host', exact=True)).to_be_disabled()
        page.keyboard.press('Escape')
        capture('empty-sites')
        page.get_by_role('button', name='Create site', exact=True).click()
        page.get_by_label('Site name', exact=True).fill('First site')
        page.get_by_role('dialog', name='Create site').get_by_role('button', name='Create site', exact=True).click()
        expect(scope).to_have_accessible_name('Scope: First site, all pools. Change scope')
        expect(page).to_have_url(url + '/#/')
        open_menu()
        expect(items.locator('button.indent')).to_have_count(0)
        page.keyboard.press('Escape')
        capture('first-site-created')
        passed('Empty inventory offers site creation; successful fixture creation chooses the new site without unrelated pools')
        for path in ('/', '/tasks'):
            page.goto(url + '/#' + path)
            expect(scope).to_have_accessible_name('Scope: First site, all pools. Change scope')
            expect(scope).to_contain_text('0 hosts')
            page.get_by_role('navigation', name='Sections').get_by_role('link', name='Overview', exact=True).click()
            expect(page).to_have_url(url + '/#/')
            expect(page.get_by_role('heading', name='Add your first host', exact=True)).to_be_visible()
            expect(page.get_by_role('button', name='Add host', exact=True).first).to_be_enabled()
            expect(page.get_by_role('button', name='Create pool')).to_have_count(0)
        capture('only-site-overview')
        passed('The sole default site with no hosts opens Overview from the rail on workspace/tasks, shows Add your first host and exposes Add host without any Create pool action')
        # A refused Add host must stay visible in its dialog across the 10 s inventory poll.
        page.get_by_role('button', name='Add host', exact=True).first.click()
        panel = page.get_by_role('dialog', name='Add host', exact=True)
        panel.get_by_label('Host address', exact=True).fill('192.0.2.10')
        panel.get_by_label('Root password', exact=True).fill('fixture-password')
        panel.get_by_role('button', name='Connect', exact=True).click()
        panel.get_by_role('button', name='Confirm and add host', exact=True).click()
        alert = panel.get_by_role('alert')
        expect(alert).to_contain_text(re.compile('already enrolled', re.I))
        polls = []
        page.on('request', lambda request: polls.append(request.url) if request.url.endswith('/api/v1/sites') and request.method == 'GET' else None)
        page.wait_for_timeout(12000)
        assert polls, 'inventory poll must run during the wait'
        expect(alert).to_contain_text(re.compile('already enrolled', re.I))
        expect(alert).to_contain_text('host-enrolled')
        expect(page.get_by_role('button', name='Retry', exact=True)).to_have_count(0)
        capture('add-host-409-after-poll')
        passed('A 409 already enrolled from Add host stays in the Add host dialog with its host ID after an inventory poll')
        panel.get_by_role('button', name='Close', exact=True).click()
        # A failed update apply is an operator request error, so the inventory poll must not clear it.
        page.goto(url + '/#/updates')
        expect(page).to_have_url(url + '/#/settings/updates')
        page.get_by_role('button', name='Update', exact=True).click()
        failure = page.get_by_role('alert').filter(has_text='fixture apt failure')
        expect(failure).to_be_visible()
        polls.clear()
        page.wait_for_timeout(12000)
        assert polls, 'inventory poll must run during the wait'
        expect(failure).to_be_visible()
        expect(page.get_by_role('button', name='Retry', exact=True)).to_have_count(0)
        capture('update-failed-after-poll')
        passed('A failed update apply stays visible without an inventory Retry after an inventory poll')
        assert not errors, errors
        browser.close()
    assert assets == {name: hashlib.sha256((ROOT / 'web/dist' / name).read_bytes()).hexdigest()
                      for name in assets}, 'Built UI changed during browser acceptance'
    return {'environment': 'Real built React UI in Chromium; scope menu with explicit API route fixtures of 500 sites, one pool/host/VM per site. All inventory and creation responses simulated; no Controller or libvirt execution.',
            'recorded_at': datetime.now(timezone.utc).isoformat(), 'checks': checks, 'captures': captures,
            'ui_sha256': assets,
            'limits': ['Browser navigation/presentation only; no backend authorization or native-host claim', 'No deployment or hosted CI execution']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--evidence', type=Path, default=ROOT / '.cache/site-selector-browser')
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer(('127.0.0.1', 0), partial(QuietHandler, directory=str(ROOT / 'web/dist')))
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        result = acceptance(f'http://127.0.0.1:{server.server_port}', args.evidence)
        (args.evidence / 'results.json').write_text(json.dumps(result, indent=2) + '\n')
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


if __name__ == '__main__':
    main()
