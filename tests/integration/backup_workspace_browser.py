#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Built backup UI with fictional API inventory; no Core or host mutations."""
import argparse
from datetime import datetime, timezone
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import re
import threading
from urllib.parse import parse_qs, urlsplit
from playwright.sync_api import sync_playwright, expect

ROOT = Path(__file__).resolve().parents[2]


def inventory():
    stamp = datetime.now(timezone.utc).isoformat()
    host = {'id': 'host-demo', 'pool_id': 'pool-demo', 'hostname': 'demo-host', 'connection': 'fresh', 'last_seen': stamp, 'revoked': False, 'capabilities': ['vm-backup', 'vm-restore'], 'inventory': {'observed_at': stamp, 'cpu_count': 4, 'memory_bytes': 16 * 1024 ** 3, 'virtualization': ['kvm'], 'nics': [], 'disks': [], 'checks': []}}
    vms = [{'id': id_, 'name': name, 'pool_id': 'pool-demo', 'host_id': host['id'], 'status': 'succeeded', 'state': 'shut off', 'freshness': 'fresh', 'operation_id': '', 'spec': {'cpu': {'count': 2}, 'memory': {'size_mib': 2048}, 'disks': [], 'nics': [], 'boot': {'firmware': 'bios'}}} for id_, name in [('vm-db', 'db-primary'), ('vm-web', 'web-01')]]
    repos = [{'id': id_, 'pool_id': 'pool-demo', 'name': name, 'kind': 'nfs', 'source': {'server': 'backup.example.test', 'export': '/exports/backups'}, 'health': {'status': 'online', 'observed_at': stamp}, 'access': {'readable': True, 'writable': True, 'observed_at': stamp}} for id_, name in [('repo-demo', 'Vault'), ('repo-other', 'Archive')]]
    points = [{'id': f'point-{vm["id"]}', 'pool_id': 'pool-demo', 'repository_id': repos[0]['id'], 'source_vm_id': vm['id'], 'source_vm_name': vm['name'], 'captured_at': stamp, 'consistency': 'stopped', 'protected': False, 'manifest_sha256': 'a' * 64} for vm in vms]
    policy = {'id': 'policy-demo', 'schedule_id': 'schedule-demo', 'pool_id': 'pool-demo', 'name': 'Nightly servers', 'enabled': True, 'paused': False, 'repository_id': repos[0]['id'], 'capture_mode': 'stopped', 'time_zone': 'UTC', 'rule': {'frequency': 'daily', 'hour': 2, 'minute': 0}, 'vm_ids': [vm['id'] for vm in vms], 'retention': {'keep_latest': 7}, 'revision': 1}
    runs = [{'operation_id': 'run-demo', 'kind': 'vm-backup', 'policy_id': policy['id'], 'vm_id': vms[0]['id'], 'repository_id': repos[0]['id'], 'host_id': host['id'], 'status': 'failed', 'updated_at': stamp, 'error': 'Simulated capture failure'}]
    data = {'session': {'username': 'operator', 'csrf_token': 'mock-session'}, 'sites': [{'id': 'site-demo', 'name': 'Demo site'}], 'pools': [{'id': 'pool-demo', 'site_id': 'site-demo', 'name': 'Demo pool', 'master_host_id': host['id']}], 'hosts': [host], 'vms': vms, 'storage': [{'id': 'storage-demo', 'pool_id': 'pool-demo', 'host_id': host['id'], 'name': 'Guest storage', 'kind': 'directory', 'status': 'succeeded', 'capabilities': {'vm_disk': True}}], 'operations': [], 'audit': [], 'networks': [], 'media': []}
    return data, repos, points, [policy], runs


def inspect_db(page):
    page.get_by_role('button', name=re.compile(r'^Inspect db-primary \(')).click()


def retained_metadata(page, fixture):
    data, repos, points, policies, runs = fixture
    page.get_by_role('tab', name='Recovery points', exact=True).click()
    inspect_db(page)
    expect(page.get_by_role('button', name='Protect from deletion', exact=True)).to_be_visible()
    points[0]['protected'] = True  # A later Core observation, independent of the inspected snapshot.
    expect(page.get_by_role('button', name='Unprotect from deletion', exact=True)).to_be_visible(timeout=20000)
    with page.expect_response(lambda response: 'backup-catalog?vm_id=vm-web' in response.url):
        page.get_by_role('combobox', name='VM', exact=True).select_option('vm-web')
    expect(page.locator('.slideover')).to_contain_text('outside the currently loaded results')
    expect(page.get_by_role('button', name='Unprotect from deletion', exact=True)).to_be_visible()


