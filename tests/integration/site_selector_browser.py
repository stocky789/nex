#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Browser regression for site-scoped navigation using explicit API route fixtures.

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
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        page = browser.new_page(viewport={'width': 1440, 'height': 1000})
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
            elif route.request.method == 'GET' and path in data:
                route.fulfill(json=data[path])
            else:
                raise AssertionError(f'Unexpected API fixture request: {route.request.method} {path}')

        page.route('**/api/v1/**', route_api)
        selector = page.get_by_role('button', name=re.compile(r'^Site: '))
        search = page.get_by_role('combobox', name='Search sites', exact=True)
        options = page.get_by_role('listbox', name='Sites', exact=True)
        tree = page.get_by_role('navigation', name='Infrastructure', exact=True)

        def capture(name):
            name += '.png'
            page.screenshot(path=str(evidence / name), full_page=page.viewport_size['width'] < 700)
            captures.append(name)

        def passed(message):
            checks.append(message)
            print('PASS:', message, flush=True)

        def selected(number):
            suffix = f'{number:03}'
            expect(selector).to_have_accessible_name('Site: Site ' + suffix)
            for kind in ('pools', 'hosts', 'vms'):
                singular = {'pools': 'pool', 'hosts': 'host', 'vms': 'vm'}[kind]
                expect(tree.locator(f'a[href="#/{kind}/{singular}-{suffix}"]')).to_have_count(1)
                expect(tree.locator(f'a[href^="#/{kind}/"]')).to_have_count(1)
            expect(tree.locator(f'a[href="#/hosts/host-{suffix}"]')).to_contain_text(f'host-{suffix} · master')
            expect(tree.get_by_role('link', name=f'Site {suffix}', exact=True)).to_have_count(0)

        def open_picker():
            selector.click()
            expect(search).to_be_focused()
            expect(selector).to_have_attribute('aria-expanded', 'true')

        def pick(name):
            open_picker()
            search.fill(name)
            options.get_by_role('option', name=name, exact=True).click()
            expect(search).to_have_count(0)
            expect(selector).to_be_focused()

        page.goto(url)
        selected(0)
        expect(search).to_have_count(0)
        expect(selector).to_have_attribute('aria-expanded', 'false')
        capture('desktop-500-sites')
        open_picker()
        expect(options.get_by_role('option')).to_have_count(500)
        assert options.evaluate('(e) => e.scrollHeight > e.clientHeight && e.clientHeight <= 360'), '500-site popup must scroll within a bounded height'
        capture('dropdown-500-sites')
        search.fill('sItE 49')
        expect(options.get_by_role('option')).to_have_count(10)
        selected(0)
        capture('search-matches')
        search.fill('no such site')
        expect(page.get_by_text('No matching sites.', exact=True)).to_be_visible()
        expect(options.get_by_role('option')).to_have_count(0)
        page.keyboard.press('Enter')
        selected(0)
        capture('search-no-matches')
        search.fill('')
        expect(options.get_by_role('option')).to_have_count(500)
        page.keyboard.press('Escape')
        expect(search).to_have_count(0)
        expect(selector).to_be_focused()
        selected(0)
        open_picker()
        search.fill('Site 49')
        page.keyboard.press('ArrowDown')
        active_down = search.get_attribute('aria-activedescendant')
        assert active_down, 'ArrowDown must identify its active option'
        page.keyboard.press('ArrowUp')
        active_up = search.get_attribute('aria-activedescendant')
        assert active_up and active_up != active_down, 'ArrowUp must move the active option'
        active_name = page.locator('[id="' + active_up + '"]').inner_text().strip()
        page.keyboard.press('Enter')
        selected(int(active_name.removeprefix('Site ')))
        expect(search).to_have_count(0)
        expect(selector).to_be_focused()
        passed('One closed control opens focused popup search; 500 options scroll within bounds; search/clear/no-match and arrows/Enter/Escape preserve correct selection and focus')

        open_picker()
        outside = page.get_by_role('button', name='Refresh view', exact=True)
        outside.click()
        expect(search).to_have_count(0)
        expect(outside).to_be_focused()
        open_picker()
        page.keyboard.press('Tab')
        expect(search).to_have_count(0)
        passed('Outside click and Tab dismiss the popup without trapping or stealing keyboard focus')
        pick('Site 499')
        open_picker()
        expect(options.get_by_role('option')).to_have_count(500)
        expect(search).to_be_focused()
        active_id = search.get_attribute('aria-activedescendant')
        active_box = page.locator('[id="' + active_id + '"]').bounding_box()
        list_box = options.bounding_box()
        assert active_box['y'] >= list_box['y'] and active_box['y'] + active_box['height'] <= list_box['y'] + list_box['height'] + 1, 'Last active site must scroll into view on reopen'
        capture('last-site-reopened')
        page.keyboard.press('Escape')
        pick('Site 123')
        selected(123)
        resource_search = page.get_by_role('searchbox', name='Search resources', exact=True)
        resource_search.fill('Guest 123')
        selected(123)
        resource_search.fill('host-000')
        expect(tree.get_by_text('No matching resources in this site.', exact=True)).to_be_visible()
        expect(tree.locator('a')).to_have_count(0)
        resource_search.fill('')
        page.goto(url + '/#/tasks')
        selected(123)
        expect(page.get_by_role('heading', name='Task history', exact=True)).to_be_visible()
        page.reload()
        selected(123)
        passed('Explicit site changes filter all three resource levels; global task navigation and reload preserve selected site')

        for path, number in [('pools/pool-222', 222), ('hosts/host-333', 333), ('vms/vm-444', 444)]:
            page.goto(url + '/#/' + path)
            selected(number)
        page.reload()
        selected(444)
        page.evaluate("location.hash = '/hosts/host-055'")
        selected(55)
        capture('deep-link')
        passed('Pool, host and VM deep links select their owning site on navigation/reload; in-page route change also updates the tree')

        page.set_viewport_size({'width': 360, 'height': 800})
        open_picker()
        search.fill('Site 001')
        long_name = data['sites'][1]['name']
        expect(options.get_by_role('option', name=long_name, exact=True)).to_be_visible()
        assert options.bounding_box()['width'] <= 360
        capture('narrow-long-name')
        options.get_by_role('option', name=long_name, exact=True).click()
        expect(selector).to_have_accessible_name('Site: ' + long_name)
        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), 'long selected site overflows'
        open_picker()
        search.fill('Site 07')
        capture('narrow-dropdown')
        options.get_by_role('option', name='Site 079', exact=True).click()
        selected(79)
        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), 'narrow viewport overflows'
        capture('narrow-selected-site')
        passed('Popup search, long names and selection remain reachable at 360px without horizontal page overflow')

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
        expect(page.get_by_text('No sites yet. Create a site to get started.', exact=True)).to_be_visible()
        expect(tree.locator('a[href^="#/pools/"]')).to_have_count(0)
        capture('empty-sites')
        page.get_by_role('button', name='Create site', exact=True).first.click()
        page.get_by_label('Site name', exact=True).fill('First site')
        page.locator('form').get_by_role('button', name='Create site', exact=True).click()
        expect(selector).to_have_accessible_name('Site: First site')
        expect(page).to_have_url(url + '/#/sites/site-created')
        expect(tree.locator('a[href^="#/pools/"]')).to_have_count(0)
        capture('first-site-created')
        passed('Empty inventory offers site creation; successful fixture creation selects its new route without unrelated resources')
        for path in ('/', '/tasks'):
            page.goto(url + '/#' + path)
            expect(selector).to_have_accessible_name('Site: First site')
            expect(tree.get_by_text('No hosts in this site.', exact=True)).to_be_visible()
            page.get_by_role('link', name='Site overview', exact=True).click()
            expect(page).to_have_url(url + '/#/sites/site-created')
            expect(page.get_by_role('heading', name='Add your first host', exact=True)).to_be_visible()
            expect(page.get_by_role('button', name='Add host', exact=True).first).to_be_enabled()
            expect(page.get_by_role('button', name='Create pool')).to_have_count(0)
        capture('only-site-overview')
        passed('The sole default site with no hosts can open Site overview from workspace/tasks, shows Add your first host and exposes Add host without any Create pool action')
        assert not errors, errors
        browser.close()
    assert assets == {name: hashlib.sha256((ROOT / 'web/dist' / name).read_bytes()).hexdigest()
                      for name in assets}, 'Built UI changed during browser acceptance'
    return {'environment': 'Real built React UI in Chromium; explicit API route fixtures with 500 sites, one pool/host/VM per site. All inventory and creation responses simulated; no Controller or libvirt execution.',
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
