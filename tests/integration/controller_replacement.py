#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Prove a Controller is replaceable with real processes.

Controller A enrolls a real nex-core-agent, which registers local storage (a root-owned
directory created for the check), a pool network (an existing bridge, verified read-only)
and receives an uploaded ISO. Controller A is then stopped and its data deleted. A brand-new
Controller B with a different certificate is started; the host is connected to it with an
ordinary enrollment, and the host, its pool (ID and name) and the network must come back
with the same IDs and names without any operator re-entry and without a command being
dispatched: identity is host-owned, so the replacement adopts what the host presents.

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
                        '-tls-key', key, '-admin-username', 'root', '-admin-password-file', password,
                        '-stale-after', '3s', '-ui-dir', ROOT / 'web/dist', '-source-archive', source)
    lab.client(cert)
    eventually(lambda: lab.api('/api/v1/sites', expected=401), name + ' startup')
    lab.csrf = lab.api('/api/v1/session', {'username': 'root', 'password': password.read_text()})['csrf_token']
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


def vm_spec(scope, name, storage_id, network_id, media_id):
    """A specification exercising the whole shape: two disks on one target with different
    buses and provisioning, a derived and a manual MAC, and the ISO in the boot order."""
    return {**scope, 'name': name, 'description': 'Controller replacement acceptance guest', 'guest_os': 'linux',
            'cpu': {'count': 2, 'sockets': 2, 'cores': 1, 'threads': 1, 'model': 'host-model'},
            'memory': {'mode': 'maximum', 'size_mib': 512},
            'disks': [{'storage_id': storage_id, 'name': 'system', 'size_gib': 1, 'bus': 'virtio', 'thin': True, 'boot_order': 1},
                      {'storage_id': storage_id, 'name': 'data', 'size_gib': 1, 'bus': 'sata', 'thin': False, 'boot_order': 0}],
            'nics': [{'network_id': network_id, 'model': 'virtio', 'mac': ''},
                     {'network_id': network_id, 'model': 'e1000e', 'mac': '52:54:00:ab:cd:ef'}],
            'boot': {'firmware': 'bios', 'machine': '', 'media_boot_order': 2},
            'media_id': media_id, 'start_after_create': False, 'autostart': False}