def draft_route(page, fixture):
    page.evaluate("location.hash='/backups?vm=vm-db'")
    inspect_db(page)
    page.get_by_role('button', name='Restore…', exact=True).click()
    page.get_by_label('New VM name').fill('retained-destination')
    page.get_by_role('combobox', name='Storage', exact=True).select_option('storage-demo')
    page.evaluate("location.hash='/backups?vm=vm-web'")
    expect(page.locator('.slideover')).to_have_count(0)
    page.evaluate("location.hash='/backups?vm=vm-db'")
    inspect_db(page)
    page.get_by_role('button', name='Restore…', exact=True).click()
    expect(page.get_by_label('New VM name')).to_have_value('retained-destination')
    expect(page.get_by_role('combobox', name='Storage', exact=True)).to_have_value('storage-demo')


def uncertain_job(page, fixture):
    submissions = []
    def submit(route):
        if route.request.method != 'POST':
            route.fallback()
            return
        submissions.append(route.request.post_data_json)
        if len(submissions) == 1:
            route.fulfill(status=503, json={'error': 'Simulated uncertain response'})
        else:
            route.fulfill(status=202, json={'operation': {'id': 'operation-demo'}})
    page.route('**/api/v1/pools/pool-demo/backup-policies', submit)
    page.get_by_role('button', name='Create backup job', exact=True).click()
    panel = page.locator('.slideover')
    panel.get_by_label('Name', exact=True).fill('Retained job')
    panel.get_by_role('combobox', name='Repository', exact=True).select_option('repo-demo')
    panel.get_by_role('checkbox', name='db-primary', exact=True).check()
    panel.get_by_role('button', name=re.compile('^Next:')).click()
    panel.get_by_role('button', name=re.compile('^Next:')).click()
    panel.get_by_role('button', name='Save backup job', exact=True).click()
    expect(panel.get_by_role('button', name='Retry exact request')).to_be_visible()
    page.evaluate("location.hash='/backups?vm=vm-web'")
    expect(panel.get_by_role('button', name='Retry exact request')).to_be_visible()
    expect(panel.get_by_role('button', name='Close', exact=True)).to_be_disabled()
    page.keyboard.press('Escape')
    expect(panel.get_by_role('button', name='Retry exact request')).to_be_visible()
    panel.get_by_role('button', name='Retry exact request').click()
    expect(panel).to_have_count(0)
    assert submissions[1] == submissions[0], 'Route changes must not replace an unresolved job intent'


def uncertain_job_across_tasks(page, fixture):
    submissions = []
    def submit(route):
        if route.request.method != 'POST':
            route.fallback()
            return
        submissions.append(route.request.post_data_json)
        if len(submissions) == 1:
            route.fulfill(status=503, json={'error': 'Simulated uncertain response'})
        else:
            route.fulfill(status=202, json={'operation': {'id': 'operation-demo'}})
    page.route('**/api/v1/pools/pool-demo/backup-policies', submit)
    page.get_by_role('button', name='Create backup job', exact=True).click()
    panel = page.locator('.slideover')
    panel.get_by_label('Name', exact=True).fill('Retained job')
    panel.get_by_role('combobox', name='Repository', exact=True).select_option('repo-demo')
    panel.get_by_role('checkbox', name='db-primary', exact=True).check()
    panel.get_by_role('button', name=re.compile('^Next:')).click()
    panel.get_by_role('button', name=re.compile('^Next:')).click()
    panel.get_by_role('button', name='Save backup job', exact=True).click()
    expect(panel.get_by_role('button', name='Retry exact request')).to_be_visible()
    # The message sends the operator to Tasks, which unmounts the page; the held request must survive.
    page.evaluate("location.hash='/tasks'")
    expect(page.get_by_role('tab', name='Backup jobs', exact=True)).to_have_count(0)
    page.evaluate("location.hash='/backups'")
    expect(panel.get_by_role('button', name='Retry exact request')).to_be_visible()
    panel.get_by_role('button', name='Retry exact request').click()
    expect(panel).to_have_count(0)
    assert submissions[1] == submissions[0], 'Leaving for Tasks and returning must replay the same request_id and body'


