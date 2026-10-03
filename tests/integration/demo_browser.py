#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise the shipped Controller UI and demo API; no browser response mocks."""
import argparse
from contextlib import contextmanager
import hashlib
import json
from pathlib import Path
import re
import socket
import subprocess
import tempfile
import time
import urllib.request

from playwright.sync_api import expect, sync_playwright

ROOT = Path(__file__).resolve().parents[2]


@contextmanager
def service(args):
    if args.url:
        yield args.url.rstrip('/')
        return
    if not all((args.binary, args.ui_dir, args.source_archive, args.docs_dir)):
        raise ValueError('Provide --url or --binary, --ui-dir, --source-archive and --docs-dir')
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    url = f'http://127.0.0.1:{port}'
    with tempfile.TemporaryFile() as log:
        process = subprocess.Popen([
            str(args.binary.resolve()), '-listen', f'127.0.0.1:{port}',
            '-ui-dir', str(args.ui_dir.resolve()),
            '-docs-dir', str(args.docs_dir.resolve()),
            '-source-archive', str(args.source_archive.resolve()),
            '-public-origin', url, '-insecure-cookie',
        ], cwd=ROOT, stdout=log, stderr=subprocess.STDOUT)
        try:
            deadline = time.monotonic() + 15
            while True:
                if process.poll() is not None:
                    log.seek(0)
                    raise RuntimeError(log.read().decode())
                try:
                    with urllib.request.urlopen(url + '/healthz', timeout=1) as response:
                        if response.status == 200:
                            break
                except OSError:
                    if time.monotonic() >= deadline:
                        raise RuntimeError('Demo did not become ready')
                    time.sleep(.1)
            yield url
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


