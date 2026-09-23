#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Real Controller/browser acceptance; --simulated uses an explicitly simulated mTLS agent.

Native mode: --config <private browser.json> --iso <guest.iso>.
Neither mode changes host networking or starts a guest. Native mode creates one
stopped VM and registers resources only in the selected disposable lab.
"""
import re
import argparse
from datetime import datetime, timedelta, timezone
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
    control = {'state': 'shut off',
               'capabilities': ['managed-vm-v1', 'pool-vm-placement-v1', 'vm-update-v1', 'vm-media-v1', 'storage-repositories-v1', 'vm-cpu-baseline-v1'],
               'reject_placement': False,
               'cpu_baseline': {'model': 'SyntheticBaseline', 'features': [{'name': 'sse4.2', 'policy': 'require'}], 'machine': 'q35', 'generation': 1, 'member_count': 1, 'observed_at': datetime.now(timezone.utc).isoformat()}}
    specs = {}
    managed_storage = {}
    managed_outcomes = []
    def reports():
        sequence, results, vms = 0, [], []
        while not stopped.wait(.2):
            try:
                sequence += 1
                now = datetime.now(timezone.utc).isoformat()
                for v in vms:
                    v['state'] = control['state']
                payload = {'version': 1, 'sequence': sequence,
                        'capabilities': control['capabilities'],
                        'results': results,
                        'inventory': {'observed_at': now, 'hostname': 'simulated-browser-host', 'cpu_count': 8, 'cpu_models': ['qemu64', 'Skylake-Server', 'FutureCPU-v7'],
                        'memory_bytes': 16 * 1024**3, 'vms': vms, 'cpu_baseline': control['cpu_baseline'],
                        'managed': {'vms': list(specs.values()), 'storage': list(managed_storage.values()),
                                    'outcomes': list(managed_outcomes)},
                        'checks': [{'name': 'libvirt', 'status': 'healthy', 'observed_at': now, 'last_success': now}],
                        'nics': [{'name': 'nex1', 'mac': '52:54:00:00:00:11', 'kind': 'physical', 'state': 'up', 'addresses': []}], 'disks': []}}
                request = urllib.request.Request(lab.url + '/api/v1/agent/report', data=json.dumps(payload).encode(), headers={'Content-Type': 'application/json'})
                with urllib.request.urlopen(request, context=context, timeout=5) as response:
                    answer = json.load(response)
                results = []
                for command in answer.get('commands') or []:
                    if command.get('vm') and control['reject_placement']:
                        control['reject_placement'] = False
                        results.append({'operation_id': command['operation_id'], 'status': 'failed',
                                        'error': 'pool master rejected placement', 'observed_at': now})
                        continue
                    results.append({'operation_id': command['operation_id'], 'status': 'succeeded', 'observed_at': now})
                    if command.get('storage'):
                        storage = {**command['storage']}
                        # Controller allocates pending-storage-* until Core journals a durable ID.
                        # IDs must stay within protocol.identifierValid (64 chars).
                        storage_id = storage.get('id') or ''
                        if not storage_id or storage_id.startswith('pending-storage-'):
                            storage_id = 'st' + command['operation_id'][:62]
                        storage.update({
                            'id': storage_id,
                            'operation_id': command['operation_id'],
                            'generation': max(1, int(storage.get('generation') or 1)),
                            'capabilities': {'vm_disk': True, 'iso': True, 'thin': True},
                            'access': {'status': 'healthy', 'readable': True, 'writable': True, 'observed_at': now},
                        })
                        for key in ('status', 'error'):
                            storage.pop(key, None)
                        managed_storage[storage_id] = storage
                        managed_outcomes[:] = [o for o in managed_outcomes if o.get('operation_id') != command['operation_id']]
                        managed_outcomes.append({
                            'operation_id': command['operation_id'], 'kind': 'storage-register',
                            'id': storage_id, 'name': storage.get('name', ''), 'status': 'succeeded',
                        })
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
    eventually(lambda: any(h['connection'] == 'fresh' and 'storage-repositories-v1' in (h.get('capabilities') or [])
                           for h in lab.api('/api/v1/hosts')), 'simulated agent fresh with storage repositories')
    # Named like the shipped driver ISO so the wizard's Driver disc preselect is exercised
    # against a host that cannot attach one (no vm-media-drivers-drive-v1).
    iso = lab.path / 'virtio-win-browser.iso'
    data = bytearray(65536)
    data[32768:32775] = b'\x01CD001\x01'
    iso.write_bytes(data)
    return {'url': lab.url, 'pool_id': pool['id'], 'host_id': state['host_id'], 'storage_path': '/simulated/browser', 'control': control}, password.read_text(), iso, stopped, thread, failures


def acceptance(config, password, iso, evidence, mode):
    from playwright.sync_api import sync_playwright, expect
    checks, captures = [], []
    suffix = datetime.now().strftime('%H%M%S')
    storage_name, network_name, vm_name = 'Browser local storage', 'Browser guest network', 'Browser VM.é-' + suffix
    def passed(message):
        checks.append(message)
        print('PASS:', message, flush=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True, **({'executable_path': os.environ['PLAYWRIGHT_CHROMIUM_EXECUTABLE']} if os.environ.get('PLAYWRIGHT_CHROMIUM_EXECUTABLE') else {}))
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
        def wait_row(name, marker='succeeded'):
            row = page.locator('tr:not([aria-label="ISO upload to Controller"])').filter(has_text=name)
            expect(row).to_contain_text(marker, timeout=180000)
        pending = []
        # The login screen skips an unauthenticated session probe; make this browser look like
        # one whose previous session expired so the existing loading-state check remains valid.
        page.add_init_script("localStorage.setItem('nex-signed-in', '1')")
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
        assert 'local-workstation' not in page.get_by_role('navigation', name='Infrastructure', exact=True).inner_text()
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
        page.get_by_role('button', name='Register storage', exact=True).click()
        form = page.get_by_role('region', name='Register storage', exact=True)
        form.get_by_label('Name', exact=True).fill(storage_name)
        form.locator('#storage-host').select_option(config['host_id'])
        form.get_by_label('Repository type').select_option('local')
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
        wait_row(storage_name, 'healthy')
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
        for invalid_name in ('   ', 'guest/name', 'é' * 101):
            page.get_by_label('VM name', exact=True).fill(invalid_name)
            assert not page.get_by_label('VM name', exact=True).evaluate('(el) => el.checkValidity()')
        page.get_by_label('VM name', exact=True).fill(vm_name)
        assert page.get_by_label('VM name', exact=True).evaluate('(el) => el.checkValidity()')
        page.get_by_label('Description').fill('Browser acceptance guest')
        # Choosing the host as a Windows guest and then switching to Linux must not leave a
        # driver disc the host cannot attach (F2) or one the wizard chose for Windows (F3).
        page.get_by_label('Guest operating system').select_option('windows')
        page.get_by_label('Placement host').select_option(config['host_id'])
        capture('guest-os-windows')
        page.get_by_label('Guest operating system').select_option('linux')
        page.get_by_role('button', name='Next', exact=True).focus()
        page.keyboard.press('Enter')
        # Step 2: an impossible topology must block Next and say why. Filling only cores
        # and threads must seed sockets (BUG-0049: blank sockets used to show placeholder
        # "1" while staying 0, so 0×2×2 looked like a calculator bug).
        expect(page.get_by_role('heading', name='Create VM · 2 of 6:', exact=False)).to_be_focused()
        if mode == 'simulated':
            # CPU presentation is OS-independent. Native discovery is simulated here; this
            # verifies fixed selections and unavailable/recovery UI, not hardware compatibility.
            cpu = page.get_by_label('CPU model')
            expect(cpu).to_have_value('__saved-baseline')
            expect(cpu.locator('option:checked')).to_contain_text('SyntheticBaseline')
            expect(page.get_by_text('Fixed features:', exact=False)).to_have_count(0)
            expect(cpu.locator('option:checked')).to_contain_text('(Recommended)')
            # The saved fixed CPU already matches the pool baseline, so it is the only recommendation.
            expect(cpu.locator('option[value="__pool-baseline"]')).not_to_contain_text('(Recommended)')
            assert 'qemu64' in cpu.locator('option').evaluate_all('(options) => options.map(o => o.value)')
            capture('pool-cpu-baseline')
            original_baseline = config['control']['cpu_baseline']
            config['control']['cpu_baseline'] = {'error': 'Simulated member unavailable'}
            warning = page.get_by_text('Pool CPU baseline unavailable.', exact=False)
            expect(warning).to_contain_text('Simulated member unavailable', timeout=30000)
            expect(cpu.locator('option:checked')).not_to_contain_text('(Recommended)')
            expect(cpu).to_have_value('__saved-baseline')
            expect(cpu.locator('option:checked')).to_contain_text('SyntheticBaseline')
            expect(page.get_by_role('button', name='Next', exact=True)).to_be_enabled()
            warning.scroll_into_view_if_needed()
            capture('pool-cpu-saved-unavailable')
            # A fresh wizard cannot silently fall back from an unknown pool default to a
            # host-derived CPU. An explicit manual model remains available.
            page.get_by_role('region', name='Create VM', exact=True).get_by_role('button', name='Close', exact=True).click()
            page.get_by_role('button', name='Create VM', exact=True).click()
            page.get_by_label('VM name', exact=True).fill(vm_name)
            page.get_by_label('Description').fill('Browser acceptance guest')
            page.get_by_label('Placement host').select_option(config['host_id'])
            page.get_by_role('button', name='Next', exact=True).click()
            expect(cpu).to_have_value('')
            expect(page.get_by_role('button', name='Next', exact=True)).to_be_disabled()
            page.get_by_role('alert').scroll_into_view_if_needed()
            capture('pool-cpu-new-unavailable')
            config['control']['cpu_baseline'] = {**original_baseline, 'model': 'FutureBaseline', 'generation': 2}
            expect(cpu.locator('option[value="__pool-baseline"]')).to_contain_text('FutureBaseline', timeout=30000)
            expect(cpu).to_have_value('')
            cpu.select_option('__pool-baseline')
            expect(cpu.locator('option:checked')).to_contain_text('FutureBaseline')
            expect(page.get_by_role('button', name='Next', exact=True)).to_be_enabled()
            cpu.select_option('qemu64')
            expect(page.get_by_text('Fixed features:', exact=False)).to_have_count(0)
            cpu.select_option('FutureCPU-v7')
            expect(cpu).to_have_value('FutureCPU-v7')
            config['control']['cpu_baseline'] = original_baseline
            expect(cpu.locator('option[value="__pool-baseline"]')).to_contain_text('SyntheticBaseline', timeout=30000)
            expect(cpu).to_have_value('FutureCPU-v7')
            cpu.scroll_into_view_if_needed()
            capture('pool-cpu-manual-preserved')
            passed('Pool CPU baseline freezes native model/features; failed recomputation preserves saved CPU; unknown new default blocks Next until explicit choice; dynamic manual models including qemu64 remain available and survive report changes')
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
        assert any(label.startswith('QEMU model · Skylake-Server') for label in page.get_by_label('CPU model').locator('option').all_text_contents())
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
        assert derived.endswith(re.sub(r"[^a-z0-9._-]+", "-", vm_name.lower())), f'disk name {derived!r} does not derive from VM name {vm_name!r}'
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
        if mode == 'simulated':
            expect(page.get_by_label('Driver disc')).to_be_disabled()
            expect(page.get_by_label('Driver disc')).to_have_value('')
            expect(page.get_by_text('does not provide a second CD drive yet', exact=False)).to_be_visible()
            capture('driver-disc-unavailable')
        page.get_by_label('Install ISO').select_option(label=iso.name)
        boot = page.get_by_role('group', name='Boot order', exact=True)
        expect(boot.locator('li')).to_have_count(3)
        for _ in range(2):  # the Up control is the first button in the row
            up = boot.locator('li').filter(has_text='Install media').get_by_role('button').first
            if up.is_disabled():
                break
            up.click()
        expect(boot.locator('li').first).to_contain_text('Install media')
        capture('boot-order')
        page.get_by_role('button', name='Next', exact=True).click()
        expect(page.get_by_role('heading', name='Create VM · 6 of 6:', exact=False)).to_be_focused()
        expect(page.get_by_text('as the last step of the creation task', exact=False)).to_be_visible()
        expect(page.get_by_text('you will see two tasks', exact=False)).to_have_count(0)
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
        bodies, create_headers = [], []
        def lose_reply(route):
            bodies.append(route.request.post_data_json)
            create_headers.append(route.request.headers)
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
        created_vm = next(v for v in vms if v['name'] == vm_name)
        # UI-2: the VM page route carries the creation operation, so a create that fails after
        # its 202 still has a route naming the task.
        assert page.url.endswith(f"#/vms/{created_vm['id']}/{created_vm['operation_id']}"), page.url
        spec = created_vm['spec']
        assert bodies and all(b['name'] == vm_name for b in bodies), bodies
        csrf = create_headers[0].get('x-csrf-token')
        assert csrf, create_headers
        assert [d['name'] for d in spec['disks']] == ['system', 'data'] and [d['bus'] for d in spec['disks']] == ['virtio', 'sata'], spec
        assert spec['disks'][1]['thin'] is False and spec['cpu'] == {'count': 2, 'sockets': 2, 'cores': 1, 'threads': 1, 'model': 'Skylake-Server'}, spec
        assert spec['memory'] == {'mode': 'maximum', 'size_mib': 512} and spec['boot']['media_boot_order'] == 1, spec
        # An automatic MAC is listed as its effective derived address.
        assert re.fullmatch(r'52:54:00:[0-9a-f]{2}:[0-9a-f]{2}:00', spec['nics'][0]['mac']) and spec['nics'][1]['mac'] == '52:54:00:ab:cd:ef', spec
        capture('created-desktop')
        # Simulator-only fault controls prove that an old Core is rejected synchronously and
        # a current Core's asynchronous placement rejection leaves an actionable task route.
        if mode == 'simulated':
            request = {**bodies[0], 'name': 'Placement browser failure ' + suffix}
            request['request_id'] = 'placement-browser-capability-' + suffix
            config['control']['capabilities'] = [cap for cap in config['control']['capabilities'] if cap != 'pool-vm-placement-v1']
            eventually(lambda: 'pool-vm-placement-v1' not in next(h for h in page.request.get(config['url'] + '/api/v1/hosts').json() if h['id'] == config['host_id'])['capabilities'], 'simulated Core without placement capability')
            rejected = page.request.post(config['url'] + '/api/v1/vms', data=request, headers={'X-CSRF-Token': csrf})
            assert rejected.status == 409 and 'master-owned VM placement; upgrade Nex Core' in rejected.text(), rejected.text()
            config['control']['capabilities'].append('pool-vm-placement-v1')
            eventually(lambda: 'pool-vm-placement-v1' in next(h for h in page.request.get(config['url'] + '/api/v1/hosts').json() if h['id'] == config['host_id'])['capabilities'], 'simulated Core placement capability restored')
            request['request_id'] = 'placement-browser-rejected-' + suffix
            config['control']['reject_placement'] = True
            rejected = page.request.post(config['url'] + '/api/v1/vms', data=request, headers={'X-CSRF-Token': csrf})
            assert rejected.status == 202, rejected.text()
            rejected_create = rejected.json()
            page.goto(config['url'] + f"/#/vms/{rejected_create['vm_id']}/{rejected_create['operation']['id']}")
            creation = page.get_by_role('region', name='Creation task', exact=True)
            expect(creation).to_contain_text('Creation failed', timeout=30000)
            expect(creation).to_contain_text('pool master rejected placement', timeout=30000)
            expect(creation.get_by_role('link', name='Task history', exact=True)).to_have_attribute('href', '#/tasks')
            capture('core-rejected-placement')
            page.goto(config['url'] + f"/#/vms/{created_vm['id']}")
            passed('A Core without master-owned placement is rejected before dispatch; a simulated Core placement rejection shows its Task history route, while the earlier interrupted-request retry succeeds')
        page.set_viewport_size({'width': 360, 'height': 800})
        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), 'details narrow page overflow'
        capture('created-360')
        passed('ISO picker validation, upload and six-step keyboard-accessible VM creation with two disks, two NICs, an explicit topology, maximum memory and an ISO-first boot order; invalid topology, duplicate disk name and malformed MAC refused in place; lost reply retry creates exactly one stopped VM; desktop and 360px checks')
        # Inject the existing GET shape only at the browser boundary. The simulated Core does not
        # claim to measure usage, and this route leaves its inventory and durable state untouched.
        usage = {'observed_at': (datetime.now(timezone.utc) - timedelta(seconds=65)).isoformat(),
                 'sample_window_ms': 30_000, 'cpu_percent': 125,
                 'memory_assigned_bytes': 4 * 1024 ** 3, 'memory_maximum_bytes': 8 * 1024 ** 3,
                 'memory_host_bytes': None, 'disk_used_bytes': 3 * 1024 ** 3, 'disk_capacity_bytes': None}
        usage_vms = [{**v, 'usage': usage} if v['id'] == created_vm['id'] else v for v in vms]
        page.route('**/api/v1/vms', lambda route: route.fulfill(json=usage_vms) if route.request.method == 'GET' else route.continue_())
        page.set_viewport_size({'width': 1440, 'height': 1000})
        page.reload()
        usage_panel = page.get_by_role('region', name='VM usage', exact=True)
        expect(usage_panel).to_contain_text('125% of allocated vCPUs')
        expect(usage_panel).to_contain_text('4 GiB assigned · 8 GiB maximum')
        expect(usage_panel).to_contain_text('3 GiB')
        expect(usage_panel).not_to_contain_text('Sample age')
        expect(usage_panel).not_to_contain_text('Sampled')
        expect(usage_panel).to_contain_text('Not reported')
        usage_panel.scroll_into_view_if_needed()
        capture('usage-desktop')
        page.set_viewport_size({'width': 360, 'height': 800})
        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), 'usage narrow page overflow'
        capture('usage-360')
        page.unroute('**/api/v1/vms')
        passed('Injected existing GET VM usage shape renders CPU, allocation, disk and honest Not reported values at desktop and 360px without a sample-age row or changing simulated Core state')
        # UH-8: Stacked with a hidden middle series. Injected at the browser boundary like usage:
        # three equal vCPUs stack to thirds; hiding vCPU 1 must leave one translucent fill band per
        # visible vCPU, neither overlapping (double alpha) nor dropping vCPU 2's band.
        stats = {'object': 'vm', 'id': created_vm['id'], 'range': '10m', 'step_seconds': 10,
                 'start': (datetime.now(timezone.utc) - timedelta(minutes=10)).isoformat(),
                 'series': [{'key': f'cpu.vcpu.{i}', 'group': 'cpu', 'label': f'vCPU {i}', 'unit': 'percent', 'values': [60] * 60} for i in range(3)]}
        page.route('**/api/v1/vms/*/stats*', lambda route: route.fulfill(json=stats))
        page.set_viewport_size({'width': 1440, 'height': 1000})
        page.get_by_role('navigation', name='Resource views').get_by_role('button', name='Stats', exact=True).click()
        cpu_chart = page.locator('figure.stats-chart').filter(has_text='CPU usage')
        page.get_by_label('Stacked').check()
        expect(cpu_chart.locator('canvas')).to_be_visible()
        # Count canvas pixels in the middle column by fill depth: one translucent fill (~0x40 alpha)
        # versus two overlapping fills (~0x70). Strokes and grid lines are opaque and ignored.
        fills = '''(canvas) => { const c = canvas.getContext('2d'), x = Math.floor(canvas.width / 2);
            const a = c.getImageData(x, 0, 1, canvas.height).data; let one = 0, two = 0;
            for (let i = 3; i < a.length; i += 4) { if (a[i] >= 48 && a[i] <= 80) one++; else if (a[i] >= 96 && a[i] <= 128) two++; }
            return [one, two]; }'''
        page.wait_for_timeout(500)
        one_all, two_all = cpu_chart.locator('canvas').evaluate(fills)
        cpu_chart.get_by_role('button', name='vCPU 1', exact=True).click()
        expect(cpu_chart.get_by_role('button', name='vCPU 1', exact=True)).to_have_attribute('aria-pressed', 'false')
        page.wait_for_timeout(500)
        one_hidden, two_hidden = cpu_chart.locator('canvas').evaluate(fills)
        cpu_chart.scroll_into_view_if_needed()
        capture('stats-stacked-hidden-middle')
        # Both stacks top out at 60% of the axis (180 of 300%, then 120 of 200%), so single-fill coverage matches.
        assert one_all > 50 and two_all < 5, (one_all, two_all)
        assert abs(one_hidden - one_all) <= one_all * .1 and two_hidden < 5, ('UH-8 stacked fill with hidden middle series', one_all, two_all, one_hidden, two_hidden)
        page.unroute('**/api/v1/vms/*/stats*')
        page.get_by_role('navigation', name='Resource views').get_by_role('button', name='General', exact=True).click()
        passed(f'Stacked CPU chart with vCPU 1 hidden keeps one non-overlapping fill band per visible vCPU (single-fill pixels {one_all} before, {one_hidden} after; overlapping {two_hidden})')
        if mode == 'simulated':
            page.set_viewport_size({'width': 1440, 'height': 1000})
            page.get_by_role('button', name='Edit', exact=True).click()
            editor = page.get_by_role('form', name='Edit VM', exact=True)
            expect(editor.get_by_label('Name', exact=True)).to_have_value(vm_name)
            editor.get_by_label('Name', exact=True).fill('guest/name')
            expect(editor.get_by_role('button', name='Save changes')).to_be_disabled()
            vm_name = '--Renamed VM.é-' + suffix
            editor.get_by_label('Name', exact=True).fill(vm_name)
            assert editor.get_by_label('Name', exact=True).evaluate('(el) => el.checkValidity()')
            capture('rename')
            editor.get_by_role('button', name='Save changes').focus()
            page.keyboard.press('Enter')
            expect(page.get_by_role('heading', name=vm_name, exact=True)).to_be_visible(timeout=30000)
            page.reload()
            expect(page.get_by_role('heading', name=vm_name, exact=True)).to_be_visible()
            vms = page.request.get(config['url'] + '/api/v1/vms').json()
            assert next(v for v in vms if v['name'] == vm_name)['spec']['name'] == vm_name
            capture('renamed')
            passed('Normal names with spaces, case, dots and Unicode survive create, uncertain retry, rename and reload; invalid names are blocked in create and edit')
        # Response fault injection validates presentation; it is not a native outage test.
        # The Controller lists a VM on a host that is not fresh as state "unknown"; the warning reads
        # the last state from the host's stored inventory instead (UI-6).
        last_state = next(v['state'] for v in next(h for h in page.request.get(config['url'] + '/api/v1/hosts').json() if h['id'] == config['host_id'])['inventory']['vms'] if v['id'] == created_vm['id'])
        page.route('**/api/v1/vms', lambda route: route.fulfill(json=[{**v, 'freshness': 'stale', 'state': 'unknown'} for v in vms]))
        page.reload()
        expect(page.get_by_text('Current execution state is unknown.', exact=False)).to_be_visible()
        # 2g: a VM on a host that is not fresh is greyed in the tree with a title saying why.
        stale_link = page.get_by_role('navigation', name='Infrastructure', exact=True).locator(f'a[href="#/vms/{created_vm["id"]}"]')
        expect(stale_link).to_have_class(re.compile(r'\bvm-unknown\b'))
        expect(stale_link).to_have_attribute('title', 'State unknown: host not fresh')
        assert 'grayscale' in stale_link.locator('.resource-icon').evaluate('(el) => getComputedStyle(el).filter'), '2g stale tree icon is not greyed'
        expect(page.get_by_text(f'Last reported state: {last_state}', exact=False)).to_be_visible()
        page.get_by_text(f'Last reported state: {last_state}', exact=False).scroll_into_view_if_needed()
        capture('stale-warning')
        stale_usage_panel = page.get_by_role('region', name='VM usage', exact=True)
        expect(stale_usage_panel).to_contain_text('No usage sample reported. This is not zero usage')
        stale_usage_panel.scroll_into_view_if_needed()
        capture('stale')
        page.unroute('**/api/v1/vms')
        passed(f'Stale VM warning shows the last state from host inventory ({last_state}) while the listing reports unknown; the tree greys the VM with "State unknown: host not fresh" (2g)')
        # UI-2: a VM page whose create task failed after its 202 names that task after the 10 s poll.
        # The failed operation and the VM's absence are injected at the browser boundary.
        operations = page.request.get(config['url'] + '/api/v1/operations').json()
        failed_create = {'id': 'injected-failed-create', 'host_id': config['host_id'], 'kind': 'vm-create', 'request_id': 'injected-request',
                         'status': 'failed', 'error': 'insufficient free storage including reserve', 'created_at': operations[0]['created_at'],
                         'updated_at': operations[0]['updated_at'], 'actor': 'root'}
        page.route('**/api/v1/vms', lambda route: route.fulfill(json=[v for v in vms if v['id'] != created_vm['id']]) if route.request.method == 'GET' else route.continue_())
        page.route('**/api/v1/operations', lambda route: route.fulfill(json=[failed_create] + operations) if route.request.method == 'GET' else route.continue_())
        page.goto(config['url'] + f"/#/vms/{created_vm['id']}/injected-failed-create")
        page.reload()  # a hash-only goto keeps the loaded inventory
        creation = page.get_by_role('region', name='Creation task', exact=True)
        expect(creation).to_contain_text('Creation failed')
        page.wait_for_timeout(11000)
        expect(creation).to_contain_text('insufficient free storage including reserve.')
        expect(creation.get_by_role('link', name='Task history', exact=True)).to_have_attribute('href', '#/tasks')
        expect(page.get_by_text('Object not found', exact=True)).to_have_count(0)
        capture('failed-create-route')
        page.goto(config['url'] + f"/#/vms/{created_vm['id']}")
        expect(page.get_by_role('heading', name='Object not found', exact=True)).to_be_visible()
        page.unroute('**/api/v1/operations')
        page.unroute('**/api/v1/vms')
        passed('A VM route naming its failed vm-create task shows the task error and a Task history link after an inventory poll, not Object not found')
        # UI-3: the VM Activity tab lists only rows audited against the VM id.
        audit = [{'id': 9001, 'actor': 'root', 'action': 'console.open', 'resource_id': created_vm['id'], 'created_at': operations[0]['created_at']},
                 {'id': 9002, 'actor': 'root', 'action': 'vm-power', 'resource_id': created_vm['id'], 'created_at': operations[0]['created_at']},
                 {'id': 9003, 'actor': 'root', 'action': 'vm-update', 'resource_id': created_vm['id'], 'created_at': operations[0]['created_at']},
                 {'id': 9004, 'actor': 'root', 'action': 'pool.create', 'resource_id': config['pool_id'], 'created_at': operations[0]['created_at']},
                 {'id': 9005, 'actor': 'root', 'action': 'host.check', 'resource_id': config['host_id'] + '/libvirt', 'created_at': operations[0]['created_at']}]
        page.route('**/api/v1/audit', lambda route: route.fulfill(json=audit))
        page.goto(config['url'] + f"/#/vms/{created_vm['id']}")
        page.reload()  # a hash-only goto keeps the loaded inventory
        page.get_by_role('navigation', name='Resource views').get_by_role('button', name='Activity', exact=True).click()
        history = page.locator('section.panel').filter(has=page.get_by_role('heading', name='Activity and health history', exact=True))
        expect(history.locator('tbody tr')).to_have_count(3)
        for action in ('console · open', 'vm power', 'vm update'):
            expect(history).to_contain_text(action)
        expect(history.locator('tbody tr').first).to_contain_text(vm_name)
        expect(history).not_to_contain_text('pool · create')
        expect(history).not_to_contain_text('host · check')
        capture('vm-activity')
        page.unroute('**/api/v1/audit')
        passed('VM Activity lists console.open, vm-power and vm-update rows for the VM and omits pool and host rows')
        # Presentation of a running VM, its power confirmation and delete notice. The host
        # capabilities and VM state are injected at the browser boundary; nothing reaches Core.
        hosts = page.request.get(config['url'] + '/api/v1/hosts').json()
        for h in hosts:
            if h['id'] == config['host_id']:
                h['capabilities'] = sorted(set((h.get('capabilities') or []) + ['vm-power-v1', 'vm-delete-v1', 'vm-media-v1']))
        page.route('**/api/v1/hosts', lambda route: route.fulfill(json=hosts) if route.request.method == 'GET' else route.continue_())
        page.route('**/api/v1/vms', lambda route: route.fulfill(json=[{**v, 'state': 'running', 'freshness': 'fresh'} for v in vms]) if route.request.method == 'GET' else route.continue_())
        page.set_viewport_size({'width': 1440, 'height': 1000})
        page.reload()
        offer = page.locator('.install-media-offer')
        expect(offer).to_contain_text('Every start boots the ISO first')
        note = page.locator('.panel-note').filter(has_text='open the Console tab and use Start VM on its toolbar')
        expect(note).to_be_visible()
        expect(page.get_by_text('Creation provisions a stopped VM', exact=False)).to_have_count(0)
        capture('general-running')
        power = page.get_by_role('group', name='VM power', exact=True)
        style = "(el) => { const s = getComputedStyle(el); return [s.borderTopColor, s.backgroundImage, s.backgroundColor]; }"
        toolbar_border, _, toolbar_background = power.get_by_role('button', name='Force stop', exact=True).evaluate(style)
        assert toolbar_border == 'rgba(0, 0, 0, 0)' and toolbar_background == 'rgba(0, 0, 0, 0)', ('toolbar buttons stay flat', toolbar_border, toolbar_background)
        power.get_by_role('button', name='Force stop', exact=True).click()
        confirm = page.get_by_role('dialog').get_by_role('button', name='Confirm force stop', exact=True)
        expect(confirm).to_be_visible()
        danger_border, _, _ = confirm.evaluate(style)
        assert danger_border != 'rgba(0, 0, 0, 0)', ('UI-5 danger confirmation keeps its border', danger_border)
        capture('power-dialog-force-stop')
        page.get_by_role('dialog').get_by_role('button', name='Cancel', exact=True).click()
        power.get_by_role('button', name='Shut down', exact=True).click()
        _, primary_image, _ = page.get_by_role('dialog').get_by_role('button', name='Confirm shut down', exact=True).evaluate(style)
        assert primary_image != 'none', ('UI-5 primary confirmation keeps its background', primary_image)
        capture('power-dialog-shut-down')
        page.get_by_role('dialog').get_by_role('button', name='Cancel', exact=True).click()
        passed('Running VM General tab shows the generic ISO banner and console-first install note; power confirmations keep danger and primary styling while toolbar buttons stay flat')
        page.unroute('**/api/v1/vms')
        # 2c: a fresh running VM whose guest agent is disconnected names that state, never "stale".
        guest = {'agent': 'disconnected', 'clipboard': 'unavailable', 'resize': False}
        page.route('**/api/v1/vms', lambda route: route.fulfill(json=[{**v, 'state': 'running', 'freshness': 'fresh', 'guest': guest} for v in vms]) if route.request.method == 'GET' else route.continue_())
        page.reload()
        guest_badge = page.get_by_role('region', name='Guest tools', exact=True).locator('.section-heading .badge')
        expect(guest_badge).to_have_text('Disconnected')
        guest_badge.scroll_into_view_if_needed()
        capture('guest-tools-disconnected')
        page.unroute('**/api/v1/vms')
        passed('Guest tools badge on a fresh VM with a disconnected agent reads disconnected, not stale (2c)')
        # 2a: a create still queued on its creation route shows the Creation task, not details with Edit or Delete.
        queued_ops = [{'id': 'injected-queued-create', 'host_id': config['host_id'], 'kind': 'vm-create', 'request_id': 'injected-queued-request',
                       'status': 'queued', 'created_at': operations[0]['created_at'], 'updated_at': operations[0]['updated_at'], 'actor': 'root'}]
        page.route('**/api/v1/vms', lambda route: route.fulfill(json=[{**v, 'status': 'queued', 'operation_id': 'injected-queued-create'} if v['id'] == created_vm['id'] else v for v in vms]) if route.request.method == 'GET' else route.continue_())
        page.route('**/api/v1/operations', lambda route: route.fulfill(json=queued_ops + operations) if route.request.method == 'GET' else route.continue_())
        page.goto(config['url'] + f"/#/vms/{created_vm['id']}/injected-queued-create")
        page.reload()
        expect(page.get_by_role('region', name='Creation task', exact=True)).to_contain_text('Creation queued')
        expect(page.get_by_role('heading', name='VM details', exact=True)).to_have_count(0)
        expect(page.get_by_role('button', name='Edit', exact=True)).to_have_count(0)
        expect(page.get_by_role('button', name='Delete VM', exact=True)).to_have_count(0)
        capture('queued-create-route')
        page.unroute('**/api/v1/operations')
        page.unroute('**/api/v1/vms')
        passed('A VM route for a create that is still queued shows the Creation task without VM details, Edit or Delete (2a)')
        # 2d and 2b: every Start path on a protected, shut-off Controller appliance opens the
        # two-Controllers confirmation and sends nothing until confirmed. The confirmed task's
        # notice clears when that task leaves queued, and on a tab change.
        power_posts, power_ops = [], {}
        def power_post(route):
            op_id = f'injected-power-{len(power_posts) + 1}'
            power_posts.append(route.request.post_data_json)
            power_ops[op_id] = 'queued'
            route.fulfill(status=202, json={'vm_id': created_vm['id'], 'action': 'start', 'operation': {'id': op_id, 'status': 'queued'}})
        def power_operations():
            return [{'id': k, 'host_id': config['host_id'], 'kind': 'vm-power', 'request_id': k, 'status': v,
                     'created_at': operations[0]['created_at'], 'updated_at': operations[0]['updated_at'], 'actor': 'root'} for k, v in power_ops.items()] + operations
        page.route('**/api/v1/vms', lambda route: route.fulfill(json=[{**v, 'state': 'shut off', 'freshness': 'fresh', 'protected': True} for v in vms]) if route.request.method == 'GET' else route.continue_())
        page.route('**/api/v1/vms/*/power', lambda route: power_post(route) if route.request.method == 'POST' else route.continue_())
        page.route('**/api/v1/operations', lambda route: route.fulfill(json=power_operations()) if route.request.method == 'GET' else route.continue_())
        page.goto(config['url'] + f"/#/vms/{created_vm['id']}")
        page.reload()
        warning = 'starting it runs two Controllers'
        page.get_by_role('group', name='VM power', exact=True).get_by_role('button', name='Start', exact=True).click()
        expect(page.get_by_role('dialog').get_by_text(warning, exact=False)).to_be_visible()
        capture('protected-start-toolbar')
        page.get_by_role('dialog').get_by_role('button', name='Cancel', exact=True).click()
        tree_link = page.get_by_role('navigation', name='Infrastructure', exact=True).locator(f'a[href="#/vms/{created_vm["id"]}"]')
        tree_link.click(button='right')
        page.get_by_role('menu').get_by_role('menuitem', name='Start', exact=True).click()
        expect(page.get_by_role('dialog').get_by_text(warning, exact=False)).to_be_visible()
        capture('protected-start-context-menu')
        page.get_by_role('dialog').get_by_role('button', name='Cancel', exact=True).click()
        page.get_by_role('navigation', name='Resource views').get_by_role('button', name='Console', exact=True).click()
        page.get_by_role('button', name='Start VM', exact=True).click()
        console_dialog = page.get_by_role('dialog')
        expect(console_dialog.get_by_text(warning, exact=False)).to_be_visible()
        capture('protected-start-console')
        page.wait_for_timeout(500)
        assert not power_posts, ('2d protected Start sent before confirmation', power_posts)
        console_dialog.get_by_role('button', name='Confirm start', exact=True).click()
        notice = page.locator('.notice[role="status"]').filter(has_text='Start queued for')
        expect(notice).to_be_visible()
        assert len(power_posts) == 1 and power_posts[0]['action'] == 'start', power_posts
        capture('start-notice-queued')
        power_ops['injected-power-1'] = 'succeeded'
        expect(notice).to_have_count(0, timeout=15000)  # the next inventory poll sees the task settle
        capture('start-notice-cleared')
        tree_link.click(button='right')
        page.get_by_role('menu').get_by_role('menuitem', name='Start', exact=True).click()
        page.get_by_role('dialog').get_by_role('button', name='Confirm start', exact=True).click()
        expect(notice).to_be_visible()
        page.get_by_role('navigation', name='Resource views').get_by_role('button', name='Activity', exact=True).click()
        expect(notice).to_have_count(0)
        assert len(power_posts) == 2, power_posts
        # A refused confirmed Start shows its error inside the modal dialog, and a retry reuses
        # the same request_id so the Controller reconciles one request instead of queuing two.
        refused = []
        def refuse_power(route):
            refused.append(route.request.post_data_json)
            route.fulfill(status=409, json={'error': 'Injected power refusal'})
        page.unroute('**/api/v1/vms/*/power')
        page.route('**/api/v1/vms/*/power', lambda route: refuse_power(route) if route.request.method == 'POST' else route.continue_())
        page.get_by_role('navigation', name='Resource views').get_by_role('button', name='Console', exact=True).click()
        page.get_by_role('button', name='Start VM', exact=True).click()
        console_dialog.get_by_role('button', name='Confirm start', exact=True).click()
        expect(console_dialog.get_by_role('alert').filter(has_text='Injected power refusal')).to_be_visible()
        capture('protected-start-refused')
        console_dialog.get_by_role('button', name='Confirm start', exact=True).click()
        page.wait_for_timeout(500)
        assert len(refused) == 2 and refused[0]['request_id'] == refused[1]['request_id'], refused
        console_dialog.get_by_role('button', name='Cancel', exact=True).click()
        expect(page.get_by_role('dialog')).to_have_count(0)
        passed('A refused confirmed Start shows its error inside the dialog and a retry reuses the request_id')
        page.unroute('**/api/v1/operations')
        page.unroute('**/api/v1/vms/*/power')
        page.unroute('**/api/v1/vms')
        passed('Toolbar, context-menu and console Start on a protected appliance open the two-Controllers confirmation and send nothing until confirmed (2d); the Start queued notice clears when its task succeeds and on a tab change (2b)')
        page.route('**/api/v1/vms', lambda route: route.fulfill(json=[{**v, 'freshness': 'fresh'} for v in vms]) if route.request.method == 'GET' else route.continue_())
        page.route('**/api/v1/vms/*', lambda route: route.fulfill(status=202, json={'vm_id': created_vm['id'], 'operation': {'id': 'injected-delete', 'status': 'queued'}}) if route.request.method == 'DELETE' else route.continue_())
        page.reload()
        page.get_by_role('button', name='Delete VM', exact=True).click()
        # 2f: the wipe checkbox sits next to its label text instead of stretching across the dialog.
        # The input box itself is measured: a stretched box centres its glyph far from the text.
        width, gap = page.get_by_role('dialog').locator('label.disk-wipe').evaluate("""(label) => {
            const box = label.querySelector('input').getBoundingClientRect();
            const range = document.createRange(); range.selectNodeContents(label); range.setStartAfter(label.querySelector('input'));
            return [box.width, range.getBoundingClientRect().left - box.right]; }""")
        assert width <= 24 and 0 <= gap <= 16, ('2f delete checkbox is far from its label', width, gap)
        capture('delete-dialog-checkbox')
        page.get_by_role('dialog').get_by_label('VM name', exact=True).fill(vm_name)
        page.get_by_role('dialog').get_by_role('button', name='Delete VM', exact=True).click()
        expect(page).to_have_url(re.compile(r'#/hosts/'))
        expect(page.get_by_text(f'Delete queued for {vm_name}', exact=False)).to_be_visible()
        page.wait_for_timeout(1000)
        expect(page.get_by_text(f'Delete queued for {vm_name}', exact=False)).to_be_visible()
        capture('delete-queued-notice')
        page.unroute('**/api/v1/vms/*')
        page.unroute('**/api/v1/vms')
        page.unroute('**/api/v1/hosts')
        passed('Injected 202 delete from the VM page lands on the host page with the Delete queued notice (GM-10)')
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
