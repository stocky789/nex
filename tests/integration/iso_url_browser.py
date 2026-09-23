#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Real built UI with mocked API responses; no public downloads or real hosts."""
import argparse
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import threading
from playwright.sync_api import sync_playwright, expect

ROOT = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--evidence', type=Path, default=ROOT / '.cache/iso-url-browser')
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer(('127.0.0.1', 0), partial(SimpleHTTPRequestHandler, directory=str(ROOT / 'web/dist')))
    threading.Thread(target=server.serve_forever, daemon=True).start()
    host = {'id': 'host-demo', 'pool_id': 'pool-demo', 'hostname': 'demo-host', 'connection': 'fresh', 'revoked': False, 'capabilities': ['managed-vm-v1', 'iso-download-v1'], 'inventory': None}
    storage = {
        'id': 'storage-demo', 'pool_id': 'pool-demo', 'host_id': host['id'], 'name': 'Guest media',
        'path': '/srv/guest-media', 'status': 'succeeded', 'operation_id': '',
        'capabilities': {'vm_disk': True, 'iso': True, 'thin': True},
        'access': {'status': 'healthy', 'readable': True, 'writable': True, 'observed_at': '2026-09-11T00:00:00Z'},
    }
    data = {'session': {'username': 'operator', 'csrf_token': 'mock-session'}, 'sites': [{'id': 'site-demo', 'name': 'Demo site'}], 'pools': [{'id': 'pool-demo', 'site_id': 'site-demo', 'name': 'Demo pool', 'master_host_id': host['id']}], 'hosts': [host], 'storage': [storage], 'vms': [], 'networks': [], 'media': [], 'operations': [], 'audit': []}
    submissions, errors, checks = [], [], []
    mode = {'failure': True, 'hold': False}
    pending = []
    def route_api(route):
        path = route.request.url.split('/api/v1/')[1].split('?')[0]
        if path == 'media/download':
            submissions.append(route.request.post_data_json)
            if mode['hold']:
                pending.append(route)
                return
            if mode['failure']:
                route.fulfill(status=502, json={'error': 'Simulated uncertain response'})
                return
            data['media'] = [{**storage, 'id': 'iso-demo', 'storage_id': storage['id'], 'name': 'installer.iso', 'status': 'queued', 'size_bytes': 0, 'sha256': ''}]
            route.fulfill(status=201, json=data['media'][0])
        elif path == 'media' and route.request.method == 'POST':
            data['media'] = [{**storage, 'id': 'iso-local', 'storage_id': storage['id'], 'name': 'local.iso', 'status': 'queued', 'size_bytes': 0, 'sha256': ''}]
            route.fulfill(status=201, json=data['media'][0])
        else:
            route.fulfill(json=data.get(path, []))
    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True)
            page = browser.new_page(viewport={'width': 1440, 'height': 1000})
            # The UI probes the session only for a browser that signed in before (BUG-0208).
            page.add_init_script("localStorage.setItem('nex-signed-in', '1')")
            page.on('pageerror', lambda error: errors.append(str(error)))
            page.route('**/api/v1/**', route_api)
            url = f'http://127.0.0.1:{server.server_port}/#/storage'
            def open_iso():
                page.goto(url)
                page.wait_for_load_state('networkidle')
                page.get_by_role('button', name='Upload ISO', exact=True).click()
            open_iso()
            expect(page.get_by_label('ISO file', exact=True)).to_be_visible()
            page.get_by_label('ISO source').select_option('url')
            page.get_by_label('Destination storage').select_option(storage['id'])
            page.get_by_label('Public ISO URL').fill('https://downloads.example.org/installer.iso')
            page.get_by_label('ISO name', exact=True).fill('installer.iso')
            button = page.get_by_role('button', name='Download ISO', exact=True)
            page.get_by_label('ISO name', exact=True).fill('installer.txt')
            button.click()
            assert not submissions
            page.get_by_label('ISO name', exact=True).fill('installer.iso')
            page.get_by_label('Public ISO URL').fill('not a URL')
            button.click()
            assert not submissions
            page.get_by_label('Public ISO URL').fill('https://downloads.example.org/installer.iso')
            mode['hold'] = True
            button.focus()
            page.keyboard.press('Enter')
            expect(page.get_by_role('button', name='Submitting…', exact=True)).to_be_disabled()
            expect(page.get_by_label('ISO source')).to_be_disabled()
            assert len(pending) == 1
            mode['hold'] = False
            pending.pop().fulfill(status=502, json={'error': 'Simulated uncertain response'})
            checks.append('Native URL and ISO name validation; submitting state disables duplicate submission and source changes')
            expect(page.get_by_role('alert')).to_contain_text('Simulated uncertain response')
            page.screenshot(path=str(args.evidence / 'mock-error.png'), full_page=True)
            original = submissions[0]
            page.reload()
            page.wait_for_load_state('networkidle')
            expect(page.get_by_label('Public ISO URL')).to_have_value(original['url'])
            expect(page.get_by_label('ISO name', exact=True)).to_have_value(original['name'])
            mode['failure'] = False
            page.get_by_role('button', name='Download ISO', exact=True).click()
            expect(page.get_by_text('The host download of installer.iso is queued.', exact=False)).to_be_visible()
            assert submissions[-1] == original
            expect(page.get_by_text('Unknown until downloaded')).to_be_visible()
            expect(page.get_by_text('SHA-256: Pending verification')).to_be_visible()
            page.screenshot(path=str(args.evidence / 'mock-queued.png'), full_page=True)
            checks.append('Keyboard submission; uncertain response retained across reload; retry kept request identity; queued size/hash remain unknown')
            host['capabilities'] = ['managed-vm-v1']
            page.reload()
            page.wait_for_load_state('networkidle')
            open_iso()
            page.get_by_label('ISO source').select_option('url')
            page.get_by_label('Destination storage').select_option(storage['id'])
            expect(page.get_by_text('URL downloads need a freshly connected host', exact=False)).to_be_visible()
            expect(page.get_by_role('button', name='Download ISO', exact=True)).to_be_disabled()
            host['capabilities'].append('iso-download-v1')
            host['connection'] = 'stale'
            page.reload()
            open_iso()
            page.get_by_label('ISO source').select_option('url')
            # A stale owner cannot report current ISO access, so its storage is not offered at all.
            expect(page.get_by_label('Destination storage').locator(f'option[value="{storage["id"]}"]')).to_have_count(0)
            expect(page.get_by_text('wait for its assigned Core to report current ISO access', exact=False)).to_be_visible()
            expect(page.get_by_role('button', name='Download ISO', exact=True)).to_be_disabled()
            checks.append('Old Core blocks URL download; stale owner host hides its storage and blocks download')
            host['connection'] = 'fresh'
            data['storage'] = []
            page.reload()
            open_iso()
            expect(page.get_by_text('Register storage and wait', exact=False)).to_be_visible()
            page.get_by_label('ISO source').select_option('url')
            expect(page.get_by_role('button', name='Download ISO', exact=True)).to_be_disabled()
            page.set_viewport_size({'width': 390, 'height': 844})
            page.screenshot(path=str(args.evidence / 'mock-empty-mobile.png'), full_page=True)
            checks.append('Empty destination storage prevents submission; narrow viewport inspected')
            data['storage'] = [storage]
            page.reload()
            open_iso()
            page.get_by_label('Destination storage').select_option(storage['id'])
            page.get_by_label('ISO file', exact=True).set_input_files({'name': 'local.iso', 'mimeType': 'application/x-iso9660-image', 'buffer': bytes(65536)})
            page.get_by_role('button', name='Upload and transfer ISO', exact=True).click()
            expect(page.get_by_text('The host transfer of local.iso is queued.', exact=False)).to_be_visible()
            checks.append('Existing local upload still submits through upload endpoint (mock content validation)')
            assert not errors, errors
            browser.close()
        (args.evidence / 'browser-results.json').write_text(json.dumps({'mode': 'real Chromium UI with fully mocked API; no host/download correctness claim', 'checks': checks, 'page_errors': errors}, indent=2) + '\n')
        print(json.dumps(checks, indent=2))
    finally:
        server.shutdown()


if __name__ == '__main__':
    main()
