#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Capture the real Controller with disposable, explicitly simulated public-demo data.

Registers demo storage, a guest network and an ISO, creates VMs through the real
API, then captures the populated console screens and the Create VM review.

Requires built web/dist, Go, openssl and Python Playwright/Chromium as in phase1 CI.
Default output is disposable CI evidence. To replace website images, pass
--output website/static/gallery --evidence docs/qa/website-controller-captures/results.json.
No native agent probes, live infrastructure, screenshot editing or browser mocks. The
host side is a protocol-level fixture: these captures evidence Controller UI rendering,
never libvirt, storage or guest behaviour.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import socket
import ssl
import sys
import tempfile
import threading
import urllib.parse
import urllib.request

from phase1 import Lab, ROOT, eventually, run

HOSTNAME = 'nex-core-01'
# Demo pool resources behind the managed-VM views. Names and paths stay neutral so
# assert_public_text keeps passing; the ISO is a 64 KiB fixture with a valid descriptor.
# A current Nex Core advertises every managed capability; omitting any renders the demo
# host as outdated ("upgrade Nex Core") and disables the power controls in the capture.
CAPABILITIES = ['managed-vm-v1', 'vm-update-v1', 'vm-power-v1', 'console-v1',
                'host-console-v1', 'vm-media-v1', 'storage-repositories-v1']
STORAGE_NAME, STORAGE_PATH = 'Local NVMe storage', '/var/lib/nex/storage'
NETWORK_NAME, ISO_NAME = 'Guest network', 'debian-13-netinst.iso'
DEMO_VMS = [('web-01', 'Public web front end', 2, 4096, 40),
            ('app-01', 'Internal application server', 4, 8192, 80),
            ('db-01', 'Database server', 4, 16384, 200)]


def assert_public_text(text):
    forbidden = {'local-workstation', '/home/', 'BEGIN PRIVATE KEY',
                 'BEGIN CERTIFICATE', 'single-use enrollment token'}
    local_hostname = socket.gethostname().lower()
    if local_hostname != HOSTNAME:
        forbidden.add(local_hostname)
    assert not any(value.lower() in text.lower() for value in forbidden), 'Private identity or credential in capture content'
    assert not re.search(r'\b(?:10(?:\.\d+){3}|192\.168(?:\.\d+){2}|172\.(?:1[6-9]|2\d|3[01])(?:\.\d+){2})\b', text), 'Private IP in capture content'