def repository_focus(page, fixture):
    opener = page.get_by_role('button', name='Change repository of Nightly servers', exact=True)
    opener.click()
    collapse = page.get_by_role('button', name='Collapse Nightly servers', exact=True)
    expect(collapse).to_be_disabled()
    page.locator('.job-row .backup-primary').click()
    expect(collapse).to_have_attribute('aria-expanded', 'true')
    expect(page.get_by_role('button', name='Collapse all', exact=True)).to_be_disabled()
    page.keyboard.press('Escape')
    expect(page.locator('.slideover')).to_have_count(0)
    expect(opener).to_be_focused()


def phone_layout(page, fixture):
    page.set_viewport_size({'width': 390, 'height': 844})
    expect(page.locator('.job-context')).to_be_visible()
    name = page.locator('.job-row td').first
    page.wait_for_function("document.querySelector('.job-row td').getBoundingClientRect().width >= 120")
    assert name.bounding_box()['width'] >= 120, 'Expanded context must not create phantom phone columns'
    assert page.evaluate('document.documentElement.scrollWidth - innerWidth') <= 1
    page.set_viewport_size({'width': 1024, 'height': 768})
    expect(page.locator('.job-context td')).to_have_attribute('colspan', '6')


def backup_refusal(page, fixture):
    def reject(route):
        route.fulfill(status=403, json={'error': 'Simulated backup refusal'})
    page.route('**/api/v1/vms/vm-db/backup', reject)
    page.set_viewport_size({'width': 390, 'height': 844})
    page.get_by_role('tab', name='Recovery points', exact=True).click()
    page.get_by_role('button', name='Back up now', exact=True).click()
    panel = page.locator('.slideover')
    panel.get_by_role('combobox', name='Virtual machine', exact=True).select_option('vm-db')
    panel.get_by_role('combobox', name='Repository', exact=True).select_option('repo-demo')
    panel.get_by_role('button', name='Review backup', exact=True).click()
    panel.get_by_role('button', name='Back up now', exact=True).click()
    expect(panel.get_by_role('alert')).to_contain_text('Simulated backup refusal')
    expect(panel.get_by_role('button', name='Back', exact=True)).to_be_enabled()
    expect(panel.get_by_role('button', name='Close', exact=True)).to_be_enabled()


def repository_inventory(page, fixture):
    data, repos, points, policies, runs = fixture
    points.clear()
    held = []
    path = '**/api/v1/pools/pool-demo/backup-repositories'
    def defer(route):
        held.append(route)
    page.route(path, defer)
    page.reload(wait_until='domcontentloaded')
    page.set_viewport_size({'width': 390, 'height': 844})
    page.get_by_role('button', name='Create backup job', exact=True).click()
    panel = page.locator('.slideover')
    expect(panel).to_contain_text('Loading backup repositories')
    expect(panel.locator('a[href="#/settings/backups"]')).to_have_count(0)
    assert held, 'Repository read must be pending'
    held.pop(0).fulfill(status=503, json={'error': 'Simulated unavailable repository inventory'})
    expect(panel.get_by_role('alert')).to_contain_text('Repository inventory unavailable')
    expect(panel.locator('a[href="#/settings/backups"]')).to_have_count(0)
    panel.get_by_role('button', name='Cancel', exact=True).click()
    page.get_by_role('tab', name='Recovery points', exact=True).click()
    expect(page.get_by_text('No backup repositories. Create a destination in Settings first.', exact=True)).to_have_count(0)
    page.unroute(path, defer)
    def empty(route):
        route.fulfill(json=[])
    page.route(path, empty)
    page.reload(wait_until='domcontentloaded')
    page.get_by_role('button', name='Create backup job', exact=True).click()
    expect(panel.locator('a[href="#/settings/backups"]')).to_be_visible()
    expect(panel.get_by_role('button', name=re.compile('^Next:'))).to_be_disabled()


def repository_poll_focus(page, fixture):
    data, repos, points, policies, runs = fixture
    opener = page.get_by_role('button', name='Change repository of Nightly servers', exact=True)
    opener.click()
    runs[0]['status'] = 'succeeded'
    runs[0]['error'] = ''
    expect(page.locator('.job-row td').nth(3)).to_contain_text('1 succeeded', timeout=20000)
    expect(opener).to_be_visible()
    page.keyboard.press('Escape')
    expect(page.locator('.slideover')).to_have_count(0)
    expect(opener).to_be_focused()


