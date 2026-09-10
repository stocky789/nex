#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Browser console acceptance against a private copy of a disposable appliance lab.

Copies an explicitly disposable nested-KVM lab (see managed_vm_lab.py), runs a fresh
Controller on the workstation, enrolls the copied host, creates a stopped guest through
the managed-VM API, starts it with the lab host's virsh, then drives the Controller
Console tab and the host-local Open console flow with Playwright, including blocked,
disconnected and lost-tunnel states. QEMU uses loopback forwards only; nothing on the
workstation or any production host is changed. Keep private keys out of QA evidence.
"""
import argparse
import hashlib
import http.client
import json
import os
import shlex
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.parse
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from managed_vm_lab import ROOT, prepare, ssh  # noqa: E402
from phase1 import Lab, eventually, run  # noqa: E402


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--copy-from', type=Path, required=True, help='explicitly disposable nex-appliance-lab.* directory')
    parser.add_argument('--iso', type=Path, default=ROOT / '.cache/nex-iso/debian-13.6.0-amd64-netinst.iso')
    parser.add_argument('--evidence', type=Path, default=ROOT / 'docs/qa/console')
    parser.add_argument('--keep-running', action='store_true')
    args = parser.parse_args()
    from playwright.sync_api import sync_playwright, expect
    go = os.environ.get('GO') or ROOT / '.cache/toolchains/go/bin/go'
    evidence = args.evidence
    evidence.mkdir(parents=True, exist_ok=True)
    hostlab = prepare(args.copy_from)
    root_password = 'Disposable-console-qa-root-8812!'
    work = Path(tempfile.mkdtemp(prefix='nex-console-qa.', dir=ROOT / '.cache'))
    lab = Lab(work)
    report = {'completed': False, 'lab': str(hostlab), 'passed': lab.results, 'captures': [], 'limits': []}
    forward = None
    try:
        for binary, package in [('controller', './cmd/nex-controller'), ('agent', './cmd/nex-core-agent'), ('local', './cmd/nex-core-local')]:
            run(go, 'build', '-o', work / binary, package, cwd=ROOT)
        source = work / 'source.tar.gz'
        run('python3', ROOT / 'scripts/build-source.py', '--output', source, env={**os.environ, 'GO': str(go)}, cwd=ROOT)
        cert, key = work / 'tls.pem', work / 'tls.key'
        run('openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes',
            '-keyout', key, '-out', cert, '-days', '1', '-subj', '/CN=localhost',
            '-addext', 'subjectAltName=DNS:localhost,IP:10.0.2.2,IP:127.0.0.1')
        password = work / 'password'
        password.write_text('Disposable-console-qa-4471!')
        password.chmod(0o600)
        controller_args = [work / 'controller', '-listen', f'127.0.0.1:{lab.port}', '-data-dir', work / 'controller-state',
            '-tls-cert', cert, '-tls-key', key, '-admin-password-file', password, '-source-archive', source,
            '-stale-after', '3s', '-ui-dir', ROOT / 'web/dist']
        lab.start('controller', *controller_args)
        lab.client(cert)
        eventually(lambda: lab.api('/api/v1/sites', expected=401), 'controller startup')
        session = lab.api('/api/v1/session', {'username': 'admin', 'password': password.read_text()})
        lab.csrf = session['csrf_token']
        site = lab.api('/api/v1/sites', {'name': 'Console QA'}, expected=201)
        token = lab.api('/api/v1/enrollment-tokens', {'site_id': site['id']}, expected=201)['token']
        suffix = work.name.split('.')[-1]
        remote = f'/opt/nex-console-qa-{suffix}'
        state_dir = f'/var/lib/nex-console-qa-{suffix}'
        storage_path = f'/var/lib/libvirt/images/nex-console-qa-{suffix}'
        ssh(hostlab, f'mkdir -p {storage_path}; chmod 0711 {storage_path}; mkdir -m 700 {remote}', text=True)
        for name in ('agent', 'tls.pem'):
            ssh(hostlab, f'cat > {remote}/{name}', input=(work / name).read_bytes())
        ssh(hostlab, f'umask 077; cat > {remote}/token', input=token.encode())
        ssh(hostlab, f'chmod 700 {remote}/agent; {remote}/agent enroll --state-dir {state_dir} --controller https://10.0.2.2:{lab.port} --ca-file {remote}/tls.pem --token-file {remote}/token', text=True)
        host_id = json.loads(ssh(hostlab, f'cat {state_dir}/state.json', text=True).stdout)['host_id']
        pool = next(p for p in lab.api('/api/v1/pools') if p['master_host_id'] == host_id)
        unit = (ROOT / 'packaging/systemd/nex-core-agent.service').read_text()
        unit = unit.replace('StateDirectory=nex-core-agent', f'StateDirectory=nex-console-qa-{suffix}')
        unit = unit.replace('/usr/local/lib/nex/nex-core-agent', f'{remote}/agent')
        unit = unit.replace('/var/lib/nex-core-agent', state_dir)
        unit = unit.replace('--heartbeat 10s --probe-interval 10s --probe-timeout 3s', '--heartbeat 500ms --probe-interval 500ms --probe-timeout 2s')
        ssh(hostlab, 'cat > /etc/systemd/system/nex-console-qa.service', input=unit.encode())
        ssh(hostlab, 'systemctl daemon-reload; systemctl start nex-console-qa', text=True)

        def host():
            return next(h for h in lab.api('/api/v1/hosts') if h['id'] == host_id)
        eventually(lambda: host()['connection'] == 'fresh' and host().get('inventory', {}).get('cpu_count', 0) > 0, 'native inventory', timeout=60)
        assert 'console-v1' in host()['capabilities'], host()['capabilities']
        lab.passed('Enrolled native agent reports the console-v1 capability')

        def operation(op_id, status='succeeded'):
            def result():
                op = next(o for o in lab.api('/api/v1/operations') if o['id'] == op_id)
                assert op['status'] not in ('failed', 'needs_attention') or op['status'] == status, op
                return op if op['status'] == status else None
            return eventually(result, f'operation {op_id} {status}', timeout=300)
        common = {'pool_id': pool['id'], 'host_id': host_id}
        storage = lab.api('/api/v1/storage', {**common, 'request_id': 'console-storage', 'name': 'Console QA storage', 'path': storage_path}, expected=201)
        operation(storage['operation_id'])
        network = lab.api('/api/v1/networks', {**common, 'request_id': 'console-network', 'name': 'Console QA NAT', 'kind': 'network', 'source': 'default'}, expected=201)
        operation(network['operation_id'])
        query = urllib.parse.urlencode({'storage_id': storage['id'], 'name': 'Debian console QA.iso', 'request_id': 'console-media'})
        conn = http.client.HTTPSConnection('localhost', lab.port, context=ssl.create_default_context(cafile=str(cert)), timeout=300)
        conn.putrequest('POST', '/api/v1/media?' + query)
        conn.putheader('Cookie', '; '.join(f'{c.name}={c.value}' for c in lab.cookies))
        conn.putheader('X-CSRF-Token', lab.csrf)
        conn.putheader('Origin', lab.url)
        conn.putheader('Content-Type', 'application/octet-stream')
        conn.putheader('Content-Length', str(args.iso.stat().st_size))
        conn.endheaders()
        with args.iso.open('rb') as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                conn.send(chunk)
        response = conn.getresponse()
        media = json.loads(response.read())
        assert response.status == 201, media
        conn.close()
        operation(media['operation_id'])
        vm_name = 'console-qa-' + suffix.lower().replace('_', '')
        created = lab.api('/api/v1/vms', {**common, 'request_id': 'console-create', 'name': vm_name, 'media_id': media['id'],
            'cpu': {'count': 2, 'model': 'host-passthrough'}, 'memory': {'mode': 'dedicated', 'size_mib': 1024},
            'disks': [{'storage_id': storage['id'], 'name': 'system', 'size_gib': 2, 'bus': 'virtio', 'thin': True, 'boot_order': 2}],
            'nics': [{'network_id': network['id'], 'model': 'virtio'}],
            'boot': {'firmware': 'bios', 'media_boot_order': 1}}, expected=202)
        operation(created['operation']['id'])
        vm_id = created['vm_id']
        lab.passed('Stopped guest created through the managed-VM API with the Debian ISO attached')

        def observed():
            return next(v for v in lab.api('/api/v1/vms') if v['id'] == vm_id)
        eventually(lambda: observed()['freshness'] == 'fresh' and observed()['state'] == 'shut off', 'stopped observation', timeout=30)
        denied = lab.api(f'/api/v1/vms/{vm_id}/console', {}, expected=409)
        assert 'not observed running' in denied['error'], denied
        lab.api(f'/api/v1/vms/{vm_id}/console', {}, expected=403, headers={'X-CSRF-Token': ''})
        lab.api('/api/v1/vms/00000000-0000-4000-a000-000000000000/console', {}, expected=404)
        lab.passed('Console ticket refused for a stopped VM, without CSRF, and for an unknown VM')

        captures = []

        def capture(page, name):
            path = evidence / f'{name}.png'
            page.screenshot(path=str(path), full_page=True)
            captures.append(name)

        def canvas_pixels(page):
            return page.evaluate('''() => {
                const c = document.querySelector('.console-screen canvas');
                if (!c) return null;
                const ctx = c.getContext('2d');
                const d = ctx.getImageData(0, 0, c.width, c.height).data;
                let lit = 0, sum = 0;
                for (let i = 0; i < d.length; i += 4) { const v = d[i] + d[i+1] + d[i+2]; sum += v; if (v > 60) lit++; }
                return {width: c.width, height: c.height, lit, digest: sum};
            }''')
        with sync_playwright() as playwright:
            browser = playwright.chromium.launch(headless=True)
            context = browser.new_context(ignore_https_errors=True, viewport={'width': 1440, 'height': 1000})
            page = context.new_page()
            errors = []
            page.on('pageerror', lambda error: errors.append(str(error)))
            page.goto(lab.url)
            page.get_by_label('Username').fill('admin')
            page.get_by_label('Password', exact=True).fill(password.read_text())
            page.get_by_role('button', name='Sign in').click()
            page.wait_for_load_state('networkidle')
            page.goto(lab.url + '/#/vms/' + vm_id)
            page.get_by_role('button', name='Console', exact=True).click()
            expect(page.get_by_text('observed shut off')).to_be_visible()
            assert page.get_by_role('button', name='Connect', exact=True).count() == 0
            capture(page, 'controller-console-blocked-stopped')
            lab.passed('Controller Console tab explains a stopped VM and offers no manual connect')

            # Central power controls: refusals first, then Start from the General tab.
            lab.api(f'/api/v1/vms/{vm_id}/power', {'request_id': 'power-bad', 'action': 'explode'}, expected=400)
            lab.api(f'/api/v1/vms/{vm_id}/power', {'request_id': 'power-1', 'action': 'start'}, expected=403, headers={'X-CSRF-Token': ''})
            page.get_by_role('button', name='General', exact=True).click()
            expect(page.get_by_role('button', name='Reboot', exact=True)).to_be_disabled()
            capture(page, 'controller-power-stopped')
            page.get_by_role('button', name='Start', exact=True).click()
            expect(page.get_by_text('Start requested')).to_be_visible(timeout=10000)
            operation(next(o for o in lab.api('/api/v1/operations') if o['kind'] == 'vm-power')['id'])
            eventually(lambda: observed()['freshness'] == 'fresh' and observed()['state'] == 'running', 'running observation', timeout=60)
            capture(page, 'controller-power-started')
            audit = lab.api('/api/v1/audit')
            assert any(a['action'] == 'vm-power' and a['actor'] == 'admin' for a in audit)
            lab.passed('Start from the Controller UI ran as a durable host task, the guest is observed running and the request is audited')
            # The Console tab was left open while the VM started: it must connect by itself.
            page.get_by_role('button', name='Console', exact=True).click()
            expect(page.get_by_text('connected', exact=True)).to_be_visible(timeout=45000)
            capture(page, 'controller-console-ready')
            eventually(lambda: (canvas_pixels(page) or {}).get('lit', 0) > 2000, 'guest display frame', timeout=30)
            before = canvas_pixels(page)
            capture(page, 'controller-console-connected')
            audit = lab.api('/api/v1/audit')
            assert any(a['action'] == 'console.open' and a['resource_id'] == vm_id and a['actor'] == 'admin' for a in audit)
            lab.passed('Controller console connected through the host tunnel; guest boot screen rendered and console.open audited')
            page.locator('.console-screen canvas').click()
            page.keyboard.press('ArrowDown')
            eventually(lambda: canvas_pixels(page)['digest'] != before['digest'], 'guest reacts to keyboard input', timeout=15)
            capture(page, 'controller-console-input')
            lab.passed('Keyboard input from the browser changed the guest display')
            page.set_viewport_size({'width': 360, 'height': 780})
            time.sleep(1)
            capture(page, 'controller-console-connected-360')
            page.set_viewport_size({'width': 1440, 'height': 1000})
            # Leaving the tab ends the console; returning issues a new ticket and re-attaches.
            page.get_by_role('button', name='General', exact=True).click()
            assert page.locator('.console-screen').count() == 0
            before_tickets = sum(1 for a in lab.api('/api/v1/audit') if a['action'] == 'console.open')
            page.get_by_role('button', name='Console', exact=True).click()
            expect(page.get_by_text('connected', exact=True)).to_be_visible(timeout=30000)
            assert sum(1 for a in lab.api('/api/v1/audit') if a['action'] == 'console.open') == before_tickets + 1
            capture(page, 'controller-console-disconnected')
            lab.passed('Leaving and reopening the Console tab ends the session and re-attaches with a new ticket')
            ssh(hostlab, 'systemctl stop nex-console-qa', text=True)
            expect(page.get_by_text('Console connection ended unexpectedly')).to_be_visible(timeout=20000)
            capture(page, 'controller-console-lost')
            eventually(lambda: host()['connection'] == 'stale', 'stale host')
            expect(page.get_by_text('not freshly connected')).to_be_visible(timeout=20000)
            capture(page, 'controller-console-blocked-stale')
            stale = lab.api(f'/api/v1/vms/{vm_id}/console', {}, expected=409)
            assert 'not freshly connected' in stale['error']
            lab.passed('Stopping the host agent ends the live console honestly and blocks new tickets until the host reports again')
            ssh(hostlab, 'systemctl start nex-console-qa', text=True)
            eventually(lambda: host()['connection'] == 'fresh', 'agent restart', timeout=30)
            eventually(lambda: observed()['freshness'] == 'fresh' and observed()['state'] == 'running', 'running observation after restart', timeout=60)
            # The open tab notices the host is back and reconnects without any click.
            expect(page.get_by_text('connected', exact=True)).to_be_visible(timeout=45000)
            capture(page, 'controller-console-reconnected')
            lab.passed('The open Console tab reconnects by itself once the host reports again')
            # Stop the agent first so the ticket is never delivered; the host is still fresh for a moment.
            ssh(hostlab, 'systemctl stop nex-console-qa', text=True)
            ticket = lab.api(f'/api/v1/vms/{vm_id}/console', {}, expected=201)
            ws_conn = http.client.HTTPSConnection('localhost', lab.port, context=ssl.create_default_context(cafile=str(cert)), timeout=40)
            ws_conn.putrequest('GET', ticket['websocket'])
            ws_conn.putheader('Cookie', '; '.join(f'{c.name}={c.value}' for c in lab.cookies))
            ws_conn.putheader('Origin', lab.url)
            ws_conn.putheader('Upgrade', 'websocket')
            ws_conn.putheader('Connection', 'Upgrade')
            ws_conn.putheader('Sec-WebSocket-Version', '13')
            ws_conn.putheader('Sec-WebSocket-Key', 'dGhlIHNhbXBsZSBub25jZQ==')
            ws_conn.endheaders()
            timeout_response = ws_conn.getresponse()
            timeout_body = timeout_response.read().decode()
            assert timeout_response.status == 504, (timeout_response.status, timeout_body)
            ws_conn.close()
            lab.passed('A ticket the host never attaches times out with 504 after the documented wait')
            ssh(hostlab, 'systemctl start nex-console-qa', text=True)
            eventually(lambda: host()['connection'] == 'fresh', 'agent restart', timeout=30)
            # Reboot (ACPI) is accepted; the isolinux boot menu ignores it, so the guest stays running.
            page.get_by_role('button', name='General', exact=True).click()
            page.get_by_role('button', name='Reboot', exact=True).click()
            expect(page.get_by_text('Reboot ' + vm_name + '?')).to_be_visible()
            capture(page, 'controller-power-confirm-reboot')
            page.get_by_role('button', name='Confirm reboot', exact=True).click()
            expect(page.get_by_text('Reboot requested')).to_be_visible(timeout=10000)
            reboot_op = max((o for o in lab.api('/api/v1/operations') if o['kind'] == 'vm-power'), key=lambda o: o['created_at'])
            operation(reboot_op['id'])
            assert observed()['state'] == 'running'
            lab.passed('Reboot request was issued as a host task; the guest without ACPI handling stays running and the UI says so')
            assert not errors, errors

            # Host-local page: run the freshly built local service on the lab host itself.
            local_remote = f'{remote}/local'
            ssh(hostlab, f'mkdir -p {local_remote}/web {local_remote}/api', text=True)
            ssh(hostlab, f'cat > {local_remote}/nex-core-local', input=(work / 'local').read_bytes())
            ssh(hostlab, f'cat > {local_remote}/source.tar.gz', input=source.read_bytes())
            web_tar = subprocess.run(['tar', '-C', str(ROOT / 'web/dist'), '-cf', '-', '.'], check=True, capture_output=True).stdout
            ssh(hostlab, f'tar -C {local_remote}/web -xf -', input=web_tar)
            api_tar = subprocess.run(['tar', '-C', str(ROOT / 'docs/api'), '-cf', '-', '.'], check=True, capture_output=True).stdout
            ssh(hostlab, f'tar -C {local_remote}/api -xf -', input=api_tar)
            local_state = f'/var/lib/nex-console-qa-local-{suffix}'
            local_storage = f'{storage_path}-local'
            # The copied lab image predates the local root helper: build it from this tree on the
            # disposable copy (Debian toolchain from the mirror) so the local sign-in is the real PAM path.
            ssh(hostlab, f'cat > {local_remote}/nex-root-auth.c', input=(ROOT / 'packaging/core-local/nex-root-auth.c').read_bytes())
            ssh(hostlab, 'cat > /etc/pam.d/nex-core-local', input=(ROOT / 'packaging/core-local/nex-core-local.pam').read_bytes())
            ssh(hostlab, f'test -x /usr/lib/nex/nex-root-auth || (DEBIAN_FRONTEND=noninteractive apt-get install -y -q --no-install-recommends gcc libc6-dev libpam0g-dev >/dev/null 2>&1 && mkdir -p /usr/lib/nex && cc -O2 -Wall {local_remote}/nex-root-auth.c -lpam -o /usr/lib/nex/nex-root-auth)', text=True)
            assert ssh(hostlab, 'test -x /usr/lib/nex/nex-root-auth && echo present', text=True).stdout.strip() == 'present', 'root helper unavailable on the lab host'
            # Disposable copy only: give root a known password for the local sign-in walkthrough.
            ssh(hostlab, 'chpasswd', input=f'root:{root_password}\n'.encode())
            ssh(hostlab, f'mkdir -m 700 {local_state} && mkdir -p {local_storage} && chmod 700 {local_remote}/nex-core-local', text=True)
            # The workstation certificate already carries IP:127.0.0.1; the local page is reached through an SSH forward.
            ssh(hostlab, f'umask 077; cat > {local_state}/tls.crt', input=cert.read_bytes())
            ssh(hostlab, f'umask 077; cat > {local_state}/tls.key', input=key.read_bytes())
            ssh(hostlab, f'systemd-run --collect --unit nex-console-qa-local-{suffix} -p WorkingDirectory={local_remote} {local_remote}/nex-core-local --listen 127.0.0.1:9444 --data-dir {local_state} --agent-state-dir {local_state}/no-agent '
                f'--tls-cert {local_state}/tls.crt --tls-key {local_state}/tls.key --ui-dir {local_remote}/web --docs-dir {local_remote}/api '
                f'--source-archive {local_remote}/source.tar.gz --storage-dir {local_storage} --bridge virbr0', text=True)
            ports = json.loads((hostlab / 'ports.json').read_text())
            local_port = Lab(work).port
            forward = subprocess.Popen(['ssh', '-N', '-o', 'BatchMode=yes', '-o', f'UserKnownHostsFile={hostlab}/known_hosts', '-i', str(hostlab / 'id_ed25519'),
                '-p', str(ports['ssh']), '-L', f'127.0.0.1:{local_port}:127.0.0.1:9444', 'root@127.0.0.1'])
            local_url = f'https://127.0.0.1:{local_port}'
            time.sleep(3)
            local_context = browser.new_context(ignore_https_errors=True, viewport={'width': 1440, 'height': 1000})
            local_page = local_context.new_page()
            local_page.on('pageerror', lambda error: errors.append(str(error)))
            def local_ready():
                try:
                    return local_page.goto(local_url).ok
                except Exception:
                    return False
            eventually(local_ready, 'local page', timeout=30)
            local_page.get_by_label('Password', exact=True).fill(root_password)
            local_page.get_by_role('button', name='Sign in').click()
            row = local_page.get_by_role('row', name=vm_name)
            expect(row.get_by_role('button', name='Open console')).to_be_visible(timeout=30000)
            capture(local_page, 'local-overview-open-console')
            row.get_by_role('button', name='Open console').click()
            expect(local_page.get_by_text('connected', exact=True)).to_be_visible(timeout=30000)
            eventually(lambda: (canvas_pixels(local_page) or {}).get('lit', 0) > 2000, 'local guest display frame', timeout=30)
            capture(local_page, 'local-console-connected')
            lab.passed('Host-local page opened the same running guest console directly on the host, without Controller')
            local_page.get_by_role('button', name='Back to overview').click()
            # Force stop through the Controller UI ends the guest the local page is watching.
            page.get_by_role('button', name='General', exact=True).click()
            page.get_by_role('button', name='Force stop', exact=True).click()
            page.get_by_role('button', name='Confirm force stop', exact=True).click()
            expect(page.get_by_text('Force stop requested')).to_be_visible(timeout=10000)
            stop_op = max((o for o in lab.api('/api/v1/operations') if o['kind'] == 'vm-power'), key=lambda o: o['created_at'])
            operation(stop_op['id'])
            expect(row.get_by_text('Start the VM first')).to_be_visible(timeout=30000)
            capture(page, 'controller-power-force-stopped')
            lab.passed('Force stop from the Controller UI stopped the guest; the host-local page reflects it')
            capture(local_page, 'local-overview-stopped')
            lab.passed('Host-local page hides the console for a stopped VM')
            eventually(lambda: observed()['state'] == 'shut off', 'stopped observation', timeout=30)
            assert not errors, errors
            chromium = browser.version
            browser.close()
        report['captures'] = captures
        report['versions'] = {
            'host': ssh(hostlab, 'cat /etc/os-release | head -2; virsh -c qemu:///system version | head -3', text=True).stdout,
            'novnc': json.loads((ROOT / 'web/node_modules/@novnc/novnc/package.json').read_text())['version'],
            'chromium': chromium,
        }
        report['artifacts'] = {p.name: hashlib.file_digest(p.open('rb'), 'sha256').hexdigest() for p in (work / 'controller', work / 'agent', work / 'local', ROOT / 'web/dist/app.js', ROOT / 'web/dist/local.js', args.iso)}
        report.update({'vm_id': vm_id, 'host_id': host_id, 'pool_id': pool['id'], 'completed': True,
            'limits': ['Nested KVM lab, not physical hardware', 'One viewer at a time', 'Root helper built from this tree on the disposable copy (lab image predates it)', 'Hosted CI not exercised']})
    finally:
        (evidence / 'results.json').write_text(json.dumps(report, indent=2) + '\n')
        if forward:
            forward.terminate()
        if not args.keep_running or not report['completed']:
            lab.close()
            pid = int((hostlab / 'qemu.pid').read_text())
            subprocess.run(['kill', str(pid)], check=False)
        print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
