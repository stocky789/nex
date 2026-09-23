#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Native observations/power checks for exact VMs created by fresh-install browser QA.

Only the named fresh-install lab and UUIDs recorded by its completed browser run
are accepted. Controller-service outage is separate from host reboot.
--guest iso-only (default) is an ISO-boot test VM with no installed OS: boot starts it and
reboot stops that exact disposable UUID first. --guest installed is a real installed guest:
boot never changes its power state and reboot leaves it running. At reboot every recorded
guest is snapshotted; one running with autostart enabled must be running again afterwards,
any other must be shut off.
Every written evidence file takes the optional --label suffix and is never overwritten, so
repeated outage runs (for example G4 then G5) keep their own timings.
--stage interrupt-delete (not yet run in a lab) needs --target-vm naming one recorded,
unprotected guest. It waits for a new journal entry for that exact VM to reach --interrupt-at
(wiping: a delete with disks; cleaning-nvram: a UEFI-to-BIOS edit), sends SIGKILL to
nex-core-local, restarts it, and requires the entry to settle as succeeded, or needs_attention
with a reason. Submit the request from the browser after the stage prints that it is waiting.
Only a kill while the entry is still pending is interrupted-delete evidence; the record and PASS
line say which case the kill hit. --hold (wiping only) makes the first disk immutable (chattr +i)
until the service has been killed: the wipe then fails and the entry is already needs_attention
before the kill, so a held run is failed-wipe restart evidence, not an interrupted delete. Run
cleaning-nvram without --hold; its virsh undefine leaves a pending window. The hold is always released.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shlex
import subprocess
import time
import xml.etree.ElementTree as ET
from fresh_install_host import ssh, guest
from fresh_install_lab import validate_lab


def domain_matches(tree, vm):
    """Assert the persistent libvirt definition carries the recorded VM specification."""
    spec = vm['spec']
    assert tree.findtext('uuid') == vm['id'] and tree.findtext('name') == vm['name']
    assert int(tree.findtext('vcpu')) == spec['cpu']['count'] and int(tree.findtext('memory')) == spec['memory']['size_mib'] * 1024
    uefi = tree.find('os').get('firmware') == 'efi' or tree.find('os/loader') is not None
    assert uefi == (spec['boot']['firmware'] == 'uefi'), 'firmware differs from the specification'
    assert (tree.find('devices/tpm') is not None) == (spec['boot'].get('tpm') == '2.0'), 'TPM presence differs from the specification'
    disks = [d.find('source').get('file') for d in tree.findall("devices/disk[@device='disk']")]
    assert len(disks) == len(spec['disks']) and all(re.search(r'/nex-vms/' + vm['id'] + r'/disk-\d+\.qcow2$', d) for d in disks), disks
    assert len(tree.findall('devices/interface')) == len(spec['nics'])
    return disks


JOURNAL_DIR = '/var/lib/nex-core-agent/managed-vms'
INTERRUPT_KINDS = {'wiping': 'vm-delete', 'cleaning-nvram': 'vm-update'}
# Runs on the host: wait for a new journal entry of this kind for this VM at this stage, then kill.
WATCH = r"""
import json, os, pathlib, subprocess, sys, time
root, kind, vm_id, stage, wait = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3], sys.argv[4], float(sys.argv[5])
def entries():
    for path in root.glob('*.json'):
        try:
            yield path, json.loads(path.read_text())
        except (OSError, ValueError):
            pass
old = {path.name for path, _ in entries()}
print('waiting', flush=True)
deadline = time.monotonic() + wait
while time.monotonic() < deadline:
    for path, j in entries():
        c = j.get('command', {})
        target = (c.get('delete') or {}).get('vm_id') if kind == 'vm-delete' else (c.get('vm') or {}).get('id')
        if path.name in old or c.get('kind') != kind or target != vm_id:
            continue
        if j.get('stage') == stage and j['result']['status'] in ('pending', 'needs_attention'):
            subprocess.run(['systemctl', 'kill', '--signal=SIGKILL', 'nex-core-local'], check=True)
            print(json.dumps({'operation_id': c['operation_id'], 'killed_at': time.time(), 'entry': j}), flush=True)
            sys.exit(0)
        if j['result']['status'] not in ('pending', 'needs_attention'):
            sys.exit('entry ' + path.name + ' settled at stage ' + str(j.get('stage')) + ' before ' + stage + ' was observed; not evidence')
    time.sleep(0.005)
sys.exit('no new ' + kind + ' for the target VM reached ' + stage)
"""