def manual_polls(page):
    # Advance the workspace's production 15-second callbacks without wall-clock sleeps.
    page.add_init_script("""
        const originalSet = window.setInterval.bind(window);
        const originalClear = window.clearInterval.bind(window);
        const polls = new Map();
        let next = -1;
        window.setInterval = (callback, delay, ...args) => {
            if (delay !== 15000) return originalSet(callback, delay, ...args);
            const id = next--;
            polls.set(id, () => callback(...args));
            return id;
        };
        window.clearInterval = id => {
            if (!polls.delete(id)) originalClear(id);
        };
        window.advanceBackupPoll = () => [...polls.values()].forEach(callback => callback());
    """)


def advance_poll(page):
    with page.expect_response(lambda response: response.url.endswith('/backup-repositories')):
        page.evaluate("window.advanceBackupPoll()")


def delayed_history(page, fixture):
    data, repos, points, policies, runs = fixture
    manual_polls(page)
    held, requests = [], []
    def defer(route):
        requests.append(route.request.url)
        held.append(route)
        page.evaluate("count => window.heldHistoryRequests = count", len(requests))
    page.route('**/api/v1/pools/pool-demo/backup-runs*', defer)
    page.reload(wait_until='domcontentloaded')
    row = page.locator('.job-row')
    expect(row).to_contain_text('Nightly servers')
    expect(row.locator('td').nth(3)).to_have_text('Unknown')
    page.get_by_role('button', name='Create backup job', exact=True).click()
    panel = page.locator('.slideover')
    expect(panel.get_by_role('combobox', name='Repository', exact=True)).to_contain_text('Vault')
    panel.get_by_role('button', name='Cancel', exact=True).click()
    page.wait_for_function("window.heldHistoryRequests === 1")
    assert len(held) == 1
    for cursor in range(1, 4):
        advance_poll(page)
        assert len(requests) == cursor, 'A poll must not abort and restart an unfinished history scan'
        with page.expect_request(lambda request: f'cursor={cursor}&generation=slow-history' in request.url):
            held.pop(0).fulfill(json={'items': [], 'generation': 'slow-history', 'total': 1, 'next_cursor': str(cursor)})
        page.wait_for_function("count => window.heldHistoryRequests === count", arg=cursor + 1)
        expect(row.locator('td').nth(3)).to_have_text('Unknown')
    held.pop(0).fulfill(json={'items': runs, 'generation': 'slow-history', 'total': 1})
    expect(row.locator('td').nth(3)).to_contain_text('1 failed')
    assert len(requests) == 4, 'All four delayed pages must complete despite three intervening polls'

    advance_poll(page)
    page.wait_for_function("window.heldHistoryRequests === 5")
    # Previous complete observations remain on screen while a refresh is pending.
    expect(row.locator('td').nth(3)).to_contain_text('1 failed')
    assert len(held) == 1
    held.pop(0).fulfill(status=503, json={'error': 'Simulated history outage'})
    expect(page.get_by_role('alert')).to_contain_text('Backup run history unavailable')
    expect(row.locator('td').nth(3)).to_have_text('Unknown')
    page.get_by_role('button', name='Inspect Nightly servers', exact=True).click()
    expect(page.get_by_role('button', name='Change repository of Nightly servers', exact=True)).to_be_enabled()

    # Generation races restart at the first page, with a bounded retry budget.
    advance_poll(page)
    page.wait_for_function("window.heldHistoryRequests === 6")
    with page.expect_request(lambda request: 'cursor=stale&generation=racing-history' in request.url):
        held.pop(0).fulfill(json={'items': [{**runs[0], 'status': 'failed'}], 'generation': 'racing-history', 'total': 2, 'next_cursor': 'stale'})
    page.wait_for_function("window.heldHistoryRequests === 7")
    for count in (8, 9):
        with page.expect_request(lambda request: request.url.endswith('/backup-runs')):
            held.pop(0).fulfill(status=409, json={'error': 'Simulated generation change'})
        page.wait_for_function("count => window.heldHistoryRequests === count", arg=count)
    recovered = [{**runs[0], 'status': 'succeeded', 'error': ''}]
    held.pop(0).fulfill(json={'items': recovered, 'generation': 'recovered', 'total': 1})
    expect(row.locator('td').nth(3)).to_contain_text('1 succeeded')
    expect(page.get_by_role('alert')).to_have_count(0)

    advance_poll(page)
    page.wait_for_function("window.heldHistoryRequests === 10")
    for count in (11, 12):
        with page.expect_request(lambda request: request.url.endswith('/backup-runs')):
            held.pop(0).fulfill(status=409, json={'error': 'Simulated repeated generation change'})
        page.wait_for_function("count => window.heldHistoryRequests === count", arg=count)
    held.pop(0).fulfill(status=409, json={'error': 'Simulated repeated generation change'})
    expect(page.get_by_role('alert')).to_contain_text('Backup run history unavailable')
    expect(row.locator('td').nth(3)).to_have_text('Unknown')
    assert not held, 'Repeated conflicts stop after three attempts instead of retrying forever'
    advance_poll(page)
    page.wait_for_function("window.heldHistoryRequests === 13")
    held.pop(0).fulfill(json={'items': recovered, 'generation': 'recovered-again', 'total': 1})
    expect(row.locator('td').nth(3)).to_contain_text('1 succeeded')
    expect(page.get_by_role('alert')).to_have_count(0)


