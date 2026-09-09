#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Native observations/power checks for exact VMs created by fresh-install browser QA.

Only the named fresh-install lab and UUIDs recorded by its completed browser run
are accepted. Controller-service outage is separate from host reboot. The ordinary
ISO-boot test VM has no installed OS; stop that exact disposable UUID before reboot.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shlex
import time
import xml.etree.ElementTree as ET
from fresh_install_host import ssh, guest
from fresh_install_lab import ROOT, validate_lab


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--lab-dir', type=Path, required=True)
    p.add_argument('--stage', choices=('boot', 'controller-stop', 'controller-start', 'reboot', 'verify-reboot'), required=True)
    p.add_argument('--evidence', type=Path, default=ROOT / 'docs/qa/fresh-install')
    args = p.parse_args()
    lab, evidence = validate_lab(args.lab_dir), args.evidence
    assert ssh(lab, 'hostname', text=True).stdout.strip() == 'nex-fresh-lab'
    config = json.loads((lab / 'config.json').read_text())
    browser = json.loads((evidence / 'browser-managed-results.json').read_text())
    vm = next(v for v in browser['vms'] if v['name'] == 'fresh-install-guest')
    controller = next(v for v in browser['vms'] if v.get('role') == 'controller')
    vm_id, controller_id = vm['id'], controller['id']
    def virsh(*args):
        return ssh(lab, shlex.join(['virsh', '-c', 'qemu:///system', *args]), text=True).stdout
    def identity():
        state = json.loads(ssh(lab, 'cat /var/lib/nex-core-agent/state.json', text=True).stdout)
        return {'host_id': state['host_id'], 'certificate_sha256': hashlib.sha256(state['certificate_pem'].encode()).hexdigest()}
    if args.stage == 'boot':
        xml = virsh('dumpxml', '--inactive', vm_id)
        tree = ET.fromstring(xml)
        assert tree.findtext('uuid') == vm_id and tree.findtext('name') == 'fresh-install-guest'
        assert int(tree.findtext('vcpu')) == 2 and int(tree.findtext('memory')) == 1024 * 1024
        assert tree.find('os/loader') is not None
        assert tree.find("devices/interface[@type='bridge']/source").get('bridge') == 'br0'
        assert [node.get('dev') for node in tree.findall('os/boot')] == ['cdrom', 'hd']
        cd = tree.find("devices/disk[@device='cdrom']/source").get('file')
        disk = tree.find("devices/disk[@device='disk']/source").get('file')
        assert ssh(lab, shlex.join(['sha256sum', cd]), text=True).stdout.split()[0] == browser['iso_sha256']
        disk_info = json.loads(ssh(lab, shlex.join(['qemu-img', 'info', '--output=json', disk]), text=True).stdout)
        assert disk_info['format'] == 'qcow2' and disk_info['virtual-size'] == 4 * 1024**3 and not disk_info.get('backing-filename')
        assert 'shut off' in virsh('domstate', vm_id)
        (evidence / 'guest-domain.xml').write_text(xml)
        virsh('start', vm_id)
        time.sleep(10)
        assert 'running' in virsh('domstate', vm_id)
        virsh('screenshot', vm_id, '/root/nex-fresh-guest-boot.png')
        (evidence / 'guest-boot-uefi.png').write_bytes(ssh(lab, 'cat /root/nex-fresh-guest-boot.png').stdout)
        before = {'vm_id': vm_id, 'controller_vm_id': controller_id, 'disk': disk,
            'disk_virtual_size': disk_info['virtual-size'], 'domain_sha256': hashlib.sha256(xml.encode()).hexdigest(),
            'boot_id': ssh(lab, 'cat /proc/sys/kernel/random/boot_id', text=True).stdout.strip(), **identity()}
        (evidence / 'recovery-before.json').write_text(json.dumps(before, indent=2) + '\n')
        print('PASS: exact GUI-created stopped UEFI VM configuration/media/disk verified; local start running; boot screen captured')
    elif args.stage in ('controller-stop', 'controller-start'):
        action = 'stop' if args.stage.endswith('stop') else 'start'
        guest(lab, controller_id, 'systemctl ' + action + ' nex-controller')
        assert 'running' in virsh('domstate', vm_id)
        assert 'running' in virsh('domstate', controller_id)
        (evidence / (args.stage + '.json')).write_text(json.dumps({'action': 'systemctl ' + action + ' nex-controller inside bundled appliance via guest agent',
            'ordinary_vm_id': vm_id, 'ordinary_vm_running': True, 'controller_vm_id': controller_id, 'controller_vm_running': True}, indent=2) + '\n')
        print('PASS: Controller service ' + action + '; both actual VM processes remain running')
    elif args.stage == 'reboot':
        # The guest only reached installer GRUB, so it cannot respond to shutdown.
        # No guest OS/data was installed; stop only this exact new test UUID.
        virsh('destroy', vm_id)
        assert 'shut off' in virsh('domstate', vm_id)
        ssh(lab, 'systemctl reboot', text=True)
        print('Requested reboot of exact freshly installed disposable host; ordinary ISO-only test guest explicitly stopped first')
    else:
        before = json.loads((evidence / 'recovery-before.json').read_text())
        assert ssh(lab, 'cat /proc/sys/kernel/random/boot_id', text=True).stdout.strip() != before['boot_id']
        assert identity() == {k: before[k] for k in ('host_id', 'certificate_sha256')}
        xml = virsh('dumpxml', '--inactive', vm_id)
        assert hashlib.sha256(xml.encode()).hexdigest() == before['domain_sha256']
        info = json.loads(ssh(lab, shlex.join(['qemu-img', 'info', '--output=json', before['disk']]), text=True).stdout)
        assert info['virtual-size'] == before['disk_virtual_size']
        assert 'shut off' in virsh('domstate', vm_id)
        assert 'running' in virsh('domstate', controller_id)
        output = ssh(lab, 'systemctl is-active nex-core-local libvirtd; ip -4 -o addr show br0; cat /etc/ssh/sshd_config.d/00-nex-core.conf', text=True).stdout
        (evidence / 'recovery-after.json').write_text(json.dumps({'host_reboot_observed': True, 'enrolled_identity_unchanged': True,
            'persistent_domain_xml_identical': True, 'disk_virtual_size_unchanged': True, 'ordinary_vm_stopped': True,
            'controller_appliance_autostart_running': True, 'installed_services': output}, indent=2) + '\n')
        print('PASS: real host reboot retains enrolled identity, exact domain XML/disk, stopped guest and running autostart Controller')


if __name__ == '__main__':
    main()
