#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Real appliance QA against an explicitly prepared, disposable nested-KVM lab.

The private lab directory must contain ports.json, id_ed25519, known_hosts,
local-password and controller-password. Never use production credentials or hosts.
"""
import argparse
import base64
import json
from pathlib import Path
import shlex
import socket
import subprocess
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--lab-dir', type=Path, required=True)
    parser.add_argument('--evidence', type=Path, required=True)
    parser.add_argument('--deploy', action='store_true', help='create the Controller on this prepared disposable host')
    parser.add_argument('--packaged', action='store_true', help='test the installed Debian package bundle paths')
    parser.add_argument('--security', action='store_true', help='verify hardened cookies, headers and peer login limits')
    args = parser.parse_args()
    lab = args.lab_dir.resolve(strict=True)
    assert lab.name.startswith('nex-appliance-lab.'), 'explicit disposable lab directory required'
    assert (lab / 'host.raw').is_file(), 'disposable copied host disk required'
    ports = json.loads((lab / 'ports.json').read_text())
    ssh = ['ssh', '-o', 'BatchMode=yes', '-o', f'UserKnownHostsFile={lab}/known_hosts',
           '-i', str(lab / 'id_ed25519'), '-p', str(ports['ssh']), 'root@127.0.0.1']
    assert subprocess.check_output(ssh + ['hostname'], text=True).strip() == 'nex-lab'
    args.evidence.mkdir(parents=True, exist_ok=True)
    url = f'https://127.0.0.1:{ports["core"]}'
    results = []
    from playwright.sync_api import sync_playwright, expect
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        context = browser.new_context(ignore_https_errors=True, viewport={'width': 1440, 'height': 1000})
        page = context.new_page()
        errors = []
        page.on('pageerror', lambda error: errors.append(str(error)))
        page.goto(url)
        expect(page.get_by_role('button', name='Sign in', exact=True)).to_be_visible()
        page.get_by_label('Password', exact=True).fill('incorrect-disposable-password')
        page.get_by_role('button', name='Sign in', exact=True).click()
        expect(page.get_by_role('alert')).to_contain_text('invalid credentials')
        page.screenshot(path=str(args.evidence / 'local-invalid-login.png'), full_page=True)
        page.wait_for_timeout(1100)
        page.get_by_label('Password', exact=True).fill((lab / 'local-password').read_text())
        page.keyboard.press('Enter')
        expect(page.get_by_role('button', name='Deploy Controller', exact=True)).to_be_visible()
        security_evidence = {}

        def security_headers(endpoint, cookie_name, label):
            response = context.request.get(endpoint)
            assert response.status == 200
            headers = response.headers
            assert headers['x-frame-options'] == 'DENY'
            assert headers['x-content-type-options'] == 'nosniff'
            assert headers['referrer-policy'] == 'no-referrer'
            assert headers['cache-control'] == 'no-store'
            csp = headers['content-security-policy']
            assert "frame-ancestors 'none'" in csp and "object-src 'none'" in csp
            assert 'unsafe-inline' not in csp and 'unsafe-eval' not in csp
            cookies = [cookie for cookie in context.cookies(endpoint) if cookie['name'] == cookie_name]
            assert len(cookies) == 1, [cookie['name'] for cookie in context.cookies(endpoint)]
            cookie = cookies[0]
            assert cookie['secure'] and cookie['httpOnly'] and cookie['sameSite'] == 'Strict'
            assert cookie['path'] == '/' and not cookie['domain'].startswith('.')
            security_evidence[label] = {
                'headers': {key: headers[key] for key in ('content-security-policy', 'x-frame-options',
                    'x-content-type-options', 'referrer-policy', 'cache-control')},
                'cookie': {key: cookie[key] for key in ('name', 'secure', 'httpOnly', 'sameSite', 'path', 'domain')},
            }

        if args.security:
            assert len((lab / 'controller-password').read_text()) == 16
            security_headers(url, '__Host-nex_local_session', 'local')
        page.wait_for_timeout(1500)
        page.screenshot(path=str(args.evidence / 'local-overview.png'), full_page=True)
        page.set_viewport_size({'width': 390, 'height': 844})
        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), 'narrow page overflows'
        page.screenshot(path=str(args.evidence / 'local-overview-narrow.png'), full_page=True)
        page.set_viewport_size({'width': 1440, 'height': 1000})
        results.append('Real host-local HTTPS invalid login, keyboard sign-in and overview')
        session = context.request.get(url + '/api/local/v1/session').json()
        anonymous = p.request.new_context(ignore_https_errors=True)
        assert anonymous.get(url + '/api/local/v1/vms').status == 401
        anonymous.dispose()
        mutation = url + '/api/local/v1/deployments'
        assert context.request.post(mutation, data={}).status == 403
        assert context.request.post(mutation, data={}, headers={'Origin': url}).status == 403
        assert context.request.post(mutation, data={}, headers={
            'Origin': url, 'X-CSRF-Token': session['csrf_token']}).status == 400
        results.append('Real API rejects anonymous inventory, cross-origin/CSRF-less control and invalid deployment')
        response = context.request.get(url + '/api/local/v1/host')
        assert response.status == 200, response.text()
        host = response.json()
        (args.evidence / 'host-preflight.json').write_text(json.dumps(host, indent=2) + '\n')
        if args.deploy:
            existing = context.request.get(url + '/api/local/v1/deployments').json()
            if not existing:
                assert host['preflight']['ready'], host['preflight']
            manifest_path = '/usr/share/nex/appliance/manifest.json' if args.packaged else '/opt/nex-qa/appliance/manifest.json'
            manifest = subprocess.check_output(ssh + ['cat ' + manifest_path])
            try:
                subprocess.run(ssh + ['cat > ' + manifest_path], input=manifest + b' ', check=True)
                rejected = context.request.get(url + '/api/local/v1/host').json()['preflight']
                assert not rejected['ready'] and any('signature' in b for b in rejected['blockers']), rejected
                (args.evidence / 'signature-rejection.json').write_text(json.dumps(rejected, indent=2) + '\n')
            finally:
                subprocess.run(ssh + ['cat > ' + manifest_path], input=manifest, check=True)
            results.append('Modified signed manifest rejected before VM creation; original restored')
            if not existing:
                page.get_by_role('button', name='Deploy Controller', exact=True).click()
                expect(page.get_by_label('Local storage', exact=True)).to_have_value(host['storage_dir'])
                page.get_by_label('LAN bridge', exact=True).select_option(host['bridge'])
                page.get_by_label('IPv4 address / prefix', exact=True).fill('192.168.122.10/24')
                page.get_by_label('IPv4 gateway', exact=True).fill('192.168.122.1')
                page.get_by_label('DNS servers', exact=True).fill('192.168.122.1')
                page.get_by_label('Controller administrator password', exact=True).fill((lab / 'controller-password').read_text())
                page.get_by_role('button', name='Review deployment').click()
                page.screenshot(path=str(args.evidence / 'deployment-review.png'), full_page=True)
                page.get_by_role('button', name='Create VM & install Controller', exact=True).click()
            elif existing[0]['status'] in ('needs_attention', 'failed'):
                vms = context.request.get(url + '/api/local/v1/vms').json()
                if any(vm['id'] == existing[0]['vm_id'] and vm['state'] == 'shut off' and vm['power_allowed'] for vm in vms):
                    page.get_by_role('button', name='Start', exact=True).click()
                    page.get_by_label('Type ' + host['hostname'] + ' to confirm', exact=True).fill(host['hostname'])
                    page.get_by_role('button', name='Confirm start', exact=True).click()
                    page.wait_for_timeout(1000)
                page.get_by_role('button', name='Retry deployment', exact=True).click()
            observed = []
            started = time.monotonic()
            deadline = time.monotonic() + 900
            while time.monotonic() < deadline:
                operations = context.request.get(url + '/api/local/v1/deployments').json()
                assert len(operations) <= 1, operations
                if operations:
                    operation = operations[0]
                    state = (operation.get('stage'), operation.get('status'), operation.get('progress'))
                    if not observed or state != observed[-1]:
                        observed.append(state)
                        print('Deployment:', state, flush=True)
                        page.screenshot(path=str(args.evidence / 'deployment-progress.png'), full_page=True)
                    if operation['status'] == 'ready' or (operation['status'] in ('needs_attention', 'failed') and time.monotonic() - started > 30):
                        page.reload()
                        page.wait_for_timeout(1500)
                        page.screenshot(path=str(args.evidence / 'deployment-progress.png'), full_page=True)
                        break
                page.wait_for_timeout(2000)
            else:
                raise AssertionError('Controller deployment did not finish within 15 minutes')
            (args.evidence / 'deployment.json').write_text(json.dumps({'operation': operation, 'observed_progress': observed}, indent=2) + '\n')
            assert operation['status'] == 'ready', operation
            results.append('Browser deployment reached ready on real nested KVM')
            page.reload()
            page.wait_for_timeout(1500)
            page.screenshot(path=str(args.evidence / 'deployment-ready.png'), full_page=True)
            domains = subprocess.check_output(ssh + ['virsh -c qemu:///system list --all --uuid'], text=True).split()
            assert domains == [operation['vm_id']], domains
            results.append('Page refresh retains one deployment and exactly one libvirt UUID')
            vm_id = operation['vm_id']

            def guest(command):
                def qga(body):
                    command_line = shlex.join(['virsh', '-c', 'qemu:///system',
                                              'qemu-agent-command', vm_id, json.dumps(body)])
                    return json.loads(subprocess.check_output(ssh + [command_line], text=True))['return']
                pid = qga({'execute': 'guest-exec', 'arguments': {
                    'path': '/bin/sh', 'arg': ['-c', 'set -eu; ' + command], 'capture-output': True}})['pid']
                for _ in range(90):
                    status = qga({'execute': 'guest-exec-status', 'arguments': {'pid': pid}})
                    if status.get('exited'):
                        output = base64.b64decode(status.get('out-data', '')).decode()
                        assert status.get('exitcode') == 0, status
                        return output
                    time.sleep(1)
                raise AssertionError('guest command timed out')

            baseline = guest('systemctl is-active nex-controller qemu-guest-agent; '
                             'test ! -e /etc/nex-controller/bootstrap.json; '
                             'test ! -e /etc/nex-controller/admin-password; '
                             'test ! -e /var/lib/cloud/instance/user-data.txt; '
                             'test ! -e /run/cloud-init/instance-data-sensitive.json; '
                             'test -e /var/lib/nex-controller/appliance-initialized')
            (args.evidence / 'controller-baseline.txt').write_text(baseline)
            results.append('Actual guest services active and bootstrap credentials/cloud-init cache removed')
            with socket.socket() as sock:
                sock.bind(('127.0.0.1', 0))
                controller_port = sock.getsockname()[1]
            tunnel = subprocess.Popen(ssh[:-1] + ['-o', 'ExitOnForwardFailure=yes', '-N', '-L',
                f'127.0.0.1:{controller_port}:192.168.122.10:8443', ssh[-1]],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                controller_url = f'https://127.0.0.1:{controller_port}'
                page.wait_for_timeout(1000)
                controller = context.new_page()
                controller.goto(controller_url)
                controller.get_by_label('Username', exact=True).fill('root')
                controller.get_by_label('Password', exact=True).fill((lab / 'controller-password').read_text())
                controller.get_by_role('button', name='Sign in', exact=True).click()
                expect(controller.get_by_role('link', name='Main site', exact=True).first).to_be_visible()
                if args.security:
                    security_headers(controller_url, '__Host-nex_session', 'controller')
                inventory = {}
                for resource in ('sites', 'pools', 'hosts', 'vms'):
                    reply = context.request.get(controller_url + '/api/v1/' + resource)
                    assert reply.status == 200, reply.text()
                    inventory[resource] = reply.json()
                    assert len(inventory[resource]) == 1, inventory[resource]
                assert inventory['vms'][0]['id'] == vm_id, inventory['vms']
                assert inventory['vms'][0]['host_id'] == inventory['hosts'][0]['id']
                assert inventory['pools'][0]['master_host_id'] == inventory['hosts'][0]['id'] and inventory['pools'][0]['name'] == inventory['hosts'][0]['hostname'], inventory['pools']
                (args.evidence / 'controller-inventory.json').write_text(json.dumps(inventory, indent=2) + '\n')
                controller.screenshot(path=str(args.evidence / 'controller-ready.png'), full_page=True)
                results.append('Actual Controller browser sign-in and exactly one Site/Pool/enrolled host/adopted original VM UUID')
                guest('systemctl stop nex-controller')
                vms = context.request.get(url + '/api/local/v1/vms').json()
                assert len(vms) == 1 and vms[0]['state'] == 'running', vms
                page.reload()
                page.wait_for_timeout(1000)
                page.screenshot(path=str(args.evidence / 'controller-outage-local-access.png'), full_page=True)
                guest('systemctl start nex-controller')
                results.append('Controller service outage preserves running VM and independent local HTTPS inventory')
                page.get_by_role('button', name='Shut down', exact=True).click()
                confirm = page.get_by_label('Type ' + host['hostname'] + ' to confirm', exact=True)
                confirm.fill('wrong-host')
                expect(page.get_by_role('button', name='Confirm shutdown', exact=True)).to_be_disabled()
                page.screenshot(path=str(args.evidence / 'shutdown-confirmation.png'), full_page=True)
                confirm.fill(host['hostname'])
                page.get_by_role('button', name='Confirm shutdown', exact=True).click()
                for _ in range(60):
                    states = context.request.get(url + '/api/local/v1/vms').json()
                    if states[0]['state'] == 'shut off':
                        break
                    page.wait_for_timeout(2000)
                else:
                    raise AssertionError('Graceful shutdown did not complete')
                page.reload()
                page.wait_for_timeout(1000)
                page.screenshot(path=str(args.evidence / 'controller-stopped.png'), full_page=True)
                page.get_by_role('button', name='Start', exact=True).click()
                page.get_by_label('Type ' + host['hostname'] + ' to confirm', exact=True).fill(host['hostname'])
                page.get_by_role('button', name='Confirm start', exact=True).click()
                for _ in range(90):
                    try:
                        if context.request.get(controller_url, timeout=2000).status == 200:
                            break
                    except Exception:
                        pass
                    page.wait_for_timeout(2000)
                else:
                    raise AssertionError('Controller HTTPS did not recover after cold start')
                restarted = guest('systemctl is-active nex-controller; '
                    'test -e /etc/cloud/cloud-init.disabled; test ! -e /etc/nex-controller/bootstrap.json; '
                    'test -e /var/lib/nex-controller/appliance-initialized; '
                    'test "$(nproc)" -eq 2; '
                    'test "$(df -B1 --output=size / | tail -n 1)" -gt 32212254720; '
                    'df -B1 /; nproc; grep MemTotal /proc/meminfo; ip -4 -o addr show nex0')
                (args.evidence / 'controller-cold-start.txt').write_text(restarted)
                results.append('Wrong shutdown target rejected; actual graceful shutdown/cold start preserves network and initialized Controller without seed')
                service = 'nex-core-local' if args.packaged else 'nex-core-qa'
                page.reload()
                page.wait_for_timeout(1000)
                subprocess.run(ssh + ['systemctl stop ' + service], check=True)
                try:
                    expect(page.get_by_text('Host observations are stale.', exact=False)).to_be_visible(timeout=15000)
                    expect(page.get_by_role('button', name='Shut down', exact=True)).to_be_disabled()
                    page.screenshot(path=str(args.evidence / 'local-service-offline.png'), full_page=True)
                    assert context.request.get(controller_url).status == 200
                finally:
                    subprocess.run(ssh + ['systemctl start ' + service], check=True)
                for _ in range(30):
                    try:
                        if context.request.get(url + '/healthz', timeout=2000).status == 200:
                            break
                    except Exception:
                        pass
                    page.wait_for_timeout(500)
                else:
                    raise AssertionError('Local service did not become ready after restart')
                page.reload()
                page.get_by_label('Password', exact=True).fill((lab / 'local-password').read_text())
                page.get_by_role('button', name='Sign in', exact=True).click()
                page.wait_for_timeout(1500)
                after = context.request.get(url + '/api/local/v1/vms').json()
                assert len(after) == 1 and after[0]['id'] == vm_id and after[0]['state'] == 'running'
                assert len(context.request.get(url + '/api/local/v1/deployments').json()) == 1
                results.append('Local service outage disables stale GUI controls while Controller remains available; restart retains one running UUID')
                if args.security:
                    for label, endpoint in (('local', url + '/api/local/v1/session'),
                                            ('controller', controller_url + '/api/v1/session')):
                        statuses = []
                        for attempt in range(6):
                            response = context.request.post(endpoint, data={
                                'username': 'root', 'password': 'incorrect-disposable-password'},
                                headers={'Origin': endpoint.split('/api/')[0],
                                         'X-Forwarded-For': f'192.0.2.{attempt + 1}'})
                            statuses.append(response.status)
                        assert statuses[-1] == 429 and set(statuses) <= {401, 429}, statuses
                        security_evidence[label]['login_attempt_statuses'] = statuses
                    (args.evidence / 'security.json').write_text(json.dumps(security_evidence, indent=2) + '\n')
                    results.append('Hardened host cookies and CSP; 16-code-point Unicode bootstrap password; peer login throttling ignores spoofed forwarding headers')
            finally:
                tunnel.terminate()
                tunnel.wait(timeout=10)
        assert not errors, errors
        browser.close()
    (args.evidence / 'results.json').write_text(json.dumps({'results': results}, indent=2) + '\n')
    print('\n'.join('PASS: ' + result for result in results))


if __name__ == '__main__':
    main()