def history_scope_cancellation(page, fixture):
    data, repos, points, policies, runs = fixture
    manual_polls(page)
    held, cancelled = [], []
    page.on('requestfailed', lambda request: cancelled.append(request.url))
    def defer(route):
        held.append(route)
        page.evaluate("window.heldHistoryRequests = (window.heldHistoryRequests || 0) + 1")
    page.route('**/api/v1/pools/pool-demo/backup-runs*', defer)
    page.reload(wait_until='domcontentloaded')
    expect(page.locator('.job-row td').nth(3)).to_have_text('Unknown')
    page.wait_for_function("window.heldHistoryRequests === 1")
    assert len(held) == 1
    old_url = held.pop(0).request.url
    with page.expect_event('requestfailed', predicate=lambda request: request.url == old_url):
        page.evaluate("location.hash='/settings/backups'")
    expect(page.get_by_role('tab', name='Backup jobs', exact=True)).to_have_count(0)
    assert old_url in cancelled, 'Leaving workspace cancels its in-flight history scan'
    with page.expect_request(lambda request: request.url.endswith('/backup-runs')):
        page.evaluate("location.hash='/backups'")
    page.wait_for_function("window.heldHistoryRequests === 2")
    held.pop(0).fulfill(json={'items': [{**runs[0], 'status': 'succeeded', 'error': ''}], 'generation': 'new-scope', 'total': 1})
    expect(page.locator('.job-row td').nth(3)).to_contain_text('1 succeeded')
    expect(page.get_by_role('alert')).to_have_count(0)


def repository_deleted_override(page, fixture):
    data, repos, points, policies, runs = fixture
    manual_polls(page)
    policy = policies[0]
    policy['vm_ids'].append('vm-deleted')
    retained = {'vm_id': 'vm-db', 'capture_mode': 'crash', 'retention': {'keep_latest': 3}}
    policy['overrides'] = [{'vm_id': 'vm-deleted', 'capture_mode': 'stopped', 'retention': {'keep_latest': 9}}, retained]
    submissions = []
    def submit(route):
        if route.request.method != 'PUT':
            route.fallback()
            return
        body = route.request.post_data_json
        submissions.append(body)
        if any(override['vm_id'] not in body['vm_ids'] for override in body.get('overrides', [])):
            route.fulfill(status=400, json={'error': 'override VM is outside policy coverage'})
        elif len(submissions) == 1:
            route.fulfill(status=503, json={'error': 'Simulated uncertain repository change'})
        else:
            route.fulfill(status=202, json={'operation': {'id': 'operation-move', 'status': 'queued'}})
    page.route('**/api/v1/pools/pool-demo/backup-policies/policy-demo', submit)
    page.reload(wait_until='domcontentloaded')
    page.get_by_role('button', name='Change repository of Nightly servers', exact=True).click()
    panel = page.locator('.slideover')
    panel.get_by_role('combobox', name='New repository', exact=True).select_option('repo-other')
    panel.get_by_role('checkbox').check()
    panel.get_by_role('button', name='Save repository change', exact=True).click()
    expect(panel.get_by_role('button', name='Retry exact request', exact=True)).to_be_visible()
    assert submissions[0]['vm_ids'] == ['vm-db', 'vm-web', 'vm-deleted'], 'The Controller sends Core-owned coverage unchanged, including VMs missing from its mirror'
    assert submissions[0]['overrides'] == policy['overrides'], 'Overrides are sent as Core reported them'
    # New observations must not change an unresolved move's coverage or retained override.
    data['vms'][:] = [vm for vm in data['vms'] if vm['id'] != 'vm-web']
    policy['overrides'][1] = {**retained, 'capture_mode': 'stopped'}
    policy['vm_ids'] = ['vm-db']
    policy['revision'] = 2
    advance_poll(page)
    panel.get_by_role('button', name='Retry exact request', exact=True).click()
    expect(panel).to_have_count(0)
    assert submissions[1] == submissions[0], 'An uncertain move retries the exact original valid payload'


