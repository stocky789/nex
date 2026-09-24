#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise Core VM device defaults through a real agent and system libvirt.

Run vm_defaults_native.sh; it confines all domains, disks, bridge and processes to
one disposable Debian container. A supplied cloud image is read only and copied
into the Core-created system disk for optional guest workload checks.
"""
import base64
import json
import os
import shutil
import socket
import subprocess
import tempfile
import time
import uuid
import xml.etree.ElementTree as ET
from pathlib import Path

from controller_replacement import binaries, controller, edit_spec, fresh_host, settled, upload
from phase1 import Lab, eventually, run
from vm_defaults_benchmark import measure


def virsh(*args):
    return run('virsh', '-c', 'qemu:///system', *args).stdout


def domain(vm_id, live=False):
    args = ('dumpxml', vm_id) if live else ('dumpxml', vm_id, '--inactive')
    return ET.fromstring(virsh(*args))


def disk(xml, target):
    return next(d for d in xml.findall('./devices/disk') if d.find('target').get('dev') == target)


def nic(xml, mac):
    return next(n for n in xml.findall('./devices/interface') if n.find('mac').get('address') == mac)


def check_devices(xml, original_mac, added_mac=None, legacy=False, thread=True, scsi=False):
    assert xml.findtext('iothreads') == ('1' if thread else None), ET.tostring(xml, encoding='unicode')[:1200]
    assert disk(xml, 'vda').find('driver').get('iothread') == (None if legacy else '1')
    controllers = [c for c in xml.findall('./devices/controller') if c.get('type') == 'scsi']
    assert len(controllers) == int(scsi)
    if scsi:
        controller = controllers[0]
        assert controller.get('model') == 'virtio-scsi' and controller.find('driver').get('iothread') == '1'
        assert disk(xml, 'sdc').find('target').get('bus') == 'scsi'
    first = nic(xml, original_mac).find('driver')
    assert (None if first is None else first.get('queues')) == (None if legacy else '4')
    if added_mac:
        assert disk(xml, 'vdb').find('driver').get('iothread') == '1'
        assert nic(xml, added_mac).find('driver').get('queues') == '4'


def operation(lab, op_id):
    def settled_operation():
        found = next((o for o in lab.api('/api/v1/operations') if o['id'] == op_id), None)
        if found and found['status'] in ('succeeded', 'failed', 'needs_attention'):
            assert found['status'] == 'succeeded', found
            return found
        return None
    return eventually(settled_operation, f'operation {op_id}', timeout=180)


def guest_seed(lab, cert, storage_id, mac):
    key = lab.path / 'guest-key'
    run('ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', key)
    seed = lab.path / 'seed'
    seed.mkdir()
    (seed / 'user-data').write_text('#cloud-config\ndisable_root: false\nusers:\n  - name: root\n    lock_passwd: true\n    ssh_authorized_keys:\n      - ' + (key.with_suffix('.pub')).read_text().strip() + '\n')
    (seed / 'meta-data').write_text('instance-id: nex-vm-defaults-test\nlocal-hostname: nex-vm-defaults-test\n')
    (seed / 'network-config').write_text('version: 2\nethernets:\n  primary:\n    match:\n      macaddress: "' + mac + '"\n    dhcp4: false\n    addresses: [198.18.99.2/24]\n    routes:\n      - to: default\n        via: 198.18.99.1\n    nameservers:\n      addresses: [1.1.1.1]\n')
    iso = lab.path / 'seed.iso'
    run('genisoimage', '-quiet', '-output', iso, '-volid', 'cidata', '-joliet', '-rock',
        seed / 'user-data', seed / 'meta-data', seed / 'network-config')
    media = upload(lab, cert, storage_id, 'nex-vm-defaults-seed.iso', str(uuid.uuid4()), iso.read_bytes())
    settled(lab, '/api/v1/media', media['id'])
    return key, media['id']


def guest_command(lab, key, *command, timeout=30):
    return subprocess.run(['ssh', '-i', str(key), '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=5',
                           '-o', f'UserKnownHostsFile={lab.path / "known-hosts"}',
                           '-o', 'StrictHostKeyChecking=accept-new', 'root@198.18.99.2', *map(str, command)],
                          text=True, capture_output=True, timeout=timeout, check=True).stdout


def guest_ready(lab, key):
    deadline = time.monotonic() + 240
    last = ''
    while time.monotonic() < deadline:
        try:
            return guest_command(lab, key, 'test', '-f', '/var/lib/cloud/instance/boot-finished')
        except (subprocess.CalledProcessError, subprocess.TimeoutExpired, OSError) as exc:
            last = str(exc)[-300:]
            time.sleep(2)
    raise RuntimeError('guest did not finish cloud-init and accept SSH: ' + last)


def guest_queues(lab, key, mac):
    script = f'for d in /sys/class/net/*; do [ "$(cat "$d/address")" = "{mac}" ] && find "$d/queues" -maxdepth 1 -name "rx-*" | wc -l; done; true'
    return int(guest_command(lab, key, script).strip())


def guest_channels(lab, key, mac, activate=None):
    script = f'for d in /sys/class/net/*; do [ "$(cat "$d/address")" = "{mac}" ] && basename "$d"; done; true'
    interface = guest_command(lab, key, script).strip()
    assert interface, 'original virtio NIC is absent from guest'
    if activate is not None:
        guest_command(lab, key, f'ethtool -L {interface} combined {activate}')
    output = guest_command(lab, key, f'ethtool -l {interface}')
    current = output.split('Current hardware settings:', 1)[1]
    import re
    return int(re.search(r'^Combined:\s*(\d+)$', current, re.M).group(1))


def iothread_ticks(vm_name):
    pid = (Path('/run/libvirt/qemu') / (vm_name + '.pid')).read_text().strip()
    for task in (Path('/proc') / pid / 'task').iterdir():
        if task.joinpath('comm').read_text().strip().startswith('IO iothread1'):
            fields = task.joinpath('stat').read_text().rsplit(')', 1)[1].split()
            return int(fields[11]) + int(fields[12])
    raise AssertionError('QEMU has no IO iothread1 execution thread')


def main():
    assert os.geteuid() == 0 and Path('/dev/kvm').is_char_device()
    assert (Path('/sys/class/net/nexqa/bridge')).is_dir(), 'test-owned bridge missing'
    base = Path('/base/guest.qcow2')
    if base.exists():
        info = json.loads(run('qemu-img', 'info', '--output=json', base).stdout)
        assert info['format'] == 'qcow2' and not info.get('backing-filename'), 'guest base must be a standalone qcow2'
        system_gib = max(4, (info['virtual-size'] + (1 << 30) - 1) >> 30)
    else:
        system_gib = 4
    lab = Lab(Path(tempfile.mkdtemp(prefix='nex-vm-defaults.', dir='/tmp')))
    storage_dir = Path('/var/lib/nexqa-defaults/images')
    storage_dir.mkdir(mode=0o711, parents=True, exist_ok=True)
    vm_id = None
    evidence_path = Path('/tmp/nex-vm-defaults-results.json')
    evidence = {'completed': False, 'checks': [], 'guest_image_supplied': base.exists(),
                'benchmark_mode': 'focused', 'limits': []}

    def save_evidence():
        evidence_path.write_text(json.dumps(evidence, indent=2) + '\n')
        evidence_path.chmod(0o600)

    try:
        controller_bin, agent_bin, source = binaries(lab.path)
        evidence['environment'] = {
            'os': next(line.split('=', 1)[1].strip('"') for line in Path('/etc/os-release').read_text().splitlines() if line.startswith('PRETTY_NAME=')),
            'kernel': run('uname', '-r').stdout.strip(),
            'libvirt': run('dpkg-query', '-W', '-f=${Version}', 'libvirt-daemon-system').stdout,
            'qemu': run('dpkg-query', '-W', '-f=${Version}', 'qemu-system-x86').stdout,
            'agent_sha256': run('sha256sum', agent_bin).stdout.split()[0],
            'controller_sha256': run('sha256sum', controller_bin).stdout.split()[0],
            'harness_sha256': run('sha256sum', __file__).stdout.split()[0],
        }
        first_controller, cert, token, first_data = controller(lab, controller_bin, source, 'vm-defaults')
        state = lab.path / 'agent-state'
        run(agent_bin, 'enroll', '--controller', lab.url, '--ca-file', cert, '--token-file', token, '--state-dir', state)
        agent_args = (agent_bin, 'run', '--state-dir', state, '--heartbeat', '1s', '--probe-interval', '1s', '--probe-timeout', '10s')
        agent = lab.start('agent', *agent_args)
        host = eventually(lambda: fresh_host(lab), 'fresh Core agent', timeout=60)
        scope = {'pool_id': host['pool_id'], 'host_id': host['id']}
        storage = lab.api('/api/v1/storage', {**scope, 'request_id': str(uuid.uuid4()), 'name': 'VM defaults test storage', 'path': str(storage_dir)}, expected=201)
        settled(lab, '/api/v1/storage', storage['id'])
        network = lab.api('/api/v1/networks', {**scope, 'request_id': str(uuid.uuid4()), 'name': 'VM defaults test bridge', 'kind': 'bridge', 'source': 'nexqa'}, expected=201)
        settled(lab, '/api/v1/networks', network['id'])
        original_mac = '52:54:00:ab:ce:01'
        key, media_id = guest_seed(lab, cert, storage['id'], original_mac) if base.exists() else (None, '')
        spec = {**scope, 'name': 'vm-defaults-test', 'guest_os': 'linux', 'description': 'native defaults test',
                'cpu': {'count': 4, 'sockets': 1, 'cores': 4, 'threads': 1, 'model': 'host-model'},
                'memory': {'mode': 'maximum', 'size_mib': 1024},
                'disks': [{'storage_id': storage['id'], 'name': 'system', 'size_gib': system_gib, 'bus': 'virtio', 'thin': True, 'boot_order': 1}],
                'nics': [{'network_id': network['id'], 'model': 'virtio', 'mac': original_mac}],
                'boot': {'firmware': 'bios', 'machine': 'q35', 'media_boot_order': 2 if media_id else 0},
                'media_id': media_id, 'start_after_create': False, 'autostart': False}
        created = lab.api('/api/v1/vms', {**spec, 'request_id': str(uuid.uuid4())}, expected=202)
        vm_id = created['vm_id']
        operation(lab, created['operation']['id'])
        vm = next(v for v in lab.api('/api/v1/vms') if v['id'] == vm_id)
        assert vm['spec']['nics'][0]['mac'] == original_mac
        initial = domain(vm_id)
        check_devices(initial, original_mac)
        lab.passed('Core vm-create produced one IOThread, a virtio-blk assignment and four virtio NIC queues in libvirt')

        if base.exists():
            evidence['guest_base_sha256'] = run('sha256sum', base).stdout.split()[0]

        # Simulate a historical Core definition in this disposable lab, updating
        # both libvirt and its Core journal so the predecessor remains coherent.
        lab.stop(agent)
        initial.remove(initial.find('iothreads'))
        thread_ids = initial.find('iothreadids')
        if thread_ids is not None:
            initial.remove(thread_ids)
        disk(initial, 'vda').find('driver').attrib.pop('iothread')
        nic(initial, original_mac).find('driver').attrib.pop('queues')
        legacy_xml = lab.path / 'legacy.xml'
        ET.ElementTree(initial).write(legacy_xml, encoding='unicode')
        virsh('define', str(legacy_xml))
        journal_path = state / 'managed-vms' / (created['operation']['id'] + '.json')
        journal = json.loads(journal_path.read_text())
        journal['domain'] = base64.b64encode(ET.tostring(initial)).decode()
        replacement = journal_path.with_suffix('.tmp')
        replacement.write_text(json.dumps(journal))
        replacement.replace(journal_path)
        agent = lab.start('agent-after-legacy-fixture', *agent_args)
        eventually(lambda: fresh_host(lab), 'fresh Core agent after legacy fixture', timeout=60)
        check_devices(domain(vm_id), original_mac, legacy=True, thread=False)
        start = lab.api(f'/api/v1/vms/{vm_id}/power', {'request_id': str(uuid.uuid4()), 'action': 'start'}, expected=202)
        operation(lab, start['operation']['id'])
        eventually(lambda: next((v for v in lab.api('/api/v1/vms') if v['id'] == vm_id and v['state'] == 'running'), None), 'running guest', timeout=60)
        check_devices(domain(vm_id, live=True), original_mac, legacy=True, thread=False)
        lab.passed('A legacy device definition started with its original disk and NIC settings')

        current = next(v for v in lab.api('/api/v1/vms') if v['id'] == vm_id)
        body = edit_spec(spec, current, disks=current['spec']['disks'] + [{'storage_id': storage['id'], 'name': 'hot-data', 'size_gib': 1, 'bus': 'virtio', 'thin': True, 'boot_order': 0}],
                         nics=current['spec']['nics'] + [{'network_id': network['id'], 'model': 'virtio', 'mac': ''}])
        body['disks'].append({'storage_id': storage['id'], 'name': 'scsi-data', 'size_gib': 1, 'bus': 'scsi', 'thin': True, 'boot_order': 0})
        changed = lab.api(f'/api/v1/vms/{vm_id}', {**body, 'request_id': str(uuid.uuid4())}, method='PUT', expected=202)
        operation(lab, changed['operation']['id'])
        current = eventually(lambda: next((v for v in lab.api('/api/v1/vms') if v['id'] == vm_id and v['spec']['revision'] == 1), None), 'live edit revision 1', timeout=60)
        added_mac = current['spec']['nics'][1]['mac']
        check_devices(domain(vm_id), original_mac, added_mac, legacy=True, scsi=True)
        check_devices(domain(vm_id, live=True), original_mac, added_mac, legacy=True, scsi=True)
        lab.passed('Core live edit added IOThread 1 before hot-plugging tuned virtio disk, new virtio-scsi controller/disk and NIC; existing devices stayed unchanged')

        lab.stop(agent)
        lab.stop(first_controller)
        shutil.rmtree(first_data)
        assert virsh('domstate', vm_id).strip() == 'running', 'Controller outage stopped the guest'
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            lab.port = listener.getsockname()[1]
        lab.url = f'https://localhost:{lab.port}'
        lab.cookies.clear()
        _, new_cert, new_token, _ = controller(lab, controller_bin, source, 'vm-defaults-replacement')
        assert lab.api('/api/v1/hosts') == [] and lab.api('/api/v1/vms') == []
        run(agent_bin, 'enroll', '--controller', lab.url, '--ca-file', new_cert, '--token-file', new_token, '--state-dir', state)
        agent = lab.start('agent-after-controller-replacement', *agent_args)
        adopted_host = eventually(lambda: fresh_host(lab), 'fresh Core on replacement Controller', timeout=60)
        assert adopted_host['id'] == host['id'] and adopted_host['pool_id'] == host['pool_id']
        adopted_vm = eventually(lambda: next((v for v in lab.api('/api/v1/vms') if v['id'] == vm_id and v['spec']['revision'] == 1 and v['state'] == 'running'), None),
                                'running VM adopted by replacement Controller', timeout=60)
        assert adopted_vm['name'] == spec['name'] and virsh('domstate', vm_id).strip() == 'running'
        check_devices(domain(vm_id), original_mac, added_mac, legacy=True, scsi=True)
        check_devices(domain(vm_id, live=True), original_mac, added_mac, legacy=True, scsi=True)
        lab.passed('Fresh Controller adopted the same running VM identity and revision with live IOThread, SCSI controller and NIC settings after original Controller data deletion')

        stop = lab.api(f'/api/v1/vms/{vm_id}/power', {'request_id': str(uuid.uuid4()), 'action': 'force-stop'}, expected=202)
        operation(lab, stop['operation']['id'])
        current = eventually(lambda: next((v for v in lab.api('/api/v1/vms') if v['id'] == vm_id and v['state'] == 'shut off'), None), 'stopped guest', timeout=60)
        body = edit_spec(spec, current, description='saved while stopped')
        changed = lab.api(f'/api/v1/vms/{vm_id}', {**body, 'request_id': str(uuid.uuid4())}, method='PUT', expected=202)
        operation(lab, changed['operation']['id'])
        check_devices(domain(vm_id), original_mac, added_mac, scsi=True)
        lab.passed('A stopped save applied defaults to existing disk/NIC devices without changing VM identity')
        if base.exists():
            path = disk(domain(vm_id), 'vda').find('source').get('file')
            assert Path(path).is_relative_to(storage_dir / 'nex-vms' / vm_id), path
            run('qemu-img', 'convert', '-O', 'qcow2', base, path)
            run('qemu-img', 'resize', path, f'{system_gib}G')
            start = lab.api(f'/api/v1/vms/{vm_id}/power', {'request_id': str(uuid.uuid4()), 'action': 'start'}, expected=202)
            operation(lab, start['operation']['id'])
            guest_ready(lab, key)
            guest_command(lab, key, 'DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq fio iperf3 iputils-ping ethtool >/tmp/nex-benchmark-packages.log 2>&1', timeout=300)
            evidence['guest'] = {'kernel': guest_command(lab, key, 'uname -r').strip(),
                                 'fio': guest_command(lab, key, 'fio --version').strip(),
                                 'iperf3': guest_command(lab, key, 'iperf3 --version | head -1').strip(),
                                 'ethtool': guest_command(lab, key, 'ethtool --version').strip()}
            stop = lab.api(f'/api/v1/vms/{vm_id}/power', {'request_id': str(uuid.uuid4()), 'action': 'force-stop'}, expected=202)
            operation(lab, stop['operation']['id'])
            tuned_xml = ET.tostring(domain(vm_id))
            lab.stop(agent)
            server = lab.start('iperf3', 'iperf3', '-s', '-B', '198.18.99.1')
            eventually(lambda: socket.create_connection(('198.18.99.1', 5201), timeout=1), 'iperf3 server', timeout=15).close()
            evidence['variants'] = {}
            for label, disk_tuned, nic_tuned in (('baseline', False, False), ('disk-only', True, False),
                                                  ('nic-only', False, True), ('both', True, True)):
                variant = ET.fromstring(tuned_xml)
                if not disk_tuned:
                    variant.remove(variant.find('iothreads'))
                    thread_ids = variant.find('iothreadids')
                    if thread_ids is not None:
                        variant.remove(thread_ids)
                    for device in variant.findall('./devices/disk'):
                        if device.get('device') == 'disk':
                            device.find('driver').attrib.pop('iothread', None)
                    scsi_controller = next(c for c in variant.findall('./devices/controller') if c.get('type') == 'scsi')
                    scsi_controller.remove(scsi_controller.find('driver'))
                if not nic_tuned:
                    for interface in variant.findall('./devices/interface'):
                        driver = interface.find('driver')
                        if driver is not None:
                            driver.attrib.pop('queues', None)
                variant_xml = lab.path / 'variant.xml'
                ET.ElementTree(variant).write(variant_xml, encoding='unicode')
                virsh('define', str(variant_xml))
                virsh('start', vm_id)
                guest_ready(lab, key)
                offered = guest_queues(lab, key, original_mac)
                automatic = guest_channels(lab, key, original_mac)
                if nic_tuned:
                    guest_channels(lab, key, original_mac, activate=4)
                current_channels = guest_channels(lab, key, original_mac)
                assert current_channels == (4 if nic_tuned else 1), (label, automatic, current_channels)
                ticks_before = iothread_ticks(spec['name']) if disk_tuned else None
                measurements = measure(lambda *args, timeout=30: guest_command(lab, key, *args, timeout=timeout))
                ticks = iothread_ticks(spec['name']) - ticks_before if disk_tuned else None
                if disk_tuned:
                    assert ticks > 0, 'QEMU IOThread did no CPU work during fio'
                evidence['variants'][label] = {'offered_rx_queues': offered, 'automatic_combined_channels': automatic,
                                               'active_combined_channels': current_channels, 'iothread_cpu_ticks': ticks,
                                               'measurements': measurements}
                save_evidence()
                print(f'BENCH: {label} completed three fio and iperf3/ping trials', flush=True)
                virsh('destroy', vm_id)
                assert virsh('domstate', vm_id).strip() == 'shut off'
            lab.stop(server)
            restored = lab.path / 'restored.xml'
            restored.write_bytes(tuned_xml)
            virsh('define', str(restored))
            check_devices(domain(vm_id), original_mac, added_mac, scsi=True)
            agent = lab.start('agent-after-benchmark', *agent_args)
            eventually(lambda: fresh_host(lab), 'Core agent after benchmark fixture restored', timeout=60)
            lab.passed('Three fio/iperf3/ping trials completed in each fixed-topology baseline, disk-only, NIC-only and both variant; Core tuning restored')
            current = next(v for v in lab.api('/api/v1/vms') if v['id'] == vm_id)
            start = lab.api(f'/api/v1/vms/{vm_id}/power', {'request_id': str(uuid.uuid4()), 'action': 'start'}, expected=202)
            operation(lab, start['operation']['id'])
            body = edit_spec(spec, current, disks=[d for d in current['spec']['disks'] if d['name'] != 'scsi-data'])
            removed = lab.api(f'/api/v1/vms/{vm_id}', {**body, 'request_id': str(uuid.uuid4())}, method='PUT', expected=202)
            operation(lab, removed['operation']['id'])
            assert not [d for d in domain(vm_id).findall('./devices/disk') if d.find('target').get('bus') == 'scsi']
            lab.passed('Core removed the last SCSI disk from a running guest and reconciled the persistent definition')
        evidence['checks'] = lab.results
        if not base.exists():
            evidence['limits'].append('No guest image was supplied; guest activation and workload performance were not measured')
        else:
            evidence['limits'].extend(['Nested KVM and in-lab virtual bridge only; no physical 10G or NFS benchmark',
                                       'Benchmark arms ran in fixed order, so cache and time drift prevent a causal performance gain claim',
                                       'fio p99 is completion latency; ping p99 is idle round-trip latency, not iperf3 transfer tail latency',
                                       'Benchmark variants directly redefined only the disposable domain while Core agent was stopped; Core XML was restored afterward'])
        evidence['completed'] = True
        save_evidence()
        print(f'Evidence: {evidence_path}', flush=True)
    finally:
        lab.close()
        if vm_id:
            run('virsh', '-c', 'qemu:///system', 'destroy', vm_id) if 'running' in virsh('domstate', vm_id) else None
            virsh('undefine', vm_id)
        shutil.rmtree(lab.path)


if __name__ == '__main__':
    main()
