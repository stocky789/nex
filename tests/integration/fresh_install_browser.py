#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Browser-only acceptance against explicitly selected fresh disposable ISO installation.

Private JSON config requires disposable=true, local_url, controller_url,
root_password_file and controller_password_file. deploy creates one Controller;
managed registers existing resources and creates one stopped guest; offline checks
local recovery, negative tests rejected/replayed requests, post-reboot verifies persistence. No host commands, networking changes or guest power actions.
"""
import argparse
import hashlib
import json
from pathlib import Path
import time
import uuid
from urllib.parse import urlparse


def main():
    from playwright.sync_api import sync_playwright, expect, Error
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--stage', choices=['deploy', 'managed', 'offline', 'negative', 'post-reboot'], required=True)
    parser.add_argument('--iso', type=Path)
    parser.add_argument('--evidence', type=Path, required=True)
    args = parser.parse_args()
    config = json.loads(args.config.read_text())
    assert config.get('disposable') is True, 'explicit disposable lab selection required'
    for key in ['local_url', 'controller_url']:
        assert urlparse(config[key]).hostname in ['127.0.0.1', 'localhost'], 'loopback lab forwards required'
    args.evidence.mkdir(parents=True, exist_ok=True)
    checks, captures = [], []
    def password(key):
        path = Path(config[key + '_password_file'])
        if not path.is_absolute():
            path = args.config.parent / path
        assert path.stat().st_mode & 0o077 == 0, 'private credential permissions required'
        return path.read_text().rstrip('\n')
    def passed(message):
        checks.append(message)
        print('PASS:', message, flush=True)
    with sync_playwright() as pw:
        browser = pw.chromium.launch(headless=True)
        page = browser.new_page(ignore_https_errors=True, viewport={'width': 1440, 'height': 1000})
        errors = []
        page.on('pageerror', lambda error: errors.append(str(error)))
        def capture(name):
            filename = 'browser-' + args.stage + '-' + name + '.png'
            page.screenshot(path=str(args.evidence / filename), full_page=True, mask=[page.locator('input[type=password]')])
            captures.append(filename)
        def api(path, local=False):
            response = page.request.get(config['local_url' if local else 'controller_url'] + path)
            assert response.ok, f'GET {path}: {response.status}'
            return response.json()
        def login(local):
            page.goto(config['local_url' if local else 'controller_url'])
            if local:
                expect(page.get_by_label('Username', exact=True)).to_have_value('root')
                expect(page.get_by_label('Password', exact=True)).to_be_focused()
            else:
                expect(page.get_by_label('Username', exact=True)).to_be_focused()
                page.keyboard.type('root')
                page.keyboard.press('Tab')
            page.get_by_label('Password', exact=True).fill(password('root' if local else 'controller'))
            page.keyboard.press('Enter')
            expect(page.get_by_role('button', name='Sign out', exact=True)).to_be_visible(timeout=30000)
        if args.stage in ['deploy', 'offline']:
            login(True)
            capture('local-login-desktop')
            host = api('/api/local/v1/host', True)
            deployments = api('/api/local/v1/deployments', True)
            if args.stage == 'offline':
                try:
                    page.request.get(config['controller_url'] + '/api/v1/session', timeout=3000)
                except Error:
                    controller_unreachable = True
                else:
                    controller_unreachable = False
                assert controller_unreachable, 'Controller is still reachable; parent must stop its service first'
                assert deployments and deployments[0]['status'] == 'ready'
                vms = api('/api/local/v1/vms', True)
                assert any(v['managed'] and v['state'] == 'running' for v in vms)
                page.set_viewport_size({'width': 360, 'height': 800})
                assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
                capture('local-login-360')
                passed('Fresh root login and local running VM inventory remain available during parent-controlled real Controller service outage')
                result = {'host': host, 'vms': vms}
            else:
                assert not deployments, 'fresh deployment stage refuses existing appliance'
                assert not api('/api/local/v1/vms', True), 'fresh host must have no domains'
                assert host['preflight']['ready'], host['preflight']
                page.get_by_role('button', name='Deploy Controller', exact=True).click()
                expect(page.get_by_label('Local storage', exact=True)).to_have_value('/var/lib/libvirt/images')
                page.get_by_label('LAN bridge', exact=True).select_option('br0')
                page.get_by_label('IP configuration', exact=True).select_option('static')
                for label, value in [('IPv4 address / prefix', '10.0.2.20/24'), ('IPv4 gateway', '10.0.2.2'), ('DNS servers', '10.0.2.3'), ('Site name', 'Fresh install site')]:
                    page.get_by_label(label, exact=True).fill(value)
                page.get_by_label('Controller administrator password', exact=True).fill(password('controller'))
                page.get_by_role('button', name='Review deployment', exact=False).focus()
                page.keyboard.press('Enter')
                expect(page.get_by_role('heading', name='Ready to create your Controller')).to_be_visible()
                capture('review-desktop')
                page.set_viewport_size({'width': 360, 'height': 800})
                assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
                capture('review-360')
                page.set_viewport_size({'width': 1440, 'height': 1000})
                page.get_by_role('button', name='Create VM & install Controller', exact=True).click()
                states = []
                deadline = time.monotonic() + 900
                while time.monotonic() < deadline:
                    deployments = api('/api/local/v1/deployments', True)
                    assert len(deployments) <= 1
                    if deployments:
                        operation = deployments[0]
                        state = (operation.get('stage'), operation['status'], operation.get('progress'))
                        if not states or list(state) != states[-1]:
                            states.append(list(state))
                            print('Deployment:', state, flush=True)
                            capture('progress-' + str(len(states)))
                        if operation['status'] in ['ready', 'failed', 'needs_attention']:
                            break
                    page.wait_for_timeout(2000)
                else:
                    raise AssertionError('deployment did not complete in 15 minutes')
                result = {'host': host, 'operation': operation, 'observed_progress': states}
                (args.evidence / 'browser-deployment-state.json').write_text(json.dumps(result, indent=2) + '\n')
                assert operation['status'] == 'ready', operation
                page.reload()
                expect(page.get_by_role('link', name='Open Nex Controller', exact=False)).to_be_visible()
                capture('ready')
                passed('Fresh empty installed host deployed bundled Controller through static-network GUI, named the site and reached ready')
        elif args.stage == 'post-reboot':
            previous = json.loads((args.evidence / 'browser-managed-results.json').read_text())
            login(True)
            local_vms = api('/api/local/v1/vms', True)
            expect(page.get_by_role('heading', name=config['hostname'], exact=True)).to_be_visible()
            capture('local')
            login(False)
            hosts, vms = api('/api/v1/hosts'), api('/api/v1/vms')
            assert [h['id'] for h in hosts] == [h['id'] for h in previous['hosts']]
            assert all(h['connection'] == 'fresh' for h in hosts)
            assert {v['id'] for v in vms} == {v['id'] for v in previous['vms']}
            guest = next(v for v in vms if v['name'] == 'fresh-install-guest')
            appliance = next(v for v in vms if v['protected'])
            assert guest['state'] == 'shut off' and guest['freshness'] == 'fresh'
            assert appliance['state'] == 'running' and appliance['freshness'] == 'fresh'
            page.goto(config['controller_url'] + '/#/vms/' + guest['id'])
            expect(page.get_by_role('heading', name='VM details', exact=True)).to_be_visible()
            capture('central')
            passed('Post-reboot root/central login, original enrolled host and both VM identities, running protected appliance and fresh stopped guest persistence')
            result = {'hosts': hosts, 'vms': vms, 'local_vms': local_vms}
        elif args.stage == 'negative':
            login(False)
            url = config['controller_url']
            before = {kind: api('/api/v1/' + kind) for kind in ['storage', 'networks', 'media', 'vms']}
            anonymous = pw.request.new_context(ignore_https_errors=True)
            for kind in ['storage', 'media', 'vms']:
                assert anonymous.get(url + '/api/v1/' + kind).status == 401
            anonymous.dispose()
            assert page.request.post(url + '/api/v1/storage', data={}, headers={'Origin': url}).status == 403
            headers = {'Origin': url, 'X-CSRF-Token': api('/api/v1/session')['csrf_token']}
            assert page.request.post(url + '/api/v1/vms', data={'request_id': str(uuid.uuid4())}, headers=headers).status == 400
            assert page.request.post(url + '/api/v1/media', params={'storage_id': before['storage'][0]['id'], 'request_id': str(uuid.uuid4()), 'name': 'invalid.iso'}, data=b'not an ISO', headers=headers).status == 400
            vm = next(v for v in before['vms'] if v['name'] == 'fresh-install-guest')
            payload = {**vm['spec'], 'name': vm['name'], 'pool_id': vm['pool_id'], 'host_id': vm['host_id']}
            payload.pop('id', None)
            wrong = {**payload, 'request_id': str(uuid.uuid4()), 'name': 'rejected-scope',
                     'nics': [{**payload['nics'][0], 'network_id': 'nonexistent-network'}]}
            assert page.request.post(url + '/api/v1/vms', data=wrong, headers=headers).status == 404
            operation = next(o for o in api('/api/v1/operations') if o['id'] == vm['operation_id'])
            replay = page.request.post(url + '/api/v1/vms', data={**payload, 'request_id': operation['request_id']}, headers=headers)
            assert replay.status == 200 and replay.json()['vm_id'] == vm['id']
            changed = {**payload, 'request_id': operation['request_id'], 'cpu': {**payload['cpu'], 'count': 1}}
            assert page.request.post(url + '/api/v1/vms', data=changed, headers=headers).status == 409
            after = {kind: api('/api/v1/' + kind) for kind in before}
            assert {kind: {r['id'] for r in rows} for kind, rows in before.items()} == {kind: {r['id'] for r in rows} for kind, rows in after.items()}
            passed('Native packaged API rejects anonymous access401, missing CSRF403, invalid create/ISO400 and nonexistent network404; invalid media publishes nothing; exact create replay preserves UUID and changed replay409')
            result = {'resource_counts': {kind: len(rows) for kind, rows in after.items()}, 'replayed_vm_id': vm['id']}
        else:
            assert args.iso and args.iso.is_file(), 'managed stage requires actual guest ISO'
            login(False)
            sites, pools, hosts = api('/api/v1/sites'), api('/api/v1/pools'), api('/api/v1/hosts')
            assert len(sites) == len(pools) == len(hosts) == 1
            assert sites[0]['name'] == 'Fresh install site'
            assert pools[0]['name'] == hosts[0]['hostname'] and pools[0]['master_host_id'] == hosts[0]['id'], pools
            assert hosts[0]['connection'] == 'fresh' and 'managed-vm-v1' in hosts[0]['capabilities']
            pool_id, host_id = pools[0]['id'], hosts[0]['id']
            original = api('/api/v1/vms')
            assert len(original) == 1 and original[0]['protected'] and original[0]['role'] == 'controller'
            passed('Central keyboard login verifies named workspace, fresh capable enrollment and exactly one protected adopted Controller appliance')
            def pool(tab):
                page.goto(config['controller_url'] + '/#/pools/' + pool_id)
                page.get_by_role('navigation', name='Resource views').get_by_role('button', name=tab, exact=True).click()
            def wait_row(name):
                expect(page.locator('tr').filter(has_text=name)).to_contain_text('succeeded', timeout=180000)
            pool('Virtual machines')
            capture('adopted-appliance')
            for tab, button, name in [('Storage', 'Register local storage', 'Fresh local storage'), ('Networking', 'Register network', 'Fresh bridge')]:
                pool(tab)
                page.get_by_role('button', name=button, exact=True).click()
                form = page.get_by_role('region', name=button, exact=True)
                form.get_by_label('Name', exact=True).fill(name)
                form.locator('select[name=host_id]').select_option(host_id)
                if tab == 'Storage':
                    form.get_by_label('Existing directory').fill('/var/lib/libvirt/images')
                else:
                    form.get_by_label('Connection').select_option('custom')
                    form.get_by_label('Network type').select_option('bridge')
                    form.get_by_label('Existing network or bridge').fill('br0')
                page.get_by_role('button', name='Register and check access').click()
                wait_row(name)
                capture(tab.lower())
            pool('ISO library')
            page.get_by_role('button', name='Upload ISO', exact=True).click()
            page.get_by_label('Destination storage').select_option(index=1)
            page.get_by_label('ISO file', exact=True).set_input_files(str(args.iso))
            page.get_by_role('button', name='Upload and transfer ISO').click()
            wait_row(args.iso.name)
            capture('iso')
            page.get_by_role('button', name='Close', exact=True).click()
            pool('Virtual machines')
            page.get_by_role('button', name='Create VM', exact=True).click()
            page.get_by_label('VM name', exact=True).fill('fresh-install-guest')
            page.get_by_label('Placement host').select_option(host_id)
            page.get_by_role('button', name='Next', exact=True).focus()
            page.keyboard.press('Enter')
            expect(page.get_by_role('heading', name='Create VM · 2 of 6:', exact=False)).to_be_focused()
            page.get_by_label('Virtual CPUs', exact=True).fill('2')
            page.get_by_label('Memory (MiB)', exact=True).fill('1024')
            page.get_by_role('button', name='Next', exact=True).click()
            disk = page.get_by_role('group', name='Disk 1', exact=True)
            disk.get_by_label('Storage').select_option(index=1)
            disk.get_by_label('Size (GiB)', exact=True).fill('4')
            page.get_by_role('button', name='Next', exact=True).click()
            page.get_by_role('group', name='Network interface 1', exact=True).get_by_label('Network').select_option(index=1)
            page.get_by_role('button', name='Next', exact=True).click()
            page.get_by_label('Firmware').select_option('uefi')
            page.get_by_label('Install ISO').select_option(label=args.iso.name)
            boot = page.get_by_role('group', name='Boot order', exact=True)
            boot.locator('li').filter(has_text='Install media').get_by_role('button').first.click()
            expect(boot.locator('li').first).to_contain_text('Install media')
            page.get_by_role('button', name='Next', exact=True).click()
            expect(page.get_by_text('created stopped', exact=False)).to_be_visible()
            capture('review-desktop')
            page.set_viewport_size({'width': 360, 'height': 800})
            assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
            capture('review-360')
            page.set_viewport_size({'width': 1440, 'height': 1000})
            page.get_by_role('button', name='Create stopped VM').click()
            expect(page.get_by_role('heading', name='VM details', exact=True)).to_be_visible()
            expect(page.locator('.details')).to_contain_text('succeeded', timeout=180000)
            expect(page.locator('.details')).to_contain_text('shut off', timeout=30000)
            for label, value in [('Virtual CPUs', '2'), ('Memory', '1024 MiB'), ('Disks', '4 GiB'), ('Install ISO', args.iso.name), ('Firmware', 'UEFI')]:
                expect(page.locator('dt').filter(has_text=label).locator('xpath=following-sibling::dd[1]')).to_contain_text(value)
            capture('created-desktop')
            page.set_viewport_size({'width': 360, 'height': 800})
            assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
            capture('created-360')
            vms = api('/api/v1/vms')
            assert len(vms) == 2 and len([v for v in vms if v['name'] == 'fresh-install-guest']) == 1
            vm = next(v for v in vms if v['name'] == 'fresh-install-guest')
            assert vm['pool_id'] == pool_id and vm['host_id'] == host_id and vm['state'] == 'shut off' and vm['freshness'] == 'fresh'
            passed('GUI registers existing local storage/br0, transfers genuine ISO and creates exactly one stopped UEFI VM; desktop/360px/keyboard verified')
            result = {'sites': sites, 'pools': pools, 'hosts': hosts, 'vms': vms, 'iso_sha256': hashlib.file_digest(args.iso.open('rb'), 'sha256').hexdigest()}
        assert not errors, errors
        result.update({'stage': args.stage, 'checks': checks, 'captures': captures, 'browser': browser.version, 'environment': 'Actual fresh ISO-installed nested KVM/libvirt with bundled systemd services; loopback forwards; no API mocking', 'artifacts': config.get('artifacts', {}), 'api_impact': 'existing contract: browser exercises local deployment/session, Controller inventory, storage/network registration, ISO transfer and VM creation without changing interfaces', 'guide_impact': 'none: verifies existing documented workflows; parent records updated evidence links'})
        (args.evidence / ('browser-' + args.stage + '-results.json')).write_text(json.dumps(result, indent=2) + '\n')
        browser.close()


if __name__ == '__main__':
    main()
