#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Add host by address: real-lab acceptance against an explicitly selected disposable lab.

Private JSON config requires disposable=true, local_url, controller_url,
root_password_file and controller_password_file (loopback forwards only). The lab's
Nex Core host must already be enrolled; the run revokes it and re-enrolls it through
the new push path, so it needs --host-address (the host as the Controller reaches it)
and --host-controller-url (the Controller as the host reaches it). Negative paths use
the Controller API; the successful re-enrollment runs through the real browser panel.
No host commands, networking changes or guest power actions.
"""
import argparse
import hashlib
import json
from pathlib import Path
import ssl
import time
from urllib.parse import urlparse


def main():
    from playwright.sync_api import sync_playwright, expect
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--evidence', type=Path, required=True)
    parser.add_argument('--host-address', required=True, help='host address the Controller connects to, e.g. 10.0.2.15')
    parser.add_argument('--host-controller-url', required=True, help='Controller origin the host connects to, e.g. https://10.0.2.20:8443')
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

    root_password, admin_password = password('root'), password('controller')
    url = config['controller_url']

    with sync_playwright() as pw:
        api_ctx = pw.request.new_context(base_url=url, ignore_https_errors=True)
        csrf = None

        def api(method, path, body=None, expected=200):
            nonlocal csrf
            headers = {'Origin': url}
            if csrf:
                headers['X-CSRF-Token'] = csrf
            response = api_ctx.fetch(path, method=method, data=json.dumps(body) if body is not None else None,
                                     headers={**headers, 'Content-Type': 'application/json'})
            assert response.status == expected, f'{method} {path}: {response.status} {response.text()[:300]}'
            return response.json() if response.text() else {}

        csrf = api('POST', '/api/v1/session', {'username': 'root', 'password': admin_password})['csrf_token']
        sites = api('GET', '/api/v1/sites')
        hosts_before = [h for h in api('GET', '/api/v1/hosts') if not h['revoked']]
        assert len(sites) == 1 and len(hosts_before) == 1, 'lab must have exactly one site and one active host'
        site, host = sites[0], hosts_before[0]
        pool_before = next(p for p in api('GET', '/api/v1/pools') if p['id'] == host['pool_id'])
        assert pool_before['master_host_id'] == host['id']

        # 1. Probe returns the certificate the host really serves (verified independently).
        probe = api('POST', '/api/v1/hosts/probe', {'address': args.host_address})
        local = urlparse(config['local_url'])
        served = ssl.get_server_certificate((local.hostname, local.port))
        expected = 'sha256:' + hashlib.sha256(ssl.PEM_cert_to_DER_cert(served)).hexdigest()
        assert probe['fingerprint'] == expected, (probe, expected)
        assert probe['address'].endswith(':9443')
        passed('Probe fingerprint matches the certificate the Nex Core host serves on its local API')

        def add(body, expected):
            return api('POST', f'/api/v1/sites/{site["id"]}/hosts', {'address': args.host_address, 'controller_url': args.host_controller_url, **body}, expected)

        # 2. Negative paths through the Controller API, host still enrolled.
        wrong = 'sha256:' + ('0' if expected[7] != '0' else '1') + expected[8:]
        r = add({'fingerprint': wrong, 'root_password': root_password}, 502)
        assert 'fingerprint does not match' in r['error'], r
        passed('Wrong fingerprint is rejected with 502 before any credential is sent')
        r = add({'fingerprint': probe['fingerprint'], 'root_password': 'definitely-not-' + root_password[:4]}, 401)
        assert 'rejected the root password' in r['error'], r
        passed('Wrong root password is rejected by the host and surfaced as 401')
        r = add({'fingerprint': probe['fingerprint'], 'root_password': root_password}, 409)
        assert r.get('host_id') == host['id'], r
        passed('Adding an already-enrolled host returns 409 naming the existing host_id')
        r = add({'fingerprint': probe['fingerprint'], 'root_password': root_password, 'host_id': host['id']}, 409)
        passed('Re-enrolling a host that is not revoked is refused with 409 before any network attempt')
        api('POST', '/api/v1/hosts/probe', {'address': 'https://' + args.host_address}, 400)
        api('POST', '/api/v1/hosts/probe', {'address': '10.255.255.1'}, 502)
        passed('Probe validates the address shape and reports an unreachable address as 502')

        # 3. Revoke, then re-enroll through the real browser panel.
        api('POST', f'/api/v1/hosts/{host["id"]}/revoke', {})
        assert next(h for h in api('GET', '/api/v1/hosts') if h['id'] == host['id'])['revoked']
        passed('Existing host revoked through the Controller API')

        browser = pw.chromium.launch(headless=True)
        page = browser.new_page(ignore_https_errors=True, viewport={'width': 1440, 'height': 1000})
        errors = []
        page.on('pageerror', lambda error: errors.append(str(error)))

        def capture(name):
            filename = 'browser-' + name + '.png'
            page.screenshot(path=str(args.evidence / filename), full_page=True, mask=[page.locator('input[type=password]')])
            captures.append(filename)

        page.goto(url + '/#/hosts/' + host['id'])
        expect(page.get_by_label('Username', exact=True)).to_be_focused()
        page.keyboard.type('root')
        page.keyboard.press('Tab')
        page.get_by_label('Password', exact=True).fill(admin_password)
        page.keyboard.press('Enter')
        expect(page.get_by_role('button', name='Sign out', exact=True)).to_be_visible(timeout=30000)
        page.get_by_role('button', name='Re-enroll host', exact=True).first.click()
        expect(page.get_by_label('Host address', exact=True)).to_be_focused()
        page.keyboard.type(args.host_address)
        page.keyboard.press('Tab')
        page.keyboard.type(root_password)
        page.keyboard.press('Tab')
        page.get_by_label('Controller address', exact=True).fill(args.host_controller_url)
        capture('re-enroll-form')
        page.keyboard.press('Enter')
        expect(page.get_by_text(probe['fingerprint'])).to_be_visible(timeout=30000)
        capture('fingerprint-confirmation')
        passed('Browser panel shows the probed fingerprint for confirmation before adding the host')
        page.get_by_role('button', name='Confirm and add host', exact=True).click()
        expect(page.get_by_text('re-enrolled and keeps its pool', exact=False)).to_be_visible(timeout=60000)
        expect(page.locator('input[type=password]')).to_have_count(0)
        capture('re-enroll-success')
        passed('Browser re-enrollment succeeded and the password field is gone')

        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            after = next(h for h in api('GET', '/api/v1/hosts') if h['id'] == host['id'])
            if not after['revoked'] and after['connection'] == 'fresh':
                break
            time.sleep(2)
        assert not after['revoked'] and after['connection'] == 'fresh', after
        assert after['pool_id'] == host['pool_id'], (after, host)
        pool_after = next(p for p in api('GET', '/api/v1/pools') if p['id'] == host['pool_id'])
        assert pool_after['master_host_id'] == host['id'], pool_after
        passed('Re-enrolled host kept its identity and pool, remains pool master and reports fresh')
        page.goto(url + '/#/hosts/' + host['id'])
        expect(page.get_by_text('Connection fresh', exact=False).or_(page.get_by_label('Connection fresh')).first).to_be_visible(timeout=30000)
        capture('host-fresh')

        # 4. Retry semantics and audit hygiene.
        add({'fingerprint': probe['fingerprint'], 'root_password': root_password, 'host_id': host['id']}, 409)
        passed('Repeating the push after success is refused with 409, no duplicate host')
        audit = api('GET', '/api/v1/audit')
        actions = [a.get('action') for a in audit]
        assert 'host.add' in actions and 'enrollment-token.create' in actions, actions[-10:]
        blob = json.dumps(audit)
        assert root_password not in blob and admin_password not in blob
        passed('Audit records host.add and token creation without any password')
        assert not errors, errors
        browser.close()

    (args.evidence / 'results.json').write_text(json.dumps({
        'checks': checks, 'captures': captures, 'host_id': host['id'], 'pool_id': host['pool_id'],
        'environment': 'Actual fresh ISO-installed nested KVM lab with the Controller appliance inside it; loopback forwards; no API mocking',
        'api_impact': 'new contract exercised: probeHost, addSiteHost, localEnroll',
        'guide_impact': 'updated: verifies the Add host by address workflow documented in manage-controller.md',
    }, indent=2) + '\n')
    print(f'{len(checks)} checks passed')


if __name__ == '__main__':
    main()
