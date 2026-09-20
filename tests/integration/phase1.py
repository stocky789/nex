#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Phase 1 acceptance using real processes, temporary state and simulated virsh.

Run: python3 tests/integration/phase1.py
Browser: python3 tests/integration/phase1.py --browser
Requires Go, C compiler, openssl; browser additionally requires Python Playwright,
Chromium and the built web/dist. No host services or VM state are changed.
"""
import argparse
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[2]


def run(*args, **kwargs):
    result = subprocess.run([str(x) for x in args], text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kwargs)
    if result.returncode:
        raise RuntimeError(f'{Path(str(args[0])).name} failed: {result.stderr}')
    return result


def eventually(check, message, timeout=15):
    until = time.monotonic() + timeout
    last = None
    while time.monotonic() < until:
        try:
            value = check()
            if value:
                return value
        except (urllib.error.URLError, ConnectionError) as exc:
            last = exc
        time.sleep(.15)
    raise AssertionError(f"{message}; last transport error: {last}")


class Lab:
    def __init__(self, path):
        self.path = path
        self.processes = []
        self.logs = []
        self.results = []
        self.csrf = ''
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            self.port = sock.getsockname()[1]
        self.url = f'https://localhost:{self.port}'
        self.cookies = http.cookiejar.CookieJar()

    def start(self, name, *args, env=None):
        log = (self.path / f'{name}.log').open('a')
        self.logs.append(log)
        process = subprocess.Popen([str(x) for x in args], stdout=log, stderr=log,
                                   cwd=ROOT, env=env)
        self.processes.append(process)
        return process

    def started(self, name='controller', timeout=60):
        """Wait for a just-started Controller to answer, then say why if it never does.

        Startup is a tenth of a second on an idle machine. CI runs every lane on one
        host, so this waits far longer than the work takes rather than calling a
        contended runner a failure; a process that really is broken still fails, and
        its log says so, which a bare connection refused cannot.
        """
        try:
            return eventually(lambda: self.api('/api/v1/sites', expected=401), name + ' startup', timeout=timeout)
        except AssertionError:
            log = self.path / f'{name}.log'
            if log.exists():
                print(f'--- {name}.log ---\n{log.read_text()[-4000:]}', file=sys.stderr, flush=True)
            raise

    def stop(self, process):
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)

    def close(self):
        for process in reversed(self.processes):
            self.stop(process)
        for log in self.logs:
            log.close()

    def client(self, cafile):
        self.http = urllib.request.build_opener(
            urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(cafile))),
            urllib.request.HTTPCookieProcessor(self.cookies))

    def api(self, path, body=None, method=None, expected=200, headers=None):
        headers = {'Content-Type': 'application/json', 'X-CSRF-Token': self.csrf, **(headers or {})}
        request = urllib.request.Request(self.url + path,
                    data=None if body is None else json.dumps(body).encode(),
                    method=method, headers=headers)
        try:
            response = self.http.open(request, timeout=5)
        except urllib.error.HTTPError as exc:
            response = exc
        with response:
            data = response.read()
            assert response.status == expected, (path, response.status, data.decode())
            return json.loads(data) if data else None

    def passed(self, message):
        self.results.append(message)
        print('PASS:', message, flush=True)


def acceptance(lab, args):
    go = os.environ.get('GO') or shutil.which('go') or ROOT / '.cache/toolchains/go/bin/go'
    controller = lab.path / 'controller'
    agent = lab.path / 'agent'
    run(go, 'build', '-o', controller, './cmd/nex-controller', cwd=ROOT)
    run(go, 'build', '-o', agent, './cmd/nex-core-agent', cwd=ROOT)
    source = lab.path / 'nex-source.tar.gz'
    run(sys.executable, ROOT / 'scripts/build-source.py', '--output', source,
        env={**os.environ, 'GO': str(go)}, cwd=ROOT)
    cert, key = lab.path / 'server.pem', lab.path / 'server.key'
    run('openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256',
        '-nodes', '-keyout', key, '-out', cert, '-days', '1', '-subj', '/CN=localhost',
        '-addext', 'subjectAltName=DNS:localhost')
    password = lab.path / 'password'
    password.write_text('Disposable-testing-8572!')
    password.chmod(0o600)
    controller_args = [controller, '-data-dir', lab.path / 'controller-state',
        '-listen', f'127.0.0.1:{lab.port}', '-tls-cert', cert, '-tls-key', key,
        '-admin-username', 'root', '-admin-password-file', password,
        '-stale-after', '2s', '-ui-dir', ROOT / 'web/dist', '-source-archive', source]
    cp = lab.start('controller', *controller_args)
    lab.client(cert)
    lab.started()
    lab.api('/api/v1/session', {'username': 'root', 'password': 'wrong'}, expected=401)
    session = lab.api('/api/v1/session', {'username': 'root', 'password': password.read_text()})
    lab.csrf = session['csrf_token']
    lab.api('/api/v1/sites', {'name': 'Denied'}, expected=403, headers={'X-CSRF-Token': ''})
    lab.api('/api/v1/sites', {'name': ''}, expected=400)
    lab.api('/api/v1/enrollment-tokens', {'site_id': 'absent'}, expected=400)
    site = lab.api('/api/v1/sites', {'name': 'Acceptance site'}, expected=201)
    # Pools are never created by hand; the manual route is gone.
    lab.api('/api/v1/pools', {'name': 'Manual pool', 'site_id': site['id']}, expected=404)
    lab.passed('administrator bootstrap, login, unauthorized/CSRF/invalid input rejection, site creation and removed manual pool creation')

    def token(host_id=None):
        payload = {'site_id': site['id']}
        if host_id:
            payload['host_id'] = host_id
        result = lab.api('/api/v1/enrollment-tokens', payload, expected=201)
        path = lab.path / 'token'
        path.write_text(result['token'])
        path.chmod(0o600)
        return path

    state_dir = lab.path / 'agent-state'
    enroll_args = [agent, 'enroll', '--controller', lab.url, '--ca-file', cert,
                   '--token-file', token(), '--state-dir', state_dir]
    run(*enroll_args)
    run(agent, 'status', '--state-dir', state_dir)
    state = lambda: json.loads((state_dir / 'state.json').read_text())
    host_id = state()['host_id']
    host = lambda: next(h for h in lab.api('/api/v1/hosts') if h['id'] == host_id)
    # Enrollment created the pool automatically: named after the host, host is its master.
    pool = next(p for p in lab.api('/api/v1/pools') if p['id'] == host()['pool_id'])
    assert pool['site_id'] == site['id'] and pool['name'] == host()['hostname'] and pool['master_host_id'] == host_id, pool
    assert len(lab.api('/api/v1/pools')) == 1
    assert any(a['action'] == 'pool.create' and a['resource_id'] == pool['id'] for a in lab.api('/api/v1/audit')), 'pool.create audit row'
    lab.api('/api/v1/enrollment-tokens', {'site_id': site['id'], 'host_id': host_id}, expected=409)
    assert (state_dir / 'state.json').stat().st_mode & 0o777 == 0o600
    # A distinct CSR must not reuse the consumed enrollment token.
    retry = subprocess.run([str(x) for x in enroll_args[:-1] + [lab.path / 'other-agent']],
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    assert retry.returncode != 0
    lab.passed('trusted TLS enrollment, automatic pool named after the master host, private state and single-use token')

    shim = lab.path / 'shim'
    shim.mkdir()
    mode = lab.path / 'probe-mode'
    mode.write_text('healthy')
    # A read-only fake libvirt command is the only simulated service in this lab.
    # Successful fork/exec can exceed 150ms on a loaded runner. Keep that latency
    # in the fixture so health/restart checks cannot depend on a subsecond deadline.
    (shim / 'virsh').write_text('#!/bin/sh\ncase "$(cat "$NEX_QA_MODE")" in\n'
        'healthy) sleep 0.25;;\n'
        'failed) echo "simulated libvirt unavailable" >&2; exit 1;;\n'
        'stalled) exec sleep 60;;\nesac\nexit 0\n')
    (shim / 'virsh').chmod(0o700)
    env = {**os.environ, 'PATH': str(shim) + os.pathsep + os.environ['PATH'], 'NEX_QA_MODE': str(mode)}
    # Retain the production probe deadline; only reporting/polling cadence is accelerated.
    # Keep the production probe deadline (3s). Heartbeat/probe cadence is accelerated
    # for CI; a loaded runner can still need longer than the default 15s eventually
    # budget for inventory to advance while a stalled probe is timing out.
    agent_args = [agent, 'run', '--state-dir', state_dir, '--heartbeat', '250ms',
                  '--probe-interval', '250ms', '--probe-timeout', '3s']
    ap = lab.start('agent', *agent_args, env=env)

    def check_status(name, status):
        h = host()
        return h['connection'] == 'fresh' and h.get('inventory') and any(
            c['name'] == name and c['status'] == status for c in h['inventory']['checks'])

    eventually(lambda: check_status('libvirt', 'healthy'), 'fresh healthy simulated libvirt')
    observed = host()['inventory']
    assert observed['cpu_count'] > 0 and observed['memory_bytes'] > 0 and observed['os_version']
    lab.passed('real CPU/RAM/OS/NIC inventory and fresh independent heartbeat (libvirt simulated)')
    mode.write_text('failed')
    eventually(lambda: check_status('libvirt', 'failed'), 'libvirt failure distinguished from connectivity')
    lab.passed('simulated libvirt failure remains fresh with failed service check')
    mode.write_text('stalled')
    eventually(lambda: check_status('libvirt', 'unknown'), 'stalled libvirt probe is unknown', timeout=30)
    before = host()['last_seen']
    eventually(lambda: host()['last_seen'] != before and check_status('inventory', 'healthy'),
               'other observations responsive during stalled probe', timeout=45)
    lab.passed('simulated stalled probe times out independently; native inventory and heartbeat continue')
    mode.write_text('healthy')
    eventually(lambda: check_status('libvirt', 'healthy'), 'probe recovery')

    lab.stop(ap)
    eventually(lambda: host()['connection'] == 'stale', 'stopped agent becomes stale')
    operation_body = {'kind': 'refresh-inventory', 'request_id': 'acceptance-refresh-1'}
    operation_url = f'/api/v1/hosts/{host_id}/operations'
    operation = lab.api(operation_url, operation_body, expected=202)
    assert lab.api(operation_url, operation_body)['id'] == operation['id']
    lab.api(operation_url, {'kind': 'refresh-inventory', 'request_id': 'conflict'}, expected=409)
    lab.stop(cp)
    cp = lab.start('controller', *controller_args)
    eventually(lambda: lab.api('/api/v1/session'), 'controller restart')
    assert host()['connection'] == 'stale'
    assert lab.api(operation_url, operation_body)['id'] == operation['id']
    ap = lab.start('agent', *agent_args, env=env)
    eventually(lambda: check_status('libvirt', 'healthy'), 'freshness after process restarts')
    eventually(lambda: any(o['id'] == operation['id'] and o['status'] == 'succeeded'
               for o in lab.api('/api/v1/operations')), 'durable refresh completes')
    assert state()['host_id'] == host_id
    assert len([o for o in lab.api('/api/v1/operations') if o['request_id'] == operation_body['request_id']]) == 1
    lab.passed('stale/unknown outage state, controller+agent restart identity, durable deduplicated refresh')

    # Disconnect only Controller; the independent agent must keep its local identity.
    lab.stop(cp)
    time.sleep(1)
    assert ap.poll() is None
    cp = lab.start('controller', *controller_args)
    eventually(lambda: check_status('libvirt', 'healthy'), 'controller outage reconciliation')
    lab.passed('agent survives controller outage and reconciles fresh inventory on reconnect')
    lab.stop(ap)
    old_cert = state()['certificate_pem']
    run(agent, 'rotate', '--state-dir', state_dir)
    run(agent, 'status', '--state-dir', state_dir)
    assert state()['certificate_pem'] != old_cert and state()['host_id'] == host_id
    ap = lab.start('agent', *agent_args, env=env)
    # Rotation restarts the agent and re-establishes mTLS; under a shared runner
    # the default 15s budget is not enough for healthy inventory to return.
    eventually(lambda: check_status('libvirt', 'healthy'), 'rotated identity reports', timeout=45)
    lab.passed('certificate rotation retains host identity and authenticated reporting')

    if args.browser:
        from phase1_browser import browser_acceptance
        browser_acceptance(lab, password.read_text(), host_id, pool['id'],
                           lambda: lab.stop(ap), mode.write_text)

    lab.api(f'/api/v1/hosts/{host_id}/revoke', {})
    assert host()['connection'] == 'revoked'
    lab.stop(ap)
    rotated = subprocess.run([str(agent), 'rotate', '--state-dir', str(state_dir)],
                             stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    assert rotated.returncode != 0
    lab.api(operation_url, {'kind': 'refresh-inventory', 'request_id': 'revoked'}, expected=409)
    run(agent, 'enroll', '--controller', lab.url, '--ca-file', cert, '--token-file', token(host_id),
        '--state-dir', state_dir, '--replace-host', host_id)
    assert state()['host_id'] == host_id
    ap = lab.start('agent', *agent_args, env=env)
    eventually(lambda: check_status('libvirt', 'healthy'), 're-enrolled host reconnects')
    assert len(lab.api('/api/v1/hosts')) == 1 and len(lab.api('/api/v1/pools')) == 1
    assert host()['pool_id'] == pool['id'] and next(p for p in lab.api('/api/v1/pools'))['master_host_id'] == host_id
    assert any(a['actor'] == 'root' and a['action'] == 'host.revoke' for a in lab.api('/api/v1/audit'))
    lab.passed('revocation rejects credential use and operations; explicit re-enrollment preserves identity; audit attribution')
    if args.evidence:
        args.evidence.parent.mkdir(parents=True, exist_ok=True)
        args.evidence.write_text(json.dumps({'results': lab.results, 'binaries': {
            p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in (controller, agent)},
            'simulation': 'virsh only; no real libvirt or guest lifecycle verified'}, indent=2) + '\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--browser', action='store_true')
    parser.add_argument('--evidence', type=Path, help='Write non-secret acceptance summary here')
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='nex-phase1-') as directory:
        lab = Lab(Path(directory))
        try:
            acceptance(lab, args)
        except Exception:
            for log in lab.path.glob('*.log'):
                print(f'{log.name}:\n{log.read_text()[-4000:]}', file=sys.stderr)
            raise
        finally:
            lab.close()


if __name__ == '__main__':
    main()
