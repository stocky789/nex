#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Managed VM API acceptance against a separately prepared disposable nested-KVM host.

Prepare with managed_vm_lab.py; this test writes only its new private Controller state,
/opt/nex-managed-qa and /var/lib/libvirt/images/nex-managed-qa within the selected copied host. It
locally starts only the VM UUID it creates, to observe boot; browser power control is
outside this slice. No real guest installation or physical-hardware claim is made.
"""
import argparse
import hashlib
import http.client
import importlib.util
import json
import os
from pathlib import Path
import shlex
import shutil
import ssl
import subprocess
import time
import tempfile
import urllib.parse
import xml.etree.ElementTree as ET

from managed_vm_lab import ROOT, ssh

spec = importlib.util.spec_from_file_location('phase1', ROOT / 'tests/integration/phase1.py')
phase1 = importlib.util.module_from_spec(spec)
spec.loader.exec_module(phase1)


class ManagedLab(phase1.Lab):
    def start(self, name, *args, env=None):
        log = (self.path / f'{name}.log').open('a')
        self.logs.append(log)
        process = subprocess.Popen([str(x) for x in args], stdout=log, stderr=log,
                                   cwd=ROOT, env=env, start_new_session=True)
        self.processes.append(process)
        return process


def derived_mac(vm_id, index):
    """protocol.DerivedMAC: the QEMU prefix, two octets of sha256(id) and the interface index."""
    digest = hashlib.sha256(vm_id.encode()).digest()
    return '52:54:00:%02x:%02x:%02x' % (digest[0], digest[1], index)


def verify_definition(hostlab, xml, body, storage_path, media):
    tree = ET.fromstring(xml)
    assert tree.findtext('uuid') == body['id'] and tree.findtext('name') == body['name']
    assert int(tree.findtext('vcpu')) == body['cpu']['count']
    assert tree.find('cpu').get('mode') == body['cpu']['model']
    topology = tree.find('cpu/topology')
    assert all(int(topology.get(k)) == body['cpu'][k] for k in ('sockets', 'cores', 'threads')), ET.tostring(topology)
    assert tree.find('memory').get('unit') == 'KiB'
    assert int(tree.findtext('memory')) == body['memory']['size_mib'] * 1024
    # The balloon device carries the memory mode. libvirt normalises currentMemory, so only the
    # dedicated allocation is asserted; the freePageReporting echo is recorded, not asserted.
    balloon = tree.find('devices/memballoon')
    if body['memory']['mode'] == 'dedicated':
        assert balloon.get('model') == 'none' and int(tree.findtext('currentMemory')) == body['memory']['size_mib'] * 1024
    else:
        assert balloon.get('model') == 'virtio'
    interfaces = tree.findall('devices/interface')
    assert len(interfaces) == len(body['nics'])
    for index, (node, nic) in enumerate(zip(interfaces, body['nics'])):
        assert node.get('type') == 'network' and node.find('source').get('network') == 'default'
        assert node.find('model').get('type') == nic['model']
        assert node.find('mac').get('address') == (nic.get('mac') or derived_mac(body['id'], index))
    # Per-device boot order replaces the <os><boot dev> list; libvirt forbids mixing the two.
    assert tree.findall('os/boot') == []
    assert (tree.find('os/loader') is not None) == (body['boot']['firmware'] == 'uefi')
    cd = tree.find("devices/disk[@device='cdrom']/source").get('file')
    assert cd == f"{storage_path}/nex-media/{media['id']}.iso"
    assert tree.find("devices/disk[@device='cdrom']/boot").get('order') == str(body['boot']['media_boot_order'])
    assert ssh(hostlab, shlex.join(['sha256sum', cd]), text=True).stdout.split()[0] == media['sha256']
    disks = tree.findall("devices/disk[@device='disk']")
    assert len(disks) == len(body['disks'])
    allocated = []
    for index, (node, wanted) in enumerate(zip(disks, body['disks'])):
        path = node.find('source').get('file')
        assert path == f"{storage_path}/nex-vms/{body['id']}/disk-{index}.qcow2"
        assert node.find('target').get('bus') == wanted['bus']
        boot = node.find('boot')
        assert (boot.get('order') if boot is not None else 0) == (str(wanted['boot_order']) if wanted['boot_order'] else 0)
        info = json.loads(ssh(hostlab, shlex.join(['qemu-img', 'info', '--force-share', '--output=json', path]), text=True).stdout)
        assert info['format'] == 'qcow2' and info['virtual-size'] == wanted['size_gib'] * 1024**3
        assert not info.get('backing-filename')
        # A thick disk is preallocated, so the host has already claimed the bytes.
        assert (info['actual-size'] < info['virtual-size'] / 2) == wanted['thin'], (wanted, info)
        allocated.append({'path': path, 'bus': wanted['bus'], 'size_gib': wanted['size_gib'], 'thin': wanted['thin'],
                          'actual_size': info['actual-size'], 'boot_order': wanted['boot_order'], 'backing_file': None})
    return {'uuid': body['id'], 'cpu': body['cpu'], 'memory': body['memory'], 'disks': allocated,
            'nics': [{'model': n['model'], 'mac': node.find('mac').get('address')} for node, n in zip(interfaces, body['nics'])],
            'firmware': body['boot']['firmware'], 'machine': tree.find('os/type').get('machine'), 'network': 'default',
            'balloon_model': balloon.get('model'), 'balloon_free_page_reporting': balloon.get('freePageReporting'),
            'iso_sha256': media['sha256'], 'media_boot_order': body['boot']['media_boot_order'], 'os_boot_dev_list': []}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--lab-dir', type=Path, required=True)
    parser.add_argument('--iso', type=Path, required=True)
    parser.add_argument('--evidence', type=Path, required=True)
    parser.add_argument('--keep-running', action='store_true', help='retain private controller/agent for separate browser QA')
    args = parser.parse_args()
    hostlab = args.lab_dir.resolve(strict=True)
    assert hostlab.name.startswith('nex-managed-vm-lab.')
    assert (hostlab / 'host.raw').is_file()
    assert ssh(hostlab, 'hostname', text=True).stdout.strip() == 'nex-lab'
    work = Path(tempfile.mkdtemp(prefix='managed-acceptance.', dir=hostlab))
    (hostlab / 'current-acceptance').write_text(str(work))
    evidence = args.evidence.resolve()
    evidence.mkdir(parents=True, exist_ok=True)
    lab = ManagedLab(work)
    go = os.environ.get('GO') or shutil.which('go') or ROOT / '.cache/toolchains/go/bin/go'
    run, eventually = phase1.run, phase1.eventually
    report = {'environment': 'Debian 13 Nex Core copied regular-file host, nested KVM, isolated QEMU user networking', 'checks': lab.results, 'completed': False}
    source_paths = sorted({p for directory in ('internal/controller', 'internal/agent', 'internal/protocol', 'internal/vmhost', 'cmd/nex-controller', 'cmd/nex-core-agent', 'web/dist') for p in (ROOT / directory).rglob('*') if p.is_file()} | {ROOT / 'packaging/systemd/nex-core-agent.service'})
    report['input_sha256'] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in source_paths}
    try:
        for binary, package in [('controller', './cmd/nex-controller'), ('agent', './cmd/nex-core-agent')]:
            run(go, 'build', '-o', work / binary, package, cwd=ROOT)
        source = work / 'source.tar.gz'
        run('python3', ROOT / 'scripts/build-source.py', '--output', source, env={**os.environ, 'GO': str(go)}, cwd=ROOT)
        cert, key = work / 'tls.pem', work / 'tls.key'
        run('openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes',
            '-keyout', key, '-out', cert, '-days', '1', '-subj', '/CN=localhost',
            '-addext', 'subjectAltName=DNS:localhost,IP:10.0.2.2')
        password = work / 'password'
        password.write_text('Disposable-managed-qa-9284!')
        password.chmod(0o600)
        controller_args = [work / 'controller', '-listen', f'127.0.0.1:{lab.port}', '-data-dir', work / 'controller-state',
            '-tls-cert', cert, '-tls-key', key, '-admin-password-file', password, '-source-archive', source,
            '-stale-after', '2s', '-ui-dir', ROOT / 'web/dist']
        cp = lab.start('controller', *controller_args)
        lab.client(cert)
        lab.started()
        for path in ('storage', 'networks', 'media', 'vms'):
            lab.api('/api/v1/' + path, expected=401)
        session = lab.api('/api/v1/session', {'username': 'root', 'password': password.read_text()})
        lab.csrf = session['csrf_token']
        site = lab.api('/api/v1/sites', {'name': 'Managed VM QA'}, expected=201)
        token = lab.api('/api/v1/enrollment-tokens', {'site_id': site['id']}, expected=201)['token']
        suffix = work.name.split('.')[-1]
        remote = f'/opt/nex-managed-qa-{suffix}'
        state_dir = f'/var/lib/nex-managed-qa-{suffix}'
        storage_path = f'/var/lib/libvirt/images/nex-managed-qa-{suffix}'
        ssh(hostlab, f'mkdir -p {storage_path}; chmod 0711 {storage_path}; mkdir -m 0700 {storage_path}-private', text=True)
        ssh(hostlab, f'mkdir -m 700 {remote}', text=True)
        for name in ('agent', 'tls.pem'):
            ssh(hostlab, f'cat > {remote}/{name}', input=(work / name).read_bytes())
        ssh(hostlab, f'umask 077; cat > {remote}/token', input=token.encode())
        ssh(hostlab, f'chmod 700 {remote}/agent; {remote}/agent enroll --state-dir {state_dir} --controller https://10.0.2.2:{lab.port} --ca-file {remote}/tls.pem --token-file {remote}/token', text=True)
        state = json.loads(ssh(hostlab, f'cat {state_dir}/state.json', text=True).stdout)
        host_id = state['host_id']
        # Enrollment created the pool automatically with the native host as its master.
        pool = next(p for p in lab.api('/api/v1/pools') if p['master_host_id'] == host_id)
        assert pool['site_id'] == site['id'] and pool['id'] == next(h for h in lab.api('/api/v1/hosts') if h['id'] == host_id)['pool_id']
        unit = (ROOT / 'packaging/systemd/nex-core-agent.service').read_text()
        unit = unit.replace('StateDirectory=nex-core-agent', f'StateDirectory=nex-managed-qa-{suffix}')
        unit = unit.replace('/usr/local/lib/nex/nex-core-agent', f'{remote}/agent')
        unit = unit.replace('/var/lib/nex-core-agent', state_dir)
        unit = unit.replace('--heartbeat 10s --probe-interval 10s --probe-timeout 3s', '--heartbeat 250ms --probe-interval 500ms --probe-timeout 2s')
        ssh(hostlab, 'cat > /etc/systemd/system/nex-managed-qa.service', input=unit.encode())
        ssh(hostlab, 'systemctl stop nex-managed-qa 2>/dev/null || true; systemctl daemon-reload', text=True)
        def agent_start():
            ssh(hostlab, 'systemctl start nex-managed-qa', text=True)
        def agent_stop():
            ssh(hostlab, 'systemctl stop nex-managed-qa', text=True)
        agent_start()
        def host():
            return next(h for h in lab.api('/api/v1/hosts') if h['id'] == host_id)
        eventually(lambda: host()['connection'] == 'fresh' and host().get('inventory', {}).get('cpu_count', 0) > 0, 'native inventory')
        common = {'pool_id': pool['id'], 'host_id': host_id}
        other_token = work / 'other-token'
        other_token.write_text(lab.api('/api/v1/enrollment-tokens', {'site_id': site['id']}, expected=201)['token'])
        other_token.chmod(0o600)
        run('openssl', 'req', '-new', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256',
            '-nodes', '-keyout', work / 'other-key.pem', '-out', work / 'other.csr', '-subj', '/CN=qa-secondary')
        (work / 'other-key.pem').chmod(0o600)
        other = lab.api('/api/v1/agent/enroll', {'version': 1, 'token': other_token.read_text(),
            'csr_pem': (work / 'other.csr').read_text(), 'hostname': 'qa-secondary'})
        (work / 'other-cert.pem').write_text(other['certificate_pem'])
        (work / 'other-cert.pem').chmod(0o600)
        other_tls = ssl.create_default_context(cafile=str(cert))
        other_tls.load_cert_chain(work / 'other-cert.pem', work / 'other-key.pem')
        def other_request(path, data=None, expected=200):
            conn = http.client.HTTPSConnection('localhost', lab.port, context=other_tls, timeout=10)
            conn.request('POST' if data is not None else 'GET', path,
                         body=json.dumps(data) if data is not None else None, headers={'Content-Type': 'application/json'})
            response = conn.getresponse()
            content = response.read()
            conn.close()
            assert response.status == expected, (response.status, content)
            return json.loads(content)
        # Explicit protocol simulation: a valid second enrolled identity reports the
        # native host snapshot without the new capability, as an agent lacking it would.
        other_request('/api/v1/agent/report', {'version': 1, 'sequence': 1, 'inventory': host()['inventory'], 'results': []})
        lab.api('/api/v1/storage', {**common, 'host_id': other['host_id'], 'request_id': 'qa-unsupported',
            'name': 'Unsupported agent', 'path': storage_path}, expected=409)
        report['simulations'] = ['Second enrolled mTLS identity reports copied native inventory with capability omitted, then advertised, for version/cross-host authorization checks; it executes no commands.']

        def operation(op_id, status='succeeded'):
            def result():
                op = next(o for o in lab.api('/api/v1/operations') if o['id'] == op_id)
                if op['status'] in ('failed', 'needs_attention') and op['status'] != status:
                    raise AssertionError(op)
                return op if op['status'] == status else None
            return eventually(result, f'operation {op_id} {status}', timeout=180)
        storage_body = {**common, 'request_id': 'qa-storage', 'name': 'QA local storage', 'path': storage_path}
        lab.api('/api/v1/storage', storage_body, expected=403, headers={'X-CSRF-Token': ''})
        storage = lab.api('/api/v1/storage', storage_body, expected=201)
        operation(storage['operation_id'])
        assert lab.api('/api/v1/storage', storage_body)['id'] == storage['id']
        lab.api('/api/v1/storage', {**storage_body, 'name': 'conflicting name'}, expected=409)
        network_body = {**common, 'request_id': 'qa-network', 'name': 'QA NAT network', 'kind': 'network', 'source': 'default'}
        network = lab.api('/api/v1/networks', network_body, expected=201)
        operation(network['operation_id'])
        assert lab.api('/api/v1/networks', network_body)['id'] == network['id']
        private_storage = lab.api('/api/v1/storage', {**common, 'request_id': 'qa-private-storage', 'name': 'Untraversable storage', 'path': storage_path + '-private'}, expected=201)
        assert operation(private_storage['operation_id'], 'failed')['error']
        lab.passed('Actual root-owned 0700 storage rejected before use; root-owned 0711 storage registration succeeded under packaged systemd restrictions')
        prefix = '/var/lib/libvirt/images/'
        oversized = lab.api('/api/v1/storage', {**common, 'request_id': 'qa-oversized-path-error',
            'name': 'Oversized native path', 'path': prefix + 'a' * (4096 - len(prefix))}, expected=201)
        failure = operation(oversized['operation_id'], 'failed')
        journal = json.loads(ssh(hostlab, f'cat {state_dir}/state.json', text=True).stdout)['journal']
        assert len(failure['error'].encode()) <= 4096
        assert len(journal[oversized['operation_id']]['error'].encode()) > 4096
        lab.passed('Native oversized path diagnostic is bounded on wire but retained fully in durable agent journal; failed operation report is accepted')

        missing = lab.api('/api/v1/storage', {**common, 'request_id': 'qa-missing-storage', 'name': 'Missing storage', 'path': '/var/lib/libvirt/images/nex-managed-qa-absent'}, expected=201)
        assert operation(missing['operation_id'], 'failed')['error']
        missing_net = lab.api('/api/v1/networks', {**network_body, 'request_id': 'qa-missing-network', 'name': 'Missing network', 'source': 'nex-managed-qa-absent'}, expected=201)
        assert operation(missing_net['operation_id'], 'failed')['error']
        # The second enrolled identity is the master of its own automatic pool.
        other_pool = next(p for p in lab.api('/api/v1/pools') if p['master_host_id'] == other['host_id'])
        assert other_pool['name'] == 'qa-secondary' and other_pool['id'] != pool['id'], other_pool
        lab.api('/api/v1/storage', {**storage_body, 'request_id': 'qa-cross-pool', 'pool_id': other_pool['id']}, expected=409)
        lab.passed('Actual agent validates existing local directory and libvirt network; registrations replay without duplication; CSRF/conflicting replay rejected')

        def upload(path, request_id, name, expected=201, interrupted=False):
            query = urllib.parse.urlencode({'storage_id': storage['id'], 'name': name, 'request_id': request_id})
            conn = http.client.HTTPSConnection('localhost', lab.port, context=ssl.create_default_context(cafile=str(cert)), timeout=180)
            conn.putrequest('POST', '/api/v1/media?' + query)
            conn.putheader('Cookie', '; '.join(f'{c.name}={c.value}' for c in lab.cookies))
            conn.putheader('X-CSRF-Token', lab.csrf)
            conn.putheader('Origin', lab.url)
            conn.putheader('Content-Type', 'application/octet-stream')
            conn.putheader('Content-Length', str(path.stat().st_size))
            conn.endheaders()
            with path.open('rb') as stream:
                for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                    conn.send(chunk)
                    if interrupted:
                        conn.close()
                        return
            response = conn.getresponse()
            data = response.read()
            conn.close()
            assert response.status == expected, (response.status, data.decode())
            return json.loads(data)
        invalid = work / 'invalid.iso'
        invalid.write_bytes(b'not an ISO')
        upload(invalid, 'qa-invalid', 'Invalid.iso', expected=400)
        upload(args.iso, 'qa-interrupted', 'Interrupted.iso', interrupted=True)
        time.sleep(.5)
        assert not lab.api('/api/v1/media'), 'interrupted upload must not publish media'
        media = upload(args.iso, 'qa-media', 'Debian QA.iso')
        operation(media['operation_id'])
        assert media['sha256'] == hashlib.file_digest(args.iso.open('rb'), 'sha256').hexdigest()
        lab.passed('Invalid and interrupted ISO uploads publish nothing; real Debian ISO uploaded, hashed and imported through enrolled agent mTLS')
        other_request('/api/v1/agent/media/' + media['id'], expected=404)
        other_request('/api/v1/agent/report', {'version': 1, 'sequence': 2, 'inventory': host()['inventory'], 'capabilities': ['managed-vm-v1'], 'results': []})
        # Two disks on one target with different buses and provisioning, plus a derived MAC.
        body = {**common, 'request_id': 'qa-create', 'name': f'managed-vm-qa-{suffix}', 'media_id': media['id'],
            'description': 'Managed VM acceptance guest', 'guest_os': 'linux',
            'cpu': {'count': 2, 'sockets': 1, 'cores': 2, 'threads': 1, 'model': 'host-passthrough'},
            'memory': {'mode': 'dedicated', 'size_mib': 1024},
            'disks': [{'storage_id': storage['id'], 'name': 'system', 'size_gib': 2, 'bus': 'virtio', 'thin': True, 'boot_order': 2},
                      {'storage_id': storage['id'], 'name': 'data', 'size_gib': 1, 'bus': 'sata', 'thin': False, 'boot_order': 0}],
            'nics': [{'network_id': network['id'], 'model': 'virtio', 'mac': ''}],
            'boot': {'firmware': 'bios', 'machine': '', 'media_boot_order': 1},
            'start_after_create': False, 'autostart': False}
        lab.api('/api/v1/vms', {**body, 'cpu': {**body['cpu'], 'count': 0}}, expected=400)
        lab.api('/api/v1/vms', {**body, 'disks': [{**body['disks'][0], 'name': 'data'}, body['disks'][1]]}, expected=400)
        lab.api('/api/v1/vms', {**body, 'nics': [{**body['nics'][0], 'mac': '53:54:00:11:22:33'}]}, expected=400)
        lab.api('/api/v1/vms', {**body, 'request_id': 'qa-cross-host', 'host_id': other['host_id']}, expected=409)
        lab.api('/api/v1/vms', {**body, 'request_id': 'qa-absent-network', 'nics': [{**body['nics'][0], 'network_id': 'absent-network'}]}, expected=404)
        lab.passed('Valid second mTLS identity without capability cannot register; invalid vCPU count, duplicate disk name and multicast MAC refused with 400; cross-host create refused with 409 and an unresolvable network with 404 (second report explicitly simulated)')
        lab.api('/api/v1/vms', {**body, 'request_id': 'qa-over-cpu', 'cpu': {'count': 32, 'sockets': 0, 'cores': 0, 'threads': 0, 'model': 'host-passthrough'}}, expected=409)
        lab.api('/api/v1/vms', {**body, 'request_id': 'qa-cross-pool-create', 'pool_id': other_pool['id']}, expected=409)
        too_large = lab.api('/api/v1/vms', {**body, 'request_id': 'qa-no-capacity', 'name': 'qa-no-capacity',
            'disks': [{**body['disks'][0], 'size_gib': 100}]}, expected=202)
        assert 'insufficient free storage' in operation(too_large['operation']['id'], 'failed')['error']
        assert too_large['vm_id'] not in ssh(hostlab, 'virsh -c qemu:///system list --all --uuid', text=True).stdout
        lab.passed('Missing native directory/network fail honestly; cross-pool and excess CPU rejected; insufficient real disk capacity creates no domain')
        created = lab.api('/api/v1/vms', body, expected=202)
        operation(created['operation']['id'])
        vm_id = created['vm_id']
        assert lab.api('/api/v1/vms', body)['vm_id'] == vm_id
        lab.api('/api/v1/vms', {**body, 'memory': {**body['memory'], 'size_mib': 2048}}, expected=409)
        xml = ssh(hostlab, shlex.join(['virsh', '-c', 'qemu:///system', 'dumpxml', vm_id]), text=True).stdout
        report['defined_bios'] = verify_definition(hostlab, xml, {**body, 'id': vm_id}, storage_path, media)
        assert 'shut off' in ssh(hostlab, shlex.join(['virsh', '-c', 'qemu:///system', 'domstate', vm_id]), text=True).stdout
        (evidence / 'created-domain.xml').write_text(xml)
        lab.passed('One persistent stopped libvirt domain with requested CPU/disk/ISO/network; invalid CPU and conflicting replay rejected')
        # The only power action is against the exact new disposable QA UUID.
        ssh(hostlab, shlex.join(['virsh', '-c', 'qemu:///system', 'start', vm_id]), text=True)
        time.sleep(8)
        assert 'running' in ssh(hostlab, shlex.join(['virsh', '-c', 'qemu:///system', 'domstate', vm_id]), text=True).stdout
        screenshot = f'{remote}/guest-boot.ppm'
        ssh(hostlab, shlex.join(['virsh', '-c', 'qemu:///system', 'screenshot', vm_id, screenshot]), text=True)
        (evidence / 'guest-boot.png').write_bytes(ssh(hostlab, f'cat {screenshot}').stdout)
        lab.passed('Exact newly-created VM starts through local lab virsh and remains running; guest screen captured for human boot inspection')
        # The UEFI variant also exercises the maximum memory mode and its virtio balloon.
        uefi_body = {**body, 'request_id': 'qa-create-uefi', 'name': f'managed-vm-qa-uefi-{suffix}',
            'boot': {**body['boot'], 'firmware': 'uefi'}, 'memory': {'mode': 'maximum', 'size_mib': 1024}}
        uefi = lab.api('/api/v1/vms', uefi_body, expected=202)
        operation(uefi['operation']['id'])
        uefi_id = uefi['vm_id']
        uefi_xml = ssh(hostlab, shlex.join(['virsh', '-c', 'qemu:///system', 'dumpxml', uefi_id]), text=True).stdout
        (evidence / 'created-domain-uefi.xml').write_text(uefi_xml)
        report['defined_uefi'] = verify_definition(hostlab, uefi_xml, {**uefi_body, 'id': uefi_id}, storage_path, media)
        assert 'shut off' in ssh(hostlab, shlex.join(['virsh', '-c', 'qemu:///system', 'domstate', uefi_id]), text=True).stdout
        ssh(hostlab, shlex.join(['virsh', '-c', 'qemu:///system', 'start', uefi_id]), text=True)
        time.sleep(10)
        assert 'running' in ssh(hostlab, shlex.join(['virsh', '-c', 'qemu:///system', 'domstate', uefi_id]), text=True).stdout
        ssh(hostlab, shlex.join(['virsh', '-c', 'qemu:///system', 'screenshot', uefi_id, screenshot]), text=True)
        (evidence / 'guest-boot-uefi.png').write_bytes(ssh(hostlab, f'cat {screenshot}').stdout)
        lab.passed('UEFI creation defines stopped domain with OVMF loader; exact new QA UUID starts and guest firmware boot screen captured')

        lab.stop(cp)
        time.sleep(3)
        assert 'running' in ssh(hostlab, shlex.join(['virsh', '-c', 'qemu:///system', 'domstate', vm_id]), text=True).stdout
        cp = lab.start('controller', *controller_args)
        eventually(lambda: lab.api('/api/v1/session'), 'controller restart')
        eventually(lambda: host()['connection'] == 'fresh', 'agent reconnect')
        assert lab.api('/api/v1/vms', body)['vm_id'] == vm_id
        agent_stop()
        eventually(lambda: host()['connection'] == 'stale', 'agent stop freshness')
        lab.api('/api/v1/vms', {**body, 'request_id': 'qa-stale-create', 'name': 'qa-stale'}, expected=409)
        agent_start()
        eventually(lambda: host()['connection'] == 'fresh', 'agent process restart')
        assert lab.api('/api/v1/vms', body)['vm_id'] == vm_id
        lab.passed('Controller outage preserves running QA VM; Controller and agent restart/reconnect preserve creation identity and replay')
        report['inputs_changed_during_run'] = [name for name, digest in report['input_sha256'].items() if not (ROOT / name).is_file() or hashlib.sha256((ROOT / name).read_bytes()).hexdigest() != digest]
        report['completed'] = True
        report.update({'vm_id': vm_id, 'uefi_vm_id': uefi_id, 'pool_id': pool['id'], 'host_id': host_id,
            'artifacts': {p.name: hashlib.file_digest(p.open('rb'), 'sha256').hexdigest() for p in (work/'controller', work/'agent', source, args.iso)},
            'limits': ['No physical hardware', 'No guest installation', 'No browser lifecycle controls', 'Browser QA recorded separately']})
        private = {'url': lab.url, 'controller_pid': cp.pid, 'pool_id': pool['id'], 'host_id': host_id,
                   'storage_path': storage_path, 'storage_id': storage['id'], 'network_id': network['id'], 'media_id': media['id'], 'vm_id': vm_id}
        (work / 'browser.json').write_text(json.dumps(private, indent=2))
    finally:
        (evidence / 'results.json').write_text(json.dumps(report, indent=2) + '\n')
        if not args.keep_running or not report['completed']:
            lab.close()
        else:
            for log in lab.logs:
                log.close()
        print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
