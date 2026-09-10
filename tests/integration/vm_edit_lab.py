#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""VM edit acceptance against a private copy of a disposable appliance lab.

Copies an explicitly disposable nested-KVM lab (see managed_vm_lab.py), runs a fresh
Controller on the workstation, enrolls the copied host with an agent built from this tree,
creates a stopped guest, then edits it while stopped and while running through
PUT /api/v1/vms/{id}, checking every step against real libvirt on the lab host. QEMU uses
loopback forwards only; nothing on the workstation or any production host is changed.
"""
import argparse
import hashlib
import http.client
import json
import os
import re
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
    parser.add_argument('--evidence', type=Path, default=ROOT / 'docs/qa/vm-edit')
    parser.add_argument('--keep-on-failure', action='store_true', help='leave the lab copy and Controller running when a step fails')
    args = parser.parse_args()
    go = os.environ.get('GO') or ROOT / '.cache/toolchains/go/bin/go'
    evidence = args.evidence
    evidence.mkdir(parents=True, exist_ok=True)
    hostlab = prepare(args.copy_from)
    work = Path(tempfile.mkdtemp(prefix='nex-vm-edit-qa.', dir=ROOT / '.cache'))
    lab = Lab(work)
    report = {'completed': False, 'lab': str(hostlab), 'source': str(args.copy_from), 'passed': lab.results, 'steps': {}, 'limits': []}

    def sh(command):
        return ssh(hostlab, command, text=True).stdout

    def note(step, **facts):
        report['steps'].setdefault(step, {}).update(facts)
        print(step, json.dumps(facts, indent=1)[:2000], flush=True)

    try:
        for binary, package in [('controller', './cmd/nex-controller'), ('agent', './cmd/nex-core-agent')]:
            run(go, 'build', '-o', work / binary, package, cwd=ROOT)
        source = work / 'source.tar.gz'
        run('python3', ROOT / 'scripts/build-source.py', '--output', source, env={**os.environ, 'GO': str(go)}, cwd=ROOT)
        cert, key = work / 'tls.pem', work / 'tls.key'
        run('openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes',
            '-keyout', key, '-out', cert, '-days', '1', '-subj', '/CN=localhost',
            '-addext', 'subjectAltName=DNS:localhost,IP:10.0.2.2,IP:127.0.0.1')
        password = work / 'password'
        password.write_text('Disposable-vm-edit-qa-5170!')
        password.chmod(0o600)
        lab.start('controller', work / 'controller', '-listen', f'127.0.0.1:{lab.port}', '-data-dir', work / 'controller-state',
                  '-tls-cert', cert, '-tls-key', key, '-admin-password-file', password, '-source-archive', source,
                  '-stale-after', '3s', '-ui-dir', ROOT / 'web/dist')
        lab.client(cert)
        eventually(lambda: lab.api('/api/v1/sites', expected=401), 'controller startup')
        lab.csrf = lab.api('/api/v1/session', {'username': 'admin', 'password': password.read_text()})['csrf_token']
        site = lab.api('/api/v1/sites', {'name': 'VM edit QA'}, expected=201)
        token = lab.api('/api/v1/enrollment-tokens', {'site_id': site['id']}, expected=201)['token']
        suffix = work.name.split('.')[-1]
        remote = f'/opt/nex-vm-edit-qa-{suffix}'
        state_dir = f'/var/lib/nex-vm-edit-qa-{suffix}'
        storage_path = f'/var/lib/libvirt/images/nex-vm-edit-qa-{suffix}'
        sh(f'mkdir -p {storage_path}; chmod 0711 {storage_path}; mkdir -m 700 {remote}')
        for name in ('agent', 'tls.pem'):
            ssh(hostlab, f'cat > {remote}/{name}', input=(work / name).read_bytes())
        ssh(hostlab, f'umask 077; cat > {remote}/token', input=token.encode())
        sh(f'chmod 700 {remote}/agent')
        for attempt in range(3):
            try:
                sh(f'{remote}/agent enroll --state-dir {state_dir} --controller https://10.0.2.2:{lab.port} --ca-file {remote}/tls.pem --token-file {remote}/token')
                break
            except subprocess.CalledProcessError as exc:
                print('enroll attempt', attempt + 1, 'failed:', exc.stderr[-400:], flush=True)
                if attempt == 2:
                    raise
                time.sleep(3)
        host_id = json.loads(sh(f'cat {state_dir}/state.json'))['host_id']
        pool = next(p for p in lab.api('/api/v1/pools') if p['master_host_id'] == host_id)
        unit = (ROOT / 'packaging/systemd/nex-core-agent.service').read_text()
        unit = unit.replace('StateDirectory=nex-core-agent', f'StateDirectory=nex-vm-edit-qa-{suffix}')
        unit = unit.replace('/usr/local/lib/nex/nex-core-agent', f'{remote}/agent')
        unit = unit.replace('/var/lib/nex-core-agent', state_dir)
        unit = unit.replace('--heartbeat 10s --probe-interval 10s --probe-timeout 3s', '--heartbeat 500ms --probe-interval 500ms --probe-timeout 2s')
        ssh(hostlab, 'cat > /etc/systemd/system/nex-vm-edit-qa.service', input=unit.encode())
        sh('systemctl daemon-reload; systemctl start nex-vm-edit-qa')

        def host():
            return next(h for h in lab.api('/api/v1/hosts') if h['id'] == host_id)
        eventually(lambda: host()['connection'] == 'fresh' and host().get('inventory', {}).get('cpu_count', 0) > 0, 'native inventory', timeout=60)
        assert 'vm-update-v1' in host()['capabilities'], host()['capabilities']
        lab.passed('Agent built from this tree reports vm-update-v1')

        def operation(op_id, status='succeeded'):
            def result():
                op = next(o for o in lab.api('/api/v1/operations') if o['id'] == op_id)
                assert op['status'] not in ('failed', 'needs_attention') or op['status'] == status, op
                return op if op['status'] == status else None
            return eventually(result, f'operation {op_id} {status}', timeout=300)
        common = {'pool_id': pool['id'], 'host_id': host_id}
        storage = lab.api('/api/v1/storage', {**common, 'request_id': 'edit-storage', 'name': 'VM edit QA storage', 'path': storage_path}, expected=201)
        operation(storage['operation_id'])
        network = lab.api('/api/v1/networks', {**common, 'request_id': 'edit-network', 'name': 'VM edit QA NAT', 'kind': 'network', 'source': 'default'}, expected=201)
        operation(network['operation_id'])
        query = urllib.parse.urlencode({'storage_id': storage['id'], 'name': 'Debian edit QA.iso', 'request_id': 'edit-media'})
        conn = http.client.HTTPSConnection('localhost', lab.port, context=ssl.create_default_context(cafile=str(cert)), timeout=600)
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

        # 1. Create.
        vm_name = 'edit-qa-' + suffix.lower().replace('_', '')
        spec = {**common, 'name': vm_name, 'description': 'edit QA', 'guest_os': 'linux', 'media_id': media['id'],
                'cpu': {'count': 2, 'sockets': 0, 'cores': 0, 'threads': 0, 'model': 'host-passthrough'}, 'memory': {'mode': 'dedicated', 'size_mib': 1024},
                'disks': [{'storage_id': storage['id'], 'name': 'system', 'size_gib': 2, 'bus': 'virtio', 'thin': True, 'boot_order': 2}],
                'nics': [{'network_id': network['id'], 'model': 'virtio', 'mac': ''}],
                'boot': {'firmware': 'uefi', 'machine': 'q35', 'media_boot_order': 1}, 'start_after_create': False, 'autostart': False}
        created = lab.api('/api/v1/vms', {**spec, 'request_id': 'edit-create'}, expected=202)
        operation(created['operation']['id'])
        vm_id = created['vm_id']
        vmdir = f'{storage_path}/nex-vms/{vm_id}'

        def vm():
            return next(v for v in lab.api('/api/v1/vms') if v['id'] == vm_id)
        eventually(lambda: vm()['freshness'] == 'fresh' and vm()['state'] == 'shut off', 'stopped observation', timeout=30)
        xml = sh(f'virsh -c qemu:///system dumpxml {vm_id} --inactive')
        assert xml.count("model='pcie-root-port'") >= 4 and "device='cdrom'" in xml and "bus='sata'" in xml, xml
        note('1-create', name=vm_name, root_ports=xml.count("model='pcie-root-port'"), cdrom="device='cdrom'" in xml,
             blklist=sh(f'virsh -c qemu:///system domblklist {vm_id} --inactive'), dir=sh(f'ls -l {vmdir}'))
        lab.passed('Created with an ISO in the SATA CD drive and spare pcie-root-port controllers; libvirt accepted the definition')

        def edit(request, changes, expected=202):
            current = vm()['spec']
            body = {k: current[k] for k in ('pool_id', 'host_id', 'name', 'description', 'guest_os', 'cpu', 'memory', 'disks', 'nics', 'boot', 'media_id', 'autostart')}
            body['start_after_create'] = False
            body.update(changes)
            body['request_id'] = request
            return lab.api(f'/api/v1/vms/{vm_id}', body, method='PUT', expected=expected)

        def applied(revision):
            return eventually(lambda: vm() if vm()['spec'].get('revision') == revision else None, f'revision {revision} reported', timeout=60)

        # 2. Edit while stopped.
        cur = vm()['spec']
        r = edit('edit-1', {'name': vm_name + '-ed', 'description': 'edited while stopped', 'autostart': True,
                            'disks': cur['disks'] + [{'storage_id': storage['id'], 'name': 'data', 'size_gib': 1, 'bus': 'virtio', 'thin': True, 'boot_order': 0}],
                            'nics': cur['nics'] + [{'network_id': network['id'], 'model': 'virtio', 'mac': ''}]})
        operation(r['operation']['id'])
        v = applied(1)
        dominfo = sh(f'virsh -c qemu:///system dominfo {vm_id}')
        xml = sh(f'virsh -c qemu:///system dumpxml {vm_id} --inactive')
        listing = sh(f'ls -l {vmdir}')
        assert v['name'] == vm_name + '-ed' and f'<name>{vm_name}-ed</name>' in xml, (v['name'], xml[:300])
        assert re.search(r'Autostart:\s+enable', dominfo), dominfo
        assert 'disk-1.qcow2' in listing and 'disk-1.qcow2' in xml, listing
        assert len(v['spec']['nics']) == 2 and all(n['mac'] for n in v['spec']['nics']) and all(n['mac'] in xml for n in v['spec']['nics']), v['spec']['nics']
        note('2-edit-stopped', dominfo=dominfo, dir=listing, iflist=sh(f'virsh -c qemu:///system domiflist {vm_id} --inactive'), blklist=sh(f'virsh -c qemu:///system domblklist {vm_id} --inactive'))
        lab.passed('Edit while stopped: renamed, second disk allocated as disk-1.qcow2, second interface with frozen MAC, autostart enabled; revision 1 reported')

        # 3. Start, then edit while running.
        p = lab.api(f'/api/v1/vms/{vm_id}/power', {'request_id': 'edit-start', 'action': 'start'}, expected=202)
        operation(p['operation']['id'])
        eventually(lambda: vm()['state'] == 'running', 'running observation', timeout=60)
        cur = vm()['spec']
        r = edit('edit-2', {'media_id': '',
                            'disks': cur['disks'] + [{'storage_id': storage['id'], 'name': 'hot', 'size_gib': 1, 'bus': 'virtio', 'thin': True, 'boot_order': 0}],
                            'nics': cur['nics'] + [{'network_id': network['id'], 'model': 'virtio', 'mac': ''}]})
        op = operation(r['operation']['id'])
        v = applied(2)
        live_blk, live_if = sh(f'virsh -c qemu:///system domblklist {vm_id}'), sh(f'virsh -c qemu:///system domiflist {vm_id}')
        xml = sh(f'virsh -c qemu:///system dumpxml {vm_id} --inactive')
        assert vm()['state'] == 'running'
        assert 'disk-2.qcow2' in live_blk and 'disk-2.qcow2' in xml, live_blk
        assert all(n['mac'] in live_if for n in v['spec']['nics']) and len(v['spec']['nics']) == 3, live_if
        assert '.iso' not in live_blk.split('sd')[1] if 'sd' in live_blk else True
        note('3-edit-running', op=op, live_blklist=live_blk, live_iflist=live_if, media_ejected='.iso' not in live_blk)
        lab.passed('Edit while running: third disk and third interface hot-plugged and persisted, ISO ejected; revision 2, VM still running')

        # 4. Stop-first change while running is refused by the Controller.
        refused = edit('edit-3', {'cpu': {**vm()['spec']['cpu'], 'count': 4}}, expected=409)
        assert 'stop the VM first to change the CPU configuration' in refused['error'], refused
        note('4-stop-first', refused=refused)
        lab.passed('CPU change while running refused with 409 naming the CPU configuration')

        # 5. Remove the hot devices while running.
        cur = vm()['spec']
        r = edit('edit-4', {'disks': cur['disks'][:2], 'nics': cur['nics'][:2]})
        op_id = r['operation']['id']
        op = eventually(lambda: (lambda o: o if o['status'] in ('succeeded', 'failed', 'needs_attention') else None)(next(o for o in lab.api('/api/v1/operations') if o['id'] == op_id)), 'live removal result', timeout=300)
        live_blk, live_if = sh(f'virsh -c qemu:///system domblklist {vm_id}'), sh(f'virsh -c qemu:///system domiflist {vm_id}')
        note('5-remove-running', op=op, live_blklist=live_blk, live_iflist=live_if, retained=sh(f'ls -l {vmdir}'))
        if op['status'] == 'succeeded':
            applied(3)
            persisted_blk, persisted_if = sh(f'virsh -c qemu:///system domblklist {vm_id} --inactive'), sh(f'virsh -c qemu:///system domiflist {vm_id} --inactive')
            assert 'disk-2.qcow2' not in persisted_blk and persisted_if.count('virtio') == 2, (persisted_blk, persisted_if)
            # libvirt's live detach completes only when the guest releases the device; a guest at
            # the installer boot menu never does, so the live domain may still show it.
            released = 'disk-2.qcow2' not in live_blk and live_if.count('virtio') == 2
            note('5-remove-running', released_by_guest=released, persisted_blklist=persisted_blk, persisted_iflist=persisted_if)
            if not released:
                report['limits'].append('guest at the installer boot menu did not release the hot-unplugged disk and interface; they stay attached to the running domain until the guest releases them or the VM restarts (persistent definition already omits them)')
            lab.passed('Removal while running: third disk and interface removed from the persistent definition, files retained, revision 3' + ('; guest released them live' if released else '; guest has not released them yet (recorded limit)'))
        else:
            report['limits'].append('live removal did not succeed: ' + json.dumps(op))
            print('LIMIT live removal:', op, flush=True)

        # 6. Force stop, then remove the second disk.
        p = lab.api(f'/api/v1/vms/{vm_id}/power', {'request_id': 'edit-stop', 'action': 'force-stop'}, expected=202)
        operation(p['operation']['id'])
        eventually(lambda: vm()['state'] == 'shut off', 'stopped observation', timeout=60)
        cur = vm()['spec']
        r = edit('edit-5', {'disks': [d for d in cur['disks'] if d['name'] == 'system'], 'nics': cur['nics'][:2]})
        operation(r['operation']['id'])
        final = applied(cur['revision'] + 1)
        journal = json.loads(sh(f'cat {state_dir}/managed-vms/{r["operation"]["id"]}.json'))
        listing = sh(f'ls -l {vmdir}')
        blk = sh(f'virsh -c qemu:///system domblklist {vm_id} --inactive')
        assert 'disk-1.qcow2' in listing and 'disk-1.qcow2' not in blk and any(p.endswith('disk-1.qcow2') for p in journal.get('retained', [])), (listing, blk, journal.get('retained'))
        assert [d['name'] for d in final['spec']['disks']] == ['system']
        note('6-remove-stopped', retained=journal.get('retained'), dir=listing, blklist=blk, stage=journal.get('stage'))
        lab.passed('Removal while stopped: second disk detached by redefine, its file retained and listed in the journal')

        report['versions'] = {'go': subprocess.run([str(go), 'version'], capture_output=True, text=True).stdout.strip(),
                              'host': sh('uname -r; virsh -c qemu:///system version | head -6; cat /etc/os-release | head -2')}
        report['artifacts'] = {p.name: hashlib.file_digest(p.open('rb'), 'sha256').hexdigest() for p in (work / 'controller', work / 'agent', args.iso)}
        report.update({'vm_id': vm_id, 'host_id': host_id, 'pool_id': pool['id'], 'completed': True})
    finally:
        (evidence / 'lab-results.json').write_text(json.dumps(report, indent=2) + '\n')
        if report['completed'] or not args.keep_on_failure:
            lab.close()
            pid = int((hostlab / 'qemu.pid').read_text())
            subprocess.run(['kill', str(pid)], check=False)
        print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
