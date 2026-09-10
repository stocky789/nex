#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Native pool VLAN network checks on a freshly installed disposable lab host.

Runs after the fresh-install deploy and managed browser stages. The register stage uses the
real Controller form to create VLAN 100 on the host's management NIC, verifies the resulting
bridge and ifupdown persistence over pinned SSH, proves refusals change nothing, and creates
and boots one ISO-only guest on the new bridge. The reboot stages prove the bridge returns
after a real host reboot. Passwords are read from private files and never printed.
"""
import argparse
import json
from pathlib import Path
import shlex
import time
import uuid
from fresh_install_lab import ROOT, validate_lab
from fresh_install_host import ssh

NETWORK, VM = 'Servers VLAN 100', 'vlan-guest'
BRIDGE, PORT, UPLINK = 'nex0v100', 'nex0.100', 'nex0'


def host_state(lab):
    out = lambda command: ssh(lab, command, text=True).stdout
    return {'br0_addr': json.loads(out('ip -j -4 addr show br0'))[0]['addr_info'],
            'routes': json.loads(out('ip -j route')),
            'links': sorted(l['ifname'] for l in json.loads(out('ip -j link'))),
            'boot_id': out('cat /proc/sys/kernel/random/boot_id').strip()}


def bridge_state(lab):
    out = lambda command: ssh(lab, command, text=True).stdout
    return {'bridge_ports': out(f'ls /sys/class/net/{BRIDGE}/brif').split(),
            'vlan_device': json.loads(out(f'ip -j -d link show {PORT}'))[0],
            'bridge_link': json.loads(out(f'ip -j link show {BRIDGE}'))[0],
            'stanza': out(f'cat /etc/network/interfaces.d/nex-{BRIDGE}'),
            'source_lines': out("grep -n '^source' /etc/network/interfaces"),
            'ifquery_auto': out('ifquery --list --allow=auto').split()}


def wait_ssh(lab, timeout=300):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            if ssh(lab, 'hostname', text=True).stdout.strip() == 'nex-fresh-lab':
                return
        except Exception:
            pass
        time.sleep(5)
    raise AssertionError('host did not return within timeout')


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--lab-dir', type=Path, required=True)
    p.add_argument('--stage', choices=('register', 'reboot', 'verify-reboot'), required=True)
    p.add_argument('--evidence', type=Path, required=True)
    args = p.parse_args()
    lab = validate_lab(args.lab_dir)
    config = json.loads((lab / 'config.json').read_text())
    args.evidence.mkdir(parents=True, exist_ok=True)
    assert ssh(lab, 'hostname', text=True).stdout.strip() == 'nex-fresh-lab'
    checks = []

    def passed(message):
        checks.append(message)
        print('PASS:', message, flush=True)

    if args.stage == 'reboot':
        state = json.loads((args.evidence / 'native-register-results.json').read_text())
        ssh(lab, shlex.join(['virsh', '-c', 'qemu:///system', 'destroy', state['vm_id']]) + ' 2>/dev/null; true')
        ssh(lab, 'systemctl reboot', text=True)
        print('Requested reboot of the disposable lab host; ISO-only VLAN test guest stopped first')
        return
    if args.stage == 'verify-reboot':
        state = json.loads((args.evidence / 'native-register-results.json').read_text())
        time.sleep(20)
        wait_ssh(lab)
        host = host_state(lab)
        assert host['boot_id'] != state['host_before']['boot_id'], 'no reboot observed'
        assert host['br0_addr'] == state['host_before']['br0_addr'] and host['routes'] == state['host_before']['routes']
        bridge = bridge_state(lab)
        assert bridge['bridge_ports'] == [PORT] and bridge['bridge_link']['operstate'] in ('UP', 'UNKNOWN'), bridge
        assert bridge['vlan_device']['linkinfo']['info_data']['id'] == 100
        virsh = lambda *a: ssh(lab, shlex.join(['virsh', '-c', 'qemu:///system', *a]), text=True).stdout
        assert 'shut off' in virsh('domstate', state['vm_id']) and 'running' in virsh('domstate', state['controller_vm_id'])
        passed('After a real host reboot the VLAN bridge, its 802.1Q port and the management address/routes are back; Controller autostarted and the VLAN guest stays stopped')
        (args.evidence / 'native-reboot-results.json').write_text(json.dumps({'host_after': host, 'bridge_after': bridge, 'checks': checks}, indent=2) + '\n')
        return

    from playwright.sync_api import sync_playwright, expect
    before = host_state(lab)
    assert BRIDGE not in before['links'] and PORT not in before['links']
    result = {'host_before': before, 'refusals': {}}
    with sync_playwright() as pw:
        browser = pw.chromium.launch(headless=True)
        page = browser.new_page(ignore_https_errors=True, viewport={'width': 1440, 'height': 1000})
        errors = []
        page.on('pageerror', lambda error: errors.append(str(error)))
        url = config['controller_url']

        def capture(name):
            page.screenshot(path=str(args.evidence / f'browser-native-{name}.png'), full_page=True, mask=[page.locator('input[type=password]')])

        def api(path, body=None, expected=200):
            headers = {'X-CSRF-Token': csrf, 'Content-Type': 'application/json'}
            response = page.request.post(url + path, data=json.dumps(body), headers=headers) if body is not None else page.request.get(url + path)
            assert response.status == expected, f'{path}: {response.status} {response.text()}'
            return response.json()
        page.goto(url)
        expect(page.get_by_label('Username', exact=True)).to_be_focused()
        page.keyboard.type('root')
        page.keyboard.press('Tab')
        page.get_by_label('Password', exact=True).fill(Path(config['controller_password_file']).read_text())
        page.keyboard.press('Enter')
        expect(page.get_by_role('button', name='Sign out', exact=True)).to_be_visible(timeout=30000)
        csrf = api('/api/v1/session')['csrf_token']
        host = api('/api/v1/hosts')[0]
        pool_id, host_id = host['pool_id'], host['id']
        kinds = {n['name']: n.get('kind') for n in host['inventory']['nics']}
        assert kinds.get(UPLINK) == 'physical' and kinds.get('br0') == 'bridge', kinds
        result['observed_nic_kinds'] = kinds
        passed('Installed host reports NIC kinds: nex0 physical, br0 bridge')

        def pool(tab):
            page.goto(url + '/#/pools/' + pool_id)
            page.get_by_role('navigation', name='Resource views').get_by_role('button', name=tab, exact=True).click()
        pool('Networking')
        page.get_by_role('button', name='Register network', exact=True).click()
        form = page.get_by_role('region', name='Register network', exact=True)
        form.get_by_label('Name', exact=True).fill(NETWORK)
        form.locator('select[name=host_id]').select_option(host_id)
        result['connection_options'] = form.locator('select[name=connection] option').all_text_contents()
        form.get_by_label('Connection').select_option('vlan:' + UPLINK)
        form.get_by_label('VLAN ID').fill('100')
        capture('network-form')
        page.get_by_role('button', name='Register and check access').click()
        expect(page.locator('tr').filter(has_text=NETWORK)).to_contain_text('succeeded', timeout=180000)
        capture('network-table')
        network = next(n for n in api('/api/v1/networks') if n['name'] == NETWORK)
        assert network['kind'] == 'vlan' and network['source'] == BRIDGE and network['vlan'] == 100 and network['uplink'] == UPLINK
        result['network'] = network
        passed('Browser form offered the real NICs and registered VLAN 100 on nex0; the host task succeeded')

        after = host_state(lab)
        assert after['br0_addr'] == before['br0_addr'] and after['routes'] == before['routes'], 'management address or routes changed'
        assert set(after['links']) - set(before['links']) == {BRIDGE, PORT}, after['links']
        bridge = bridge_state(lab)
        assert bridge['bridge_ports'] == [PORT] and bridge['vlan_device']['linkinfo']['info_data']['id'] == 100
        assert f'auto {BRIDGE}' in bridge['stanza'] and BRIDGE in bridge['ifquery_auto'] and 'interfaces.d' in bridge['source_lines']
        result.update({'host_after': after, 'bridge': bridge})
        passed('Host gained exactly nex0.100 and bridge nex0v100 with that port; br0 addresses/routes unchanged; ifupdown stanza sourced and listed by ifquery')

        base = {'pool_id': pool_id, 'host_id': host_id, 'kind': 'vlan', 'uplink': UPLINK, 'vlan': 100}
        result['refusals']['duplicate_vlan'] = api('/api/v1/networks', {**base, 'request_id': str(uuid.uuid4()), 'name': 'Duplicate'}, 409)
        result['refusals']['bridge_as_uplink'] = api('/api/v1/networks', {**base, 'request_id': str(uuid.uuid4()), 'name': 'Bridge uplink', 'uplink': 'br0'}, 409)
        result['refusals']['bad_tag'] = api('/api/v1/networks', {**base, 'request_id': str(uuid.uuid4()), 'name': 'Bad tag', 'vlan': 4095}, 400)
        untagged = api('/api/v1/networks', {**base, 'request_id': str(uuid.uuid4()), 'name': 'Untagged nex0', 'vlan': 0}, 201)
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            untagged = next(n for n in api('/api/v1/networks') if n['id'] == untagged['id'])
            if untagged['status'] in ('failed', 'succeeded', 'needs_attention'):
                break
            time.sleep(2)
        assert untagged['status'] == 'failed' and 'br0' in untagged['error'], untagged
        result['refusals']['untagged_management_nic'] = untagged
        assert host_state(lab)['links'] == after['links'] and bridge_state(lab)['bridge_ports'] == [PORT]
        passed('Controller refused duplicate/bridge/out-of-range requests; Core refused untagged use of the management port without changing any device')

        storage = next(s for s in api('/api/v1/storage') if s['status'] == 'succeeded' and s['host_id'] == host_id)
        media = next(m for m in api('/api/v1/media') if m['status'] == 'succeeded' and m['storage_id'] == storage['id'])
        created = api('/api/v1/vms', {'request_id': str(uuid.uuid4()), 'pool_id': pool_id, 'host_id': host_id, 'name': VM, 'media_id': media['id'],
                                      'cpu': {'count': 1, 'model': 'host-passthrough'}, 'memory': {'mode': 'dedicated', 'size_mib': 1024},
                                      'disks': [{'storage_id': storage['id'], 'name': 'system', 'size_gib': 2, 'bus': 'virtio', 'thin': True, 'boot_order': 2}],
                                      'nics': [{'network_id': network['id'], 'model': 'virtio'}],
                                      'boot': {'firmware': 'bios', 'media_boot_order': 1}}, 202)
        vm_id = created['vm_id']
        deadline = time.monotonic() + 180
        while time.monotonic() < deadline:
            vm = next(v for v in api('/api/v1/vms') if v['id'] == vm_id)
            if vm['status'] in ('failed', 'succeeded', 'needs_attention'):
                break
            time.sleep(3)
        assert vm['status'] == 'succeeded', vm
        virsh = lambda *a: ssh(lab, shlex.join(['virsh', '-c', 'qemu:///system', *a]), text=True).stdout
        xml = virsh('dumpxml', '--inactive', vm_id)
        assert f'<source bridge="{BRIDGE}"/>' in xml and "<interface type='bridge'>" in xml or f'<interface type="bridge">' in xml, xml
        (args.evidence / 'vlan-guest-domain.xml').write_text(xml)
        virsh('start', vm_id)
        time.sleep(12)
        assert 'running' in virsh('domstate', vm_id)
        ports = ssh(lab, f'ls /sys/class/net/{BRIDGE}/brif', text=True).stdout.split()
        assert PORT in ports and any(p.startswith('vnet') for p in ports), ports
        virsh('screenshot', vm_id, '/root/nex-vlan-guest-boot.png')
        (args.evidence / 'vlan-guest-boot.png').write_bytes(ssh(lab, 'cat /root/nex-vlan-guest-boot.png').stdout)
        virsh('destroy', vm_id)
        result.update({'vm_id': vm_id, 'bridge_ports_while_running': ports,
                       'controller_vm_id': next(v['id'] for v in api('/api/v1/vms') if v.get('role') == 'controller')})
        passed('Created a stopped guest on the VLAN network through the API; libvirt attached it to nex0v100, it started with its tap on that bridge, then was stopped')
        page.goto(url + '/#/vms/' + vm_id)
        expect(page.locator('.details')).to_contain_text(NETWORK)
        capture('vlan-vm-details')
        assert not errors, errors
        browser.close()
    result.update({'checks': checks, 'artifacts': config.get('artifacts', {}), 'environment': 'Fresh ISO-installed nested KVM host, packaged Core with embedded agent and bundled Controller; QEMU user networking, so tagged frames leaving the host are not switched'})
    (args.evidence / 'native-register-results.json').write_text(json.dumps(result, indent=2) + '\n')


if __name__ == '__main__':
    main()
