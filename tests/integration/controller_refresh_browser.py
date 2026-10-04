#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise delayed Controller refreshes in Chromium with a fully mocked API."""
import argparse
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import re
import threading
import time

from playwright.sync_api import expect, sync_playwright

from ui_review_browser import FIXTURE, resolve, series

ROOT = Path(__file__).resolve().parents[2]


def clone(value):
    return json.loads(json.dumps(value))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--evidence', type=Path, default=ROOT / '.cache/controller-refresh')
    parser.add_argument('--dist', type=Path, default=ROOT / 'web/dist')
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    fx = resolve(FIXTURE, FIXTURE['hosts'])
    data = {key: clone(fx[key]) for key in ('sites', 'pools', 'hosts', 'vms', 'storage', 'networks', 'media', 'operations', 'audit', 'updates')}
    data['operations'] = [operation for operation in data['operations'] if not (operation.get('host_id') == 'host-1' and operation.get('kind') == 'refresh-inventory')]
    members = clone(fx['members'])
    members['members'][1]['fence'] = {'provisioning_disabled': True}
    network = {'host_id': 'host-1', 'hostname': 'lab-node-01', 'connection': 'fresh', 'supported': True, 'state': 'none', 'management_network': None,
               'nics': [{'name': 'br0', 'mac': '52:54:00:00:00:01', 'kind': 'physical', 'state': 'up', 'addresses': ['192.0.2.10/24']} ]}
    state = {
        'host_refresh': False, 'host_operation_reads': 0, 'host_hardware_reads': 0, 'host_reads': 0,
        'host_post_count': 0, 'host_request_ids': [], 'fail_next_hardware': False,
        'network_posts': 0, 'network_reads_after_post': 0, 'fail_next_network': False,
        'bmc_posts': 0, 'members_reads_after_post': 0, 'cluster_reads': 0, 'cluster_complete': False, 'cluster_fail_next': False,
        'console_rename_vm': False, 'console_remove_vm': False, 'power_posts': 0,
        'update_posts': 0, 'update_status_reads': 0,
    }
    errors, checks = [], []
    cluster_operation = {'request_id': 'fixture-cluster-request', 'membership_generation': 4,
                         'config': {'cluster_id': 'fixture-cluster', 'repository_id': 'st-local-1', 'generation': 1, 'lock_table': 'nex-locks', 'nodes': []},
                         'status': 'applying', 'members': []}

    def route_api(route):
        req = route.request
        path = req.url.split('/api/v1/', 1)[-1].split('?', 1)[0]
        if req.method == 'GET':
            if path == 'session':
                return route.fulfill(json={'username': 'operator', 'csrf_token': 'fixture-session'})
            if path == 'hosts':
                state['host_reads'] += 1
                hosts = clone(data['hosts'])
                if state['host_refresh']:
                    hosts[0]['hostname'] = 'fixture-refreshed-host'
                return route.fulfill(json=hosts)
            if path == 'operations':
                state['host_operation_reads'] += 1
                if not state['host_refresh']:
                    return route.fulfill(json=data['operations'])
                status = 'queued' if state['host_operation_reads'] < 2 else 'succeeded'
                return route.fulfill(json=[{'id': 'fixture-refresh-op', 'host_id': 'host-1', 'kind': 'refresh-inventory', 'request_id': 'fixture-refresh-request', 'status': status}])
            if path == 'updates':
                if state['update_posts']:
                    state['update_status_reads'] += 1
                    if state['update_status_reads'] == 1:
                        return route.fulfill(status=503, json={'error': 'fixture update service restarting'})
                    status = clone(data['updates'])
                    status.update(phase='done' if state['update_status_reads'] >= 3 else 'downloading', message='Update complete' if state['update_status_reads'] >= 3 else 'Downloading')
                    return route.fulfill(json=status)
                return route.fulfill(json=data['updates'])
            if path == 'storage/cluster':
                state['cluster_reads'] += 1
                if state['cluster_fail_next']:
                    state['cluster_fail_next'] = False
                    return route.fulfill(status=503, json={'error': 'fixture cluster status read failed'})
                if state['cluster_complete']:
                    cluster_operation['status'] = 'succeeded'
                return route.fulfill(json={'status': 'succeeded', 'pool_id': 'pool-a', 'observed_at': '2026-09-24T00:00:00Z', 'operations': [clone(cluster_operation)]})
            match = re.fullmatch(r'hosts/([^/]+)/hardware', path)
            if match:
                state['host_hardware_reads'] += 1
                if state['fail_next_hardware']:
                    state['fail_next_hardware'] = False
                    return route.fulfill(status=503, json={'error': 'fixture hardware read failed'})
                hw = clone(fx['hardware'])
                hw['host_id'] = match[1]
                hw['system']['model'] = 'Fixture refreshed model' if state['host_refresh'] and state['host_hardware_reads'] > 1 else 'Fixture initial model'
                return route.fulfill(json=hw)
            match = re.fullmatch(r'(hosts|vms)/([^/]+)/stats', path)
            if match:
                query = req.url.split('?', 1)[1] if '?' in req.url else ''
                range_name = dict(item.split('=', 1) for item in query.split('&') if '=' in item).get('range', '10m')
                return route.fulfill(json=series('host' if match[1] == 'hosts' else 'vm', match[2], range_name))
            match = re.fullmatch(r'hosts/([^/]+)/network', path)
            if match:
                if state['network_posts']:
                    state['network_reads_after_post'] += 1
                if state['fail_next_network']:
                    state['fail_next_network'] = False
                    return route.fulfill(status=503, json={'error': 'fixture network report unavailable'})
                result = clone(network)
                result['host_id'] = match[1]
                if state['network_reads_after_post'] >= 2:
                    result.update(state='staged', management_network={'revision': 1, 'stage': 'staged', 'operation_id': 'fixture-network-op', 'window_seconds': 600,
                                                                       'spec': {'interfaces': [{'name': 'br0', 'mac': '52:54:00:00:00:01', 'mode': 'dhcp', 'management': True}], 'dns': []}})
                return route.fulfill(json=result)
            match = re.fullmatch(r'pools/([^/]+)/members', path)
            if match:
                if state['bmc_posts']:
                    state['members_reads_after_post'] += 1
                report = clone(members)
                if state['members_reads_after_post'] >= 2:
                    member = next(m for m in report['members'] if m['host_id'] == 'host-2')
                    member['fence'] = {'provisioning_disabled': False, 'configured': True, 'provisioned_by_core': True,
                                       'bmc': {'state': 'provisioned', 'address': 'bmc.example.test', 'account': 'nex'}}
                return route.fulfill(json=report)
            if path == 'storage/credentials':
                return route.fulfill(json=[])
            if path == 'storage/cluster':
                return route.fulfill(json={'status': 'succeeded', 'pool_id': 'pool-a', 'operations': []})
            if path in data:
                if path == 'vms':
                    vms = clone(data['vms'])
                    if state['console_rename_vm']:
                        next(vm for vm in vms if vm['id'] == 'vm-ctl')['name'] = 'fixture-appliance-renamed'
                    if state['console_remove_vm']:
                        vms = [vm for vm in vms if vm['id'] != 'vm-ctl']
                    return route.fulfill(json=vms)
                return route.fulfill(json=data[path])
        if req.method == 'POST' and re.fullmatch(r'hosts/[^/]+/operations', path):
            body = req.post_data_json
            state['host_post_count'] += 1
            state['host_request_ids'].append(body.get('request_id'))
            state['host_refresh'] = True
            state['host_operation_reads'] = 0
            if state['host_post_count'] == 1:
                state['fail_next_hardware'] = True
                return route.fulfill(status=502, json={'error': 'fixture accepted request but lost its response'})
            return route.fulfill(status=202, json={'id': 'fixture-refresh-op', 'host_id': 'host-1', 'kind': 'refresh-inventory', 'request_id': 'fixture-refresh-request', 'status': 'queued'})
        if req.method == 'POST' and re.fullmatch(r'hosts/[^/]+/network', path):
            state['network_posts'] += 1
            state['network_reads_after_post'] = 0
            if state['network_posts'] == 1:
                return route.fulfill(status=502, json={'error': 'fixture accepted staged request but lost its response'})
            return route.fulfill(status=202, json={'status': 'queued'})
        if req.method == 'POST' and re.fullmatch(r'pools/[^/]+/members/[^/]+/bmc-provisioning', path):
            state['bmc_posts'] += 1
            return route.fulfill(status=202, json={'status': 'queued'})
        if req.method == 'POST' and re.fullmatch(r'vms/[^/]+/power', path):
            state['power_posts'] += 1
            return route.fulfill(status=202, json={'status': 'queued'})
        if req.method == 'POST' and path == 'updates/apply':
            state['update_posts'] += 1
            return route.fulfill(status=502, json={'error': 'fixture accepted update but lost its response'})
        return route.fulfill(status=200, json={})

    server = ThreadingHTTPServer(('127.0.0.1', 0), partial(SimpleHTTPRequestHandler, directory=str(args.dist.resolve())))
    threading.Thread(target=server.serve_forever, daemon=True).start()
    url = f'http://127.0.0.1:{server.server_port}/'
    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True)
            page = browser.new_page(viewport={'width': 1440, 'height': 1000})
            page.set_default_timeout(30000)
            expect.set_options(timeout=30000)
            page.add_init_script("localStorage.setItem('nex-signed-in', '1'); window.addEventListener('load', () => sessionStorage.setItem('refresh-check-loads', String(Number(sessionStorage.getItem('refresh-check-loads') || 0) + 1)))")
            page.on('pageerror', lambda error: errors.append(str(error)))
            page.route('**/api/v1/**', route_api)

            page.goto(url + '#/hosts/host-1')
            expect(page.get_by_role('tab', name='Hardware', exact=True)).to_be_visible()
            page.get_by_role('tab', name='Hardware', exact=True).click()
            system = page.locator('details.stats-hw-section').filter(has_text='System')
            if system.get_attribute('open') is None:
                system.locator('summary').click()
            expect(page.get_by_text('Fixture initial model', exact=False)).to_be_visible()
            loads = int(page.evaluate("sessionStorage.getItem('refresh-check-loads')"))
            page.get_by_role('button', name='Refresh inventory', exact=True).click()
            expect(page.get_by_role('alert')).to_contain_text('fixture accepted request but lost its response')
            page.get_by_role('button', name='Refresh inventory', exact=True).click()
            assert len(state['host_request_ids']) == 2 and state['host_request_ids'][0] == state['host_request_ids'][1], 'retry after an uncertain POST did not reuse its request identity'
            page.wait_for_timeout(300)
            assert state['host_hardware_reads'] == 1, 'the initial accepted refresh should leave the old hardware report visible until Core reports completion'
            expect(page.get_by_role('tabpanel', name='Hardware').get_by_text('Could not read the hardware', exact=False)).to_be_visible(timeout=13000)
            expect(page.get_by_text('Inventory refresh queued.', exact=False)).to_have_count(0)
            expect(page.get_by_text('Fixture initial model', exact=False)).to_be_visible()
            page.evaluate("window.dispatchEvent(new Event('online'))")
            expect(page.get_by_text('Fixture refreshed model', exact=False)).to_be_visible()
            assert state['host_hardware_reads'] > 1, 'terminal refresh-inventory outcome did not reread host hardware'
            assert int(page.evaluate("sessionStorage.getItem('refresh-check-loads')")) == loads, 'refresh required full navigation'
            page.screenshot(path=str(args.evidence / 'host-inventory-refreshed.png'), full_page=True)
            checks.append('refresh-inventory queued then terminal result refreshes hardware in the open host panel without navigation')
            checks.append('uncertain refresh submission reuses its request ID; a hardware read error keeps the old report and retry recovers')

            before_host_reads = state['host_reads']
            assert page.evaluate('document.visibilityState') == 'visible'
            page.evaluate("document.dispatchEvent(new Event('visibilitychange'))")
            deadline = time.monotonic() + 3
            while state['host_reads'] <= before_host_reads and time.monotonic() < deadline:
                page.wait_for_timeout(100)
            assert state['host_reads'] > before_host_reads, 'returning to a visible tab did not immediately reload inventory'
            checks.append('a visible-tab change triggers immediate inventory catch-up')

            before_host_reads = state['host_reads']
            page.evaluate("window.dispatchEvent(new Event('online'))")
            deadline = time.monotonic() + 3
            while state['host_reads'] <= before_host_reads and time.monotonic() < deadline:
                page.wait_for_timeout(100)
            assert state['host_reads'] > before_host_reads, 'returning online did not immediately reload Controller inventory'
            checks.append('returning online triggers an immediate Controller inventory catch-up')

            page.evaluate("location.hash = '#/hosts/host-1'")
            page.get_by_role('tab', name='Networking', exact=True).click()
            expect(page.get_by_text('No change staged', exact=False)).to_be_visible()
            page.locator('#mode-br0').select_option('dhcp')
            state['fail_next_network'] = True
            page.evaluate("window.dispatchEvent(new Event('online'))")
            network_panel = page.get_by_role('region', name='Management network')
            expect(network_panel.get_by_role('alert')).to_contain_text('fixture network report unavailable')
            expect(network_panel.locator('#mode-br0')).to_have_value('dhcp')
            expect(network_panel.get_by_role('button', name='Stage this configuration', exact=True)).to_be_disabled()
            page.screenshot(path=str(args.evidence / 'network-read-error.png'), full_page=True)
            network_panel.get_by_role('button', name='Refresh', exact=True).click()
            expect(network_panel.get_by_role('alert')).to_have_count(0)
            expect(network_panel.locator('#mode-br0')).to_have_value('dhcp')
            expect(network_panel.get_by_role('button', name='Stage this configuration', exact=True)).to_be_enabled()
            page.get_by_role('button', name='Stage this configuration', exact=True).click()
            expect(page.get_by_role('alert')).to_contain_text('fixture accepted staged request but lost its response')
            expect(page.get_by_text('No change staged', exact=False)).to_be_visible()
            expect(page.get_by_text('Staged, not applied', exact=False)).to_be_visible(timeout=13000)
            assert state['network_posts'] == 1, 'an uncertain network response was automatically resubmitted'
            page.screenshot(path=str(args.evidence / 'host-network-staged.png'), full_page=True)
            checks.append('network read failure preserves the draft, blocks staging, and recovers on Refresh')
            checks.append('uncertain network POST is not repeated; a later host report displays its staged result')

            page.evaluate("location.hash = '#/settings/members'")
            page.wait_for_load_state('networkidle')
            page.locator('li[data-host-id="host-2"] .pool-member-more > summary').click()
            page.locator('.pool-row-actions summary').nth(1).click()
            expect(page.get_by_role('button', name='Let Nex configure this controller again', exact=True)).to_be_visible()
            page.get_by_role('button', name='Let Nex configure this controller again', exact=True).click()
            expect(page.get_by_text('Nex Core accepted the request', exact=False)).to_be_visible()
            # One read still shows the old master record; the next poll catches up with Core.
            expect(page.get_by_text('Management controller: configured by Nex Core at bmc.example.test as nex.', exact=True)).to_be_visible(timeout=13000)
            page.screenshot(path=str(args.evidence / 'pool-bmc-configured.png'), full_page=True)
            checks.append('BMC reprovision acceptance is followed by master-reported configuration without manual refresh')

            page.evaluate("location.hash = '#/storage'")
            cluster_details = page.locator('section.cluster-storage details.fold')
            expect(cluster_details).to_be_visible()
            expect(cluster_details.get_by_text('Applying', exact=True)).to_be_visible()
            repository_name = cluster_details.get_by_label('Repository name', exact=True)
            repository_name.fill('draft repository')
            repository_name.focus()
            state['cluster_fail_next'] = True
            expect(cluster_details.get_by_text('Core status refresh failed', exact=False)).to_be_visible(timeout=13000)
            expect(cluster_details.get_by_role('button', name='Submit Core setup', exact=True)).to_be_disabled()
            page.screenshot(path=str(args.evidence / 'cluster-read-error.png'), full_page=True)
            state['cluster_complete'] = True
            page.evaluate("window.dispatchEvent(new Event('online'))")
            expect(cluster_details.get_by_text('Succeeded', exact=True)).to_be_visible(timeout=13000)
            assert repository_name.input_value() == 'draft repository', 'background cluster refresh discarded the operator draft'
            expect(repository_name).to_be_focused()
            page.screenshot(path=str(args.evidence / 'cluster-status-succeeded.png'), full_page=True)
            checks.append('cluster operation advances from applying to succeeded via its own background poll while preserving a dirty field and focus')
            checks.append('cluster status read failure warns and blocks setup until the next online catch-up succeeds')

            appliance = next(vm for vm in data['vms'] if vm['id'] == 'vm-ctl')
            appliance['state'] = 'shut off'
            appliance['protected'] = True
            state['console_rename_vm'] = False
            state['console_remove_vm'] = False
            page.evaluate("location.hash = '#/console/vm-ctl'")
            page.evaluate("window.dispatchEvent(new Event('online'))")
            page.get_by_role('button', name='Start VM', exact=True).click()
            dialog = page.get_by_role('dialog')
            expect(dialog.get_by_role('heading', name='Start nex-controller?', exact=True)).to_be_visible()
            state['console_rename_vm'] = True
            page.evaluate("window.dispatchEvent(new Event('online'))")
            expect(dialog.get_by_role('heading', name='Start fixture-appliance-renamed?', exact=True)).to_be_visible()
            page.screenshot(path=str(args.evidence / 'console-confirmation-renamed.png'), full_page=True)
            state['console_remove_vm'] = True
            page.evaluate("window.dispatchEvent(new Event('online'))")
            expect(dialog).to_have_count(0)
            assert state['power_posts'] == 0, 'VM refresh scenario submitted a power action'
            page.screenshot(path=str(args.evidence / 'console-confirmation-reconciled.png'), full_page=True)
            checks.append('console power confirmation follows the VM reported by inventory and closes when that VM disappears')

            page.evaluate("location.hash = '#/settings/updates'")
            page.get_by_role('button', name='Update', exact=True).click()
            expect(page.get_by_text('Update complete. Reloading to load the new version…', exact=True)).to_be_visible(timeout=13000)
            assert state['update_posts'] == 1 and state['update_status_reads'] >= 3, 'an uncertain update response was not followed by read-only status checks'
            checks.append('uncertain update submission follows status through a temporary server error without resubmitting')
            page.wait_for_function("Number(sessionStorage.getItem('refresh-check-loads')) === %d" % (loads + 1), timeout=10000)
            expect(page.get_by_role('button', name='Update', exact=True)).to_be_visible()
            page.get_by_role('button', name='Update', exact=True).click()
            expect(page.get_by_text('this update could not be confirmed', exact=False)).to_be_visible(timeout=13000)
            assert state['update_posts'] == 2, 'an old done status caused a duplicate update submission'
            checks.append('a previous terminal update status does not falsely confirm a new uncertain request')

            assert int(page.evaluate("sessionStorage.getItem('refresh-check-loads')")) == loads + 1, 'only the update restart reload may navigate the page; an action or background catch-up required full-page navigation'
            assert not errors, errors
            browser.close()
        (args.evidence / 'browser-results.json').write_text(json.dumps({'mode': 'real Chromium against built web/dist with fully mocked API; no live Controller or host', 'checks': checks, 'page_errors': errors}, indent=2) + '\n')
        print(json.dumps(checks, indent=2))
    finally:
        server.shutdown()


if __name__ == '__main__':
    main()