def interrupt_target(recorded, target_name, controller_ids):
    """Return the one recorded, unprotected, non-Controller guest named on the command line."""
    assert target_name, '--target-vm is required: interrupt-delete never picks a VM itself'
    matches = [v for v in recorded.values() if v['name'] == target_name]
    assert len(matches) == 1, f'{target_name!r} is not exactly one VM recorded by this browser run'
    vm = matches[0]
    assert not vm.get('protected') and vm.get('role') != 'controller' and vm['id'] not in controller_ids, \
        'refusing to interrupt the Controller appliance or a protected VM'
    return vm


def hold_path(tree, vm, stage):
    """The first managed disk of this VM, which a held wiping stage cannot remove."""
    # A failed cleaning-nvram saves the entry as failed, which the watcher rejects as not evidence.
    assert stage == 'wiping', '--hold applies only to wiping; run cleaning-nvram without it'
    path = tree.find("devices/disk[@device='disk']/source").get('file')
    assert re.search(r'/nex-vms/' + re.escape(vm['id']) + r'/disk-\d+\.qcow2$', path), path
    return path


def interrupt_case(killed):
    """Which evidence a kill is: only a still-pending entry is an interrupted operation."""
    return 'interrupted-pending' if killed['entry']['result']['status'] == 'pending' else 'held-needs-attention'


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--lab-dir', type=Path, required=True)
    p.add_argument('--stage', choices=('boot', 'controller-stop', 'controller-start', 'reboot', 'verify-reboot', 'interrupt-delete'), required=True)
    p.add_argument('--target-vm', help='interrupt-delete only: exact name of the disposable guest to interrupt')
    p.add_argument('--interrupt-at', choices=tuple(INTERRUPT_KINDS), default='wiping')
    p.add_argument('--hold', action='store_true', help='interrupt-delete, wiping only: chattr +i the first disk so the wipe fails (needs_attention) before the kill; not interrupted-delete evidence')
    p.add_argument('--wait', type=float, default=900, help='interrupt-delete: seconds to wait for the request')
    p.add_argument('--evidence', type=Path, required=True, help='evidence directory holding browser-managed-results.json; never the committed docs/qa evidence')
    p.add_argument('--vm-name', default='fresh-install-guest', help='guest recorded by the browser managed stage')
    p.add_argument('--guest', choices=('iso-only', 'installed'), default='iso-only')
    p.add_argument('--label', help='evidence name suffix; reboot and verify-reboot must use the same one')
    args = p.parse_args()
    lab, evidence = validate_lab(args.lab_dir), args.evidence
    def named(name, new=True):
        stem, dot, ext = name.rpartition('.')
        path = evidence / (stem + ('-' + args.label if args.label else '') + dot + ext)
        assert not new or not path.exists(), 'refusing to overwrite existing evidence ' + path.name
        return path
    assert ssh(lab, 'hostname', text=True).stdout.strip() == json.loads((lab / 'config.json').read_text())['hostname']
    browser = json.loads((evidence / 'browser-managed-results.json').read_text())
    # Guests created later by the browser create-vm stage are recorded in their own results.
    recorded = {v['id']: v for path in [evidence / 'browser-managed-results.json', *sorted(evidence.glob('browser-create-vm-*-results.json'))]
                for v in json.loads(path.read_text())['vms']}
    if args.stage == 'interrupt-delete':
        return interrupt_delete(lab, evidence, recorded, args, named)
    assert not args.target_vm, '--target-vm applies only to interrupt-delete'
    vm = next(v for v in recorded.values() if v['name'] == args.vm_name)
    controller = next(v for v in recorded.values() if v.get('role') == 'controller')
    guests = [v for v in recorded.values() if not v.get('protected')]
    vm_id, controller_id = vm['id'], controller['id']
    def virsh(*args):
        return ssh(lab, shlex.join(['virsh', '-c', 'qemu:///system', *args]), text=True).stdout
    def state(uuid):
        # A guest submitted just before an outage may not be defined yet; report it instead of failing.
        try:
            return virsh('domstate', uuid).strip()
        except subprocess.CalledProcessError:
            return 'not defined'
    def autostart(uuid):
        return bool(re.search(r'Autostart:\s+enable', virsh('dominfo', uuid)))
    def disk_info(path):
        info = json.loads(ssh(lab, shlex.join(['qemu-img', 'info', '-U', '--output=json', path]), text=True).stdout)
        return {'virtual_size': info['virtual-size'], 'actual_size': info.get('actual-size'), 'format': info['format'], 'backing': info.get('backing-filename')}
    def identity():
        host = json.loads(ssh(lab, 'cat /var/lib/nex-core-agent/state.json', text=True).stdout)
        return {'host_id': host['host_id'], 'certificate_sha256': hashlib.sha256(host['certificate_pem'].encode()).hexdigest()}
    def boot_id():
        return ssh(lab, 'cat /proc/sys/kernel/random/boot_id', text=True).stdout.strip()
    if args.stage == 'boot':
        outputs = [named(f'{args.vm_name}-domain.xml'), named(f'{args.vm_name}-boot.json')] + ([named('guest-boot.png')] if args.guest == 'iso-only' else [])
        xml = virsh('dumpxml', '--inactive', vm_id)
        tree = ET.fromstring(xml)
        disks = domain_matches(tree, vm)
        infos = [disk_info(d) for d in disks]
        for info, want in zip(infos, vm['spec']['disks']):
            assert info['format'] == 'qcow2' and info['virtual_size'] == want['size_gib'] * 1024**3 and not info['backing'], info
        cd = tree.find("devices/disk[@device='cdrom']/source")
        if cd is not None and cd.get('file') and browser.get('iso_sha256'):
            assert ssh(lab, shlex.join(['sha256sum', cd.get('file')]), text=True).stdout.split()[0] == browser['iso_sha256']
        outputs[0].write_text(xml)
        allocation = ssh(lab, shlex.join(['du', '--block-size=1', *disks]), text=True).stdout
        result = {'vm_id': vm_id, 'controller_vm_id': controller_id, 'disks': [{'path': d, 'thin': w['thin'], **i} for d, i, w in zip(disks, infos, vm['spec']['disks'])],
            'du_bytes': allocation, 'autostart': autostart(vm_id), 'state': state(vm_id), 'domain_sha256': hashlib.sha256(xml.encode()).hexdigest()}
        if args.guest == 'iso-only':
            assert result['state'] == 'shut off'
            virsh('start', vm_id)
            time.sleep(10)
            assert state(vm_id) == 'running'
            virsh('screenshot', vm_id, '/root/nex-fresh-guest-boot.png')
            outputs[2].write_bytes(ssh(lab, 'cat /root/nex-fresh-guest-boot.png').stdout)
        outputs[1].write_text(json.dumps(result, indent=2) + '\n')
        print('PASS: exact GUI-created VM definition, firmware, TPM, disks and NICs match the recorded specification' + ('; local start running; boot screen captured' if args.guest == 'iso-only' else '; power state untouched'))
    elif args.stage in ('controller-stop', 'controller-start'):
        action = 'stop' if args.stage.endswith('stop') else 'start'
        output = named(args.stage + '.json')
        before = time.time()
        guest(lab, controller_id, 'systemctl ' + action + ' nex-controller')
        running = {v['id']: state(v['id']) == 'running' for v in guests}
        assert running[vm_id] and state(controller_id) == 'running', running
        output.write_text(json.dumps({'action': 'systemctl ' + action + ' nex-controller inside bundled appliance via guest agent',
            'requested_at': before, 'completed_at': time.time(), 'selected_vm_id': vm_id, 'guests_running': running,
            'controller_vm_id': controller_id, 'controller_vm_running': True}, indent=2) + '\n')
        print('PASS: Controller service ' + action + '; selected guest and appliance VM processes remain running')
    elif args.stage == 'reboot':
        output = named('recovery-before.json')
        if args.guest == 'iso-only':
            # The guest only reached installer GRUB, so it cannot respond to shutdown.
            # No guest OS/data was installed; stop only this exact test UUID.
            virsh('destroy', vm_id)
        else:
            assert state(vm_id) == 'running' and autostart(vm_id), 'installed guest must be running with autostart before the reboot'
        snapshot = {}
        for v in guests + [controller]:
            xml = virsh('dumpxml', '--inactive', v['id'])
            disks = [d.find('source').get('file') for d in ET.fromstring(xml).findall("devices/disk[@device='disk']")]
            current = state(v['id'])
            snapshot[v['id']] = {'name': v['name'], 'state': current, 'autostart': autostart(v['id']),
                'expect_running': current == 'running' and autostart(v['id']), 'domain_sha256': hashlib.sha256(xml.encode()).hexdigest(),
                'disk_virtual_sizes': {d: disk_info(d)['virtual_size'] for d in disks}}
        # After an external Controller replacement the old appliance is excluded: shut off with autostart disabled.
        app = snapshot[controller_id]
        assert app['expect_running'] or (app['state'] == 'shut off' and not app['autostart']), 'Controller appliance must autostart or be excluded'
        before = {'boot_id': boot_id(), 'vms': snapshot, 'requested_at': time.time(), **identity()}
        output.write_text(json.dumps(before, indent=2) + '\n')
        ssh(lab, 'systemctl reboot', text=True)
        print('Requested reboot of exact freshly installed disposable host; ' + ('ISO-only test guest stopped first' if args.guest == 'iso-only' else 'installed guest left running with autostart'))
    else:
        before = json.loads(named('recovery-before.json', new=False).read_text())
        assert boot_id() != before['boot_id']
        assert identity() == {k: before[k] for k in ('host_id', 'certificate_sha256')}
        observed = {}
        for uuid, want in before['vms'].items():
            deadline = time.monotonic() + 300
            while want['expect_running'] and state(uuid) != 'running' and time.monotonic() < deadline:
                time.sleep(5)
            xml = virsh('dumpxml', '--inactive', uuid)
            observed[uuid] = {'name': want['name'], 'state': state(uuid), 'autostart': autostart(uuid),
                'domain_identical': hashlib.sha256(xml.encode()).hexdigest() == want['domain_sha256'],
                'disk_sizes_unchanged': all(disk_info(d)['virtual_size'] == size for d, size in want['disk_virtual_sizes'].items())}
            assert observed[uuid]['domain_identical'] and observed[uuid]['disk_sizes_unchanged'], observed[uuid]
            assert observed[uuid]['state'] == ('running' if want['expect_running'] else 'shut off'), observed[uuid]
            assert observed[uuid]['autostart'] == want['autostart'], observed[uuid]
        output = ssh(lab, 'systemctl is-active nex-core-local libvirtd.socket; ip -4 -o addr show br0; cat /etc/ssh/sshd_config.d/00-nex-core.conf', text=True).stdout
        named('recovery-after.json').write_text(json.dumps({'host_reboot_observed': True, 'enrolled_identity_unchanged': True,
            'verified_at': time.time(), 'vms': observed, 'installed_services': output}, indent=2) + '\n')
        print('PASS: real host reboot retains enrolled identity and exact domain XML/disks; autostart VMs running, others shut off')


