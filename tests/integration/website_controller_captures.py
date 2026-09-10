#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Capture the real Controller with disposable, explicitly simulated public-demo data.

Requires built web/dist, Go, openssl and Python Playwright/Chromium as in phase1 CI.
Default output is disposable CI evidence. To replace website images, pass
--output website/static/gallery --evidence docs/qa/website-controller-captures/results.json.
No native agent probes, live infrastructure, screenshot editing or browser mocks.
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
import urllib.request

from phase1 import Lab, ROOT, eventually, run

HOSTNAME = 'nex-core-01'
VIEWS = [('General', 'Health checks', 'desktop-shell.png'),
         ('Networking', 'Network interfaces', 'networking-desktop.png'),
         ('Storage', 'Disks', 'storage-desktop.png'),
         ('Activity', 'Activity and health history', 'activity-desktop.png')]


def assert_public_text(text):
    forbidden = {'matt', 'matt-arch', '/home/', 'BEGIN PRIVATE KEY',
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
    eventually(lambda: lab.api('/api/v1/sites', expected=401), 'controller startup')
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
        'os_version': 'Nex Core 0.1.4', 'agent_version': '0.1.4',
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
    request = urllib.request.Request(lab.url + '/api/v1/agent/report',
        data=json.dumps({'version': 1, 'sequence': 1, 'inventory': inventory, 'results': []}).encode(),
        headers={'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, context=tls, timeout=5) as response:
        assert response.status == 200
    hosts = lab.api('/api/v1/hosts')
    assert len(hosts) == 1 and hosts[0]['hostname'] == HOSTNAME and hosts[0]['connection'] == 'fresh'
    observed = hosts[0]['inventory']
    assert {k: observed[k] for k in inventory if k not in {'checks', 'observed_at'}} == {k: v for k, v in inventory.items() if k not in {'checks', 'observed_at'}}
    assert_public_text(json.dumps(hosts))
    lab.passed('real Controller, neutral site, CSR enrollment creating the automatic pool with the host as master; authenticated mTLS simulated inventory')
    # Regression: the guard rejects the original workstation-label leak.
    for private in ['matt-arch', 'Matt', 'single-use enrollment token']:
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
        expect(page.get_by_role('heading', name='Your workspace', exact=True)).to_be_visible()
        page.goto(lab.url + '/#/hosts/' + identity['host_id'])
        for tab, heading, filename in VIEWS:
            page.get_by_role('navigation', name='Resource views', exact=True).get_by_role('button', name=tab, exact=True).click()
            expect(page.get_by_role('heading', name=heading, exact=True)).to_be_visible()
            expect(page.get_by_role('heading', name=HOSTNAME, exact=True)).to_be_visible()
            if tab != 'Activity':
                expect(page.locator('.badge.fresh').first).to_be_visible()
            page.wait_for_load_state('networkidle')
            expect(page.locator('input[type="password"]')).to_have_count(0)
            expect(page.get_by_label('Single-use enrollment token')).to_have_count(0)
            body = page.locator('body').inner_text()
            assert_public_text(body)
            # One Nex Core version: the package version, shown once; no separate agent row.
            assert 'Agent version' not in body, body
            if tab == 'General':
                assert 'Version\nNex Core 0.1.4' in body, body
            assert password.read_text() not in page.content() and token not in page.content()
            assert all(label in body for label in ['Community demo', HOSTNAME, HOSTNAME + ' · master'])
            if tab == 'General':
                assert 'Pool master' in body
            assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
            path = args.output / filename
            page.screenshot(path=str(path))
            hashes[filename] = hashlib.sha256(path.read_bytes()).hexdigest()
        # Exercise the original identity leak through real API/rendering after all safe captures.
        # This disposable negative fixture is never written to a screenshot.
        lab.api('/api/v1/sites', {'name': 'matt-arch'}, expected=201)
        page.reload()
        # Sites are listed by the site picker rather than as tree links.
        page.get_by_role('button', name=re.compile(r'^Site: ')).click()
        expect(page.get_by_role('option', name='matt-arch', exact=True)).to_be_visible()
        try:
            assert_public_text(page.locator('body').inner_text())
        except AssertionError:
            lab.passed('privacy guard rejected the original workstation label rendered by the real Controller')
        else:
            raise AssertionError('Privacy guard accepted original rendered identity leak')
        assert not errors, errors
        version = browser.version
        browser.close()
    lab.passed('four real browser resource views, neutral rendered identity, no credential fields or browser errors')
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
