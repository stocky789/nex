#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Host Storage ISO presentation checks; real Controller and explicitly simulated agent.

Default mode is disposable/CI. Additional status/isolation/failure rows are injected
browser responses and never establish real disk access. --config is read-only browser
verification against the explicitly disposable retained native lab.
"""
import argparse
import hashlib
import json
from pathlib import Path
import tempfile
import urllib.parse
import urllib.request
from managed_vm_browser import simulated
from phase1 import Lab, ROOT, eventually


def login(page, url, password):
    from playwright.sync_api import expect
    page.goto(url)
    page.get_by_label('Username', exact=True).fill('root')
    page.get_by_label('Password', exact=True).fill(password)
    page.keyboard.press('Enter')
    expect(page.get_by_role('navigation', name='Sections', exact=True)).to_be_visible()
    page.wait_for_load_state('networkidle')


def host_storage(page, url, host_id):
    from playwright.sync_api import expect
    page.goto(url + '/#/hosts/' + host_id)
    storage = page.get_by_role('tab', name='Storage', exact=True)
    storage.focus()
    page.keyboard.press('Enter')
    panel = page.get_by_role('region', name='Host ISO images')
    expect(panel).to_be_visible()
    return panel


def refresh(page):
    # The console re-reads inventory every ten seconds; wait for the next ISO read.
    with page.expect_response(lambda r: r.url.endswith('/api/v1/media'), timeout=15000):
        pass


def main():
    from playwright.sync_api import sync_playwright, expect
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--evidence', type=Path, default=ROOT / '.cache/host-iso-browser')
    parser.add_argument('--config', type=Path, help='read-only verification of explicitly disposable native lab')
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    checks, captures = [], []
    stop = thread = lab = None
    with tempfile.TemporaryDirectory(prefix='nex-host-isos.') as temp:
        try:
            if args.config:
                config = json.loads(args.config.read_text())
                assert config.get('disposable') is True
                url = config['controller_url']
                assert urllib.parse.urlparse(url).hostname in ('localhost', '127.0.0.1')
                password_file = Path(config['controller_password_file'])
                assert password_file.stat().st_mode & 0o077 == 0
                password = password_file.read_text()
                mode = 'native'
            else:
                lab = Lab(Path(temp))
                config, password, iso, stop, thread, failures = simulated(lab)
                url, mode = config['url'], 'simulated'
            with sync_playwright() as pw:
                browser = pw.chromium.launch(headless=True)
                page = browser.new_page(ignore_https_errors=True, viewport={'width': 1440, 'height': 1000})
                errors = []
                page.on('pageerror', lambda e: errors.append(str(e)))
                def shot(name):
                    filename = mode + '-' + name + '.png'
                    page.screenshot(path=str(args.evidence / filename), full_page=True)
                    captures.append(filename)
                login(page, url, password)
                host = next(h for h in page.request.get(url + '/api/v1/hosts').json() if h['id'] == config['host_id']) if lab else next(h for h in page.request.get(url + '/api/v1/hosts').json() if not h['revoked'])
                panel = host_storage(page, url, host['id'])
                if lab:
                    expect(panel.get_by_text('No ISO transfers recorded for this host.', exact=False)).to_be_visible()
                    shot('empty')
                    storage = lab.api('/api/v1/storage', {'pool_id': config['pool_id'], 'host_id': host['id'], 'name': 'ISO test storage', 'path': '/simulated/isos', 'request_id': 'host-isos-storage'}, expected=201)
                    eventually(lambda: any(s['id'] == storage['id'] and s['status'] == 'succeeded' for s in lab.api('/api/v1/storage')), 'simulated storage result')
                    query = urllib.parse.urlencode({'storage_id': storage['id'], 'name': 'available.iso', 'request_id': 'host-isos-media'})
                    request = urllib.request.Request(url + '/api/v1/media?' + query, data=iso.read_bytes(), headers={'X-CSRF-Token': lab.csrf, 'Content-Type': 'application/octet-stream', 'Origin': url})
                    with lab.http.open(request) as response:
                        assert response.status == 201
                        media = json.load(response)
                    eventually(lambda: any(m['id'] == media['id'] and m['status'] == 'succeeded' for m in lab.api('/api/v1/media')), 'simulated media result')
                    base = next(m for m in lab.api('/api/v1/media') if m['id'] == media['id'])
                    rows = [base]
                    for status in ('queued', 'dispatched', 'failed', 'needs_attention'):
                        rows.append({**base, 'id': status + '-fixture', 'name': status + '.iso', 'status': status, 'error': 'Simulated transfer failure' if status == 'failed' else ''})
                    rows.extend([{**base, 'id': 'other-host', 'host_id': 'different-host', 'name': 'other-host.iso'},
                        {**base, 'id': 'other-pool', 'pool_id': 'different-pool', 'name': 'other-pool.iso'},
                        {**base, 'id': 'unknown-storage', 'storage_id': 'unknown-storage-id', 'name': 'unknown-storage.iso'}])
                    host_override = {}
                    def hosts_route(route):
                        response = route.fetch()
                        values = response.json()
                        for value in values:
                            if value['id'] == host['id']:
                                # Presentation checks must not inherit a transient reconnect
                                # window from earlier browser jobs on a shared runner.
                                value['connection'] = 'fresh'
                                value['revoked'] = False
                                value['capabilities'] = sorted(set(value.get('capabilities') or []) | {'managed-vm-v1'})
                                value.update(host_override)
                        route.fulfill(response=response, json=values)
                    page.route('**/api/v1/hosts', hosts_route)
                    page.route('**/api/v1/media', lambda route: route.fulfill(json=rows))
                    refresh(page)
                    expect(panel.get_by_text('Transfer succeeded · not rescanned', exact=True)).to_be_visible()
                    for name, availability in [('queued', 'Pending transfer'), ('dispatched', 'Dispatched · awaiting result'), ('failed', 'Transfer failed'), ('needs_attention', 'Needs attention')]:
                        expect(panel.get_by_role('row').filter(has_text=name + '.iso')).to_contain_text(availability)
                    expect(panel.get_by_text('other-host.iso', exact=True)).to_have_count(0)
                    expect(panel.get_by_text('other-pool.iso', exact=True)).to_have_count(0)
                    expect(panel.get_by_role('row').filter(has_text='unknown-storage.iso')).to_contain_text('Availability unknown · storage access unverified')
                    expect(panel.get_by_role('row').filter(has_text='unknown-storage.iso')).to_contain_text('unknown-storage-id')
                    # Matching IDs from another scope must not supply a storage name or access proof.
                    for scope in ('host_id', 'pool_id'):
                        page.route('**/api/v1/storage', lambda route: route.fulfill(json=[{**storage, 'status': 'succeeded', scope: 'foreign-scope', 'name': 'Foreign storage must stay hidden'}]))
                        refresh(page)
                        expect(panel.get_by_role('row').filter(has_text='available.iso')).to_contain_text('Availability unknown · storage access unverified')
                        expect(panel.get_by_text('Foreign storage must stay hidden', exact=True)).to_have_count(0)
                        page.unroute('**/api/v1/storage')
                    refresh(page)
                    expect(panel.get_by_text('Transfer succeeded · not rescanned', exact=True)).to_be_visible()
                    checks.append('A matching storage ID from another host or pool supplies neither a name nor access verification')
                    shot('statuses-desktop')
                    checks.append('Actual empty-to-completed Controller transfer record appears; explicitly injected status rows distinguish pending/dispatched/failed/attention; other host/pool excluded and unknown storage stays unverified')
                    page.set_viewport_size({'width': 360, 'height': 800})
                    assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), 'narrow document overflow'
                    shot('statuses-360')
                    link = panel.get_by_role('row').filter(has_text='available.iso').get_by_role('link', name='Task ' + media['operation_id'])
                    link.focus()
                    assert panel.locator('.table-scroll').evaluate('el => el.scrollLeft > 0'), 'keyboard must reveal clipped task column'
                    shot('task-keyboard-360')
                    page.keyboard.press('Enter')
                    expect(page).to_have_url(url + '/#/tasks')
                    expect(page.get_by_role('heading', name='Tasks', exact=True).first).to_be_attached()
                    expect(page.get_by_role('row').filter(has_text=media['operation_id'])).to_be_visible()
                    panel = host_storage(page, url, host['id'])
                    checks.append('Keyboard opens host Storage and the real transfer task history; 360px document has no horizontal overflow')
                    for name, override, label in [('stale', {'connection': 'stale'}, 'Availability unknown · host is not freshly connected'),
                        ('revoked', {'revoked': True}, 'Unavailable · host access revoked'),
                        ('old-agent', {'capabilities': []}, 'Unavailable · host agent upgrade required')]:
                        host_override.clear(); host_override.update(override)
                        refresh(page)
                        expect(panel.get_by_role('row').filter(has_text='available.iso')).to_contain_text(label)
                        shot(name)
                    host_override.clear(); host_override['inventory'] = None
                    refresh(page)
                    expect(panel.get_by_text('available.iso', exact=True)).to_be_visible()
                    shot('no-inventory')
                    host_override.clear()
                    refresh(page)
                    expect(panel.get_by_role('row').filter(has_text='available.iso')).to_contain_text('Transfer succeeded · not rescanned')
                    page.route('**/api/v1/storage', lambda route: route.abort('failed'))
                    expect(page.get_by_role('alert')).to_be_visible(timeout=15000)
                    expect(panel.get_by_role('row').filter(has_text='available.iso')).to_contain_text('Availability unknown · Controller data may be stale')
                    shot('controller-unavailable')
                    page.unroute('**/api/v1/storage')
                    page.get_by_role('button', name='Retry', exact=True).click()
                    expect(page.get_by_role('alert')).to_have_count(0)
                    expect(panel.get_by_role('row').filter(has_text='available.iso')).to_contain_text('Transfer succeeded · not rescanned')
                    checks.append('Injected stale/revoked/old-agent/no-inventory states preserve honest ISO records; failed Controller refresh retains records as unknown and Retry recovers')
                    assert not failures, failures
                else:
                    scope = page.locator('summary[aria-label^="Scope:"]')
                    selected_site = scope.get_attribute('aria-label').removeprefix('Scope: ').split(',')[0]
                    scope.focus()
                    page.keyboard.press('Enter')
                    choice = page.get_by_role('button', name=selected_site + ' · all pools', exact=True)
                    expect(choice).to_be_visible()
                    shot('scope-menu')
                    choice.focus()
                    page.keyboard.press('Enter')
                    expect(choice).to_be_hidden()
                    panel = host_storage(page, url, host['id'])
                    checks.append('Actual scope menu opens and selects the current site with Enter without changing records')
                    media = page.request.get(url + '/api/v1/media').json()
                    selected = [m for m in media if m['host_id'] == host['id'] and m['pool_id'] == host['pool_id']]
                    assert selected, 'native host must have existing dispatched media'
                    storages = page.request.get(url + '/api/v1/storage').json()
                    for record in selected:
                        row = panel.get_by_role('row').filter(has_text=record['name'])
                        expect(row.get_by_text(record['name'], exact=True)).to_be_visible()
                        expect(row).to_contain_text(f"{record['size_bytes']:,} bytes")
                        expect(row.get_by_role('link', name='Task ' + record['operation_id'])).to_have_attribute('href', '#/tasks')
                        destination = next(s for s in storages if s['id'] == record['storage_id'] and s['host_id'] == host['id'] and s['pool_id'] == host['pool_id'])
                        expect(row).to_contain_text(destination['name'])
                        expect(row).to_contain_text(destination['path'])
                    expect(panel.get_by_text('Transfer succeeded · not rescanned', exact=True).first).to_be_visible()
                    shot('desktop')
                    page.set_viewport_size({'width': 360, 'height': 800})
                    assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
                    shot('360')
                    checks.append('Read-only actual retained host Storage shows its existing ISO with completed-transfer record, storage/size/task, via keyboard navigation and at 360px')
                assert not errors, errors
                response = page.request.get(url + '/app.js')
                assert response.ok
                result = {'mode': mode, 'completed': True, 'checks': checks, 'captures': captures,
                    'served_ui_sha256': hashlib.sha256(response.body()).hexdigest(),
                    'environment': 'Playwright Chromium, real HTTPS Controller; ' + ('simulated mTLS agent and synthetic ISO; additional media/host/error browser responses explicitly injected' if lab else 'actual retained disposable fresh-install Nex host; read-only browser interactions'),
                    'limits': ['Availability derives from transfer records, host connectivity and validated storage, not continuous file rescanning', 'Native storage correctness is covered separately by fresh-install acceptance']}
                (args.evidence / (mode + '-results.json')).write_text(json.dumps(result, indent=2) + '\n')
                print('PASS:', '; '.join(checks))
                browser.close()
        finally:
            if stop:
                stop.set(); thread.join(timeout=10)
            if lab:
                lab.close()


if __name__ == '__main__':
    main()