def exercise(args, url):
    args.evidence.mkdir(parents=True, exist_ok=True)
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(headless=True)
        context = browser.new_context(ignore_https_errors=args.ignore_https_errors,
                                      viewport={'width': 1440, 'height': 1000}, locale='en-GB')
        page = context.new_page()
        errors, failed_reads, checked = [], [], []
        page.on('pageerror', lambda error: errors.append(str(error)))
        page.on('response', lambda response: failed_reads.append(f'{response.status} {response.url}')
                if response.request.method == 'GET' and response.status >= 400
                and '/api/v1/session' not in response.url else None)
        page.goto(url)
        expect(page.get_by_label('Public demo information')).to_be_visible()
        page.get_by_label('Username', exact=True).wait_for(state='visible')
        page.screenshot(path=str(args.evidence / 'login.png'))
        page.get_by_label('Username', exact=True).fill('demo')
        page.get_by_label('Password', exact=True).fill('incorrect-demo-password')
        with page.expect_response(lambda response: response.url.endswith('/api/v1/session')
                                  and response.request.method == 'POST') as refused:
            page.get_by_role('button', name='Sign in', exact=True).click()
        assert refused.value.status == 401, 'Incorrect credentials must not establish a session'
        page.get_by_label('Password', exact=True).fill('demo')
        with page.expect_response(lambda response: response.url.endswith('/api/v1/session')
                                  and response.request.method == 'POST') as accepted:
            page.get_by_role('button', name='Sign in', exact=True).click()
        assert accepted.value.status == 200
        session = accepted.value.json()
        checked.append('invalid credentials followed by successful public login')

        def api(path):
            response = context.request.get(url + '/api/v1/' + path)
            assert response.status == 200, f'{path}: {response.status}'
            return response.json()

        sites, pools, vms = api('sites'), api('pools'), api('vms')
        pool = next(pool for pool in pools if pool['site_id'] == sites[0]['id'])
        scoped = [vm for vm in vms if vm['pool_id'] == pool['id']]
        vm, other = scoped[:2]
        navigation = page.get_by_label('Sections', exact=True)
        navigation.get_by_role('link', name='VMs', exact=True).click()
        expect(page.get_by_role('link', name=vm['name'], exact=True)).to_be_visible()
        page.get_by_label('Search name, IP address or tag').fill(vm['name'])
        matches = [item for item in scoped if vm['name'].lower() in item['name'].lower()]
        expect(page.locator('tr[data-vm]')).to_have_count(len(matches))
        for item in matches:
            expect(page.locator(f'tr[data-vm="{item["id"]}"]')).to_be_visible()
        expect(page.get_by_role('link', name=other['name'], exact=True)).to_have_count(0)
        page.get_by_role('link', name=vm['name'], exact=True).click()
        expect(page.locator('.slideover')).to_contain_text(vm['name'])
        page.locator('.slideover').get_by_role('tab', name='Configuration', exact=True).click()
        expect(page.locator('.slideover')).to_contain_text(str(vm['spec']['cpu']['count']))
        page.wait_for_load_state('networkidle')
        page.screenshot(path=str(args.evidence / 'vm-detail.png'))
        page.locator('.slideover').get_by_role('tab', name='Performance', exact=True).click()
        usage = page.locator('.slideover').get_by_role('region', name='Usage history', exact=True)
        usage.get_by_role('switch', name='Stacked', exact=True).check()
        cpu = usage.locator('.stats-chart-cpu')
        expect(cpu.locator('.stats-legend button')).to_have_count(vm['spec']['cpu']['count'])
        expect(cpu.locator('canvas')).to_be_visible()
        page.screenshot(path=str(args.evidence / 'vm-stacked-cpu.png'))
        checked.append('VM stacked CPU renders one plotted series per configured vCPU')
        page.locator('.slideover').get_by_role('button', name='Close', exact=True).click()
        checked.append('VM filtering and detail/configuration browsing')
        page.get_by_label('Search name, IP address or tag').fill('')
        next_pool = next(p for p in pools if p['site_id'] != pool['site_id'])
        next_vm = next(v for v in vms if v['pool_id'] == next_pool['id'])
        page.locator('.scope summary').click()
        page.locator('.scope').get_by_role('button', name=next_pool['name'], exact=True).click()
        expect(page.locator(f'tr[data-vm="{next_vm["id"]}"]')).to_be_visible()
        expect(page.locator(f'tr[data-vm="{vm["id"]}"]')).to_have_count(0)
        page.screenshot(path=str(args.evidence / 'pool-scope.png'))
        page.locator('.scope summary').click()
        page.locator('.scope').get_by_role('button', name=pool['name'], exact=True).click()
        expect(page.get_by_role('link', name=vm['name'], exact=True)).to_be_visible()
        checked.append('site/pool scope switches inventory without leaking another pool')

        navigation.get_by_role('link', name='Hosts', exact=True).click()
        expect(page.get_by_role('heading', name='Hosts', exact=True)).to_be_visible()
        host = next(host for host in api('hosts') if host['pool_id'] == pool['id'])
        page.get_by_role('link', name=host['hostname'], exact=True).click()
        expect(page.locator('.slideover')).to_contain_text(host['hostname'])
        page.locator('.slideover').get_by_role('tab', name='Hardware', exact=True).click()
        expect(page.locator('.slideover')).to_contain_text(host['inventory']['cpu_model'])
        page.locator('.slideover').get_by_role('tab', name='Performance', exact=True).click()
        usage = page.locator('.slideover').get_by_role('region', name='Usage history', exact=True)
        usage.get_by_role('switch', name='Stacked', exact=True).check()
        cpu = usage.locator('.stats-chart-cpu')
        expect(cpu.locator('.stats-legend button')).to_have_count(host['inventory']['cpu_count'])
        expect(cpu.locator('canvas')).to_be_visible()
        page.screenshot(path=str(args.evidence / 'host-stacked-cpu.png'))
        checked.append('Host stacked CPU renders one plotted series per reported CPU')
        page.locator('.slideover').get_by_role('button', name='Close', exact=True).click()
        checked.append('host detail and hardware')

        for route, endpoint in [('Storage', 'storage'), ('Networks', 'networks')]:
            navigation.get_by_role('link', name=route, exact=True).click()
            resource = next(item for item in api(endpoint) if item['pool_id'] == pool['id'])
            expect(page.get_by_text(resource['name'], exact=True).first).to_be_visible()
        checked.append('storage and network registrations')

        navigation.get_by_role('link', name='Backups', exact=True).click()
        policies = api(f'pools/{pool["id"]}/backup-policies')
        policy = policies[0]
        page.get_by_role('button', name='Expand all', exact=True).click()
        covered_vm = next(v['name'] for v in vms if v['id'] == policy['vm_ids'][0])
        expect(page.locator('.job-vm').filter(has_text=covered_vm).first).to_be_visible()
        # A real UI mutation must reach a refusal and leave the shared job unchanged.
        action = 'Resume' if policy.get('paused') else 'Pause'
        with page.expect_response(lambda response: response.request.method == 'POST'
                                  and '/backup-policies/' in response.url) as mutation:
            page.get_by_role('button', name=f'{action} {policy["name"]}', exact=True).click()
        assert mutation.value.status == 403, 'Public users must not change jobs'
        expect(page.get_by_role('alert').filter(has_text=re.compile('read.only', re.I)).first).to_be_visible()
        refreshed = next(item for item in api(f'pools/{pool["id"]}/backup-policies') if item['id'] == policy['id'])
        assert refreshed.get('paused') == policy.get('paused')
        checked.append('backup job detail and server-enforced read-only action')
        page.get_by_role('tab', name='Recovery points', exact=True).click()
        point = api(f'pools/{pool["id"]}/backup-catalog')['items'][0]
        page.get_by_role('button', name=re.compile('^Inspect ' + re.escape(point['source_vm_name']) + r' \(')).first.click()
        expect(page.locator('.slideover')).to_contain_text(point['id'])
        page.wait_for_load_state('networkidle')
        expect(page.locator('.slideover .backup-loading')).to_have_count(0)
        page.screenshot(path=str(args.evidence / 'recovery-point.png'))
        page.locator('.slideover').get_by_role('button', name='Close', exact=True).click()
        checked.append('recovery-point inspection')

        navigation.get_by_role('link', name='Settings', exact=True).click()
        page.get_by_role('link', name='Members', exact=True).click()
        page.locator('.pool-status > summary').click()
        expect(page.locator('.pool-host-fencing-cluster')).to_be_visible()
        page.screenshot(path=str(args.evidence / 'pool-quorum.png'))
        checked.append('simulated pool membership and quorum inspection')
        page.get_by_role('link', name='Backup repositories', exact=True).click()
        repositories = api(f'pools/{pool["id"]}/backup-repositories')
        expect(page.get_by_text(repositories[0]['name'], exact=True).first).to_be_visible()
        expect(page.get_by_role('link', name=re.compile('^' + re.escape(host['hostname']) + r': Accessible')).first).to_be_visible()
        for label in ('Tasks', 'Events'):
            navigation.get_by_role('link', name=label, exact=True).click()
            expect(page.get_by_role('heading', name=label, exact=True).first).to_be_visible()
        checked.append('backup repository settings, tasks and events')

        navigation.get_by_role('link', name='Overview', exact=True).click()
        for scheme in ('light', 'dark'):
            page.emulate_media(color_scheme=scheme)
            expect(page.locator('html')).to_have_attribute('data-theme', scheme)
            page.screenshot(path=str(args.evidence / f'overview-{scheme}.png'))
        page.set_viewport_size({'width': 390, 'height': 844})
        navigation.get_by_role('link', name='Backups', exact=True).click()
        expect(page.get_by_role('tab', name='Backup jobs', exact=True)).to_be_visible()
        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), 'Demo frame must not overflow on phones'
        page.screenshot(path=str(args.evidence / 'backups-phone.png'))
        checked.append('light/dark overview and phone backups layout')

        source = context.request.get(url + '/source')
        assert source.status == 200
        if args.source_archive:
            assert hashlib.sha256(source.body()).digest() == hashlib.sha256(args.source_archive.read_bytes()).digest(), 'Served source must match shipped artifact'
        checked.append('corresponding source download')
        reference = context.request.get(url + '/api/docs/')
        assert reference.status == 200 and b'Nex' in reference.body()
        checked.append('API reference link')
        logout = context.request.delete(url + '/api/v1/session', headers={'X-CSRF-Token': session['csrf_token'], 'Origin': url})
        assert logout.status in (200, 204)
        assert context.request.get(url + '/api/v1/vms').status == 401, 'Logout must remove browsing authority'
        checked.append('logout removes authenticated access')
        assert not errors, errors
        assert not failed_reads, failed_reads
        report = {'result': 'passed', 'method': 'real shipped UI and demo API; no browser response mocks',
                  'checks': checked, 'page_errors': errors, 'failed_read_requests': failed_reads,
                  'limits': ['fictional inventory; no real host, storage, backup or recovery proof']}
        (args.evidence / 'results.json').write_text(json.dumps(report, indent=2) + '\n')
        print(json.dumps(report, indent=2))
        browser.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--url')
    parser.add_argument('--binary', type=Path)
    parser.add_argument('--ui-dir', type=Path)
    parser.add_argument('--source-archive', type=Path)
    parser.add_argument('--docs-dir', type=Path)
    parser.add_argument('--evidence', type=Path, default=ROOT / '.cache/demo-browser')
    parser.add_argument('--ignore-https-errors', action='store_true', help='Only for disposable self-signed verification TLS')
    args = parser.parse_args()
    with service(args) as url:
        exercise(args, url)


if __name__ == '__main__':
    main()