def capture(lab, args):
    from playwright.sync_api import sync_playwright, expect
    go = os.environ.get('GO') or shutil.which('go') or ROOT / '.cache/toolchains/go/bin/go'
    controller = lab.path / 'controller'
    run(go, 'build', '-o', controller, './cmd/nex-controller', cwd=ROOT)
    source = lab.path / 'nex-source.tar.gz'
    run(sys.executable, ROOT / 'scripts/build-source.py', '--output', source,
        env={**os.environ, 'GO': str(go)}, cwd=ROOT)
    cert, key = lab.path / 'server.pem', lab.path / 'server.key'
    run('openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256',
        '-nodes', '-keyout', key, '-out', cert, '-days', '1', '-subj', '/CN=localhost',
        '-addext', 'subjectAltName=DNS:localhost')
    password = lab.path / 'password'
    password.write_text(secrets.token_urlsafe(32))
    password.chmod(0o600)
    lab.start('controller', controller, '-data-dir', lab.path / 'controller-state',
        '-listen', f'127.0.0.1:{lab.port}', '-tls-cert', cert, '-tls-key', key,
        '-admin-username', 'root', '-admin-password-file', password,
        '-stale-after', '5m', '-ui-dir', ROOT / 'web/dist', '-source-archive', source)
    lab.client(cert)
    lab.started()
    lab.csrf = lab.api('/api/v1/session', {'username': 'root', 'password': password.read_text()})['csrf_token']
    site = lab.api('/api/v1/sites', {'name': 'Community demo'}, expected=201)
    token = lab.api('/api/v1/enrollment-tokens', {'site_id': site['id']}, expected=201)['token']
    agent_key, csr = lab.path / 'agent.key', lab.path / 'agent.csr'
    run('openssl', 'req', '-new', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256',
        '-nodes', '-keyout', agent_key, '-out', csr, '-subj', '/CN=nex-core-01')
    identity = lab.api('/api/v1/agent/enroll', {'version': 1, 'token': token,
                       'csr_pem': csr.read_text(), 'hostname': HOSTNAME})
    agent_cert = lab.path / 'agent.pem'
    agent_cert.write_text(identity['certificate_pem'])
    pools = lab.api('/api/v1/pools')
    assert len(pools) == 1 and pools[0]['name'] == HOSTNAME and pools[0]['master_host_id'] == identity['host_id'], pools
    tls = ssl.create_default_context(cafile=str(cert))
    tls.load_cert_chain(str(agent_cert), str(agent_key))
    now = datetime.now(timezone.utc).isoformat()
    inventory = {
        'observed_at': now, 'hostname': HOSTNAME, 'cpu_model': 'Example x86-64 processor',
        'cpu_count': 16, 'memory_bytes': 64 * 1024**3, 'virtualization': ['vmx'],
        'os_version': 'Nex Core 0.4.10', 'agent_version': '0.4.10',
        'nics': [{'name': 'enp1s0', 'mac': '02:00:00:00:00:01',
                  'addresses': ['198.51.100.10/24'], 'state': 'up'},
                 {'name': 'enp2s0', 'mac': '02:00:00:00:00:02',
                  'addresses': ['203.0.113.10/24'], 'state': 'up'}],
        'disks': [{'name': 'nvme0n1', 'size_bytes': 1024**4},
                  {'name': 'sda', 'size_bytes': 2 * 1024**4}],
        'vms': [],
        'checks': [{'name': name, 'status': 'healthy', 'observed_at': now, 'last_success': now}
                   for name in ['inventory', 'libvirt']],
    }
    assert_public_text(json.dumps(inventory))
    # The simulated host keeps reporting and answers the Controller's commands, so demo
    # registrations and VM creation reach a real recorded result instead of staying pending.
    # Create VM only lists storage Core has reported as healthy and VM-disk capable.
    managed, managed_storage, managed_outcomes = {}, {}, []
    observed_vms, vm_state = [], {'value': 'shut off'}
    stopped, failures = threading.Event(), []

    def report(sequence, results):
        # Each report carries a current observation: the Controller keeps the newest
        # inventory, so a frozen observed_at would discard every update after the first.
        stamp = datetime.now(timezone.utc).isoformat()
        payload = {'version': 1, 'sequence': sequence, 'results': results,
                   'capabilities': CAPABILITIES,
                   'inventory': {**inventory, 'observed_at': stamp, 'vms': observed_vms,
                                 'managed': {'vms': list(managed.values()),
                                             'storage': list(managed_storage.values()),
                                             'outcomes': list(managed_outcomes)},
                                 'checks': [{**check, 'observed_at': stamp, 'last_success': stamp}
                                            for check in inventory['checks']]}}
        request = urllib.request.Request(lab.url + '/api/v1/agent/report',
            data=json.dumps(payload).encode(), headers={'Content-Type': 'application/json'})
        with urllib.request.urlopen(request, context=tls, timeout=5) as response:
            assert response.status == 200
            return json.load(response)

    def reporter():
        sequence, results = 2, []
        while not stopped.wait(.2):
            try:
                for vm in observed_vms:
                    vm['state'] = vm_state['value']
                answer = report(sequence, results)
                sequence, results = sequence + 1, []
                now = datetime.now(timezone.utc).isoformat()
                for command in answer.get('commands') or []:
                    results.append({'operation_id': command['operation_id'], 'status': 'succeeded', 'observed_at': now})
                    if command.get('storage'):
                        storage = {**command['storage']}
                        # Durable IDs must stay within protocol.identifierValid (64 chars).
                        storage_id = storage.get('id') or ''
                        if not storage_id or storage_id.startswith('pending-storage-'):
                            storage_id = 'st' + command['operation_id'][:62]
                        storage.update({
                            'id': storage_id,
                            'operation_id': command['operation_id'],
                            'generation': max(1, int(storage.get('generation') or 1)),
                            'capabilities': {'vm_disk': True, 'iso': True, 'thin': True},
                            'access': {'status': 'healthy', 'readable': True, 'writable': True,
                                       'total_bytes': 1024**4, 'available_bytes': 640 * 1024**3,
                                       'observed_at': now},
                        })
                        for key in ('status', 'error'):
                            storage.pop(key, None)
                        managed_storage[storage_id] = storage
                        managed_outcomes[:] = [o for o in managed_outcomes
                                               if o.get('operation_id') != command['operation_id']]
                        managed_outcomes.append({
                            'operation_id': command['operation_id'], 'kind': 'storage-register',
                            'id': storage_id, 'name': storage.get('name', ''), 'status': 'succeeded',
                        })
                    if command.get('vm'):
                        # The host journals the definition and reports it as the current revision.
                        vm = command['vm']
                        managed[vm['id']] = vm
                        if any(v['id'] == vm['id'] for v in observed_vms):
                            next(v for v in observed_vms if v['id'] == vm['id'])['name'] = vm['name']
                        else:
                            observed_vms.append({'id': vm['id'], 'name': vm['name'], 'state': vm_state['value']})
            except Exception as exc:
                failures.append(str(exc))
                return

    report(1, [])
    threading.Thread(target=reporter, daemon=True).start()
    hosts = lab.api('/api/v1/hosts')
    assert len(hosts) == 1 and hosts[0]['hostname'] == HOSTNAME and hosts[0]['connection'] == 'fresh'
    observed = hosts[0]['inventory']
    assert {k: observed[k] for k in inventory if k not in {'checks', 'observed_at'}} == {k: v for k, v in inventory.items() if k not in {'checks', 'observed_at'}}
    assert_public_text(json.dumps(hosts))
    lab.passed('real Controller, neutral site, CSR enrollment creating the automatic pool with the host as master; authenticated mTLS simulated inventory')
    # Regression: the guard rejects the original workstation-label leak.
    for private in ['local-workstation', 'single-use enrollment token']:
        try:
            assert_public_text(private)
        except AssertionError:
            pass
        else:
            raise AssertionError('Privacy guard accepted private content')
    args.output.mkdir(parents=True, exist_ok=True)
    hashes = {}
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(headless=True)
        context = browser.new_context(ignore_https_errors=True,
            viewport={'width': 1440, 'height': 1000}, timezone_id='UTC', locale='en-GB')
        page = context.new_page()
        errors = []
        page.on('pageerror', lambda error: errors.append(str(error)))
        page.goto(lab.url)
        page.get_by_label('Username').fill('root')
        page.get_by_label('Password', exact=True).fill(password.read_text())
        page.get_by_role('button', name='Sign in', exact=True).click()
        expect(page.locator('summary[aria-label^="Scope: Community demo, ' + HOSTNAME + '."]')).to_be_visible()
        def shot(filename):
            page.wait_for_load_state('networkidle')
            expect(page.locator('input[type="password"]')).to_have_count(0)
            expect(page.get_by_label('Single-use enrollment token')).to_have_count(0)
            assert_public_text(page.locator('body').inner_text())
            assert password.read_text() not in page.content() and token not in page.content()
            assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
            path = args.output / filename
            page.screenshot(path=str(path))
            hashes[filename] = hashlib.sha256(path.read_bytes()).hexdigest()

        def vms():
            # Reload so the list renders resources registered after sign-in.
            page.goto(lab.url + '/#/vms')
            page.reload()

        def option(field, prefix):
            texts = field.locator('option').all_text_contents()
            for text in texts:
                if text.startswith(prefix):
                    return text
            raise AssertionError('no option starting with %r in %r' % (prefix, texts))

        # Demo pool resources created through the real API and confirmed by the simulated host,
        # so the managed-VM views show recorded state instead of an empty pool.
        common = {'pool_id': pools[0]['id'], 'host_id': identity['host_id']}

        def settled(operation_id):
            return eventually(lambda: next((o for o in lab.api('/api/v1/operations')
                if o['id'] == operation_id and o['status'] == 'succeeded'), None), 'operation ' + operation_id)
        storage = lab.api('/api/v1/storage', {**common, 'request_id': 'gallery-storage',
            'name': STORAGE_NAME, 'path': STORAGE_PATH}, expected=201)
        settled(storage['operation_id'])
        eventually(lambda: next((s for s in lab.api('/api/v1/storage')
            if s['id'] == storage['id'] and s.get('access', {}).get('status') == 'healthy'
            and s.get('access', {}).get('writable') and s.get('capabilities', {}).get('vm_disk')), None),
            'storage access healthy for Create VM')
        network = lab.api('/api/v1/networks', {**common, 'request_id': 'gallery-network',
            'name': NETWORK_NAME, 'kind': 'network', 'source': 'default'}, expected=201)
        settled(network['operation_id'])
        iso = lab.path / ISO_NAME
        image = bytearray(65536)
        image[32768:32775] = b'\x01CD001\x01'
        iso.write_bytes(bytes(image))
        query = urllib.parse.urlencode({'storage_id': storage['id'], 'name': ISO_NAME, 'request_id': 'gallery-media'})
        upload = urllib.request.Request(lab.url + '/api/v1/media?' + query, data=iso.read_bytes(),
            headers={'Content-Type': 'application/octet-stream', 'X-CSRF-Token': lab.csrf, 'Origin': lab.url})
        with lab.http.open(upload, timeout=60) as response:
            media = json.load(response)
        settled(media['operation_id'])
        for name, description, cpus, memory_mib, disk_gib in DEMO_VMS:
            created = lab.api('/api/v1/vms', {**common, 'request_id': 'gallery-' + name, 'name': name,
                'description': description, 'guest_os': 'linux', 'media_id': media['id'],
                'cpu': {'count': cpus, 'sockets': 1, 'cores': cpus, 'threads': 1, 'model': 'host-passthrough'},
                'memory': {'mode': 'dedicated', 'size_mib': memory_mib},
                'disks': [{'storage_id': storage['id'], 'name': 'system', 'size_gib': disk_gib,
                           'bus': 'virtio', 'thin': True, 'boot_order': 2}],
                'nics': [{'network_id': network['id'], 'model': 'virtio', 'mac': ''}],
                'boot': {'firmware': 'uefi', 'machine': '', 'media_boot_order': 1},
                'start_after_create': False, 'autostart': False}, expected=202)
            settled(created['operation']['id'])
        vm_state['value'] = 'running'
        seen = {}

        def running():
            seen['vms'] = lab.api('/api/v1/vms')
            return len(seen['vms']) == len(DEMO_VMS) and all(v.get('state') == 'running' for v in seen['vms'])
        try:
            eventually(running, 'demo VMs observed running')
        except AssertionError:
            raise AssertionError('demo VMs not observed running: ' + json.dumps(seen.get('vms'), indent=2)[:3000])
        assert_public_text(json.dumps(lab.api('/api/v1/vms')))
        lab.passed('three demo VMs created through the real API on registered storage, network and uploaded media; the simulated host confirmed every operation and reports them running')

        for route, label, filename in [
            ('/', 'Placement', 'desktop-shell.png'),
            ('/networks', NETWORK_NAME, 'networking-desktop.png'),
            ('/storage', STORAGE_NAME, 'storage-desktop.png'),
        ]:
            page.goto(lab.url + '/#' + route)
            page.reload()
            expect(page.get_by_text(label, exact=True).first).to_be_visible()
            shot(filename)

        page.goto(lab.url + '/#/')
        page.reload()
        page.locator('summary[aria-label="No tasks running. Show tasks"]').click()
        expect(page.locator('.activity-tasks').get_by_text('Create VM').first).to_be_visible()
        assert identity['host_id'] not in page.locator('body').inner_text()
        shot('activity-desktop.png')

        vms()
        table = page.locator('table.list')
        for name, *_ in DEMO_VMS:
            expect(table.get_by_role('link', name=name, exact=True)).to_be_visible()
        shot('vms-desktop.png')
        table.get_by_role('link', name=DEMO_VMS[0][0], exact=True).click()
        vm_panel = page.get_by_role('region', name=DEMO_VMS[0][0], exact=True)
        expect(vm_panel.get_by_role('tab', name='Overview', exact=True)).to_have_attribute('aria-selected', 'true')
        expect(vm_panel.locator('header .badge')).to_have_text(re.compile('running', re.I))
        shot('vm-general-desktop.png')

        vms()
        page.get_by_role('button', name='Create VM', exact=True).click()
        page.get_by_label('VM name', exact=True).fill('build-01')
        page.get_by_label('Description').fill('Continuous integration builder')
        page.get_by_label('Guest operating system').select_option('linux')
        page.get_by_label('Placement host').select_option(identity['host_id'])
        page.get_by_role('button', name='Next', exact=True).click()
        page.get_by_label('Virtual CPUs', exact=True).fill('4')
        page.get_by_label('Sockets', exact=True).fill('1')
        page.get_by_label('Cores per socket', exact=True).fill('4')
        page.get_by_label('Threads per core', exact=True).fill('1')
        page.get_by_label('Memory (MiB)', exact=True).fill('8192')
        page.get_by_role('button', name='Next', exact=True).click()
        disk = page.get_by_role('group', name='Disk 1', exact=True)
        disk.get_by_label('Storage').select_option(label=option(disk.get_by_label('Storage'), STORAGE_NAME))
        disk.get_by_label('Disk name', exact=True).fill('system')
        disk.get_by_label('Size (GiB)', exact=True).fill('60')
        page.get_by_role('button', name='Next', exact=True).click()
        nic = page.get_by_role('group', name='Network interface 1', exact=True).get_by_role('combobox', name='Network')
        nic.select_option(label=option(nic, NETWORK_NAME))
        page.get_by_role('button', name='Next', exact=True).click()
        page.get_by_label('Firmware').select_option('uefi')
        page.get_by_label('Install ISO').select_option(label=ISO_NAME)
        page.get_by_role('button', name='Next', exact=True).click()
        expect(page.get_by_role('heading', name='Create VM · 6 of 6:', exact=False)).to_be_visible()
        shot('create-vm-desktop.png')
        lab.passed('VM list, VM panel and the completed six-step Create VM review captured from the real browser UI')
        stopped.set()
        assert not failures, failures
        # Exercise the original identity leak through real API/rendering after all safe captures.
        # This disposable negative fixture is never written to a screenshot.
        lab.api('/api/v1/sites', {'name': 'local-workstation'}, expected=201)
        page.reload()
        # Sites are listed by the scope menu.
        page.locator('summary[aria-label^="Scope: "]').click()
        expect(page.locator('details.scope').get_by_role('button', name='local-workstation · all pools', exact=True)).to_be_visible()
        try:
            assert_public_text(page.locator('body').inner_text())
        except AssertionError:
            lab.passed('privacy guard rejected the original workstation label rendered by the real Controller')
        else:
            raise AssertionError('Privacy guard accepted original rendered identity leak')
        assert not errors, errors
        version = browser.version
        browser.close()
    lab.passed('seven populated console screens, neutral rendered identity, no credential fields or browser errors')
    args.evidence.parent.mkdir(parents=True, exist_ok=True)
    args.evidence.write_text(json.dumps({
        'date_utc': datetime.now(timezone.utc).isoformat(), 'results': lab.results,
        'simulation': 'All host inventory is an explicit demo fixture; actual Controller, TLS enrollment, mTLS report and browser UI. No native probes, live host changes, DOM replacements or image editing.',
        'viewport': '1440x1000', 'browser': 'Chromium ' + version,
        'controller_sha256': hashlib.sha256(controller.read_bytes()).hexdigest(),
        'web_dist_sha256': {str(p.relative_to(ROOT / 'web/dist')): hashlib.sha256(p.read_bytes()).hexdigest()
                            for p in sorted((ROOT / 'web/dist').rglob('*')) if p.is_file()},
        'images_sha256': hashes,
    }, indent=2) + '\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, default=ROOT / '.cache/website-controller-captures')
    parser.add_argument('--evidence', type=Path, default=ROOT / '.cache/website-controller-captures/results.json')
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='nex-gallery-') as directory:
        lab = Lab(Path(directory))
        try:
            capture(lab, args)
        finally:
            lab.close()


if __name__ == '__main__':
    main()