def same_spec(item, sent):
    """Every requested field must come back unchanged. pool_id/host_id are asserted
    separately against the (preserved) host, and id against the VM."""
    ignored = ('id', 'pool_id', 'host_id', 'request_id')
    got = {k: v for k, v in item['spec'].items() if k not in ignored}
    want = {k: v for k, v in sent.items() if k not in ignored}
    assert got == want, (got, want)
    return True


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
            spec = vm_spec(scope, 'replacement-guest', storage['id'], network['id'], media['id'])
            created = lab.api('/api/v1/vms', {**spec, 'request_id': str(uuid.uuid4())}, expected=202)
            vm = settled(lab, '/api/v1/vms', created['vm_id'])
            assert vm['id'] in virsh('list', '--all', '--uuid'), 'VM not defined in libvirt'
            assert same_spec(vm, spec) and vm['spec']['id'] == vm['id'], vm['spec']
        old_identity = json.loads((state / 'state.json').read_text())
        pool_a = next(p for p in lab.api('/api/v1/pools') if p['id'] == host['pool_id'])
        assert old_identity['host_id'] == host['id'] and old_identity['pool_id'] == pool_a['id'] and old_identity['pool_name'] == pool_a['name'], 'Controller A did not adopt the host-owned identity'
        evidence['steps'].append({'controller': 'A', 'url': lab.url, **scope, 'pool': pool_a, 'storage': storage, 'network': applied, 'media': media, 'vm': vm})
        lab.passed('Controller A enrolled the real agent; the agent verified local storage, applied a pool network, transferred an uploaded ISO' + (' and defined a stopped VM in libvirt' if vm else ''))

        # Destroy Controller A completely: process and database.
        lab.stop(a_process)
        shutil.rmtree(a_data)
        lab.stop(agent)
        evidence['steps'].append({'controller': 'A', 'destroyed': True, 'data_dir_removed': not a_data.exists()})

        # Before Controller B exists, seed what a real host would hold that Controller A never
        # created: an appliance engine deploy journal (the host's ownership record of a Controller
        # VM), a managed journal entry that ended failed, and one corrupt journal file. Controller B
        # must list the failed task, must not lose the other entries over the corrupt file, and,
        # when libvirt observes the appliance VM, must list it as the protected Controller VM.
        appliance_uuid = str(uuid.uuid4())
        appliance_journal = lab.path / 'appliance-journal'
        appliance_journal.mkdir(mode=0o700)
        (appliance_journal / 'deploy-seed.json').write_text(json.dumps({
            'id': 'deploy-seed', 'vm_id': appliance_uuid, 'kind': 'deploy', 'status': 'booted', 'stage': 'ready', 'progress': 100,
            'digest': '', 'name': 'seeded-controller', 'disk': str(lab.path / 'unused.qcow2'), 'bridge': args.bridge}))
        managed_journal = state / 'managed-vms'
        failed_command = {'operation_id': 'seed-failed', 'kind': 'storage-register',
                          'storage': {'id': 'seed-broken', 'pool_id': scope['pool_id'], 'host_id': old_identity['host_id'], 'name': 'Seeded broken storage',
                                      'path': '/nonexistent/seeded', 'operation_id': 'seed-failed', 'status': 'failed'}}
        (managed_journal / 'seed-failed.json').write_text(json.dumps({
            'command': failed_command, 'stage': 'validating',
            'result': {'operation_id': 'seed-failed', 'status': 'failed', 'error': 'seeded failure: storage must be an existing directory', 'observed_at': '2026-09-10T00:00:00Z'}}))
        (managed_journal / 'corrupt.json').write_text('{"command":{"kind":"storage-register","operation_id":"corrupt"},"result":{"status":"pending"}')
        if vm:
            # A domain with the appliance UUID, never started, so real libvirt observes the seeded deployment.
            domain = lab.path / 'seeded-appliance.xml'
            domain.write_text(f"<domain type='qemu'><name>seeded-controller</name><uuid>{appliance_uuid}</uuid><memory unit='MiB'>64</memory>"
                              "<vcpu>1</vcpu><os><type arch='x86_64'>hvm</type></os></domain>")
            virsh('define', str(domain))
        agent_args = lambda: [agent_bin, 'run', '--state-dir', state, '--heartbeat', '1s', '--probe-interval', '1s', '--probe-timeout', '10s', '--appliance-journal-dir', appliance_journal]

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
        assert new_identity['controller'] == lab.url and new_identity['certificate_pem'] != old_identity['certificate_pem'], 'host did not take a new credential on Controller B'
        assert {k: new_identity[k] for k in ('host_id', 'pool_id', 'pool_name')} == {k: old_identity[k] for k in ('host_id', 'pool_id', 'pool_name')}, 'host identity changed on Controller B'
        lab.start('agent-b', *agent_args())
        host_b = eventually(lambda: fresh_host(lab), 'host fresh on Controller B', timeout=30)
        assert host_b['id'] == host['id'] and host_b['pool_id'] == host['pool_id'], 'Controller B did not adopt the host-owned identity'
        pool_b = next(p for p in lab.api('/api/v1/pools') if p['id'] == host_b['pool_id'])
        assert pool_b['name'] == pool_a['name'] and pool_b['master_host_id'] == host_b['id'], (pool_a, pool_b)
        adopted, operations = {}, {}
        for kind, path, original in (('storage', '/api/v1/storage', storage), ('network', '/api/v1/networks', applied), ('media', '/api/v1/media', media)):
            item = eventually(lambda p=path, i=original['id']: next((x for x in lab.api(p) if x['id'] == i), None), kind + ' adopted by Controller B', timeout=30)
            assert item['name'] == original['name'] and item['status'] == 'succeeded', item
            assert item['host_id'] == host_b['id'] and item['pool_id'] == host_b['pool_id'], f'adopted {kind} not under the preserved pool/host'
            operation = next(op for op in lab.api('/api/v1/operations') if op['id'] == item['operation_id'])
            assert operation['actor'] == 'system' and operation['status'] == 'succeeded' and operation['request_id'].startswith('adopt-' + kind + ':'), operation
            adopted[kind], operations[kind] = item, operation
        assert adopted['storage']['path'] == str(storage_dir) and adopted['network']['source'] == args.bridge and adopted['media']['storage_id'] == storage['id']
        if vm:
            item = eventually(lambda: next((x for x in lab.api('/api/v1/vms') if x['id'] == vm['id']), None), 'VM adopted by Controller B', timeout=30)
            assert item['name'] == vm['name'] and item['status'] == 'succeeded' and item['host_id'] == host_b['id'] and item['pool_id'] == host_b['pool_id'], item
            # The whole specification, not just the references, must survive the replacement.
            assert item['spec']['id'] == vm['id'] and same_spec(item, spec), item['spec']
            assert item['spec']['pool_id'] == host_b['pool_id'] and item['spec']['host_id'] == host_b['id'], item['spec']
            item = eventually(lambda: next((x for x in lab.api('/api/v1/vms') if x['id'] == vm['id'] and x['state'] == 'shut off' and x['freshness'] == 'fresh'), None),
                              'adopted VM observed shut off by libvirt on Controller B', timeout=30)
            adopted['vm'] = item
        # Seeded host truth on Controller B: the failed task with the host's operation ID, status and
        # error; every other entry adopted despite the corrupt file, which the host names in a check.
        seeded = eventually(lambda: next((op for op in lab.api('/api/v1/operations') if op['id'] == 'seed-failed'), None), 'failed journal entry listed by Controller B', timeout=30)
        assert seeded['status'] == 'failed' and seeded['kind'] == 'storage-register' and seeded['actor'] == 'system' and seeded['host_id'] == host_b['id'] and 'seeded failure' in seeded['error'], seeded
        checks = {c['name']: c for c in (lab.api('/api/v1/hosts')[0].get('inventory') or {}).get('checks') or []}
        assert checks['managed-journal']['status'] == 'failed' and 'corrupt.json' in checks['managed-journal']['error'], checks.get('managed-journal')
        adopted['seeded_failed_operation'], adopted['managed_journal_check'] = seeded, checks['managed-journal']
        # The host refuses new mutations while a journal file is unreadable (fail closed); removing
        # the file is the operator's recovery, after which the check returns to healthy.
        (managed_journal / 'corrupt.json').unlink()
        eventually(lambda: next((c for c in (lab.api('/api/v1/hosts')[0].get('inventory') or {}).get('checks') or [] if c['name'] == 'managed-journal' and c['status'] == 'healthy'), None),
                   'managed-journal check healthy after the corrupt file is removed', timeout=30)
        if vm:
            appliance = eventually(lambda: next((x for x in lab.api('/api/v1/vms') if x['id'] == appliance_uuid), None), 'seeded appliance adopted by Controller B', timeout=30)
            assert appliance['role'] == 'controller' and appliance['protected'] and appliance['name'] == 'seeded-controller' and appliance['host_id'] == host_b['id'] and appliance['pool_id'] == host_b['pool_id'], appliance
            lab.api('/api/v1/appliances/adopt', {'request_id': 'deploy-seed', 'host_id': host_b['id'], 'vm_id': appliance_uuid, 'name': 'seeded-controller'}, expected=200)
            adopted['seeded_appliance'] = appliance
        else:
            print('NOTE: appliance re-adoption needs libvirt to observe the seeded VM; not covered in this run (no --vm)', flush=True)
        time.sleep(4)  # several more reports: nothing may be duplicated
        eventually(lambda: not [op for op in lab.api('/api/v1/operations') if op['status'] in ('queued', 'dispatched')],
                   'auto registrations settled', timeout=60)
        counts = {p: len(lab.api(p)) for p in ('/api/v1/networks', '/api/v1/media', '/api/v1/vms')}
        assert counts == {'/api/v1/networks': 1, '/api/v1/media': 1, '/api/v1/vms': 2 if vm else 0}, counts
        assert sum(1 for op in lab.api('/api/v1/operations') if op['id'] == 'seed-failed') == 1, 'seeded outcome duplicated'
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
            after = vm_spec({'pool_id': host_b['pool_id'], 'host_id': host_b['id']}, 'after-replacement-guest', storage['id'], network['id'], second['id'])
            after['nics'][1]['mac'] = '52:54:00:ab:cd:f0'  # pool-wide MAC uniqueness is enforced
            created = lab.api('/api/v1/vms', {**after, 'request_id': str(uuid.uuid4())}, expected=202)
            second_vm = settled(lab, '/api/v1/vms', created['vm_id'])
            assert same_spec(second_vm, after), second_vm['spec']
            assert second_vm['id'] in virsh('list', '--all', '--uuid') and vm['id'] in virsh('list', '--all', '--uuid')
            adopted['vm_after_replacement'] = second_vm
        evidence['steps'].append({'controller': 'B', 'url': lab.url, 'host_id': host_b['id'], 'pool_id': host_b['pool_id'], 'pool': pool_b, 'identity_preserved': True, 'adopted': adopted,
                                  'adoption_operations': operations, 'counts_after_repeat_reports': counts, 'new_media_after_replacement': second})
        lab.passed('Controller B, started empty with a different certificate, adopted the host and pool with their original IDs and pool name, plus storage, network, ISO record' + (', VM definition' if vm else '') + ' with the same IDs and names under actor system, with no command and no duplicates; a new ISO transfer' + (' and a new VM' if vm else '') + ' on the adopted resources succeeded')
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
        if args.vm:
            try:
                run('virsh', '-c', 'qemu:///system', 'undefine', 'seeded-controller')
            except Exception:  # noqa: BLE001
                pass
        lab.close()
        shutil.rmtree(args.storage_root, ignore_errors=True)
        (args.evidence / 'controller-replacement.json').write_text(json.dumps(evidence, indent=2) + '\n')
    print('Evidence:', args.evidence / 'controller-replacement.json')


if __name__ == '__main__':
    main()