CHECKS = {
    'retained-metadata': retained_metadata,
    'draft-route': draft_route,
    'uncertain-job': uncertain_job,
    'uncertain-job-across-tasks': uncertain_job_across_tasks,
    'repository-focus': repository_focus,
    'phone-layout': phone_layout,
    'backup-refusal': backup_refusal,
    'repository-inventory': repository_inventory,
    'repository-poll-focus': repository_poll_focus,
    'delayed-history': delayed_history,
    'history-scope-cancellation': history_scope_cancellation,
    'repository-deleted-override': repository_deleted_override,
}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--evidence', type=Path, default=ROOT / '.cache/backup-workspace-browser')
    parser.add_argument('--check', choices=CHECKS)
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer(('127.0.0.1', 0), partial(SimpleHTTPRequestHandler, directory=str(ROOT / 'web/dist')))
    threading.Thread(target=server.serve_forever, daemon=True).start()
    results, failures = [], []
    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True)
            for name, check in CHECKS.items():
                if args.check and args.check != name:
                    continue
                fixture = inventory()
                data, repos, points, policies, runs = fixture
                errors = []
                context = browser.new_context(viewport={'width': 1440, 'height': 900})
                page = context.new_page()
                page.set_default_timeout(10000)
                page.add_init_script("localStorage.setItem('nex-signed-in', '1')")
                page.on('pageerror', lambda error: errors.append(str(error)))
                def route_api(route):
                    url = urlsplit(route.request.url)
                    path = url.path.split('/api/v1/')[1]
                    if path.endswith('/backup-catalog'):
                        query = parse_qs(url.query)
                        items = [point for point in points if (not query.get('vm_id') or point['source_vm_id'] == query['vm_id'][0]) and (not query.get('repository_id') or point['repository_id'] == query['repository_id'][0])]
                        route.fulfill(json={'items': items, 'generation': 'catalog-demo', 'total': len(items), 'untested_count': len(items), 'backup_days_14': []})
                    elif path.endswith('/backup-runs'):
                        route.fulfill(json={'items': runs, 'generation': 'runs-demo', 'total': len(runs)})
                    elif path.endswith('/backup-repositories'):
                        route.fulfill(json=repos)
                    elif path.endswith('/backup-policies'):
                        route.fulfill(json=policies)
                    else:
                        route.fulfill(json=data.get(path, []))
                page.route('**/api/v1/**', route_api)
                try:
                    page.goto(f'http://127.0.0.1:{server.server_port}/#/backups')
                    expect(page.get_by_role('tab', name='Backup jobs', exact=True)).to_be_visible()
                    check(page, fixture)
                    assert not errors, errors
                    page.screenshot(path=str(args.evidence / f'{name}.png'), full_page=True)
                    results.append({'check': name, 'passed': True})
                except Exception as error:
                    page.screenshot(path=str(args.evidence / f'{name}-failed.png'), full_page=True)
                    results.append({'check': name, 'passed': False, 'error': str(error)})
                    failures.append(name)
                finally:
                    context.close()
            version = browser.version
            browser.close()
        report = {'method': 'Built UI with simulated API responses; no native Core or storage proof', 'browser': version, 'results': results}
        (args.evidence / 'results.json').write_text(json.dumps(report, indent=2) + '\n')
        print(json.dumps(report, indent=2))
        if failures:
            raise SystemExit(1)
    finally:
        server.shutdown()
        server.server_close()


if __name__ == '__main__':
    main()
