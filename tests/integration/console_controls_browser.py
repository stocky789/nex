#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Built UI regression with simulated API/RFB; no real host or image-quality claim."""
import argparse
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from datetime import datetime, timedelta, timezone
from pathlib import Path
import struct
import threading
from playwright.sync_api import sync_playwright, expect

ROOT = Path(__file__).resolve().parents[2]
POLL = 15000  # ms: the UI re-reads inventory every 10 s


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--evidence', type=Path, default=ROOT / '.cache/console-controls-browser')
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer(('127.0.0.1', 0), partial(SimpleHTTPRequestHandler, directory=str(ROOT / 'web/dist')))
    threading.Thread(target=server.serve_forever, daemon=True).start()
    host = {'id': 'host-demo', 'pool_id': 'pool-demo', 'hostname': 'demo-host', 'connection': 'fresh', 'revoked': False, 'capabilities': ['console-v1', 'host-console-v1', 'vm-media-v1'], 'inventory': None}
    vm = {'id': 'vm-demo', 'pool_id': 'pool-demo', 'host_id': host['id'], 'name': 'Demo VM', 'operation_id': '', 'status': 'succeeded', 'state': 'running', 'freshness': 'fresh', 'role': '', 'protected': False, 'spec': {'media_id': 'iso-demo', 'cpu': {'count': 2, 'model': 'host'}, 'memory': {'size_mib': 2048, 'mode': 'dedicated'}, 'boot': {'firmware': 'uefi'}, 'disks': [], 'nics': []}, 'guest': {'agent': 'ready', 'clipboard': 'ready', 'resize': True}}
    data = {'session': {'username': 'operator', 'csrf_token': 'mock-session'}, 'sites': [{'id': 'site-demo', 'name': 'Demo site'}], 'pools': [{'id': 'pool-demo', 'site_id': 'site-demo', 'name': 'Demo pool', 'master_host_id': host['id']}], 'hosts': [host], 'vms': [vm], 'storage': [], 'networks': [], 'media': [{'id': 'iso-demo', 'host_id': host['id'], 'pool_id': 'pool-demo', 'name': 'demo-installer.iso', 'status': 'succeeded'}], 'operations': [], 'audit': []}
    errors, checks, tickets = [], [], []
    mode = {'failure': False, 'stats': 'ready'}
    live_requests = []

    def route_api(route):
        path = route.request.url.split('/api/v1/')[1].split('?')[0]
        if path.endswith('/stats'):
            live = 'range=live' in route.request.url
            if live:
                live_requests.append(route.request.url)
                if mode['stats'] == 'error':
                    route.fulfill(status=503, json={'error': 'Simulated stats unavailable'})
                    return
            route.fulfill(json={'object': 'vm' if path.startswith('vms/') else 'host', 'id': path.split('/')[1],
                'range': 'live' if live else '10m', 'step_seconds': 1 if live else 10,
                'start': (datetime.now(timezone.utc) - timedelta(seconds=120)).isoformat(),
                'series': [{'key': 'cpu.vcpu.0', 'group': 'cpu', 'label': 'CPU', 'unit': 'percent', 'values': [None] * 120 if live and mode['stats'] == 'collecting' else [25, 50] * 60}]})
        elif path in ('vms/vm-demo/console', 'hosts/host-demo/console'):
            tickets.append(path)
            route.fulfill(status=502 if mode['failure'] else 200, json={'error': 'Simulated console unavailable'} if mode['failure'] else {'websocket': '/mock-console'})
        elif path == 'vms/vm-demo/media' and route.request.method == 'POST':
            request = route.request.post_data_json
            operation = {'id': f'media-{request["request_id"]}', 'host_id': host['id'], 'kind': 'vm-media', 'request_id': request['request_id'], 'status': 'queued', 'created_at': '2026-09-11T00:00:00Z', 'updated_at': '2026-09-11T00:00:00Z', 'actor': 'operator'}
            data['operations'].append(operation)
            route.fulfill(status=202, json={'vm_id': vm['id'], 'action': request['action'], 'media_id': request.get('media_id', ''), 'operation': operation})
        else:
            route.fulfill(json=data.get(path, []))

    def route_console(ws):
        # Minimal RFB 3.8 handshake, exercising real noVNC controls without guest pixels.
        state = {'step': 0}
        def receive(message):
            step = state['step']
            state['step'] += 1
            if step == 0:
                ws.send(bytes([1, 1]))  # One security type: None.
            elif step == 1:
                ws.send(bytes(4))
            elif step == 2:
                pixel_format = struct.pack('>BBBBHHHBBB3x', 32, 24, 0, 1, 255, 255, 255, 16, 8, 0)
                name = b'Simulated framebuffer'
                ws.send(struct.pack('>HH', 1600, 900) + pixel_format + struct.pack('>I', len(name)) + name)
        ws.on_message(receive)
        ws.send(b'RFB 003.008\n')

    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True, executable_path=os.environ.get('PLAYWRIGHT_CHROMIUM_EXECUTABLE'))
            page = browser.new_page(viewport={'width': 1280, 'height': 900})
            # Match the returning signed-in browser whose session the API fixture supplies.
            page.add_init_script("localStorage.setItem('nex-signed-in', '1')")
            page.on('pageerror', lambda error: errors.append(str(error)))
            page.route('**/api/v1/**', route_api)
            page.route_web_socket('**/mock-console', route_console)
            # Playwright's WebSocket mock inherits methods one level deeper than noVNC accepts.
            page.add_init_script("""window.WebSocket = new Proxy(window.WebSocket, {
                construct(Target, args) {
                    const socket = new Target(...args);
                    for (const name of ['send', 'close', 'binaryType', 'onerror', 'onmessage', 'onopen', 'protocol', 'readyState']) {
                        let proto = socket;
                        while (!Object.hasOwn(proto, name)) proto = Object.getPrototypeOf(proto);
                        const descriptor = Object.getOwnPropertyDescriptor(proto, name);
                        Object.defineProperty(socket, name, {...descriptor, enumerable: true});
                    }
                    return socket;
                }
            });""")
            url = f'http://127.0.0.1:{server.server_port}/#/console/vm-demo'
            page.goto(url)
            controls = page.get_by_role('toolbar', name='Console controls')
            expect(controls).to_be_visible()
            expect(page.get_by_label('Image quality', exact=True)).to_have_count(0)
            expect(page.locator('.console-screen')).to_have_attribute('data-phase', 'connected')
            expect(page.locator('canvas')).to_be_visible(timeout=5000)
            for quality in ('low', 'high'):
                page.evaluate('(quality) => localStorage.setItem("nex-console-prefs", JSON.stringify({view:"actual", quality}))', quality)
                page.reload()
                expect(page.locator('.console-screen')).to_have_attribute('data-phase', 'connected')
                expect(page.get_by_label('Image quality', exact=True)).to_have_count(0)
                expect(page.get_by_label('Display scaling')).to_have_value('actual')
                expect(page.locator('.console-screen')).to_have_attribute('data-view', 'actual')
                expect(page.locator('canvas')).to_have_attribute('width', '1600')
                page.get_by_label('Display scaling').select_option('fit')
                expect(page.get_by_label('Display scaling')).to_have_value('fit')
                assert page.evaluate('JSON.parse(localStorage.getItem("nex-console-prefs"))') == {'view': 'fit'}
                page.reload()
                expect(page.get_by_label('Display scaling')).to_have_value('fit')
                expect(page.locator('.console-screen')).to_have_attribute('data-phase', 'connected')
            checks.append('Old low/high quality preferences have no control; scaling change persists across reload and drops obsolete quality')
            # The VM panel's Console quick action opens this full-screen console page.
            page.goto(url.replace('#/console/', '#/vms/'))
            page.get_by_role('button', name='Console', exact=True).click()
            expect(page).to_have_url(url)
            expect(page.locator('.console-screen')).to_have_attribute('data-phase', 'connected')
            # Collapse all toolbar content without replacing the live noVNC canvas. The embedded
            # console is the host panel's Console tab; VM consoles open only as the page above.
            for embedded in (False, True):
                if embedded:
                    page.goto(url.replace('#/console/vm-demo', '#/hosts/host-demo'))
                    page.get_by_role('tab', name='Console', exact=True).click()
                    expect(page.locator('.console-screen')).to_have_attribute('data-phase', 'connected')
                page.get_by_role('button', name='Clipboard', exact=True).click()
                draft = page.get_by_role('textbox', name='Text to send to the VM', exact=True)
                draft.fill('Draft retained')
                page.get_by_label('View only', exact=True).check()
                before = page.locator('.console-screen').bounding_box()['height']
                previous = len(tickets)
                page.locator('canvas').evaluate('(canvas) => window.originalCanvas = canvas')
                toggle = page.get_by_role('button', name='Collapse console toolbar')
                toggle.focus()
                toggle.press('Enter')
                toggle = page.get_by_role('button', name='Expand console toolbar')
                expect(toggle).to_have_attribute('aria-expanded', 'false')
                expect(controls).to_be_hidden()
                expect(draft).to_be_hidden()
                assert page.locator('.console-screen').bounding_box()['height'] > before
                assert page.locator('canvas').evaluate('(canvas) => canvas === window.originalCanvas')
                assert len(tickets) == previous
                page.screenshot(path=str(args.evidence / f'mock-collapsed-{embedded}.png'), full_page=True)
                toggle.press('Space')
                expect(controls).to_be_visible()
                expect(draft).to_have_value('Draft retained')
                expect(page.get_by_label('View only', exact=True)).to_be_checked()
                page.get_by_role('button', name='Clipboard', exact=True).click()
                page.get_by_label('View only', exact=True).uncheck()
            checks.append('Panel Console action opens the console page; console page and embedded host console collapse grow screen; Enter/Space restore controls, draft and view-only without reconnect or canvas replacement')
            # Live charts share the console page and leave the existing RFB session intact.
            page.goto(url)
            expect(page.locator('.console-screen')).to_have_attribute('data-phase', 'connected')
            drawer = page.locator('.stats-drawer')
            expect(drawer).to_have_attribute('aria-expanded', 'false')
            before = len(live_requests)
            page.wait_for_timeout(1200)
            assert len(live_requests) == before, 'collapsed console stats polled'
            drawer.focus()
            drawer.press('Enter')
            page.wait_for_timeout(1200)
            assert len(live_requests) == before, 'expanded but disabled console stats polled'
            canvas = page.locator('.console-screen canvas')
            canvas.evaluate('(canvas) => window.originalCanvas = canvas')
            ticket_count = len(tickets)
            mode['stats'] = 'collecting'
            page.get_by_label('Show realtime stats (1-second samples)').check()
            expect(page.get_by_text('Collecting realtime samples…', exact=True)).to_be_visible()
            expect(page.locator('.stats-grid-live canvas')).to_have_count(0)
            mode['stats'] = 'ready'
            expect(page.locator('.stats-grid-live canvas').first).to_be_visible()
            page.wait_for_timeout(1200)
            assert len(live_requests) >= before + 2
            page.get_by_label('Stacked', exact=True).check()
            expect(page.get_by_label('Stacked', exact=True)).to_be_checked()
            mode['stats'] = 'error'
            expect(page.get_by_role('alert').filter(has_text='Simulated stats unavailable')).to_be_visible()
            expect(page.locator('.stats-grid-live canvas').first).to_be_visible()
            mode['stats'] = 'ready'
            page.get_by_role('button', name='Retry', exact=True).click()
            expect(page.get_by_role('alert').filter(has_text='Simulated stats unavailable')).to_have_count(0)
            expect(page.locator('.stats-live')).to_be_visible()
            assert canvas.evaluate('(canvas) => canvas === window.originalCanvas')
            assert len(tickets) == ticket_count, 'opening stats reconnected console'
            page.locator('.stats-realtime').evaluate('(el) => el.scrollTop = 0')
            screen_box, stats_box = page.locator('.console-screen').bounding_box(), drawer.bounding_box()
            page.screenshot(path=str(args.evidence / 'mock-console-realtime.png'), full_page=True)
            assert stats_box['y'] >= screen_box['y'] + screen_box['height'] - 1, ('stats must follow console', screen_box, stats_box)
            chart_box = page.locator('.stats-grid-live figure').first.bounding_box()
            drawer_box = page.locator('.stats-realtime').bounding_box()
            assert chart_box['y'] + chart_box['height'] <= drawer_box['y'] + drawer_box['height'] + 1, 'desktop realtime chart or legend clipped'
            assert screen_box['height'] >= 80, 'realtime hid the console'
            console_box = page.locator('section.console').bounding_box()
            assert screen_box['y'] + screen_box['height'] <= console_box['y'] + console_box['height'] + 1, 'console viewport clipped'
            page.set_viewport_size({'width': 390, 'height': 844})
            expect(page.locator('.stats-grid-live canvas').first).to_be_visible()
            assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), 'console stats overflow'
            page.screenshot(path=str(args.evidence / 'mock-console-realtime-mobile.png'), full_page=True)
            drawer.click()
            before = len(live_requests)
            page.wait_for_timeout(1600)
            assert len(live_requests) == before, 'closing console stats kept polling'
            drawer.click()
            page.get_by_label('Show realtime stats (1-second samples)').check()
            expect(page.locator('.stats-grid-live canvas').first).to_be_visible()
            page.goto(url.replace('#/console/', '#/vms/'))
            page.get_by_role('tab', name='Performance', exact=True).click()
            expect(page.locator('.stats-drawer')).to_have_count(0)
            before = len(live_requests)
            page.wait_for_timeout(1600)
            assert len(live_requests) == before, 'leaving console kept polling'
            page.set_viewport_size({'width': 1280, 'height': 900})
            page.goto(url.replace('#/console/vm-demo', '#/hosts/host-demo'))
            page.get_by_role('tab', name='Performance', exact=True).click()
            expect(page.locator('.stats-drawer')).to_be_visible()
            checks.append('Console page realtime stays off by default, renders loading/live/error/retry and stacked charts below the unchanged RFB canvas, fits mobile, and stops polling on collapse/navigation; VM panel Performance is history-only and host Performance retains realtime')
            page.goto(url)
            expect(page.locator('.console-screen')).to_have_attribute('data-phase', 'connected')
            # The mounted console picks changes up from the 10 s inventory poll (there is no
            # refresh button); a page reload would hide the stale-note bug.
            attach = page.get_by_label('Attach ISO', exact=True)
            expect(attach.locator('option:checked')).to_have_text('demo-installer.iso')
            for action, final_status in [('eject', 'succeeded'), ('insert', 'succeeded'), ('eject', 'failed'), ('eject', 'needs_attention')]:
                if action == 'eject':
                    page.get_by_role('button', name='Eject', exact=True).click()
                else:
                    attach.select_option('iso-demo')
                pending = page.get_by_role('status').filter(has_text='Ejecting the install media…' if action == 'eject' else 'Inserting the install media…')
                expect(pending).to_be_visible()
                operation = data['operations'][-1]
                operation['status'] = final_status
                if final_status == 'succeeded':
                    vm['spec']['media_id'] = '' if action == 'eject' else 'iso-demo'
                else:
                    operation['error'] = 'Simulated drive is locked'
                expect(pending).to_have_count(0, timeout=POLL)
                expect(attach.locator('option:checked')).to_have_text('Empty' if action == 'eject' and final_status == 'succeeded' else 'demo-installer.iso', timeout=POLL)
                if final_status != 'succeeded':
                    expect(page.locator('.console-status').filter(has_text=operation['error'])).to_be_visible()
                page.screenshot(path=str(args.evidence / f'mock-media-{action}-{final_status}.png'), full_page=True)
                # Recent history can evict the task; later host changes must not revive its note.
                data['operations'].clear()
                if final_status == 'succeeded':
                    vm['spec']['media_id'] = 'iso-demo' if action == 'eject' else ''
                page.wait_for_timeout(POLL)  # one poll after the eviction
                expect(pending).to_have_count(0)
                if final_status != 'succeeded':
                    expect(page.locator('.console-status').filter(has_text=operation['error'])).to_be_visible()
                else:
                    vm['spec']['media_id'] = '' if action == 'eject' else 'iso-demo'
                    expect(attach.locator('option:checked')).to_have_text('Empty' if action == 'eject' else 'demo-installer.iso', timeout=POLL)
            checks.append('ISO eject/insert pending notes clear after successful task refresh; failed eject shows host error and preserves attached ISO; task eviction/external tray changes do not revive progress or lose failure')
            page.get_by_role('button', name='Full screen', exact=True).click()
            page.wait_for_function('Boolean(document.fullscreenElement)')
            before = page.locator('.console-screen').bounding_box()['height']
            page.get_by_role('button', name='Collapse console toolbar').click()
            assert page.locator('.console-screen').bounding_box()['height'] > before
            page.screenshot(path=str(args.evidence / 'mock-fullscreen-collapsed.png'), full_page=True)
            page.get_by_role('button', name='Expand console toolbar').click()
            page.get_by_role('button', name='Full screen', exact=True).click()
            page.wait_for_function('!document.fullscreenElement')
            checks.append('Fullscreen collapse increases screen height and restores toolbar/exit control')
            # A guest can eject the disc Nex recorded: an installer does after the base system.
            # The host reports the live tray, so the console must say the VM ejected it and keep
            # the recorded ISO insertable instead of presenting it as still loaded.
            vm['guest']['media'] = 'empty'
            page.goto(url)
            page.reload()  # A hash change keeps the earlier failure note in the client state.
            expect(page.locator('.console-screen')).to_have_attribute('data-phase', 'connected')
            attach = page.get_by_label('Attach ISO', exact=True)
            expect(attach.locator('option:checked')).to_have_text('demo-installer.iso (ejected in the VM)')
            expect(page.get_by_role('status').filter(has_text='The VM ejected the disc')).to_be_visible()
            page.screenshot(path=str(args.evidence / 'mock-media-guest-ejected.png'), full_page=True)
            attach.select_option('iso-demo')
            expect(page.get_by_role('status').filter(has_text='Inserting the install media…')).to_be_visible()
            assert data['operations'][-1]['kind'] == 'vm-media', data['operations']
            data['operations'][-1]['status'] = 'succeeded'
            vm['guest']['media'] = 'loaded'
            expect(attach.locator('option:checked')).to_have_text('demo-installer.iso', timeout=POLL)
            expect(page.get_by_role('status').filter(has_text='The VM ejected the disc')).to_have_count(0)
            expect(page.get_by_role('status').filter(has_text='Inserting the install media…')).to_have_count(0)
            data['operations'].clear()
            del vm['guest']['media']
            checks.append('A guest-ejected tray is reported as ejected, the recorded ISO stays insertable, and reconnecting it clears the notice')
            page.goto(url)
            expect(page.locator('.console-screen')).to_have_attribute('data-phase', 'connected')
            page.screenshot(path=str(args.evidence / 'mock-connected.png'), full_page=True)
            page.get_by_label('View only', exact=True).check()
            expect(page.get_by_role('button', name='Send Ctrl+Alt+Del', exact=True)).to_be_disabled()
            previous = len(tickets)
            page.get_by_role('button', name='Reconnect', exact=True).click()
            expect(page.locator('.console-screen')).to_have_attribute('data-phase', 'connected')
            assert len(tickets) > previous
            expect(page.get_by_label('View only', exact=True)).to_be_checked()
            expect(page.get_by_role('button', name='Send Ctrl+Alt+Del', exact=True)).to_be_disabled()
            checks.append('Manual reconnect reconnects simulated RFB and preserves view-only input protection')
            mode['failure'] = True
            page.get_by_role('button', name='Reconnect', exact=True).click()
            page.get_by_role('button', name='Collapse console toolbar').click()
            expect(page.get_by_role('status').filter(has_text='Simulated console unavailable')).to_be_visible()
            page.get_by_role('button', name='Expand console toolbar').click()
            expect(page.get_by_role('button', name='Screenshot', exact=True)).to_be_disabled()
            page.screenshot(path=str(args.evidence / 'mock-error.png'), full_page=True)
            mode['failure'] = False
            expect(page.locator('.console-screen')).to_have_attribute('data-phase', 'connected', timeout=10000)
            checks.append('Ticket failure shows error and disables screenshot; automatic retry restores simulated console')
            host['connection'] = 'stale'
            page.reload()
            expect(page.get_by_text("The VM's host is not freshly connected.", exact=False)).to_be_visible()
            expect(page.get_by_role('button', name='Reconnect', exact=True)).to_be_disabled()
            expect(page.get_by_role('button', name='Focus console', exact=True)).to_be_disabled()
            page.set_viewport_size({'width': 390, 'height': 844})
            page.get_by_role('button', name='Collapse console toolbar').click()
            expect(page.get_by_text("The VM's host is not freshly connected.", exact=False)).to_be_visible()
            page.screenshot(path=str(args.evidence / 'mock-stale-mobile.png'), full_page=True)
            page.get_by_role('button', name='Expand console toolbar').click()
            expect(controls).to_be_visible()
            checks.append('Stale host disables connection and input controls; narrow viewport captured')
            assert not errors, errors
            browser.close()
        (args.evidence / 'browser-results.json').write_text(json.dumps({'mode': __doc__, 'checks': checks, 'page_errors': errors}, indent=2) + '\n')
        print(json.dumps(checks, indent=2))
    finally:
        server.shutdown()


if __name__ == '__main__':
    main()
