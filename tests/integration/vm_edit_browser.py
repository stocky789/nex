#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Browser walkthrough of editing a VM in place on its panel's Configuration tab against a simulated host.

The VM and its references are created through the API so the walkthrough proves the editor
alone: an edit while stopped (rename, description, a new disk out of the boot order, a new
interface, an in-place validation refusal), and an edit while running (stop-first fields locked
with their reason, a device removal saved). The simulated host applies every revision and
reports it as current, which is what the details follow; no libvirt runs here.
"""
import argparse
import json
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from phase1 import Lab, ROOT, eventually  # noqa: E402
from managed_vm_browser import simulated  # noqa: E402


def setup(lab, config):
    pool, host = config['pool_id'], config['host_id']
    network = lab.api('/api/v1/networks', {'request_id': 'edit-network', 'pool_id': pool, 'host_id': host,
                                           'name': 'Edit guest network', 'kind': 'bridge', 'source': 'br0'}, expected=201)
    eventually(lambda: any(n['id'] == network['id'] and n['status'] == 'succeeded' for n in lab.api('/api/v1/networks')) or None, 'network verified')
    storage = lab.api('/api/v1/storage', {'request_id': 'edit-storage', 'pool_id': pool, 'host_id': host,
                                          'name': 'Edit storage', 'path': config['storage_path']}, expected=201)
    eventually(lambda: any(s['id'] == storage['id'] and s['status'] == 'succeeded' for s in lab.api('/api/v1/storage')) or None, 'storage verified')
    spec = {'request_id': 'edit-vm', 'pool_id': pool, 'host_id': host, 'name': 'edit-vm', 'description': 'created for the edit walkthrough', 'guest_os': 'linux',
            'cpu': {'count': 2, 'sockets': 0, 'cores': 0, 'threads': 0, 'model': 'host-passthrough'}, 'memory': {'mode': 'dedicated', 'size_mib': 1024},
            'disks': [{'storage_id': storage['id'], 'name': 'system', 'size_gib': 10, 'bus': 'virtio', 'thin': True, 'boot_order': 1}],
            'nics': [{'network_id': network['id'], 'model': 'virtio', 'mac': ''}],
            'boot': {'firmware': 'uefi', 'machine': 'q35', 'media_boot_order': 0}, 'media_id': '', 'start_after_create': False, 'autostart': False}
    created = lab.api('/api/v1/vms', spec, expected=202)
    vm = eventually(lambda: next((v for v in lab.api('/api/v1/vms') if v['id'] == created['vm_id'] and v['status'] == 'succeeded' and v['state'] == 'shut off'), None), 'VM created and observed stopped', timeout=30)
    return vm


def walkthrough(lab, config, password, evidence):
    from playwright.sync_api import sync_playwright, expect
    checks, captures = [], []
    vm = setup(lab, config)
    url = config['url']

    def passed(message):
        checks.append(message)
        print('PASS:', message, flush=True)

    def current():
        return next(v for v in lab.api('/api/v1/vms') if v['id'] == vm['id'])

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        page = browser.new_page(ignore_https_errors=True, viewport={'width': 1440, 'height': 1000})
        errors = []
        page.on('pageerror', lambda error: errors.append(str(error)))

        def capture(name):
            filename = 'vm-edit-' + name + '.png'
            page.screenshot(path=str(evidence / filename), full_page=True)
            captures.append(filename)

        page.goto(url)
        page.get_by_label('Username').fill('root')
        page.get_by_label('Password', exact=True).fill(password)
        page.keyboard.press('Enter')
        expect(page.get_by_role('navigation', name='Sections', exact=True)).to_be_visible()

        def edit(vm_id=None):
            if vm_id:
                page.goto(url + '/#/vms/' + vm_id)
            page.get_by_role('tab', name='Configuration', exact=True).click()
            page.get_by_role('button', name='Edit hardware', exact=True).click()

        definition = page.get_by_role('region', name='VM definition', exact=True)
        page.goto(url + '/#/vms/' + vm['id'])
        page.get_by_role('tab', name='Configuration', exact=True).click()
        expect(definition).to_contain_text('CD drive empty')
        capture('details-before')

        # Edit while stopped.
        page.get_by_role('button', name='Edit hardware', exact=True).click()
        editor = page.get_by_role('form', name='Edit VM')
        expect(editor.get_by_text('every field can be changed', exact=False)).to_be_visible()
        expect(editor.get_by_label('Name', exact=True)).to_be_focused()
        cpu = editor.get_by_role('combobox', name='CPU model', exact=True)
        expect(cpu.locator('option[value="__pool-baseline"]')).to_contain_text('(Recommended)', timeout=30000)
        expect(editor.get_by_text('Fixed features:', exact=False)).to_have_count(0)
        cpu.scroll_into_view_if_needed()
        capture('cpu-choice')
        editor.get_by_label('Name', exact=True).fill('edit-vm-renamed')
        editor.get_by_label('Description').fill('edited while stopped')
        editor.get_by_role('button', name='Add disk', exact=True).click()
        disk2 = editor.get_by_role('group', name='Disk 2 · new', exact=True)
        expect(disk2.get_by_label('Storage')).to_be_enabled()
        expect(editor.get_by_role('group', name='Disk 1', exact=True).get_by_label('Size (GiB)')).to_be_disabled()
        editor.get_by_role('button', name='Add network interface', exact=True).click()
        nic1 = editor.get_by_role('group', name='Network interface 1', exact=True)
        expect(nic1.get_by_label('MAC address')).to_have_value(vm['spec']['nics'][0]['mac'])
        # A repeated boot order is refused in place and blocks Save until corrected.
        disk2.get_by_label('Boot order').fill('1')
        expect(editor.locator('.task-error').filter(has_text='Boot order values must be unique')).to_be_visible()
        expect(editor.get_by_role('button', name='Save changes', exact=True)).to_be_disabled()
        disk2.get_by_label('Boot order').fill('0')
        expect(editor.get_by_role('button', name='Save changes', exact=True)).to_be_enabled()
        # UEFI to BIOS deletes the variable store: warned in place, Save held until acknowledged.
        editor.get_by_label('Firmware').select_option('bios')
        expect(editor.get_by_text('permanently deletes its UEFI variable store', exact=False)).to_be_visible()
        expect(editor.get_by_role('button', name='Save changes', exact=True)).to_be_disabled()
        editor.get_by_text('permanently deletes its UEFI variable store', exact=False).scroll_into_view_if_needed()
        capture('firmware-bios-warning')
        editor.get_by_label('I understand the UEFI variable store will be deleted').check()
        expect(editor.get_by_role('button', name='Save changes', exact=True)).to_be_enabled()
        editor.get_by_label('Firmware').select_option('uefi')
        expect(editor.get_by_text('permanently deletes its UEFI variable store', exact=False)).to_have_count(0)
        page.set_viewport_size({'width': 360, 'height': 800})
        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), 'editor narrow page overflow'
        capture('edit-stopped-360')
        page.set_viewport_size({'width': 1440, 'height': 1000})
        capture('edit-stopped')
        editor.get_by_role('button', name='Save changes', exact=True).click()
        expect(page.get_by_role('status')).to_contain_text('requested')
        edited = eventually(lambda: (lambda v: v if v['spec']['description'] == 'edited while stopped' else None)(current()), 'first revision reported by the host')
        assert edited['name'] == 'edit-vm-renamed' and edited['spec']['revision'] == 1, edited
        assert [d['name'] for d in edited['spec']['disks']] == ['system', 'simulated-browser-host-edit-vm-2'] and edited['spec']['disks'][1]['boot_order'] == 0, edited['spec']
        assert len(edited['spec']['nics']) == 2 and all(n['mac'] for n in edited['spec']['nics']) and edited['spec']['nics'][0]['mac'] == vm['spec']['nics'][0]['mac'], edited['spec']
        page.reload()
        page.get_by_role('tab', name='Configuration', exact=True).click()
        expect(definition).to_contain_text('edited while stopped', timeout=30000)
        expect(page.get_by_role('region', name='edit-vm-renamed', exact=True)).to_be_visible()
        capture('details-after-stopped-edit')
        ops = [o for o in lab.api('/api/v1/operations') if o['kind'] == 'vm-update']
        assert len(ops) == 1 and ops[0]['status'] == 'succeeded' and ops[0]['actor'] == 'root', ops
        passed('Edit while stopped from Configuration: UEFI to BIOS warns that the variable store is deleted and holds Save until acknowledged; rename, description, second disk out of the boot order and second interface saved; repeated boot order refused in place; existing disk size locked; first interface keeps its MAC; vm-update succeeded and details follow the host report; 360px editor without overflow')

        # Edit while running: stop-first fields are locked with the reason; a device change goes through.
        config['control']['state'] = 'running'
        eventually(lambda: current()['state'] == 'running' or None, 'running observed')
        page.reload()
        edit()
        editor = page.get_by_role('form', name='Edit VM')
        expect(editor.get_by_text('The VM is running', exact=False)).to_be_visible()
        for label in ('Name', 'Virtual CPUs', 'Memory (MiB)'):
            expect(editor.get_by_label(label, exact=True)).to_be_disabled()
        # A wrapped select's accessible name includes its option text, so Firmware is matched loosely.
        expect(editor.get_by_label('Firmware')).to_be_disabled()
        assert editor.get_by_label('Name', exact=True).get_attribute('title') == 'Stop the VM to change this.'
        expect(editor.get_by_label('Description')).to_be_enabled()
        expect(editor.get_by_role('group', name='Disk 1', exact=True).get_by_role('button', name='Remove disk 1')).to_be_disabled()
        editor.get_by_role('button', name='Add disk', exact=True).click()
        expect(editor.get_by_role('group', name='Disk 3 · new', exact=True).get_by_label('Bus').locator('option[value="sata"]')).to_be_disabled()
        editor.get_by_role('group', name='Disk 3 · new', exact=True).get_by_role('button', name='Remove disk 3').click()
        editor.get_by_role('button', name='Remove network interface 2', exact=True).click()
        capture('edit-running')
        editor.get_by_role('button', name='Save changes', exact=True).click()
        expect(page.get_by_role('status')).to_contain_text('requested')
        second = eventually(lambda: (lambda v: v if v['spec']['revision'] == 2 else None)(current()), 'second revision reported by the host')
        assert len(second['spec']['nics']) == 1 and second['state'] == 'running', second
        passed('Edit while running: name, CPU, memory and firmware locked with the stop-first reason, SATA refused for a new disk, boot disk removal locked, description open; interface removal saved as revision 2 while the VM stayed running')

        # A pending edit is refused honestly: the second submission is blocked until the task ends.
        config['control']['state'] = 'shut off'
        eventually(lambda: current()['state'] == 'shut off' or None, 'stopped observed')
        page.reload()
        page.route('**/api/v1/agent/report', lambda route: route.abort())
        edit()
        editor = page.get_by_role('form', name='Edit VM')
        editor.get_by_label('Description').fill('third')
        editor.get_by_role('button', name='Save changes', exact=True).click()
        expect(page.get_by_role('status')).to_contain_text('requested')
        page.unroute('**/api/v1/agent/report')
        eventually(lambda: (lambda v: v if v['spec']['revision'] == 3 else None)(current()), 'third revision reported by the host')

        # A never-edited VM with two automatic interfaces: removing the first and adding a new one in
        # the same save names the survivor by the MAC the listing reports, so the Controller accepts
        # it, the survivor keeps its address and the new interface gets a fresh one.
        spec = {k: vm['spec'][k] for k in ('pool_id', 'host_id', 'description', 'guest_os', 'cpu', 'memory', 'boot', 'media_id', 'autostart')}
        network = vm['spec']['nics'][0]['network_id']
        spec.update(request_id='edit-vm-nics', name='edit-vm-nics', start_after_create=False,
                    disks=[{**vm['spec']['disks'][0], 'name': 'nics-system'}],
                    nics=[{'network_id': network, 'model': 'virtio', 'mac': ''}, {'network_id': network, 'model': 'e1000e', 'mac': ''}])
        created = lab.api('/api/v1/vms', spec, expected=202)
        fresh = eventually(lambda: next((v for v in lab.api('/api/v1/vms') if v['id'] == created['vm_id'] and v['status'] == 'succeeded' and v['state'] == 'shut off'), None), 'two-interface VM created and observed stopped', timeout=30)
        assert fresh['spec']['revision'] == 0 and all(n['mac'] for n in fresh['spec']['nics']), fresh['spec']
        edit(fresh['id'])
        editor = page.get_by_role('form', name='Edit VM')
        expect(editor.get_by_role('group', name='Network interface 2', exact=True).get_by_label('MAC address')).to_have_value(fresh['spec']['nics'][1]['mac'])
        editor.get_by_role('button', name='Remove network interface 1', exact=True).click()
        expect(editor.get_by_role('group', name='Network interface 1', exact=True).get_by_label('MAC address')).to_have_value(fresh['spec']['nics'][1]['mac'])
        editor.get_by_role('button', name='Add network interface', exact=True).click()
        editor.get_by_role('group', name='Network interface 2 · new', exact=True).get_by_label('Adapter model').select_option('rtl8139')
        capture('remove-first-nic-never-edited')
        editor.get_by_role('button', name='Save changes', exact=True).click()
        expect(page.get_by_role('status')).to_contain_text('requested')
        dropped = eventually(lambda: next((v for v in lab.api('/api/v1/vms') if v['id'] == fresh['id'] and v['spec']['revision'] == 1), None), 'first-interface removal reported by the host')
        nics = dropped['spec']['nics']
        assert [(n['mac'], n['model']) for n in nics[:1]] == [(fresh['spec']['nics'][1]['mac'], 'e1000e')] and len(nics) == 2, dropped['spec']
        assert nics[1]['model'] == 'rtl8139' and nics[1]['mac'] not in {n['mac'] for n in fresh['spec']['nics']}, dropped['spec']
        page.reload()
        page.get_by_role('tab', name='Configuration', exact=True).click()
        expect(definition).to_contain_text(fresh['spec']['nics'][1]['mac'], timeout=30000)
        capture('remove-first-nic-details')
        passed('Never-edited VM with two automatic interfaces: the editor shows both listed MACs, removing interface 1 and adding an rtl8139 interface in one save is accepted, and the host-reported revision 1 keeps the second interface with its original MAC plus the new interface with a fresh MAC')
        assert not errors, errors
        passed('Third edit queued and applied; no browser page errors across the walkthrough')
        browser.close()
    return {'mode': 'simulated', 'environment': 'Chromium; real HTTPS Controller; simulated mTLS agent that applies every vm-update and reports it as current; no libvirt execution',
            'checks': checks, 'captures': captures, 'vm': current(),
            'limits': ['Simulated host only: no libvirt redefine, hot-plug, media change or disk allocation executed', 'Pending-edit refusal (409) exercised in Go tests, not in the browser']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--evidence', type=Path, default=ROOT / '.cache/vm-edit-browser')
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='nex-vm-edit-browser.') as tmp:
        lab = Lab(Path(tmp))
        stop = thread = None
        try:
            config, password, _iso, stop, thread, failures = simulated(lab)
            result = walkthrough(lab, config, password, args.evidence)
            assert not failures, failures
        finally:
            if stop:
                stop.set()
                thread.join(timeout=10)
            lab.close()
    (args.evidence / 'results.json').write_text(json.dumps(result, indent=2) + '\n')


if __name__ == '__main__':
    main()
