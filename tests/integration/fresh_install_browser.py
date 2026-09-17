#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Browser-only acceptance against explicitly selected fresh disposable ISO installation.

Private JSON config requires disposable=true, local_url, controller_url,
root_password_file and controller_password_file. deploy creates one Controller;
managed registers existing resources, transfers the ISO and creates the --vm presets through
the six-step wizard; create-vm creates more presets from those resources (--no-wait returns as soon
as the Controller accepts the request, for outage timing); offline checks local recovery, negative
tests rejected/replayed requests, post-reboot verifies persistence. No host commands or networking
changes; a preset may request start after creation. --evidence must be a new directory, never the
committed docs/qa evidence.
"""
import argparse
import hashlib
import json
import re
from pathlib import Path
import time
import uuid
from urllib.parse import urlparse

STORAGE_PATH = '/var/lib/libvirt/images'
# Guest shapes. disks are (size GiB, thin); thick disks are preallocated by Core (falloc).
PRESETS = {
    'guest': {'name': 'fresh-install-guest', 'cpus': 2, 'memory_mib': 1024, 'disks': [(4, True)], 'nics': 1,
              'firmware': 'uefi', 'tpm': 'none', 'iso': True, 'start': False, 'autostart': False},
    'a': {'name': 'gate-vm-a', 'cpus': 2, 'memory_mib': 4096, 'disks': [(20, True), (2, False)], 'nics': 2,
          'firmware': 'uefi', 'tpm': 'none', 'iso': True, 'start': False, 'autostart': True},
    'b': {'name': 'gate-vm-b', 'cpus': 1, 'memory_mib': 1024, 'disks': [(4, True)], 'nics': 1,
          'firmware': 'uefi', 'tpm': 'none', 'iso': False, 'start': True, 'autostart': False},
    'c': {'name': 'gate-vm-c', 'cpus': 1, 'memory_mib': 1024, 'disks': [(4, True)], 'nics': 1,
          'firmware': 'uefi', 'tpm': '2.0', 'iso': False, 'start': False, 'autostart': False},
    'd': {'name': 'gate-vm-d', 'cpus': 1, 'memory_mib': 1024, 'disks': [(4, True)], 'nics': 1,
          'firmware': 'uefi', 'tpm': 'none', 'iso': True, 'start': False, 'autostart': False},
}

# US-layout shifted symbols and the unshifted key that produces each one.
SHIFTED = dict(zip('~!@#$%^&*()_+{}|:"<>?', '`1234567890-=[]\\;\',./'))


def type_into_console(keyboard, text):
    """Type into the focused console canvas with an explicit Shift key event for shifted characters.

    Playwright keyboard.type sends shifted characters without a Shift keydown, so the guest receives
    them unshifted; a physical keyboard sends Shift as its own event, and so does this.
    """
    for char in text:
        base = char.lower() if char.isupper() else SHIFTED.get(char)
        if base is None:
            keyboard.press(char)
            continue
        keyboard.down('Shift')
        keyboard.press(base)
        keyboard.up('Shift')


def main():
    from playwright.sync_api import sync_playwright, expect, Error
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--stage', choices=['deploy', 'managed', 'create-vm', 'offline', 'negative', 'post-reboot'], required=True)
    parser.add_argument('--iso', type=Path)
    parser.add_argument('--evidence', type=Path, required=True)
    parser.add_argument('--vm', action='append', choices=sorted(PRESETS), help='guest preset to create (repeatable); managed defaults to guest')
    parser.add_argument('--firmware', choices=['uefi', 'bios'], help='override firmware for presets other than c (UEFI fallback)')
    parser.add_argument('--no-wait', action='store_true', help='create-vm: return once the Controller accepts each request')
    parser.add_argument('--site-name', default='Fresh install site', help='expected single site name (a replacement Controller has its own site)')
    parser.add_argument('--replay-vm', default='fresh-install-guest', help='negative: revision-0 guest whose create request is replayed')
    args = parser.parse_args()
    if args.stage == 'managed' and not args.vm:
        args.vm = ['guest']
    if args.stage == 'create-vm' and not args.vm:
        parser.error('create-vm requires --vm')
    if args.stage not in ['managed', 'create-vm'] and (args.vm or args.no_wait or args.firmware):
        parser.error('--vm, --firmware and --no-wait apply only to managed and create-vm')
    results_name = 'browser-' + args.stage + ('-' + '-'.join(args.vm) if args.stage == 'create-vm' else '') + '-results.json'
    assert not (args.evidence / results_name).exists(), 'refusing to overwrite existing evidence ' + results_name
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
            filename = results_name.replace('-results.json', '-' + name + '.png')
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
                # An external replacement Controller leaves the local appliance shut off; any running VM proves the local inventory.
                assert any(v['state'] == 'running' for v in vms)
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
            for later in sorted(args.evidence.glob('browser-create-vm-*-results.json')):
                previous['vms'] += [v for v in json.loads(later.read_text())['vms'] if v['id'] not in {p['id'] for p in previous['vms']}]
            login(True)
            local_vms = api('/api/local/v1/vms', True)
            expect(page.get_by_role('heading', name=config['hostname'], exact=True)).to_be_visible()
            capture('local')
            login(False)
            hosts, vms = api('/api/v1/hosts'), api('/api/v1/vms')
            assert [h['id'] for h in hosts] == [h['id'] for h in previous['hosts']]
            assert all(h['connection'] == 'fresh' for h in hosts)
            assert {v['id'] for v in vms} == {v['id'] for v in previous['vms']}
            guests = [v for v in vms if not v['protected']]
            appliance = next(v for v in vms if v['protected'])
            # A guest with autostart must be running again; any other must be shut off.
            for guest in guests:
                assert guest['freshness'] == 'fresh' and guest['state'] == ('running' if guest['spec']['autostart'] else 'shut off'), guest
            assert appliance['state'] == 'running' and appliance['freshness'] == 'fresh'
            guest = guests[0]
            page.goto(config['controller_url'] + '/#/vms/' + guest['id'])
            expect(page.get_by_role('heading', name='VM details', exact=True)).to_be_visible()
            capture('central')
            passed('Post-reboot root/central login, original enrolled host and all VM identities, running protected appliance, autostart guests running and other guests shut off')
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
            vm = next(v for v in before['vms'] if v['name'] == args.replay_vm and v['spec'].get('revision', 0) == 0)
            payload = {**vm['spec'], 'name': vm['name'], 'pool_id': vm['pool_id'], 'host_id': vm['host_id']}
            payload.pop('id', None)
            # MACs the wizard left blank are assigned on create; the original request carried none.
            payload['nics'] = [{**n, 'mac': ''} for n in payload['nics']]
            wrong = {**payload, 'request_id': str(uuid.uuid4()), 'name': 'rejected-scope',
                     'nics': [{**payload['nics'][0], 'network_id': 'nonexistent-network'}]}
            assert page.request.post(url + '/api/v1/vms', data=wrong, headers=headers).status == 404
            operation = next(o for o in api('/api/v1/operations') if o['id'] == vm['operation_id'])
            replay = page.request.post(url + '/api/v1/vms', data={**payload, 'request_id': operation['request_id']}, headers=headers)
            assert replay.status == 200 and replay.json()['vm_id'] == vm['id']
            changed = {**payload, 'request_id': operation['request_id'], 'cpu': {**payload['cpu'], 'count': payload['cpu']['count'] + 1}}
            assert page.request.post(url + '/api/v1/vms', data=changed, headers=headers).status == 409
            after = {kind: api('/api/v1/' + kind) for kind in before}
            assert {kind: {r['id'] for r in rows} for kind, rows in before.items()} == {kind: {r['id'] for r in rows} for kind, rows in after.items()}
            passed('Native packaged API rejects anonymous access401, missing CSRF403, invalid create/ISO400 and nonexistent network404; invalid media publishes nothing; exact create replay preserves UUID and changed replay409')
            result = {'resource_counts': {kind: len(rows) for kind, rows in after.items()}, 'replayed_vm_id': vm['id']}
        else:
            login(False)
            sites, pools, hosts = api('/api/v1/sites'), api('/api/v1/pools'), api('/api/v1/hosts')
            assert len(sites) == len(pools) == len(hosts) == 1
            assert sites[0]['name'] == args.site_name
            assert pools[0]['name'] == hosts[0]['hostname'] and pools[0]['master_host_id'] == hosts[0]['id'], pools
            assert hosts[0]['connection'] == 'fresh' and 'managed-vm-v1' in hosts[0]['capabilities']
            pool_id, host_id = pools[0]['id'], hosts[0]['id']
            def pool(tab):
                page.goto(config['controller_url'] + '/#/pools/' + pool_id)
                page.get_by_role('navigation', name='Resource views').get_by_role('button', name=tab, exact=True).click()
            def wait_row(name, marker='succeeded'):
                row = page.locator('tr:not([aria-label="ISO upload to Controller"])').filter(has_text=re.compile('^' + re.escape(name)))
                expect(row).to_contain_text(marker, timeout=180000)
            def register(tab, kind, name, fill, match):
                # A fresh enrollment may already hold the default directory; reuse it on 409.
                pool(tab)
                # Core registers its own Local storage and untagged bridge network at enrollment
                # (guide steps 2-3); registering the same path/bridge again is a failed host task.
                owned = [r for r in api('/api/v1/' + kind) if r['host_id'] == host_id and r['status'] == 'succeeded' and match(r)]
                if owned:
                    wait_row(owned[0]['name'], 'healthy' if kind == 'storage' else 'succeeded')
                    capture(tab.lower())
                    return owned[0]['name']
                button = 'Register storage' if kind == 'storage' else 'Register network'
                page.get_by_role('button', name=button, exact=True).click()
                form = page.get_by_role('region', name=button, exact=True)
                form.get_by_label('Name', exact=True).fill(name)
                fill(form)
                with page.expect_response(lambda r: r.url.endswith('/api/v1/' + kind) and r.request.method == 'POST') as received:
                    page.get_by_role('button', name='Register and check access').click()
                if received.value.status == 409:
                    name = next(r['name'] for r in api('/api/v1/' + kind) if r['host_id'] == host_id and match(r))
                    page.get_by_role('button', name='Close', exact=True).click()
                else:
                    assert received.value.ok, received.value.text()
                    name = received.value.json()['name']
                wait_row(name, 'healthy' if kind == 'storage' else 'succeeded')
                capture(tab.lower())
                return name
            if args.stage == 'managed':
                assert args.iso and args.iso.is_file(), 'managed stage requires actual guest ISO'
                original = api('/api/v1/vms')
                assert len(original) == 1 and original[0]['protected'] and original[0]['role'] == 'controller'
                passed('Central keyboard login verifies named workspace, fresh capable enrollment and exactly one protected adopted Controller appliance')
                pool('Virtual machines')
                capture('adopted-appliance')
                def storage_form(form):
                    form.locator('#storage-host').select_option(host_id)
                    form.get_by_label('Repository type').select_option('local')
                    form.get_by_label('Existing directory').fill(STORAGE_PATH)
                def network_form(form):
                    form.locator('select[name=host_id]').select_option(host_id)
                    form.get_by_label('Connection').select_option('custom')
                    form.get_by_label('Network type').select_option('bridge')
                    form.get_by_label('Existing network or bridge').fill('br0')
                storage_name = register('Storage', 'storage', 'Fresh local storage', storage_form, lambda r: r['path'] == STORAGE_PATH)
                network_name = register('Networking', 'networks', 'Fresh bridge', network_form, lambda r: r['source'] == 'br0')
                iso_name = args.iso.name
                pool('ISO library')
                # A rerun after a later failure reuses the ISO an earlier attempt already imported.
                if not [m for m in api('/api/v1/media') if m['host_id'] == host_id and m['name'] == iso_name and m['status'] == 'succeeded']:
                    page.get_by_role('button', name='Upload ISO', exact=True).click()
                    page.get_by_label('Destination storage').select_option(label=next(t for t in page.get_by_label('Destination storage').locator('option').all_text_contents() if t.startswith(storage_name + ' ·')))
                    page.get_by_label('ISO file', exact=True).set_input_files(str(args.iso))
                    page.get_by_role('button', name='Upload and transfer ISO').click()
                    wait_row(iso_name)
                    capture('iso')
                    page.get_by_role('button', name='Close', exact=True).click()
                else:
                    wait_row(iso_name)
                    capture('iso-reused')
            else:
                managed = json.loads((args.evidence / 'browser-managed-results.json').read_text())
                storage_name, network_name, iso_name = managed['storage_name'], managed['network_name'], managed['iso_name']
            created = []
            for key in args.vm:
                preset = {**PRESETS[key]}
                if args.firmware and key != 'c':
                    preset['firmware'] = args.firmware
                name, tag = preset['name'], key + '-'
                assert not [v for v in api('/api/v1/vms') if v['name'] == name], name + ' already exists'
                pool('Virtual machines')
                page.get_by_role('button', name='Create VM', exact=True).click()
                page.get_by_label('VM name', exact=True).fill(name)
                page.get_by_label('Placement host').select_option(host_id)
                page.get_by_role('button', name='Next', exact=True).focus()
                page.keyboard.press('Enter')
                expect(page.get_by_role('heading', name='Create VM · 2 of 6:', exact=False)).to_be_focused()
                page.get_by_label('Virtual CPUs', exact=True).fill(str(preset['cpus']))
                page.get_by_label('Memory (MiB)', exact=True).fill(str(preset['memory_mib']))
                page.get_by_role('button', name='Next', exact=True).click()
                expect(page.get_by_role('heading', name='Create VM · 3 of 6:', exact=False)).to_be_focused()
                for i, (size, thin) in enumerate(preset['disks']):
                    if i:
                        page.get_by_role('button', name='Add disk', exact=True).click()
                    disk = page.get_by_role('group', name=f'Disk {i + 1}', exact=True)
                    disk.get_by_label('Storage').select_option(label=next(t for t in disk.get_by_label('Storage').locator('option').all_text_contents() if t.startswith(storage_name + ' ·')))
                    disk.get_by_label('Size (GiB)', exact=True).fill(str(size))
                    disk.get_by_label('Provisioning').select_option('thin' if thin else 'thick')
                page.get_by_role('button', name='Next', exact=True).click()
                expect(page.get_by_role('heading', name='Create VM · 4 of 6:', exact=False)).to_be_focused()
                for i in range(preset['nics']):
                    if i:
                        page.get_by_role('button', name='Add network interface', exact=True).click()
                    network = page.get_by_role('group', name=f'Network interface {i + 1}', exact=True).get_by_role('combobox', name='Network')
                    network.select_option(label=next(t for t in network.locator('option').all_text_contents() if t.startswith(network_name + ' ·')))
                page.get_by_role('button', name='Next', exact=True).click()
                expect(page.get_by_role('heading', name='Create VM · 5 of 6:', exact=False)).to_be_focused()
                page.get_by_label('Firmware').select_option(preset['firmware'])
                if preset['firmware'] == 'uefi':
                    page.get_by_role('combobox', name=re.compile('^TPM')).select_option(preset['tpm'])
                if preset['iso']:
                    page.get_by_label('Install ISO').select_option(label=iso_name)
                    expect(page.get_by_role('group', name='Boot order', exact=True).locator('li').first).to_contain_text('Install media')
                page.get_by_role('button', name='Next', exact=True).click()
                expect(page.get_by_role('heading', name='Create VM · 6 of 6:', exact=False)).to_be_focused()
                page.get_by_label('Start after creation').set_checked(preset['start'])
                page.get_by_label('Start automatically when the host boots').set_checked(preset['autostart'])
                capture(tag + 'review-desktop')
                page.set_viewport_size({'width': 360, 'height': 800})
                assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
                capture(tag + 'review-360')
                page.set_viewport_size({'width': 1440, 'height': 1000})
                with page.expect_response(lambda r: r.url.endswith('/api/v1/vms') and r.request.method == 'POST') as received:
                    page.get_by_role('button', name='Create and start VM' if preset['start'] else 'Create stopped VM', exact=True).click()
                assert received.value.ok, received.value.text()
                vm_id = received.value.json()['vm_id']
                submitted_at = time.time()
                if args.no_wait:
                    created.append({'preset': key, 'vm_id': vm_id, 'submitted_at': submitted_at})
                    passed(f'GUI submitted {name} ({vm_id}) without waiting for the host outcome')
                    continue
                # Until the host reports the VM the page shows the Creation panel (guide step 6),
                # so VM details appear only after the create task leaves the queue.
                expect(page.get_by_role('heading', name='VM details', exact=True)).to_be_visible(timeout=300000)
                details = page.locator('.details')
                expect(details).to_contain_text('succeeded', timeout=300000)
                expect(details).to_contain_text('running' if preset['start'] else 'shut off', timeout=120000)
                tpm = 'TPM 2.0' if preset['firmware'] == 'uefi' and preset['tpm'] == '2.0' else 'None'
                for label, value in [('Virtual CPUs', str(preset['cpus'])), ('Memory', f"{preset['memory_mib']} MiB"), ('Firmware', preset['firmware'].upper()),
                                     ('TPM', tpm), ('Autostart', 'Starts when' if preset['autostart'] else 'Does not start'), ('Install ISO', iso_name if preset['iso'] else 'None')]:
                    expect(page.locator('dt').filter(has_text=re.compile('^' + label + '$')).locator('xpath=following-sibling::dd[1]')).to_contain_text(value)
                capture(tag + 'created-desktop')
                page.set_viewport_size({'width': 360, 'height': 800})
                assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
                capture(tag + 'created-360')
                page.set_viewport_size({'width': 1440, 'height': 1000})
                matches = [v for v in api('/api/v1/vms') if v['name'] == name]
                assert len(matches) == 1 and matches[0]['id'] == vm_id, matches
                vm = matches[0]
                spec = vm['spec']
                assert vm['pool_id'] == pool_id and vm['host_id'] == host_id and vm['freshness'] == 'fresh', vm
                assert [(d['size_gib'], d['thin']) for d in spec['disks']] == [tuple(d) for d in preset['disks']], spec['disks']
                assert len(spec['nics']) == preset['nics'] and spec['boot']['firmware'] == preset['firmware'] and spec['autostart'] == preset['autostart'], spec
                assert (spec['boot'].get('tpm') == '2.0') == (preset['firmware'] == 'uefi' and preset['tpm'] == '2.0'), spec['boot']
                created.append({'preset': key, 'vm_id': vm_id, 'submitted_at': submitted_at})
                passed(f"GUI six-step wizard created exactly one {name}: {preset['cpus']} vCPU, {preset['memory_mib']} MiB, disks {preset['disks']}, {preset['nics']} NIC(s), {preset['firmware'].upper()}, TPM {tpm}, autostart {preset['autostart']}, start after create {preset['start']}; desktop/360px/keyboard verified")
            result = {'sites': sites, 'pools': pools, 'hosts': hosts, 'vms': api('/api/v1/vms'), 'created': created,
                      'storage_name': storage_name, 'network_name': network_name, 'iso_name': iso_name}
            if args.stage == 'managed':
                result['iso_sha256'] = hashlib.file_digest(args.iso.open('rb'), 'sha256').hexdigest()
        assert not errors, errors
        result.update({'stage': args.stage, 'checks': checks, 'captures': captures, 'browser': browser.version, 'environment': 'Actual fresh ISO-installed nested KVM/libvirt with bundled systemd services; loopback forwards; no API mocking', 'artifacts': config.get('artifacts', {}), 'api_impact': 'none: browser exercises existing local deployment/session, Controller inventory, storage/network registration, ISO transfer and VM creation contracts without changing them', 'guide_impact': 'none: verifies existing documented workflows; parent records updated evidence links'})
        (args.evidence / results_name).write_text(json.dumps(result, indent=2) + '\n')
        browser.close()


if __name__ == '__main__':
    main()
