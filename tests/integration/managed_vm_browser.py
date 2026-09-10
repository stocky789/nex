#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Real Controller/browser acceptance; --simulated uses an explicitly simulated mTLS agent.

Native mode: --config <private browser.json> --iso <guest.iso>.
Neither mode changes host networking or starts a guest. Native mode creates one
stopped VM and registers resources only in the selected disposable lab.
"""
import re
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import shutil
import ssl
import tempfile
import threading
import urllib.request

from phase1 import Lab, ROOT, run, eventually


def simulated(lab):
    go = os.environ.get('GO') or shutil.which('go') or ROOT / '.cache/toolchains/go/bin/go'
    for name, package in [('controller', 'nex-controller')]:
        run(go, 'build', '-o', lab.path / name, './cmd/' + package, cwd=ROOT)
    cert, key = lab.path / 'tls.pem', lab.path / 'tls.key'
    run('openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256',
        '-nodes', '-keyout', key, '-out', cert, '-days', '1', '-subj', '/CN=localhost',
        '-addext', 'subjectAltName=DNS:localhost')
    password = lab.path / 'password'
    password.write_text('Disposable-browser-qa-9284!')
    password.chmod(0o600)
    source = lab.path / 'source.tar.gz'
    run('python3', ROOT / 'scripts/build-source.py', '--output', source, env={**os.environ, 'GO': str(go)}, cwd=ROOT)
    lab.start('controller', lab.path / 'controller', '-listen', f'127.0.0.1:{lab.port}',
        '-data-dir', lab.path / 'controller-state', '-tls-cert', cert, '-tls-key', key,
        '-admin-password-file', password, '-ui-dir', ROOT / 'web/dist', '-source-archive', source, '-stale-after', '2s')
    lab.client(cert)
    eventually(lambda: lab.api('/api/v1/sites', expected=401), 'controller ready')
    lab.csrf = lab.api('/api/v1/session', {'username': 'root', 'password': password.read_text()})['csrf_token']
    site = lab.api('/api/v1/sites', {'name': 'Browser QA'}, expected=201)
    token = lab.path / 'token'
    token.write_text(lab.api('/api/v1/enrollment-tokens', {'site_id': site['id']}, expected=201)['token'])
    token.chmod(0o600)
    client_cert, client_key = lab.path / 'client.pem', lab.path / 'client.key'
    csr = lab.path / 'client.csr'
    run('openssl', 'req', '-new', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256',
        '-nodes', '-keyout', client_key, '-out', csr, '-subj', '/CN=simulated-browser-host')
    state = lab.api('/api/v1/agent/enroll', {'version': 1, 'token': token.read_text(),
        'csr_pem': csr.read_text(), 'hostname': 'simulated-browser-host'})
    client_cert.write_text(state['certificate_pem'])
    client_key.chmod(0o600)
    # Enrollment created the pool: named after the host, with the host as pool master.
    pool = next(p for p in lab.api('/api/v1/pools') if p['site_id'] == site['id'])
    assert pool['name'] == 'simulated-browser-host' and pool['master_host_id'] == state['host_id'], pool
    assert next(h for h in lab.api('/api/v1/hosts') if h['id'] == state['host_id'])['pool_id'] == pool['id']
    context = ssl.create_default_context(cafile=str(cert))
    context.load_cert_chain(client_cert, client_key)
    stopped = threading.Event()
    failures = []
    # control lets a walkthrough change what the simulated host observes and applies.
    control = {'state': 'shut off'}
    specs = {}
    def reports():
        sequence, results, vms = 0, [], []
        while not stopped.wait(.2):
            try:
                sequence += 1
                now = datetime.now(timezone.utc).isoformat()
                for v in vms:
                    v['state'] = control['state']
                payload = {'version': 1, 'sequence': sequence, 'capabilities': ['managed-vm-v1', 'vm-update-v1'], 'results': results,
                        'inventory': {'observed_at': now, 'hostname': 'simulated-browser-host', 'cpu_count': 8, 'cpu_models': ['qemu64', 'Skylake-Server'],
                        'memory_bytes': 16 * 1024**3, 'vms': vms, 'managed': {'vms': list(specs.values())}, 'checks': [{'name': 'libvirt', 'status': 'healthy', 'observed_at': now, 'last_success': now}], 'nics': [{'name': 'nex1', 'mac': '52:54:00:00:00:11', 'kind': 'physical', 'state': 'up', 'addresses': []}], 'disks': []}}
                request = urllib.request.Request(lab.url + '/api/v1/agent/report', data=json.dumps(payload).encode(), headers={'Content-Type': 'application/json'})
                with urllib.request.urlopen(request, context=context, timeout=5) as response:
                    answer = json.load(response)
                results = []
                for command in answer.get('commands') or []:
                    results.append({'operation_id': command['operation_id'], 'status': 'succeeded', 'observed_at': now})
                    if command.get('vm'):
                        vm = command['vm']
                        # The host journals the definition and reports it as current; an edit
                        # replaces the earlier revision, which is what the mirror follows.
                        specs[vm['id']] = vm
                        if not any(v['id'] == vm['id'] for v in vms):
                            vms.append({'id': vm['id'], 'name': vm['name'], 'state': control['state']})
                        else:
                            next(v for v in vms if v['id'] == vm['id'])['name'] = vm['name']
            except Exception as exc:
                failures.append(str(exc))
                return
    thread = threading.Thread(target=reports, daemon=True)
    thread.start()
    eventually(lambda: any(h['connection'] == 'fresh' for h in lab.api('/api/v1/hosts')), 'simulated agent fresh')
    iso = lab.path / 'browser.iso'
    data = bytearray(65536)
    data[32768:32775] = b'\x01CD001\x01'
    iso.write_bytes(data)
    return {'url': lab.url, 'pool_id': pool['id'], 'host_id': state['host_id'], 'storage_path': '/simulated/browser', 'control': control}, password.read_text(), iso, stopped, thread, failures


def acceptance(config, password, iso, evidence, mode):
    from playwright.sync_api import sync_playwright, expect
    checks, captures = [], []
    suffix = datetime.now().strftime('%H%M%S')
    storage_name, network_name, vm_name = 'Browser local storage', 'Browser guest network', 'browser-vm-' + suffix
    def passed(message):
        checks.append(message)
        print('PASS:', message, flush=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        page = browser.new_page(ignore_https_errors=True, viewport={'width': 1440, 'height': 1000})
        errors = []
        page.on('pageerror', lambda error: errors.append(str(error)))
        def capture(name):
            filename = 'browser-' + mode + '-' + name + '.png'
            page.screenshot(path=str(evidence / filename), full_page=True)
            captures.append(filename)
        def pool(tab):
            page.goto(config['url'] + '/#/pools/' + config['pool_id'])
            page.get_by_role('navigation', name='Resource views').get_by_role('button', name=tab, exact=True).click()
        def wait_row(name):
            row = page.locator('tr:not([aria-label="ISO upload to Controller"])').filter(has_text=name)
            expect(row).to_contain_text('succeeded', timeout=180000)
        pending = []
        page.route('**/api/v1/session', lambda route: pending.append(route))
        page.goto(config['url'])
        expect(page.get_by_text('Connecting to Nex Controller…')).to_be_visible()
        capture('loading')
        for route in pending:
            route.continue_()
        page.unroute('**/api/v1/session')
        expect(page.get_by_label('Username')).to_be_focused()
        page.keyboard.type('root')
        page.keyboard.press('Tab')
        expect(page.get_by_label('Password', exact=True)).to_be_focused()
        page.keyboard.type('incorrect')
        page.keyboard.press('Enter')
        expect(page.get_by_role('alert')).to_contain_text('invalid credentials')
        capture('unauthorized')
        page.get_by_label('Password', exact=True).fill(password)
        page.keyboard.press('Enter')
        expect(page.get_by_role('heading', name='Your workspace', exact=True)).to_be_visible()
        assert 'matt-arch' not in page.get_by_role('navigation', name='Infrastructure', exact=True).inner_text()
        passed('Loading and rejected login captured; keyboard sign-in succeeds; enrolled fixture labels are neutral')
        pool('General')
        master = page.locator('.metric').filter(has_text='Pool master')
        expect(master).to_be_visible()
        hostname = master.locator('strong').inner_text()
        assert hostname and hostname != 'Unknown', hostname
        tree = page.get_by_role('navigation', name='Infrastructure', exact=True)
        expect(tree.locator('a[href^="#/hosts/"]').first).to_contain_text(hostname + ' · master')
        capture('pool-master')
        tree.locator('a[href^="#/hosts/"]').first.click()
        expect(page.locator('.host-overview').get_by_text('Pool master', exact=True)).to_be_visible()
        capture('host-master-badge')
        passed('Pool General shows the automatic pool master hostname; the tree suffixes the master and the host badge reads Pool master')
        pool('Virtual machines')
        if mode == 'simulated':
            expect(page.get_by_text('No virtual machines in this pool.', exact=False)).to_be_visible()
            capture('empty')
        pool('Storage')
        page.get_by_role('button', name='Register local storage', exact=True).click()
        form = page.get_by_role('region', name='Register local storage', exact=True)
        form.get_by_label('Name', exact=True).fill(storage_name)
        form.locator('select[name=host_id]').select_option(config['host_id'])
        form.get_by_label('Existing directory').fill(config.get('storage_path', '/var/lib/libvirt/images/nex-managed-qa'))
        with page.expect_response(lambda r: r.url.endswith('/api/v1/storage') and r.request.method == 'POST') as received:
            page.get_by_role('button', name='Register and check access').click()
        storage_reused = received.value.status == 409
        if storage_reused:
            resources = page.request.get(config['url'] + '/api/v1/storage').json()
            storage_name = next(r['name'] for r in resources if r['host_id'] == config['host_id'] and r['path'] == config.get('storage_path', '/var/lib/libvirt/images/nex-managed-qa'))
            page.get_by_role('button', name='Close', exact=True).click()
        else:
            assert received.value.ok, received.value.text()
            storage_name = received.value.json()['name']
        wait_row(storage_name)
        capture('storage')
        pool('Networking')
        page.get_by_role('button', name='Register network', exact=True).click()
        form = page.get_by_role('region', name='Register network', exact=True)
        form.get_by_label('Name', exact=True).fill(network_name)
        form.locator('select[name=host_id]').select_option(config['host_id'])
        if mode == 'simulated':
            # The simulated host reports one physical NIC; register VLAN 100 on it.
            form.get_by_label('Connection').select_option('vlan:nex1')
            form.get_by_label('VLAN ID').fill('100')
        else:
            form.get_by_label('Connection').select_option('custom')
            form.get_by_label('Network type').select_option('bridge')
            form.get_by_label('Existing network or bridge').fill('virbr0')
        capture('network-form')
        with page.expect_response(lambda r: r.url.endswith('/api/v1/networks') and r.request.method == 'POST') as received:
            page.get_by_role('button', name='Register and check access').click()
        network_reused = received.value.status == 409
        if network_reused:
            resources = page.request.get(config['url'] + '/api/v1/networks').json()
            network_name = next(r['name'] for r in resources if r['host_id'] == config['host_id'] and r['source'] == ('nex1v100' if mode == 'simulated' else 'virbr0'))
            page.get_by_role('button', name='Close', exact=True).click()
        else:
            assert received.value.ok, received.value.text()
            network_name = received.value.json()['name']
        wait_row(network_name)
        capture('network')
        passed(('GUI explicitly rejects duplicate storage registration; reuses previously validated directory; ' if storage_reused else 'GUI registers existing local directory; durable task succeeds; ') + ('GUI explicitly rejects duplicate network registration; reuses previously validated network' if network_reused else 'GUI registers existing guest network; durable task succeeds'))
        pool('ISO library')
        page.get_by_role('button', name='Upload ISO', exact=True).click()
        page.get_by_label('Destination storage').select_option(label=next(t for t in page.get_by_label('Destination storage').locator('option').all_text_contents() if t.startswith(storage_name)))
        page.get_by_label('ISO file', exact=True).set_input_files({'name': 'bad.txt', 'mimeType': 'text/plain', 'buffer': b'invalid'})
        expect(page.get_by_role('alert')).to_contain_text('Choose an ISO image ending in .iso of at least 32 KiB.')
        capture('invalid-iso')
        if mode == 'simulated':
            transfer = page.evaluate_handle('(name) => { const d = new DataTransfer(); const data = new Uint8Array(65536); data.set([1,67,68,48,48,49,1],32768); d.items.add(new File([data], name, {type: "application/x-iso9660-image"})); return d; }', iso.name)
            page.locator('.iso-drop').dispatch_event('drop', {'dataTransfer': transfer})
        else:
            page.get_by_label('ISO file', exact=True).set_input_files(str(iso))
        page.get_by_role('button', name='Upload and transfer ISO').click()
        # The browser->Controller upload is a row of the Recent tasks table, not a panel above it.
        upload_row = page.locator('.activity-dock table tbody tr[aria-label="ISO upload to Controller"]')
        expect(upload_row).to_be_visible()
        expect(upload_row).to_contain_text(iso.name)
        expect(upload_row.get_by_role('progressbar')).to_be_visible()
        assert page.locator('.activity-dock section[aria-label="ISO upload to Controller"]').count() == 0
        capture('iso-upload-row')
        wait_row(iso.name)
        capture('iso')
        page.get_by_role('button', name='Close', exact=True).click()
        pool('Virtual machines')
        page.get_by_role('button', name='Create VM', exact=True).click()
        page.get_by_label('VM name', exact=True).fill('Invalid Uppercase')
        assert page.get_by_label('VM name', exact=True).evaluate('(el) => el.validity.patternMismatch')
        page.get_by_label('VM name', exact=True).fill(vm_name)
        page.get_by_label('Description').fill('Browser acceptance guest')
        page.get_by_label('Guest operating system').select_option('linux')
        page.get_by_label('Placement host').select_option(config['host_id'])
        page.get_by_role('button', name='Next', exact=True).focus()
        page.keyboard.press('Enter')
        # Step 2: an impossible topology must block Next and say why. Filling only cores
        # and threads must seed sockets (BUG-0049: blank sockets used to show placeholder
        # "1" while staying 0, so 0×2×2 looked like a calculator bug).
        expect(page.get_by_role('heading', name='Create VM · 2 of 6:', exact=False)).to_be_focused()
        page.get_by_label('Virtual CPUs', exact=True).fill('4')
        page.get_by_label('Cores per socket', exact=True).fill('2')
        page.get_by_label('Threads per core', exact=True).fill('2')
        assert page.get_by_label('Sockets', exact=True).input_value() == '1'
        expect(page.locator('#vm-topology-issue')).to_have_count(0)
        page.get_by_label('Virtual CPUs', exact=True).fill('2')
        page.get_by_label('Sockets', exact=True).fill('3')
        expect(page.locator('#vm-topology-issue')).to_be_visible()
        expect(page.get_by_role('button', name='Next', exact=True)).to_be_disabled()
        capture('topology-invalid')
        page.get_by_label('Sockets', exact=True).fill('2')
        page.get_by_label('Cores per socket', exact=True).fill('1')
        page.get_by_label('Threads per core', exact=True).fill('1')
        expect(page.locator('#vm-topology-issue')).to_have_count(0)
        assert 'QEMU model · Skylake-Server' in page.get_by_label('CPU model').locator('option').all_text_contents()
        page.get_by_label('CPU model').select_option('Skylake-Server')
        page.get_by_role('radio', name='Maximum', exact=True).check()
        page.get_by_label('Memory (MiB)', exact=True).fill('512')
        page.get_by_role('button', name='Next', exact=True).click()
        # Step 3: two disks on the same storage with different buses and provisioning.
        expect(page.get_by_role('heading', name='Create VM · 3 of 6:', exact=False)).to_be_focused()
        def pick_storage(group):
            options = group.get_by_label('Storage').locator('option').all_text_contents()
            group.get_by_label('Storage').select_option(label=next(t for t in options if t.startswith(storage_name)))
        first_disk = page.get_by_role('group', name='Disk 1', exact=True)
        pick_storage(first_disk)
        # The disk name defaults to <pool>-<vm> and follows the VM name typed on page 1 until
        # the operator overrides it, so an untouched disk must already carry the derived label.
        derived = first_disk.get_by_label('Disk name', exact=True).input_value()
        assert derived.endswith(vm_name), f'disk name {derived!r} does not derive from VM name {vm_name!r}'
        assert derived != vm_name, f'disk name {derived!r} is missing the pool prefix'
        first_disk.get_by_label('Disk name', exact=True).fill('system')
        first_disk.get_by_label('Size (GiB)', exact=True).fill('1')
        page.get_by_role('button', name='Add disk', exact=True).click()
        second_disk = page.get_by_role('group', name='Disk 2', exact=True)
        pick_storage(second_disk)
        # An added disk takes the same derived name with a suffix, because the protocol
        # requires disk names to be unique within a VM.
        added = second_disk.get_by_label('Disk name', exact=True).input_value()
        assert added == f'{derived}-2', f'second disk {added!r} is not the suffixed default of {derived!r}'
        second_disk.get_by_label('Disk name', exact=True).fill('system')
        expect(page.locator('#vm-disk-issue')).to_be_visible()
        expect(page.get_by_role('button', name='Next', exact=True)).to_be_disabled()
        capture('duplicate-disk-name')
        second_disk.get_by_label('Disk name', exact=True).fill('data')
        second_disk.get_by_label('Size (GiB)', exact=True).fill('1')
        second_disk.get_by_label('Bus').select_option('sata')
        second_disk.get_by_label('Provisioning').select_option('thick')
        expect(page.locator('#vm-disk-issue')).to_have_count(0)
        page.get_by_role('button', name='Next', exact=True).click()
        # Step 4: a derived MAC and a manual one; a malformed manual MAC must block Next.
        expect(page.get_by_role('heading', name='Create VM · 4 of 6:', exact=False)).to_be_focused()
        def pick_network(group):
            options = group.get_by_role('combobox', name='Network').locator('option').all_text_contents()
            group.get_by_role('combobox', name='Network').select_option(label=next(t for t in options if t.startswith(network_name)))
        pick_network(page.get_by_role('group', name='Network interface 1', exact=True))
        page.get_by_role('button', name='Add network interface', exact=True).click()
        second_nic = page.get_by_role('group', name='Network interface 2', exact=True)
        pick_network(second_nic)
        second_nic.get_by_label('Adapter model').select_option('e1000e')
        second_nic.get_by_label('MAC address').fill('52:54:00:ab:cd')
        expect(page.locator('#vm-nic-issue')).to_be_visible()
        expect(page.get_by_role('button', name='Next', exact=True)).to_be_disabled()
        capture('invalid-mac')
        second_nic.get_by_label('MAC address').fill('52:54:00:ab:cd:ef')
        expect(page.locator('#vm-nic-issue')).to_have_count(0)
        page.get_by_role('button', name='Next', exact=True).click()
        # Step 5: firmware, install media and an explicit boot order with the ISO first.
        expect(page.get_by_role('heading', name='Create VM · 5 of 6:', exact=False)).to_be_focused()
        page.get_by_label('Firmware').select_option('bios')
        page.get_by_label('Install ISO').select_option(label=iso.name)
        boot = page.get_by_role('group', name='Boot order', exact=True)
        expect(boot.locator('li')).to_have_count(3)
        for _ in range(2):  # the Up control is the first button in the row
            boot.locator('li').filter(has_text='Install media').get_by_role('button').first.click()
        expect(boot.locator('li').first).to_contain_text('Install media')
        capture('boot-order')
        page.get_by_role('button', name='Next', exact=True).click()
        expect(page.get_by_role('heading', name='Create VM · 6 of 6:', exact=False)).to_be_focused()
        expect(page.get_by_text('created stopped', exact=False)).to_be_visible()
        # Scoped to the wizard: the fixed toolbar also has a Back button (browser history), which
        # would otherwise make this locator ambiguous in strict mode.
        page.get_by_role('region', name='Create VM', exact=True).get_by_role('button', name='Back', exact=True).click()  # entries survive stepping back
        expect(page.get_by_role('group', name='Boot order', exact=True).locator('li').first).to_contain_text('Install media')
        expect(page.get_by_label('Firmware')).to_have_value('bios')
        page.get_by_role('button', name='Next', exact=True).click()
        capture('review-desktop')
        page.set_viewport_size({'width': 360, 'height': 800})
        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), 'narrow page overflow'
        capture('review-360')
        page.set_viewport_size({'width': 1440, 'height': 1000})
        # Let the real server accept creation, then lose only its response.
        bodies = []
        def lose_reply(route):
            bodies.append(route.request.post_data_json)
            response = route.fetch()
            assert response.status == 202
            route.abort('connectionreset')
        page.route('**/api/v1/vms', lambda route: lose_reply(route) if route.request.method == 'POST' else route.continue_())
        page.get_by_role('button', name='Create stopped VM').click()
        expect(page.get_by_role('alert')).to_be_visible()
        capture('interrupted-create')
        page.unroute('**/api/v1/vms')
        page.reload()
        page.get_by_role('navigation', name='Resource views').get_by_role('button', name='Virtual machines', exact=True).click()
        page.get_by_role('button', name='Create VM', exact=True).click()
        expect(page.get_by_label('VM name', exact=True)).to_have_value(vm_name)
        for _ in range(5):
            page.get_by_role('button', name='Next', exact=True).click()
        page.get_by_role('button', name='Create stopped VM').click()
        expect(page.get_by_role('heading', name='VM details', exact=True)).to_be_visible()
        expect(page.locator('.details')).to_contain_text('succeeded', timeout=180000)
        expect(page.locator('.details')).to_contain_text('shut off', timeout=30000)
        for label, value in [('Virtual CPUs', '2'), ('Memory', '512 MiB'), ('Disks', 'system'), ('Disks', 'data'),
                             ('Networks', 'Intel E1000E'), ('Networks', '52:54:00:ab:cd:ef'), ('Install ISO', iso.name),
                             ('Firmware', 'BIOS'), ('Creation task', 'succeeded')]:
            expect(page.locator('dt').filter(has_text=label).locator('xpath=following-sibling::dd[1]')).to_contain_text(value)
        vms = page.request.get(config['url'] + '/api/v1/vms').json()
        assert len([v for v in vms if v['name'] == vm_name]) == 1
        # The retry must reconcile the original request, so exactly one submission body exists.
        spec = next(v for v in vms if v['name'] == vm_name)['spec']
        assert bodies and all(b['name'] == vm_name for b in bodies), bodies
        assert [d['name'] for d in spec['disks']] == ['system', 'data'] and [d['bus'] for d in spec['disks']] == ['virtio', 'sata'], spec
        assert spec['disks'][1]['thin'] is False and spec['cpu'] == {'count': 2, 'sockets': 2, 'cores': 1, 'threads': 1, 'model': 'Skylake-Server'}, spec
        assert spec['memory'] == {'mode': 'maximum', 'size_mib': 512} and spec['boot']['media_boot_order'] == 1, spec
        # An automatic MAC is listed as its effective derived address.
        assert re.fullmatch(r'52:54:00:[0-9a-f]{2}:[0-9a-f]{2}:00', spec['nics'][0]['mac']) and spec['nics'][1]['mac'] == '52:54:00:ab:cd:ef', spec
        capture('created-desktop')
        page.set_viewport_size({'width': 360, 'height': 800})
        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), 'details narrow page overflow'
        capture('created-360')
        passed('ISO picker validation, upload and six-step keyboard-accessible VM creation with two disks, two NICs, an explicit topology, maximum memory and an ISO-first boot order; invalid topology, duplicate disk name and malformed MAC refused in place; lost reply retry creates exactly one stopped VM; desktop and 360px checks')
        # Response fault injection validates presentation; it is not a native outage test.
        page.route('**/api/v1/vms', lambda route: route.fulfill(json=[{**v, 'freshness': 'stale'} for v in vms]))
        page.reload()
        expect(page.get_by_text('Current execution state is unknown.', exact=False)).to_be_visible()
        capture('stale')
        page.unroute('**/api/v1/vms')
        page.route('**/api/v1/storage', lambda route: route.fulfill(status=503, json={'error': 'Injected Controller unavailable'}))
        pool('Storage')
        page.reload()
        expect(page.get_by_role('alert')).to_be_visible()
        capture('unavailable')
        page.unroute('**/api/v1/storage')
        page.reload()
        expect(page.get_by_role('alert')).to_have_count(0)
        assert not errors, errors
        passed('Injected stale observation, Controller request failure and reload recovery; no browser page errors')
        browser.close()
    return {'mode': mode, 'environment': 'Chromium; real HTTPS Controller; ' + ('simulated mTLS agent, synthetic ISO, no libvirt execution' if mode == 'simulated' else 'disposable Debian nested-KVM host and real libvirt; stale/error/response-loss presentation injected'),
        'checks': checks, 'captures': captures, 'vm': next(v for v in vms if v['name'] == vm_name), 'storage_reused_after_409': storage_reused, 'network_reused_after_409': network_reused, 'ui_sha256': hashlib.file_digest((ROOT / 'web/dist/app.js').open('rb'), 'sha256').hexdigest() if (ROOT / 'web/dist/app.js').exists() else None,
        'limits': ['No guest installation, console or power controls', 'Native domain inspection and controller outage evidence recorded separately']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--simulated', action='store_true')
    parser.add_argument('--config', type=Path)
    parser.add_argument('--iso', type=Path)
    parser.add_argument('--evidence', type=Path, default=ROOT / '.cache/managed-vm-browser')
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    if args.simulated:
        with tempfile.TemporaryDirectory(prefix='nex-managed-browser.') as tmp:
            lab = Lab(Path(tmp))
            stop = thread = None
            try:
                config, password, iso, stop, thread, failures = simulated(lab)
                result = acceptance(config, password, iso, args.evidence, 'simulated')
                assert not failures, failures
            finally:
                if stop:
                    stop.set()
                    thread.join(timeout=10)
                lab.close()
    else:
        if not args.config or not args.iso:
            parser.error('native mode requires --config and --iso')
        result = acceptance(json.loads(args.config.read_text()), (args.config.parent / 'password').read_text(), args.iso, args.evidence, 'native')
    (args.evidence / ('browser-' + result['mode'] + '-results.json')).write_text(json.dumps(result, indent=2) + '\n')


if __name__ == '__main__':
    main()