def interrupt_delete(lab, evidence, recorded, args, named):
    controllers = {v['id'] for v in recorded.values() if v.get('role') == 'controller' or v.get('protected')}
    deployment = evidence / 'browser-deployment-state.json'
    if deployment.exists():
        controllers.add(json.loads(deployment.read_text())['operation']['vm_id'])
    vm = interrupt_target(recorded, args.target_vm, controllers)
    output = named(f'interrupt-{args.interrupt_at}-{vm["name"]}.json')
    tree = ET.fromstring(ssh(lab, shlex.join(['virsh', '-c', 'qemu:///system', 'dumpxml', '--inactive', vm['id']]), text=True).stdout)
    assert tree.findtext('uuid') == vm['id'] and tree.findtext('name') == vm['name'], 'libvirt domain is not the named VM'
    held = hold_path(tree, vm, args.interrupt_at) if args.hold else None
    try:
        if held:
            ssh(lab, shlex.join(['chattr', '+i', held]), text=True)
        print(f"Waiting up to {args.wait:.0f} s: submit the request for {vm['name']} now", flush=True)
        watch = ssh(lab, shlex.join(['python3', '-c', WATCH, JOURNAL_DIR, INTERRUPT_KINDS[args.interrupt_at], vm['id'], args.interrupt_at, str(args.wait)]), text=True)
        killed = json.loads(watch.stdout.strip().splitlines()[-1])
    finally:
        if held:
            ssh(lab, shlex.join(['chattr', '-i', held]), text=True)
    ssh(lab, 'systemctl start nex-core-local', text=True)
    journal = f"{JOURNAL_DIR}/{killed['operation_id']}.json"
    before = killed['entry']['result']
    # A held stage may already read needs_attention when killed; only a re-observation after restart counts.
    deadline, entry, reobserved = time.monotonic() + 600, {}, False
    while time.monotonic() < deadline and not reobserved:
        entry = json.loads(ssh(lab, shlex.join(['cat', journal]), text=True).stdout)
        reobserved = entry['result']['status'] != 'pending' and entry['result'] != before
        time.sleep(0 if reobserved else 5)
    result = entry['result']
    case = interrupt_case(killed)
    record = {'vm_id': vm['id'], 'vm_name': vm['name'], 'interrupted_stage': args.interrupt_at, 'held_file': held,
        'killed': killed, 'killed_while_pending': case == 'interrupted-pending', 'case': case, 'settled_at': time.time(), 'settled_stage': entry.get('stage'), 'reobserved_after_restart': reobserved, 'result': result}
    output.write_text(json.dumps(record, indent=2) + '\n')
    assert reobserved, 'entry was not re-observed after restart: ' + json.dumps(result)
    assert result['status'] == 'succeeded' or (result['status'] == 'needs_attention' and result.get('error')), result
    print(f"PASS: nex-core-local killed at {args.interrupt_at} for {vm['name']} ({case}); after restart the entry is {result['status']}")


if __name__ == '__main__':
    main()
