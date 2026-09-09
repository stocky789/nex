#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Prove a Controller is replaceable with real processes.

Controller A enrolls a real nex-core-agent, which registers local storage (a root-owned
directory created for the check), a pool network (an existing bridge, verified read-only)
and receives an uploaded ISO. Controller A is then stopped and its data deleted. A brand-new
Controller B with a different certificate is started; the host is connected to it with an
ordinary enrollment, and the network must come back with the same ID and name without any
operator re-entry and without a command being dispatched.

Run inside a disposable network namespace that owns the named bridge, for example:
  unshare -Urnm sh -c 'mount -t sysfs none /sys && ip link set lo up && ip link add nexqa type bridge
      && python3 tests/integration/controller_replacement.py --bridge nexqa --evidence OUT'
Set NEX_BIN to a directory holding prebuilt nex-controller, nex-core-agent and nex-source.tar.gz
to skip building (needed when the namespace has no Go toolchain access). Storage registration
needs root-owned directories, so run the namespace as root (sudo unshare -nm) with
--storage-root naming a new directory on a local ext4/XFS/Btrfs filesystem; it is removed at the end.
With --vm (needs system libvirt, qemu-img and /dev/kvm in the namespace) a stopped VM is also
defined on Controller A and must come back on Controller B; a second VM is then created on B
from the adopted storage/network/ISO, proving the host resolves them by ID.
"""
import argparse
import http.client
import json
import os
import ssl
import urllib.parse
from pathlib import Path
import shutil
import sys
import tempfile
import time
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))
from phase1 import ROOT, Lab, eventually, run  # noqa: E402


def binaries(path):
    prebuilt = os.environ.get('NEX_BIN')
    if prebuilt:
        b = Path(prebuilt)
        return b / 'nex-controller', b / 'nex-core-agent', b / 'nex-source.tar.gz'
    go = os.environ.get('GO') or shutil.which('go')
    controller, agent, source = path / 'nex-controller', path / 'nex-core-agent', path / 'nex-source.tar.gz'
    run(go, 'build', '-o', controller, './cmd/nex-controller', cwd=ROOT)
    run(go, 'build', '-o', agent, './cmd/nex-core-agent', cwd=ROOT)
    run(sys.executable, ROOT / 'scripts/build-source.py', '--output', source, env={**os.environ, 'GO': str(go)}, cwd=ROOT)
    return controller, agent, source


def controller(lab, binary, source, name):
    """Start an independent Controller with its own data directory and certificate."""
    cert, key = lab.path / f'{name}.pem', lab.path / f'{name}.key'
    run('openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes',
        '-keyout', key, '-out', cert, '-days', '1', '-subj', f'/CN=localhost {name}', '-addext', 'subjectAltName=DNS:localhost')
    password = lab.path / f'{name}-password'
    password.write_text('Disposable-testing-8572!')
    password.chmod(0o600)
    data = lab.path / f'{name}-data'
    process = lab.start(name, binary, '-data-dir', data, '-listen', f'127.0.0.1:{lab.port}', '-tls-cert', cert,
                        '-tls-key', key, '-admin-username', 'admin', '-admin-password-file', password,
                        '-stale-after', '3s', '-ui-dir', ROOT / 'web/dist', '-source-archive', source)
    lab.client(cert)
    eventually(lambda: lab.api('/api/v1/sites', expected=401), name + ' startup')
    lab.csrf = lab.api('/api/v1/session', {'username': 'admin', 'password': password.read_text()})['csrf_token']
    site = lab.api('/api/v1/sites', {'name': name + ' site'}, expected=201)
    token = lab.path / f'{name}-token'
    token.write_text(lab.api('/api/v1/enrollment-tokens', {'site_id': site['id']}, expected=201)['token'])
    token.chmod(0o600)
    return process, cert, token, data


def upload(lab, cert, storage_id, name, request_id, data):
    """Raw ISO upload; the agent then fetches it from the Controller over mTLS."""
    query = urllib.parse.urlencode({'storage_id': storage_id, 'name': name, 'request_id': request_id})
    conn = http.client.HTTPSConnection('localhost', lab.port, context=ssl.create_default_context(cafile=str(cert)), timeout=60)
    conn.putrequest('POST', '/api/v1/media?' + query)
    conn.putheader('Cookie', '; '.join(f'{c.name}={c.value}' for c in lab.cookies))
    conn.putheader('X-CSRF-Token', lab.csrf)
    conn.putheader('Origin', lab.url)
    conn.putheader('Content-Type', 'application/octet-stream')
    conn.putheader('Content-Length', str(len(data)))
    conn.endheaders()
    conn.send(data)
    response = conn.getresponse()
    body = response.read()
    conn.close()
    assert response.status == 201, (response.status, body.decode())
    return json.loads(body)


def synthetic_iso():
    data = bytearray(65536)
    data[32768:32775] = b'\x01CD001\x01'
    return bytes(data)


def settled(lab, path, item_id):
    """Wait for a registration/import to reach a terminal state and return it."""
    def check():
        item = next((i for i in lab.api(path) if i['id'] == item_id), None)
        return item if item and item['status'] in ('succeeded', 'failed', 'needs_attention') else None
    item = eventually(check, f'{path} {item_id} settled', timeout=60)
    assert item['status'] == 'succeeded', item
    return item


def fresh_host(lab):
    hosts = lab.api('/api/v1/hosts')
    return hosts[0] if hosts and hosts[0]['connection'] == 'fresh' and 'managed-vm-v1' in (hosts[0].get('capabilities') or []) else None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bridge', required=True, help='existing Linux bridge owned by this disposable namespace')
    parser.add_argument('--storage-root', type=Path, required=True, help='new root-owned directory to create for local storage; removed at the end')
    parser.add_argument('--evidence', type=Path, required=True)
    parser.add_argument('--vm', action='store_true', help='also define a VM (system libvirt, qemu-img and /dev/kvm required)')
    args = parser.parse_args()
    assert (Path('/sys/class/net') / args.bridge / 'bridge').is_dir(), 'bridge must exist in this namespace'
    assert os.geteuid() == 0, 'storage registration requires root-owned directories; run as root inside the namespace'
    assert args.storage_root.is_absolute() and args.storage_root.parent.is_dir() and (not args.storage_root.exists() or not any(args.storage_root.iterdir())), 'storage root must be a new or empty absolute path (a mounted local filesystem is fine)'
    args.storage_root.mkdir(mode=0o711, exist_ok=True)
    args.storage_root.chmod(0o711)
    storage_dir = args.storage_root / 'images'
    storage_dir.mkdir(mode=0o711)
    args.evidence.mkdir(parents=True, exist_ok=True)
    lab = Lab(Path(tempfile.mkdtemp(prefix='nex-replacement.', dir=os.environ.get('NEX_LAB_DIR') or ROOT / '.cache')))
    evidence = {'bridge': args.bridge, 'vm_covered': args.vm, 'steps': []}
    if not args.vm:
        print('NOTE: VM definitions are not covered in this run (no --vm); they are covered by Controller unit tests only', flush=True)
    virsh = lambda *a: run('virsh', '-c', 'qemu:///system', *a).stdout
    try:
        controller_bin, agent_bin, source = binaries(lab.path)
        state = lab.path / 'agent-state'
        agent_args = lambda: [agent_bin, 'run', '--state-dir', state, '--heartbeat', '1s', '--probe-interval', '1s', '--probe-timeout', '10s']

        # Controller A: enroll, register a network, wait for the host to apply it.
        a_process, a_cert, a_token, a_data = controller(lab, controller_bin, source, 'controller-a')
        run(agent_bin, 'enroll', '--controller', lab.url, '--ca-file', a_cert, '--token-file', a_token, '--state-dir', state)
        agent = lab.start('agent-a', *agent_args())
        host = eventually(lambda: fresh_host(lab), 'host fresh on Controller A', timeout=30)
        scope = {'pool_id': host['pool_id'], 'host_id': host['id']}
        storage = lab.api('/api/v1/storage', {**scope, 'request_id': str(uuid.uuid4()), 'name': 'Replacement test storage', 'path': str(storage_dir)}, expected=201)
        storage = settled(lab, '/api/v1/storage', storage['id'])
        network = lab.api('/api/v1/networks', {**scope, 'request_id': str(uuid.uuid4()), 'name': 'Replacement test network', 'kind': 'bridge', 'source': args.bridge}, expected=201)
        applied = settled(lab, '/api/v1/networks', network['id'])
        media = upload(lab, a_cert, storage['id'], 'replacement.iso', str(uuid.uuid4()), synthetic_iso())
        media = settled(lab, '/api/v1/media', media['id'])
        assert (storage_dir / 'nex-media' / (media['id'] + '.iso')).is_file(), 'agent did not store the transferred ISO'
        vm = None
        if args.vm:
            spec = {**scope, 'request_id': str(uuid.uuid4()), 'name': 'replacement-guest', 'storage_id': storage['id'], 'network_id': network['id'],
                    'media_id': media['id'], 'cpus': 1, 'memory_mib': 512, 'disk_gib': 1, 'firmware': 'bios'}
            created = lab.api('/api/v1/vms', spec, expected=202)
            vm = settled(lab, '/api/v1/vms', created['vm_id'])
            assert vm['id'] in virsh('list', '--all', '--uuid'), 'VM not defined in libvirt'
        old_identity = json.loads((state / 'state.json').read_text())
        evidence['steps'].append({'controller': 'A', 'url': lab.url, **scope, 'storage': storage, 'network': applied, 'media': media, 'vm': vm})
        lab.passed('Controller A enrolled the real agent; the agent verified local storage, applied a pool network, transferred an uploaded ISO' + (' and defined a stopped VM in libvirt' if vm else ''))

        # Destroy Controller A completely: process and database.
        lab.stop(a_process)
        shutil.rmtree(a_data)
        lab.stop(agent)
        evidence['steps'].append({'controller': 'A', 'destroyed': True, 'data_dir_removed': not a_data.exists()})

        # Controller B: new port, new certificate, new database. Connect the host with an ordinary enrollment.
        import socket
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            lab.port = sock.getsockname()[1]
        lab.url = f'https://localhost:{lab.port}'
        lab.cookies.clear()
        b_process, b_cert, b_token, _ = controller(lab, controller_bin, source, 'controller-b')
        assert lab.api('/api/v1/networks') == [] and lab.api('/api/v1/storage') == [] and lab.api('/api/v1/media') == [] and lab.api('/api/v1/hosts') == [], 'Controller B must start empty'
        run(agent_bin, 'enroll', '--controller', lab.url, '--ca-file', b_cert, '--token-file', b_token, '--state-dir', state)
        new_identity = json.loads((state / 'state.json').read_text())
        assert new_identity['host_id'] != old_identity['host_id'] and new_identity['controller'] == lab.url, 'host did not take a new identity on Controller B'
        lab.start('agent-b', *agent_args())
        host_b = eventually(lambda: fresh_host(lab), 'host fresh on Controller B', timeout=30)
        adopted, operations = {}, {}
        for kind, path, original in (('storage', '/api/v1/storage', storage), ('network', '/api/v1/networks', applied), ('media', '/api/v1/media', media)):
            item = eventually(lambda p=path, i=original['id']: next((x for x in lab.api(p) if x['id'] == i), None), kind + ' adopted by Controller B', timeout=30)
            assert item['name'] == original['name'] and item['status'] == 'succeeded', item
            assert item['host_id'] == host_b['id'] and item['pool_id'] == host_b['pool_id'], f'adopted {kind} not rewritten to the new pool/host'
            operation = next(op for op in lab.api('/api/v1/operations') if op['id'] == item['operation_id'])
            assert operation['actor'] == 'system' and operation['status'] == 'succeeded' and operation['request_id'].startswith('adopt-' + kind + ':'), operation
            adopted[kind], operations[kind] = item, operation
        assert adopted['storage']['path'] == str(storage_dir) and adopted['network']['source'] == args.bridge and adopted['media']['storage_id'] == storage['id']
        if vm:
            item = eventually(lambda: next((x for x in lab.api('/api/v1/vms') if x['id'] == vm['id']), None), 'VM adopted by Controller B', timeout=30)
            assert item['name'] == vm['name'] and item['status'] == 'succeeded' and item['host_id'] == host_b['id'] and item['pool_id'] == host_b['pool_id'], item
            assert item['spec']['storage_id'] == storage['id'] and item['spec']['network_id'] == network['id'] and item['spec']['media_id'] == media['id'], item
            item = eventually(lambda: next((x for x in lab.api('/api/v1/vms') if x['id'] == vm['id'] and x['state'] == 'shut off' and x['freshness'] == 'fresh'), None),
                              'adopted VM observed shut off by libvirt on Controller B', timeout=30)
            adopted['vm'] = item
        time.sleep(4)  # several more reports: nothing may be duplicated
        eventually(lambda: not [op for op in lab.api('/api/v1/operations') if op['status'] in ('queued', 'dispatched')],
                   'auto registrations settled', timeout=60)
        counts = {p: len(lab.api(p)) for p in ('/api/v1/networks', '/api/v1/media', '/api/v1/vms')}
        assert counts == {'/api/v1/networks': 1, '/api/v1/media': 1, '/api/v1/vms': 1 if vm else 0}, counts
        storages = lab.api('/api/v1/storage')
        assert any(s['id'] == storage['id'] and s['status'] == 'succeeded' for s in storages), storages
        # Default ISO at /var/lib/libvirt/images is auto-registered when not already held.
        assert sum(1 for s in storages if s['path'] == '/var/lib/libvirt/images') <= 1, storages
        assert not [op for op in lab.api('/api/v1/operations') if op['status'] in ('queued', 'dispatched')], 'unexpected commands dispatched'
        # New work on Controller B must resolve the adopted storage on the host by ID.
        second = upload(lab, b_cert, storage['id'], 'after-replacement.iso', str(uuid.uuid4()), synthetic_iso())
        second = settled(lab, '/api/v1/media', second['id'])
        assert (storage_dir / 'nex-media' / (second['id'] + '.iso')).is_file()
        second_vm = None
        if vm:
            created = lab.api('/api/v1/vms', {'pool_id': host_b['pool_id'], 'host_id': host_b['id'], 'request_id': str(uuid.uuid4()), 'name': 'after-replacement-guest',
                                              'storage_id': storage['id'], 'network_id': network['id'], 'media_id': second['id'], 'cpus': 1, 'memory_mib': 512, 'disk_gib': 1, 'firmware': 'bios'}, expected=202)
            second_vm = settled(lab, '/api/v1/vms', created['vm_id'])
            assert second_vm['id'] in virsh('list', '--all', '--uuid') and vm['id'] in virsh('list', '--all', '--uuid')
            adopted['vm_after_replacement'] = second_vm
        evidence['steps'].append({'controller': 'B', 'url': lab.url, 'host_id': host_b['id'], 'pool_id': host_b['pool_id'], 'adopted': adopted,
                                  'adoption_operations': operations, 'counts_after_repeat_reports': counts, 'new_media_after_replacement': second})
        lab.passed('Controller B, started empty with a different certificate, adopted storage, network, ISO record' + (', VM definition' if vm else '') + ' with the same IDs and names under actor system, with no command and no duplicates; a new ISO transfer' + (' and a new VM' if vm else '') + ' on the adopted resources succeeded')
        evidence['checks'] = lab.results
        evidence['environment'] = 'Real nex-controller and nex-core-agent processes on loopback inside a disposable network namespace as root; real directory storage and ISO transfer; bridge verified read-only; ' + ('real system libvirt defines the VMs (KVM device present, VMs never started)' if vm else 'no libvirt, so VM definitions are not covered')
        evidence['agent_state_dir_reused'] = True
    except BaseException:
        # Keep enough to diagnose: current host view and the tail of every process log.
        try:
            evidence['diagnostics'] = {'hosts': lab.api('/api/v1/hosts')}
        except Exception as exc:  # noqa: BLE001
            evidence['diagnostics'] = {'hosts_error': str(exc)}
        evidence['diagnostics']['logs'] = {log.name: Path(log.name).read_text()[-4000:] for log in lab.logs if Path(log.name).exists()}
        raise
    finally:
        lab.close()
        shutil.rmtree(args.storage_root, ignore_errors=True)
        (args.evidence / 'controller-replacement.json').write_text(json.dumps(evidence, indent=2) + '\n')
    print('Evidence:', args.evidence / 'controller-replacement.json')


if __name__ == '__main__':
    main()
