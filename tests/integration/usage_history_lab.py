#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Usage history acceptance against a private copy of a disposable appliance lab.

Copies an explicitly disposable nested-KVM lab (see managed_vm_lab.py), runs a fresh Controller
on the workstation, enrolls the copied host with this tree's agent, creates and starts a guest,
then checks the stats query path end to end: real virsh formats, VM and host history, gaps after
a stop, persistence across an agent restart and across a Controller replacement, error statuses,
the Stats tabs in a browser, and history removal on VM deletion. Nothing outside the copy and
temporary workstation processes is changed. Keep private keys out of QA evidence.
"""
import argparse
import hashlib
import http.client
import json
import os
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timedelta
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from managed_vm_lab import ROOT, prepare, ssh  # noqa: E402
from phase1 import Lab, eventually, run  # noqa: E402

VIRSH = 'virsh -c qemu:///system'


def points(history, finished=False):
    """Map (series key, ISO time) -> value for every known point; finished drops the step in progress."""
    start = datetime.fromisoformat(history['start'].replace('Z', '+00:00'))
    step = timedelta(seconds=history['step_seconds'])
    return {(s['key'], (start + i * step).isoformat()): v for s in history['series'] for i, v in enumerate(s['values'])
            if v is not None and not (finished and i == len(s['values']) - 1)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--copy-from', type=Path, required=True, help='explicitly disposable nex-appliance-lab.* directory')
    parser.add_argument('--iso', type=Path, default=ROOT / '.cache/nex-iso/debian-13.6.0-amd64-netinst.iso')
    parser.add_argument('--evidence', type=Path, default=ROOT / 'docs/qa/usage-history')
    parser.add_argument('--keep-on-failure', action='store_true')
    args = parser.parse_args()
    from playwright.sync_api import sync_playwright, expect
    go = os.environ.get('GO') or ROOT / '.cache/toolchains/go/bin/go'
    evidence = args.evidence
    evidence.mkdir(parents=True, exist_ok=True)
    hostlab = prepare(args.copy_from)
    work = Path(tempfile.mkdtemp(prefix='nex-usage-qa.', dir=ROOT / '.cache'))
    lab = Lab(work)
    report = {'completed': False, 'passed': lab.results, 'measurements': {}, 'captures': [], 'limits': []}
    m = report['measurements']
    failures = report.setdefault('failures', [])
    try:
        for binary, package in [('controller', './cmd/nex-controller'), ('agent', './cmd/nex-core-agent')]:
            run(go, 'build', '-o', work / binary, package, cwd=ROOT)
        source = work / 'source.tar.gz'
        run('python3', ROOT / 'scripts/build-source.py', '--output', source, env={**os.environ, 'GO': str(go)}, cwd=ROOT)
        password = work / 'password'
        password.write_text('Disposable-usage-qa-5517!')
        password.chmod(0o600)

        def controller(name):
            cert, key = work / f'{name}.pem', work / f'{name}.key'
            run('openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes',
                '-keyout', key, '-out', cert, '-days', '1', '-subj', '/CN=localhost',
                '-addext', 'subjectAltName=DNS:localhost,IP:10.0.2.2,IP:127.0.0.1')
            process = lab.start(name, work / 'controller', '-listen', f'127.0.0.1:{lab.port}', '-data-dir', work / f'{name}-state',
                '-tls-cert', cert, '-tls-key', key, '-admin-password-file', password, '-source-archive', source,
                '-stale-after', '3s', '-ui-dir', ROOT / 'web/dist')
            lab.client(cert)
            lab.started()
            lab.csrf = lab.api('/api/v1/session', {'username': 'root', 'password': password.read_text()})['csrf_token']
            site = lab.api('/api/v1/sites', {'name': 'Usage QA'}, expected=201)
            return process, cert, lab.api('/api/v1/enrollment-tokens', {'site_id': site['id']}, expected=201)['token']

        a_process, cert, token = controller('controller-a')
        suffix = work.name.split('.')[-1]
        remote, state_dir, unit_name = f'/opt/nex-usage-qa-{suffix}', f'/var/lib/nex-usage-qa-{suffix}', 'nex-usage-qa'
        storage_path = f'/var/lib/libvirt/images/nex-usage-qa-{suffix}'
        ssh(hostlab, f'mkdir -p {storage_path}; chmod 0711 {storage_path}; mkdir -m 700 {remote}', text=True)
        ssh(hostlab, f'cat > {remote}/agent', input=(work / 'agent').read_bytes())

        def enroll(ca, enrollment_token):
            ssh(hostlab, f'cat > {remote}/ca.pem', input=ca.read_bytes())
            ssh(hostlab, f'umask 077; cat > {remote}/token', input=enrollment_token.encode())
            try:
                ssh(hostlab, f'chmod 700 {remote}/agent; {remote}/agent enroll --state-dir {state_dir} --controller https://10.0.2.2:{lab.port} --ca-file {remote}/ca.pem --token-file {remote}/token', text=True)
            except subprocess.CalledProcessError as exc:
                raise RuntimeError(f'agent enroll failed: {exc.stderr}{exc.stdout}') from exc
        enroll(cert, token)
        host_id = json.loads(ssh(hostlab, f'cat {state_dir}/state.json', text=True).stdout)['host_id']
        pool = next(p for p in lab.api('/api/v1/pools') if p['master_host_id'] == host_id)
        unit = (ROOT / 'packaging/systemd/nex-core-agent.service').read_text()
        unit = unit.replace('StateDirectory=nex-core-agent', f'StateDirectory=nex-usage-qa-{suffix}')
        unit = unit.replace('/usr/local/lib/nex/nex-core-agent', f'{remote}/agent')
        unit = unit.replace('/var/lib/nex-core-agent', state_dir)
        # A short heartbeat keeps the run quick; first-query latency is therefore bounded by 1 s, not the 10 s default.
        unit = unit.replace('--heartbeat 10s --probe-interval 10s --probe-timeout 3s', '--heartbeat 1s --probe-interval 1s --probe-timeout 3s')
        assert '--heartbeat 1s' in unit, 'unit template changed; update the heartbeat replacement'
        ssh(hostlab, f'cat > /etc/systemd/system/{unit_name}.service', input=unit.encode())
        ssh(hostlab, f'systemctl daemon-reload; systemctl start {unit_name}', text=True)
        ssh(hostlab, 'command -v strace >/dev/null || DEBIAN_FRONTEND=noninteractive apt-get install -y -q --no-install-recommends strace >/dev/null 2>&1; command -v strace', text=True)

        def host():
            return next(h for h in lab.api('/api/v1/hosts') if h['id'] == host_id)
        eventually(lambda: host()['connection'] == 'fresh' and host().get('inventory', {}).get('cpu_count', 0) > 0, 'native inventory', timeout=60)
        assert 'usage-history-v1' in host()['capabilities'], host()['capabilities']
        lab.passed('Enrolled agent built from the working tree reports usage-history-v1')

        # 1. Real virsh output formats the sampler parses.
        listing = ssh(hostlab, f'{VIRSH} list --uuid --name', text=True).stdout
        (evidence / 'virsh-list-uuid-name.txt').write_text(listing)

        def operation(op_id, status='succeeded'):
            def result():
                op = next(o for o in lab.api('/api/v1/operations') if o['id'] == op_id)
                assert op['status'] not in ('failed', 'needs_attention') or op['status'] == status, op
                return op if op['status'] == status else None
            return eventually(result, f'operation {op_id} {status}', timeout=300)
        common = {'pool_id': pool['id'], 'host_id': host_id}
        storage = lab.api('/api/v1/storage', {**common, 'request_id': 'usage-storage', 'name': 'Usage QA storage', 'path': storage_path}, expected=201)
        operation(storage['operation_id'])
        network = lab.api('/api/v1/networks', {**common, 'request_id': 'usage-network', 'name': 'Usage QA NAT', 'kind': 'network', 'source': 'default'}, expected=201)
        operation(network['operation_id'])
        query = urllib.parse.urlencode({'storage_id': storage['id'], 'name': 'Debian usage QA.iso', 'request_id': 'usage-media'})
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
        vm_name = 'usage-qa-' + suffix.lower().replace('_', '')
        # Maximum memory mode keeps the virtio balloon, so the stats period is exercised.
        created = lab.api('/api/v1/vms', {**common, 'request_id': 'usage-create', 'name': vm_name, 'media_id': media['id'],
            'cpu': {'count': 2, 'model': 'host-passthrough'}, 'memory': {'mode': 'maximum', 'size_mib': 1024},
            'disks': [{'storage_id': storage['id'], 'name': 'system', 'size_gib': 2, 'bus': 'virtio', 'thin': True, 'boot_order': 2}],
            'nics': [{'network_id': network['id'], 'model': 'virtio'}],
            'boot': {'firmware': 'bios', 'media_boot_order': 1}}, expected=202)
        operation(created['operation']['id'])
        vm_id = created['vm_id']
        xml = ssh(hostlab, f'{VIRSH} dumpxml {vm_id}', text=True).stdout
        assert "<stats period='5'/>" in xml, 'virtio balloon has no stats period'
        lab.passed("New VM definition carries <stats period='5'/> on its virtio balloon")

        def observed():
            return next(v for v in lab.api('/api/v1/vms') if v['id'] == vm_id)

        def power(action, request_id, state):
            lab.api(f'/api/v1/vms/{vm_id}/power', {'request_id': request_id, 'action': action}, expected=202)
            op = max((o for o in lab.api('/api/v1/operations') if o['kind'] == 'vm-power'), key=lambda o: o['created_at'])
            operation(op['id'])
            eventually(lambda: observed()['freshness'] == 'fresh' and observed()['state'] == state, f'{state} observation', timeout=90)
        power('start', 'usage-start', 'running')
        started = time.time()
        domstats = ssh(hostlab, f'{VIRSH} domstats --raw --nowait --list-running --cpu-total --vcpu --balloon --block --interface', text=True).stdout
        (evidence / 'virsh-domstats-list-running.txt').write_text(domstats)
        listing = ssh(hostlab, f'{VIRSH} list --uuid --name', text=True).stdout
        (evidence / 'virsh-list-uuid-name.txt').write_text(listing)
        line = next(l for l in listing.splitlines() if vm_id in l)
        assert line.split() == [vm_id, vm_name], line
        assert f"Domain: '{vm_name}'" in domstats and 'cpu.time=' in domstats and 'vcpu.current=' in domstats and 'block.count=' in domstats and 'net.count=' in domstats, domstats
        lab.passed('virsh list --uuid --name prints "uuid name"; domstats --list-running --nowait prints Domain blocks with cpu, vcpu, balloon, block and net counters')


        def stats(path, expected=200):
            began = time.monotonic()
            request = urllib.request.Request(lab.url + path, headers={'X-CSRF-Token': lab.csrf})
            try:
                response = lab.http.open(request, timeout=40)
            except urllib.error.HTTPError as exc:
                response = exc
            with response:
                body = json.loads(response.read() or b'null')
                assert response.status == expected, (path, response.status, body)
            return body, round(time.monotonic() - began, 3)

        # 2. Let the sampler record over two full minutes, then query.
        time.sleep(max(0, 150 - (time.time() - started)))
        vm10, m['vm_10m_first_query_seconds'] = stats(f'/api/v1/vms/{vm_id}/stats?range=10m')
        _, m['vm_10m_second_query_seconds'] = stats(f'/api/v1/vms/{vm_id}/stats?range=10m')
        vm24, m['vm_24h_query_seconds'] = stats(f'/api/v1/vms/{vm_id}/stats?range=24h')
        host10, m['host_10m_query_seconds'] = stats(f'/api/v1/hosts/{host_id}/stats?range=10m')

        def known(history, prefix):
            return {s['key']: sum(v is not None for v in s['values']) for s in history['series'] if s['key'].startswith(prefix)}
        m['vm_10m_known_points'] = known(vm10, '')
        m['vm_24h_known_points'] = known(vm24, '')
        m['host_10m_known_points'] = known(host10, '')
        assert vm10['step_seconds'] == 5 and vm24['step_seconds'] == 60 and host10['host_id'] == host_id
        for prefix in ('cpu.total', 'memory.', 'disk.', 'net.'):
            assert any(n > 0 for n in known(vm10, prefix).values()), (prefix, m['vm_10m_known_points'])
        assert any(n > 0 for n in known(vm24, 'cpu.total').values()), m['vm_24h_known_points']
        for prefix in ('cpu.total', 'cpu.core.', 'memory.used', 'net.', 'disk.', 'load.'):
            assert any(n > 0 for n in known(host10, prefix).values()), (prefix, m['host_10m_known_points'])
        lab.passed('VM 10m/24h and host 10m history contain known CPU, memory, disk and network points after two minutes')
        before_stop = points(vm24, finished=True)

        def live_domstats_pids(vm, seconds):
            # execve calls by the agent for `virsh domstats ... <this VM's UUID>` (the 5 s sampler passes
            # --list-running and no UUID). strace is test tooling installed on the disposable copy.
            script = (f'pid=$(systemctl show -p MainPID --value {unit_name}); timeout {seconds} strace -f -qq -s 128 -e trace=execve -p $pid 2>&1'
                      f' | grep "execve(" | grep "domstats" | grep -c "{vm}"; true')
            return int(ssh(hostlab, script, text=True).stdout.split()[0])

        def poll_live(path, seconds=20):
            polls = []
            for i in range(seconds):
                began = time.monotonic()
                body, latency = stats(path)
                assert body['step_seconds'] == 1 and all(len(x['values']) == 120 for x in body['series']), body['step_seconds']
                cpu = next((x['values'] for x in body['series'] if x['key'] == 'cpu.total'), [None])
                polls.append({'latency': latency, 'newest_cpu': cpu[-1], 'newest_any': any(x['values'][-1] is not None for x in body['series']),
                              'known_cpu': sum(v is not None for v in cpu)})
                time.sleep(max(0, 1 - (time.monotonic() - began)))
            return body, polls

        # L1. VM realtime at 1 s for about 20 s.
        live_vm, polls = poll_live(f'/api/v1/vms/{vm_id}/stats?range=live')
        later = polls[3:]
        m['vm_live_polls'] = len(polls)
        m['vm_live_newest_cpu_known_after_2s'] = f"{sum(p['newest_cpu'] is not None for p in later)}/{len(later)}"
        m['vm_live_latency_seconds'] = {'first': polls[0]['latency'], 'max_after_first': max(p['latency'] for p in polls[1:]), 'median': sorted(p['latency'] for p in polls)[len(polls) // 2]}
        m['vm_live_cpu_range'] = [min(p['newest_cpu'] for p in later if p['newest_cpu'] is not None), max(p['newest_cpu'] for p in later if p['newest_cpu'] is not None)]
        m['vm_live_known_cpu_points_last_poll'] = polls[-1]['known_cpu']
        assert sum(p['newest_cpu'] is not None for p in later) >= len(later) - 2, polls
        assert 0 <= m['vm_live_cpu_range'][0] and m['vm_live_cpu_range'][1] <= 400, m['vm_live_cpu_range']
        assert {x['group'] for x in live_vm['series']} >= {'cpu', 'memory', 'disk', 'network'}, [x['key'] for x in live_vm['series']]
        m['vm_live_domstats_execs_while_leased_10s'] = live_domstats_pids(vm_id, 10)
        stats(f'/api/v1/vms/{vm_id}/stats?range=live')  # renew: the lease starts from this query
        last_vm_live = time.time()
        assert m['vm_live_domstats_execs_while_leased_10s'] >= 7, m['vm_live_domstats_execs_while_leased_10s']
        lab.passed('Realtime VM query: 1 s steps, 120 values, newest CPU point known on nearly every poll after 2 s, plausible rates')

        # L2. Host realtime.
        live_host, polls = poll_live(f'/api/v1/hosts/{host_id}/stats?range=live')
        later = polls[3:]
        m['host_live_newest_cpu_known_after_2s'] = f"{sum(p['newest_cpu'] is not None for p in later)}/{len(later)}"
        m['host_live_latency_seconds'] = {'first': polls[0]['latency'], 'max_after_first': max(p['latency'] for p in polls[1:]), 'median': sorted(p['latency'] for p in polls)[len(polls) // 2]}
        m['host_live_cpu_range'] = [min(p['newest_cpu'] for p in later if p['newest_cpu'] is not None), max(p['newest_cpu'] for p in later if p['newest_cpu'] is not None)]
        assert sum(p['newest_cpu'] is not None for p in later) >= len(later) - 2, polls
        assert 0 <= m['host_live_cpu_range'][0] and m['host_live_cpu_range'][1] <= 100, m['host_live_cpu_range']
        lab.passed('Realtime host query: 1 s steps, 120 values, newest CPU point known on nearly every poll after 2 s, plausible rates')

        # L3. Lease expiry: after the lease, no per-second domstats for the VM and no files written by realtime.
        usage_listing = lambda: sorted(ssh(hostlab, f'stat -c "%n %s" {state_dir}/usage/*', text=True).stdout.splitlines())
        files_during_live = usage_listing()
        time.sleep(max(0, last_vm_live + 120 + 5 - time.time()))
        m['vm_live_domstats_execs_10s_after_lease'] = live_domstats_pids(vm_id, 10)
        files_after_lease = usage_listing()
        m['usage_files_unchanged_by_realtime'] = files_during_live == files_after_lease
        m['usage_dir_entries'] = [line.split('/')[-1].replace(vm_id, '<vm-uuid>') for line in files_after_lease]
        assert m['vm_live_domstats_execs_10s_after_lease'] == 0, m['vm_live_domstats_execs_10s_after_lease']
        assert files_during_live == files_after_lease and not any('live' in line for line in files_after_lease), (files_during_live, files_after_lease)
        lab.passed('After the 2-minute lease plus 5 s, per-VM domstats stopped and realtime added or resized no history files')

        # 3. Stop the VM: new 5 s points are unknown (a gap), not zero.
        power('force-stop', 'usage-stop', 'shut off')
        time.sleep(30)
        stopped, _ = stats(f'/api/v1/vms/{vm_id}/stats?range=10m')
        cpu = next(s for s in stopped['series'] if s['key'] == 'cpu.total')['values']
        m['vm_10m_cpu_tail_after_stop'] = cpu[-5:]
        assert all(v is None for v in cpu[-4:]) and any(v is not None for v in cpu), cpu[-12:]
        lab.passed('After a force stop the VM history keeps earlier points and records a gap, not zeros')

        # L5. Realtime for a stopped VM answers 200 with nulls.
        for _ in range(4):
            stopped_live, _ = stats(f'/api/v1/vms/{vm_id}/stats?range=live')
            time.sleep(1)
        stopped_live, _ = stats(f'/api/v1/vms/{vm_id}/stats?range=live')
        m['stopped_vm_live_known_points'] = sum(v is not None for x in stopped_live['series'] for v in x['values'])
        m['stopped_vm_live_known_series'] = {x['key']: [v for v in x['values'] if v is not None][-3:] for x in stopped_live['series'] if any(v is not None for v in x['values'])}
        m['stopped_vm_domstats'] = ssh(hostlab, f'{VIRSH} domstate {vm_id}; {VIRSH} domstats --raw --nowait --cpu-total --balloon {vm_id}; ', text=True).stdout.replace(vm_id, '<vm-uuid>')
        if m['stopped_vm_live_known_points'] == 0:
            lab.passed('Realtime query for a stopped VM returns 200 with only null values')
        else:
            failures.append(f"L5: realtime for a stopped VM returned {m['stopped_vm_live_known_points']} known points: {m['stopped_vm_live_known_series']}")

        # L4 (cap of 8 realtime views -> 429) is not exercised here: reaching it needs 9 objects the
        # Controller knows, and the lab workstation's memory budget allows no extra guests. Unit tests
        # cover the host cap and the Controller's 429 relay.

        # The minute the VM stopped in: is it kept once later minutes have been flushed?
        before_restart = points(stats(f'/api/v1/vms/{vm_id}/stats?range=24h')[0])
        last_running = max(t for k, t in before_restart if k == 'cpu.total')
        while time.time() < datetime.fromisoformat(last_running).timestamp() + 60 + 75:
            time.sleep(5)  # past the end of that minute plus one flush
        m['vm_last_running_minute'] = last_running
        # Restart the agent: the file persists and the channel reopens.
        files_before = ssh(hostlab, f'ls -l --time-style=+ {state_dir}/usage', text=True).stdout
        ssh(hostlab, f'systemctl restart {unit_name}', text=True)
        eventually(lambda: host()['connection'] == 'fresh', 'agent restart', timeout=30)
        after_restart, m['vm_24h_query_after_agent_restart_seconds'] = stats(f'/api/v1/vms/{vm_id}/stats?range=24h')
        kept = points(after_restart)
        missing = [k for k in before_stop if k not in kept]
        m['vm_24h_points_before_stop'], m['vm_24h_points_missing_after_agent_restart'] = len(before_stop), len(missing)
        m['vm_last_running_minute_kept_after_restart'] = ('cpu.total', last_running) in kept
        assert before_stop and not missing, missing[:10]
        lab.passed('After an agent restart the stats channel reopens and every earlier 24h point is still served from the host file')

        # 5. Errors.
        stats(f'/api/v1/vms/{vm_id}/stats?range=5m', expected=400)
        stats('/api/v1/vms/00000000-0000-4000-a000-000000000000/stats?range=10m', expected=404)
        anonymous = urllib.request.build_opener(urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(cert))))
        try:
            anonymous.open(lab.url + f'/api/v1/vms/{vm_id}/stats?range=10m', timeout=10)
            raise AssertionError('unauthenticated stats query succeeded')
        except urllib.error.HTTPError as exc:
            assert exc.code == 401, exc.code
        lab.passed('Stats queries reject an invalid range (400), an unknown VM (404) and a missing session (401)')

        # 4. Controller replacement: new port, certificate and database; ordinary enrollment.
        host_before = points(stats(f'/api/v1/hosts/{host_id}/stats?range=24h')[0], finished=True)
        ssh(hostlab, f'systemctl stop {unit_name}', text=True)
        lab.stop(a_process)
        import socket
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            lab.port = sock.getsockname()[1]
        lab.url = f'https://localhost:{lab.port}'
        lab.cookies.clear()
        _, cert, token = controller('controller-b')
        assert lab.api('/api/v1/hosts') == [], 'Controller B must start empty'
        enroll(cert, token)
        ssh(hostlab, f'systemctl start {unit_name}', text=True)
        eventually(lambda: any(h['id'] == host_id and h['connection'] == 'fresh' for h in lab.api('/api/v1/hosts')), 'host fresh on Controller B', timeout=60)
        eventually(lambda: any(v['id'] == vm_id for v in lab.api('/api/v1/vms')), 'VM adopted by Controller B', timeout=60)
        replaced, m['vm_24h_first_query_on_controller_b_seconds'] = stats(f'/api/v1/vms/{vm_id}/stats?range=24h')
        host_replaced = points(stats(f'/api/v1/hosts/{host_id}/stats?range=24h')[0])
        vm_missing = [k for k in before_restart if k not in points(replaced) and k[1] != last_running]
        host_missing = [k for k in host_before if k not in host_replaced]
        m['vm_24h_points_missing_on_controller_b'], m['host_24h_points_before_replacement'], m['host_24h_points_missing_on_controller_b'] = len(vm_missing), len(host_before), len(host_missing)
        assert not vm_missing and host_before and not host_missing, (vm_missing[:5], host_missing[:5])
        lab.passed('A replacement Controller with an empty database serves the same VM and host history from the host')

        # 6. Browser: Stats tabs with real data.
        power('start', 'usage-start-2', 'running')
        time.sleep(40)
        captures = report['captures']

        def capture(page, name):
            page.screenshot(path=str(evidence / f'{name}.png'), full_page=True)
            captures.append(name)
        with sync_playwright() as playwright:
            browser = playwright.chromium.launch(headless=True)
            context = browser.new_context(ignore_https_errors=True, viewport={'width': 1440, 'height': 1100})
            page = context.new_page()
            errors = []
            page.on('pageerror', lambda error: errors.append(str(error)))
            page.goto(lab.url)
            page.get_by_label('Username').fill('root')
            page.get_by_label('Password', exact=True).fill(password.read_text())
            page.get_by_role('button', name='Sign in').click()
            page.wait_for_load_state('networkidle')
            page.goto(lab.url + '/#/vms/' + vm_id)
            page.get_by_role('button', name='Stats', exact=True).click()
            expect(page.locator('.stats canvas').first).to_be_visible(timeout=40000)
            time.sleep(2)
            capture(page, 'vm-stats-10m')
            page.get_by_label('Stacked').check()
            time.sleep(1)
            capture(page, 'vm-stats-10m-stacked')
            page.get_by_label('Range').select_option('24h')
            expect(page.locator('.stats canvas').first).to_be_visible(timeout=40000)
            time.sleep(2)
            capture(page, 'vm-stats-24h')

            # L6. Realtime drawer, tooltip, legend chip, and polling stopping when the drawer closes.
            live_requests = []
            page.on('request', lambda r: live_requests.append(time.monotonic()) if 'range=live' in r.url else None)
            page.get_by_label('Stacked').uncheck()
            expect(page.locator('.stats-drawer')).to_have_count(0)
            page.get_by_role('button', name='Console', exact=True).click()
            page.locator('.stats-drawer').click()
            assert live_requests == [], 'realtime polled before it was turned on'
            page.get_by_label('Show realtime stats (1-second samples)').check()
            expect(page.locator('.stats-live')).to_be_visible(timeout=10000)
            expect(page.locator('.stats-grid-live canvas').first).to_be_visible(timeout=20000)
            time.sleep(8)
            capture(page, 'vm-stats-realtime')
            first_frame = page.locator('.stats-grid-live canvas').first.screenshot()
            time.sleep(3)
            m['browser_realtime_canvas_changed_over_3s'] = first_frame != page.locator('.stats-grid-live canvas').first.screenshot()
            m['browser_realtime_requests_in_first_11s'] = len(live_requests)
            assert m['browser_realtime_canvas_changed_over_3s'] and len(live_requests) >= 8, (m['browser_realtime_canvas_changed_over_3s'], len(live_requests))
            history_plot = page.locator('.stats-grid-live .u-over').first
            history_plot.scroll_into_view_if_needed()
            box = history_plot.bounding_box()
            page.mouse.move(box['x'] + box['width'] * 0.97, box['y'] + box['height'] / 2)
            expect(history_plot.locator('.stats-tooltip')).to_be_visible(timeout=5000)
            capture(page, 'vm-stats-tooltip')
            m['browser_tooltip_text'] = history_plot.locator('.stats-tooltip').inner_text()[:200]
            page.mouse.move(5, 5)
            chip = page.locator('.stats-grid-live .stats-chip').first
            chip.click()
            expect(chip).to_have_attribute('aria-pressed', 'false')
            capture(page, 'vm-stats-chip-hidden')
            chip.click()
            expect(chip).to_have_attribute('aria-pressed', 'true')
            page.locator('.stats-drawer').click()
            closed = time.monotonic()
            expect(page.locator('.stats-live')).to_have_count(0)
            time.sleep(6)
            m['browser_realtime_requests_after_close'] = sum(t > closed + 1.5 for t in live_requests)
            assert m['browser_realtime_requests_after_close'] == 0, m['browser_realtime_requests_after_close']
            capture(page, 'vm-stats-realtime-closed')
            page.goto(lab.url + '/#/hosts/' + host_id)
            page.get_by_role('button', name='Stats', exact=True).click()
            expect(page.locator('.stats canvas').first).to_be_visible(timeout=40000)
            time.sleep(2)
            capture(page, 'host-stats-10m')
            m['host_stats_chart_count'] = page.locator('.stats canvas').count()
            assert not errors, errors
            report['versions'] = {'chromium': browser.version, 'uplot': json.loads((ROOT / 'web/node_modules/uplot/package.json').read_text())['version']}
            browser.close()
        lab.passed('VM and host Stats tabs render real host history (Stacked, 24h), the Console realtime drawer shows moving live charts with a Live badge, the tooltip and legend chip work, closing the drawer stops polling, no page errors')

        # 8. File sizes and the usage check.
        sizes = ssh(hostlab, f'stat -c "%n %s" {state_dir}/usage/*', text=True).stdout
        m['history_files'] = [f'{Path(line.split()[0]).name.replace(vm_id, "<vm-uuid>")} {line.split()[1]}' for line in sizes.splitlines()]
        usage = next(c for c in host()['inventory']['checks'] if c['name'] == 'usage')
        m['usage_check_status'] = usage['status']
        assert usage['status'] == 'healthy', usage
        lab.passed('History files have fixed sizes on the host and the usage check stays healthy')

        # 7. Delete the stopped VM: its history file goes with it.
        power('force-stop', 'usage-stop-2', 'shut off')
        deleted = lab.api(f'/api/v1/vms/{vm_id}', {'request_id': 'usage-delete', 'delete_disks': True}, method='DELETE', expected=202)
        op = deleted.get('operation_id') or deleted.get('operation', {}).get('id') or max((o for o in lab.api('/api/v1/operations') if o['kind'] == 'vm-delete'), key=lambda o: o['created_at'])['id']
        operation(op)
        eventually(lambda: ssh(hostlab, f'test -e {state_dir}/usage/vm-{vm_id}.rrd && echo present || echo gone', text=True).stdout.strip() == 'gone', 'VM history file removed', timeout=30)
        stats(f'/api/v1/vms/{vm_id}/stats?range=10m', expected=404)
        lab.passed('Deleting the VM removes its history file on the host; its stats are no longer served')

        report['versions'] = {**report.get('versions', {}), 'host': ssh(hostlab, f'head -2 /etc/os-release; {VIRSH} version | head -3', text=True).stdout}
        report['artifacts'] = {p.name: hashlib.file_digest(p.open('rb'), 'sha256').hexdigest() for p in (work / 'controller', work / 'agent', ROOT / 'web/dist/app.js')}
        report['completed'] = True
        assert not failures, failures
        report['limits'] = ['Nested KVM lab, not physical hardware', 'Guest idle at the installer boot menu: no guest-generated disk or network load',
            'Heartbeat shortened to 1 s, so first-query latency does not show the 10 s default', 'Runs of a few minutes: 30d and 1y tiers are not filled', 'Hosted CI not exercised']
    finally:
        (evidence / 'results.json').write_text(json.dumps(report, indent=2) + '\n')
        if report['completed'] or not args.keep_on_failure:
            lab.close()
            subprocess.run(['kill', (hostlab / 'qemu.pid').read_text().strip()], check=False)
        print(json.dumps(report, indent=2))
        print('lab copy:', hostlab, 'work:', work)


if __name__ == '__main__':
    main()
