# SPDX-License-Identifier: AGPL-3.0-only
"""Browser acceptance called by phase1.py against its disposable HTTPS controller."""
import os
import re
import hashlib
import sqlite3
import time
from copy import deepcopy

from web_security import browser_security, peer_throttle
from pathlib import Path
from phase1 import eventually


def browser_acceptance(lab, password, host_id, pool_id, stop_agent, probe_mode):
    from playwright.sync_api import sync_playwright, expect
    captures = Path(os.environ.get('NEX_QA_CAPTURES', '.cache/phase1-captures'))
    captures.mkdir(parents=True, exist_ok=True)
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(headless=True)
        context = browser.new_context(ignore_https_errors=True, viewport={'width': 1440, 'height': 1000})
        page = context.new_page()
        errors = []
        page.on('pageerror', lambda error: errors.append(str(error)))

        def capture(name):
            page.screenshot(path=str(captures / (name + '.png')), full_page=True)

        def menu_item(label, item):
            """A button inside a top-bar menu (Scope, Account) or the VM panel's More menu."""
            menu = page.locator('details.vm-more') if label == 'More' else page.locator(f'details.menu:has(> summary[aria-label^="{label}"])')
            if menu.get_attribute('open') is None:
                menu.locator('summary').click()
            return menu.get_by_role('button', name=item, exact=True)

        def no_page_scroll():
            return page.evaluate("document.documentElement.scrollWidth <= window.innerWidth && "
                                 "!(document.querySelector('.page')?.scrollWidth > document.querySelector('.page')?.clientWidth)")

        rail = page.get_by_role('navigation', name='Sections', exact=True)
        panel = page.locator('section.slideover')
        quick = panel.locator('.quick')
        tabs = panel.get_by_role('tablist')

        def panel_tab(name):
            tabs.get_by_role('tab', name=name, exact=True).click()

        def refresh_route(route):
            page.goto(lab.url + '/#/' + route)
            page.reload()
            page.wait_for_load_state('networkidle')

        loading_requests = []
        page.route('**/api/v1/session', lambda route: loading_requests.append(route))
        initial_response = page.goto(lab.url)
        page.wait_for_load_state('networkidle')
        expect(page.get_by_label('Username')).to_be_focused()
        assert not loading_requests, 'a fresh browser must not probe a nonexistent session'
        # A returning browser does probe; hold that request to exercise genuine loading.
        page.evaluate("localStorage.setItem('nex-signed-in', '1')")
        with page.expect_request('**/api/v1/session'):
            page.reload()
        expect(page.get_by_text('Connecting to Nex Controller…')).to_be_visible()
        assert len(loading_requests) == 1, 'returning-session probe was not intercepted'
        capture('loading')
        for request in loading_requests:
            request.continue_()
        page.unroute('**/api/v1/session')
        page.wait_for_load_state('networkidle')
        expect(page.get_by_label('Username')).to_be_focused()
        page.get_by_label('Username').fill('root')
        page.keyboard.press('Tab')
        expect(page.get_by_label('Password', exact=True)).to_be_focused()
        page.keyboard.type('wrong')
        page.keyboard.press('Enter')
        expect(page.get_by_role('alert')).to_contain_text('invalid credentials')
        capture('unauthorized')
        page.get_by_label('Password', exact=True).fill(password)
        page.keyboard.press('Enter')
        expect(rail.get_by_role('link', name='Overview', exact=True)).to_have_attribute('aria-current', 'page')
        browser_cookie = browser_security(page, initial_response, '__Host-nex_session', lab.url + '/api/v1/sites')
        session_hash = hashlib.sha256(browser_cookie['value'].encode()).hexdigest()
        def session_idle(value=None):
            with sqlite3.connect(lab.path / 'controller-state/controller.db') as db:
                if value is not None:
                    db.execute('UPDATE sessions SET idle_expires=? WHERE token=?', (value, session_hash))
                return db.execute('SELECT idle_expires FROM sessions WHERE token=?', (session_hash,)).fetchone()[0]
        idle_deadline = int(time.time()) + 1200
        session_idle(idle_deadline)
        assert page.request.get(lab.url + '/api/v1/session').status == 200
        assert session_idle() == idle_deadline, 'GET polling extended idle session'
        expect(page.locator('[aria-label="Application menus"]')).to_have_count(0)
        account = page.locator('details.account')
        account.locator('summary').click()
        expect(account.get_by_role('link', name='API reference')).to_be_visible()
        expect(account.get_by_role('link', name='Nex source code')).to_be_visible()
        page.keyboard.press('Escape')
        menu_item('Scope', 'Create site').click()
        page.get_by_label('Site name').fill('Browser walkthrough')
        page.get_by_label('Site name').press('Enter')
        expect(page.locator('details.scope > summary')).to_contain_text('Browser walkthrough')
        assert session_idle() > idle_deadline, 'authenticated mutation did not renew idle session'
        # Pools are never created by hand: an empty site invites the first host.
        expect(page.get_by_role('heading', name='Add your first host', exact=True)).to_be_visible()
        expect(page.locator('details.scope > summary')).to_contain_text('0 hosts')
        expect(page.get_by_role('button', name='Create pool')).to_have_count(0)
        expect(page.get_by_role('button', name='Add host', exact=True).first).to_be_enabled()
        capture('empty-site-desktop')
        page.get_by_role('button', name='Add host', exact=True).first.click()
        expect(page.get_by_role('heading', name='Add host to Browser walkthrough', exact=True)).to_be_visible()
        expect(page.get_by_text('A new host becomes the master of a new pool in this site; a host that already belongs to a pool keeps it.', exact=False)).to_be_visible()
        capture('add-host-form-desktop')
        page.get_by_text('Manual enrollment', exact=True).click()
        page.get_by_role('button', name='Generate enrollment token', exact=True).click()
        expect(page.get_by_label('Single-use enrollment token')).not_to_have_value('')
        # Enrollment credentials are never written to capture artifacts.
        page.get_by_role('dialog').get_by_role('button', name='Close', exact=True).click()
        expect(page.get_by_label('Single-use enrollment token')).to_have_count(0)
        # Simulated add-host-by-address flow: probe then confirm, both stubbed so no
        # real host is contacted; the host request must wait for fingerprint confirmation.
        page.get_by_role('button', name='Add host', exact=True).first.click()
        expect(page.get_by_role('heading', name='Add host to Browser walkthrough', exact=True)).to_be_visible()
        fingerprint = 'sha256:' + 'ab' * 32
        add_host_bodies = []

        def stub_add_host(route):
            add_host_bodies.append(route.request.post_data_json)
            route.fulfill(json={'host_id': 'host-sim', 'pool_id': 'pool-sim'})

        page.route('**/api/v1/hosts/probe', lambda route: route.fulfill(json={
            'address': '10.0.0.5:9443', 'fingerprint': fingerprint,
            'subject': 'CN=10.0.0.5', 'not_after': '2030-01-01T00:00:00Z',
        }))
        page.route('**/api/v1/sites/*/hosts', stub_add_host)
        page.get_by_label('Host address').fill('10.0.0.5')
        page.get_by_label('Root password').fill('hunter2')
        page.get_by_role('button', name='Connect', exact=True).click()
        expect(page.get_by_text(fingerprint)).to_be_visible()
        assert not add_host_bodies, 'host add request sent before fingerprint was confirmed'
        capture('add-host-probe-desktop')
        page.get_by_role('button', name='Confirm and add host', exact=True).click()
        expect(page.get_by_role('status').filter(has_text='Host host-sim enrolled in pool pool-sim; it appears in the host list after its first report.')).to_be_visible()
        assert len(add_host_bodies) == 1, add_host_bodies
        body = add_host_bodies[0]
        assert body['address'] and body['fingerprint'] == fingerprint and body['controller_url'] and body['root_password'] == 'hunter2', body
        expect(page.get_by_label('Root password')).to_have_count(0)
        page.unroute('**/api/v1/hosts/probe')
        page.unroute('**/api/v1/sites/*/hosts')
        page.get_by_role('dialog').get_by_role('button', name='Close', exact=True).click()
        lab.passed('browser add-host-by-address probe/confirm flow: host request withheld until fingerprint confirmed, root password reaches the request and is cleared from the form on success')
        # A delayed token belongs only to the site where it was requested.
        pending_tokens = []
        page.route('**/api/v1/enrollment-tokens', lambda route: pending_tokens.append((route, route.fetch())))
        page.get_by_role('button', name='Add host', exact=True).first.click()
        page.get_by_text('Manual enrollment', exact=True).click()
        page.get_by_role('button', name='Generate enrollment token', exact=True).click()
        for _ in range(100):
            if pending_tokens:
                break
            page.wait_for_timeout(50)
        assert len(pending_tokens) == 1
        acceptance_pool = next(p for p in lab.api('/api/v1/pools') if p['id'] == pool_id)
        acceptance_site = next(s for s in lab.api('/api/v1/sites') if s['id'] == acceptance_pool['site_id'])
        # The dialog is modal, so the scope changes by navigation, as a deep link or Back would.
        page.evaluate(f"location.hash = '#/pools/{pool_id}'")
        expect(page.get_by_role('heading', name=re.compile(r'^Add host to '))).to_have_count(0)
        expect(page.locator('details.scope > summary')).to_contain_text(acceptance_pool['name'])
        pending_tokens[0][0].fulfill(response=pending_tokens[0][1])
        page.unroute('**/api/v1/enrollment-tokens')
        menu_item('Scope', 'Add host').click()
        expect(page.get_by_role('heading', name='Add host to ' + acceptance_site['name'], exact=True)).to_be_visible()
        expect(page.get_by_label('Single-use enrollment token')).to_have_count(0)
        page.get_by_role('dialog').get_by_role('button', name='Close', exact=True).click()
        lab.passed('browser keyboard sign-in, invalid credentials, site creation, add-first-host empty state and ephemeral enrollment token')

        refresh_route('hosts/missing-browser-resource')
        expect(panel.get_by_role('heading', name='Host not found', exact=True)).to_be_visible()
        expect(quick).to_have_count(0)
        capture('invalid-object-desktop')
        page.keyboard.press('Escape')
        expect(panel).to_have_count(0)
        assert page.evaluate('location.hash') == '#/hosts'
        refresh_route('no-such-screen')
        expect(page.get_by_role('heading', name='Object not found', exact=True)).to_be_visible()
        page.get_by_role('link', name='Return to overview', exact=True).click()
        expect(rail.get_by_role('link', name='Overview', exact=True)).to_have_attribute('aria-current', 'page')
        # A single stale sample must recover on the next normal inventory poll.
        # The fixture's 2s freshness window can expire during runner contention;
        # inject that sample deterministically, then restore the live agent API.
        def stale_sample(route):
            response = route.fetch()
            hosts = response.json()
            for host in hosts:
                if host['id'] == host_id:
                    host['connection'] = 'stale'
            route.fulfill(response=response, json=hosts)

        page.route('**/api/v1/hosts', stale_sample)
        refresh_route('hosts/' + host_id)
        expect(page.get_by_role('heading', name='Health checks', exact=True)).to_be_visible()
        expect(panel.locator('header .badge')).to_have_text('Stale')
        page.unroute('**/api/v1/hosts', stale_sample)
        # The default 5s assertion expires before the UI's 10s inventory poll.
        expect(panel.locator('header .badge')).to_have_text('Online', timeout=15000)
        lab.passed('browser injected stale connection recovers from live inventory on the next automatic poll')
        host = next(h for h in lab.api('/api/v1/hosts') if h['id'] == host_id)
        pool = next(p for p in lab.api('/api/v1/pools') if p['id'] == pool_id)
        site = next(s for s in lab.api('/api/v1/sites') if s['id'] == pool['site_id'])
        assert pool['name'] == host['hostname'] and pool['master_host_id'] == host_id, pool
        expect(panel.locator('.sub')).to_contain_text('Master of ' + pool['name'])
        expect(page.get_by_text('Member', exact=True)).to_have_count(0)
        capture('host-desktop')
        expect(rail.get_by_role('link', name='Hosts', exact=True)).to_have_attribute('aria-current', 'page')
        row = page.locator('table.list tbody tr').filter(has=page.locator(f'a[href="#/hosts/{host_id}"]'))
        expect(row).to_have_class(re.compile(r'\bsel\b'))
        expect(row.get_by_text('Master', exact=True)).to_be_visible()
        expect(page.locator('details.scope > summary')).to_have_accessible_name(
            f"Scope: {site['name']}, {pool['name']}. Change scope")
        # Global search: "/" focuses it, matching ignores case, Enter opens the match, Escape clears.
        page.keyboard.press('/')
        search = page.get_by_role('combobox', name='Search VMs, hosts, IP addresses and settings')
        expect(search).to_be_focused()
        search.fill(host['hostname'].upper())
        expect(page.get_by_role('option', name=re.compile('^' + re.escape(host['hostname'])))).to_be_visible()
        search.fill('no-such-resource-acceptance')
        expect(page.get_by_role('listbox', name='Search results')).to_contain_text('No matches')
        page.keyboard.press('Escape')
        expect(search).to_have_value('')
        search.fill('members')
        page.keyboard.press('Enter')
        assert page.evaluate('location.hash') == '#/settings/members'
        rail.get_by_role('link', name='Hosts', exact=True).click()
        row.get_by_role('link', name=host['hostname'], exact=True).click()
        expect(panel.get_by_role('heading', name=re.compile('^' + re.escape(host['hostname'])))).to_be_visible()
        # Escape closes the panel and keeps the list.
        page.keyboard.press('Escape')
        expect(panel).to_have_count(0)
        expect(row).to_be_visible()
        row.get_by_role('link', name=host['hostname'], exact=True).click()

        panel_tab('Networking')
        expect(tabs.get_by_role('tab', name='Networking', exact=True)).to_have_attribute('aria-selected', 'true')
        expect(panel.get_by_role('heading', name='Network interfaces', exact=True)).to_be_visible()
        expect(panel.get_by_role('heading', name='Health checks', exact=True)).to_have_count(0)
        expect(panel.get_by_role('heading', name='Disks', exact=True)).to_have_count(0)
        capture('networking-desktop')
        tabs.get_by_role('tab', name='Storage', exact=True).focus()
        page.keyboard.press('Enter')
        expect(panel.get_by_role('heading', name='Disks', exact=True)).to_be_visible()
        expect(panel.get_by_text('Observed device capacity.', exact=False)).to_be_visible()
        expect(panel.get_by_role('heading', name='Network interfaces', exact=True)).to_have_count(0)
        capture('storage-desktop')
        panel_tab('Activity')
        expect(panel.get_by_role('heading', name='Activity and health history', exact=True)).to_be_visible()
        expect(panel).not_to_contain_text('Browser walkthrough')
        capture('activity-desktop')
        panel_tab('Overview')
        expect(panel.get_by_role('heading', name='Health checks', exact=True)).to_be_visible()
        chrome = {name: page.locator(selector).bounding_box() for name, selector in {
            'logo': '.logo', 'top': 'header.top', 'rail': 'nav.rail', 'main': '#main', 'panel': 'section.slideover',
        }.items()}
        assert all(chrome.values()), 'console shell elements must be visible'
        assert chrome['top']['y'] < chrome['main']['y'] and chrome['logo']['y'] < chrome['rail']['y'], 'top bar and logo above the content and rail'
        assert chrome['rail']['x'] + chrome['rail']['width'] <= chrome['main']['x'] + 1, 'rail must sit left of the content'
        assert chrome['panel']['x'] > chrome['main']['x'] and chrome['panel']['x'] + chrome['panel']['width'] <= 1441, 'slide-over sits on the right'
        assert no_page_scroll(), 'desktop page overflow'
        capture('desktop-shell')
        lab.passed('browser global search ("/", case-insensitive, no-match, Escape, settings jump), scope label, host list selection, slide-over Escape, keyboard panel tabs Overview/Networking/Storage/Activity and desktop console layout')
        # The activity pill opens Tasks; browser history moves between hash routes.
        page.locator('a.activity').click()
        expect(page.get_by_role('heading', name='Tasks', exact=True)).to_be_visible()
        expect(rail.get_by_role('link', name='Tasks', exact=True)).to_have_attribute('aria-current', 'page')
        page.go_back()
        expect(panel.get_by_role('heading', name='Health checks', exact=True)).to_be_visible()
        page.go_forward()
        expect(page.get_by_role('heading', name='Tasks', exact=True)).to_be_visible()
        page.go_back()
        expect(panel.get_by_role('heading', name='Health checks', exact=True)).to_be_visible()
        rail.get_by_role('link', name='VMs', exact=True).click()
        create_vm = page.get_by_role('button', name='Create VM', exact=True)
        expect(create_vm).to_be_enabled()
        create_vm.click()
        expect(page.get_by_role('heading', name='Create VM · 1 of 6:', exact=False)).to_be_visible(timeout=15000)
        capture('create-vm-wizard')
        page.get_by_role('region', name='Create VM').get_by_role('button', name='Close', exact=True).click()
        expect(create_vm).to_be_visible()
        lab.passed('browser activity pill to Tasks, Back/Forward over hash routes, Create VM opening the scoped pool wizard')
        # An accepted create may include Core's advisory placement warning.  The create itself and
        # its observations are fixture responses: neither request reaches the disposable agent.
        warning_storage = {'id': 'warning-storage', 'pool_id': pool_id, 'host_id': host_id, 'name': 'Warning storage',
                           'operation_id': '', 'status': 'succeeded', 'kind': 'local', 'path': '/fixture',
                           'access': {'status': 'healthy', 'readable': True, 'writable': True}, 'capabilities': {'vm_disk': True}}
        warning_network = {'id': 'warning-network', 'pool_id': pool_id, 'host_id': host_id, 'name': 'Warning network',
                           'operation_id': '', 'status': 'succeeded', 'kind': 'bridge', 'source': 'br0'}
        warning_operation = [{'id': 'warning-create', 'host_id': host_id, 'kind': 'vm-create', 'request_id': 'warning-create',
                              'status': 'queued', 'actor': 'operator', 'created_at': '2026-09-20T00:00:00Z', 'updated_at': '2026-09-20T00:00:00Z'}]
        warning_vms = []
        page.route('**/api/v1/pools/*/members', lambda route: route.fulfill(json={
            'authoritative': True, 'members': [{'host_id': host_id, 'maintenance': False}],
        }))
        page.route('**/api/v1/storage', lambda route: route.fulfill(json=[warning_storage]))
        page.route('**/api/v1/networks', lambda route: route.fulfill(json=[warning_network]))
        page.route('**/api/v1/operations', lambda route: route.fulfill(json=warning_operation))
        page.route('**/api/v1/vms', lambda route: route.fulfill(status=202, json={
            'vm_id': 'warning-vm', 'operation': {'id': 'warning-create'}, 'warnings': ['Placement warning from Core fixture'],
        }) if route.request.method == 'POST' else route.fulfill(json=warning_vms))
        refresh_route('vms')
        page.get_by_role('button', name='Create VM', exact=True).click()
        page.get_by_label('VM name').fill('Warning fixture')
        page.get_by_label('Placement host').select_option(host_id)
        page.get_by_role('button', name='Next', exact=True).click()
        page.get_by_label('CPU model').select_option('host-passthrough')
        page.get_by_role('button', name='Next', exact=True).click()
        page.get_by_role('group', name='Disk 1').get_by_label('Storage').select_option('warning-storage')
        page.get_by_role('button', name='Next', exact=True).click()
        page.get_by_role('group', name='Network interface 1').get_by_role('button', name='Remove network interface 1', exact=True).click()
        for _ in range(2): page.get_by_role('button', name='Next', exact=True).click()
        page.get_by_role('region', name='Create VM').get_by_role('button', name='Create stopped VM', exact=True).click()
        warning = page.get_by_text('Placement warning from Core fixture', exact=False)
        expect(warning).to_be_visible()
        assert page.evaluate('location.hash') == '#/vms/warning-vm/warning-create', 'accepted create must navigate to its task route'
        # The result route must render without a browser refresh after the wizard closes.
        expect(page.get_by_role('region', name='Create VM', exact=True)).not_to_be_visible()
        expect(page.get_by_role('region', name='Creation task', exact=True)).to_be_visible()
        capture('create-result-without-refresh')
        warning_operation[0]['status'] = 'succeeded'
        warning_vms.append({'id': 'warning-vm', 'pool_id': pool_id, 'host_id': host_id, 'name': 'Warning fixture',
                            'status': 'succeeded', 'state': 'shut off', 'freshness': 'fresh', 'operation_id': 'warning-create'})
        # The next automatic inventory poll (every 10 s) sees the terminal task.
        expect(warning).not_to_be_visible(timeout=15000)
        expect(panel.get_by_role('heading', name=re.compile('^Warning fixture'))).to_be_visible()
        capture('created-vm-without-refresh')
        page.unroute('**/api/v1/vms')
        page.unroute('**/api/v1/operations')
        page.unroute('**/api/v1/networks')
        page.unroute('**/api/v1/storage')
        page.unroute('**/api/v1/pools/*/members')
        lab.passed('browser intercepted VM create keeps Core placement warning through its task-route hashchange and clears it only after the task is terminal')
        # Simulated VM on the real host: the VM panel's power actions, its confirmation dialog and the
        # rejected/accepted request paths are exercised without a guest. Labeled simulation.
        fixture_vm = {'id': 'fixture-vm', 'pool_id': pool_id, 'host_id': host_id, 'name': 'Toolbar fixture', 'operation_id': '',
                      'status': 'succeeded', 'state': 'running', 'freshness': 'fresh', 'role': 'guest', 'protected': False}
        page.route('**/api/v1/vms', lambda route: route.fulfill(json=[fixture_vm]))

        def with_power(route):
            hosts = route.fetch().json()
            for h in hosts:
                h['capabilities'] = sorted(set(h.get('capabilities') or []) | {'vm-power-v1', 'vm-power-queue-v1'})
                # This block is a simulated power UI against a fixture VM. Keep the real
                # host's inventory, but do not inherit a transient stale/reconnect window
                # left by earlier controller restart or cert-rotation steps in this lab.
                if h.get('id') == host_id:
                    h['connection'] = 'fresh'
                    h['revoked'] = False
            route.fulfill(json=hosts)
        page.route('**/api/v1/hosts', with_power)
        power_requests = []
        reject_next = [True]

        def power(route):
            power_requests.append(route.request.post_data_json)
            if reject_next:
                reject_next.pop()
                route.fulfill(status=409, json={'error': 'the VM host is unavailable'})
            else:
                route.fulfill(json={'id': 'fixture-power', 'status': 'queued'})
        page.route('**/api/v1/vms/fixture-vm/power', power)
        refresh_route('vms/fixture-vm')
        expect(panel.get_by_role('heading', name=re.compile('^Toolbar fixture'))).to_be_visible()
        expect(panel.locator('header .badge')).to_have_text('Running')
        power = quick.get_by_role('group', name='VM power')
        expect(power.get_by_role('button', name='Start', exact=True)).to_be_disabled()
        expect(power.get_by_role('button', name='Shut down', exact=True)).to_be_enabled()
        for name in ['Reboot', 'Force stop', 'Force reboot']:
            expect(menu_item('More', name)).to_be_enabled()
        expect(quick.get_by_role('button', name='Migrate', exact=True)).to_be_disabled()
        expect(page.locator('.vm-power dialog')).not_to_be_visible()
        menu_item('More', 'Force stop').click()
        dialog = page.get_by_role('dialog')
        expect(dialog).to_be_visible()
        expect(dialog).to_contain_text('Force stop Toolbar fixture?')
        expect(dialog).to_contain_text('Unsaved guest data is lost')
        capture('power-confirm-dialog')
        dialog.get_by_role('button', name='Cancel', exact=True).click()
        expect(dialog).not_to_be_visible()
        assert power_requests == [], 'cancel must not submit'
        menu_item('More', 'Reboot').click()
        expect(dialog).to_contain_text('Reboot Toolbar fixture?')
        page.keyboard.press('Escape')
        expect(dialog).not_to_be_visible()
        assert power_requests == [], 'escape must not submit'
        menu_item('More', 'Force stop').click()
        dialog.get_by_role('button', name='Confirm force stop', exact=True).click()
        expect(dialog.get_by_role('alert')).to_contain_text('host is unavailable')
        expect(dialog).to_be_visible()
        capture('power-confirm-rejected')
        dialog.get_by_role('button', name='Confirm force stop', exact=True).click()
        expect(dialog).not_to_be_visible()
        expect(page.locator('.notice')).to_contain_text('Force stop queued for Toolbar fixture')
        assert [r['action'] for r in power_requests] == ['force-stop', 'force-stop']
        assert power_requests[0]['request_id'] == power_requests[1]['request_id'], 'retry must reuse the request identity'
        power_requests.clear()
        menu_item('More', 'Force reboot').click()
        expect(dialog).to_contain_text('Force reboot Toolbar fixture?')
        expect(dialog).to_contain_text('Hard-resets the guest immediately')
        expect(dialog.get_by_role('button', name='Confirm force reboot', exact=True)).to_have_class(re.compile(r'\bdanger\b'))
        dialog.get_by_role('button', name='Confirm force reboot', exact=True).click()
        expect(dialog).not_to_be_visible()
        expect(page.locator('.notice')).to_contain_text('Force reboot queued for Toolbar fixture')
        assert [r['action'] for r in power_requests] == ['force-reboot']
        # Start has no confirmation; a rejected Start opens the dialog as its error surface and retries from there.
        fixture_vm['state'] = 'shut off'
        power_requests.clear()
        reject_next.append(True)
        expect(panel.locator('header .badge')).to_have_text('Shut off', timeout=15000)
        expect(menu_item('More', 'Force stop')).to_be_disabled()
        expect(menu_item('More', 'Force reboot')).to_be_disabled()
        page.keyboard.press('Escape')
        power.get_by_role('button', name='Start', exact=True).click()
        expect(dialog).to_contain_text('Start Toolbar fixture?')
        expect(dialog.get_by_role('alert')).to_contain_text('host is unavailable')
        capture('power-start-rejected')
        dialog.get_by_role('button', name='Confirm start', exact=True).click()
        expect(dialog).not_to_be_visible()
        expect(page.locator('.notice')).to_contain_text('Start queued for Toolbar fixture')
        expect(power.get_by_role('button', name='Start', exact=True)).to_be_focused()
        assert [r['action'] for r in power_requests] == ['start', 'start'] and power_requests[0]['request_id'] == power_requests[1]['request_id']
        # A busy VM accepts Start once as a queued task; the browser never retries the request.
        power_requests.clear()
        pending_operations = [{'id': 'fixture-edit', 'host_id': host_id, 'kind': 'vm-update',
                               'request_id': 'fixture-edit', 'status': 'dispatched', 'actor': 'operator',
                               'created_at': '2026-09-11T00:00:00Z', 'updated_at': '2026-09-11T00:00:00Z'}]
        page.route('**/api/v1/operations', lambda route: route.fulfill(json=pending_operations))
        page.reload()
        tasks_row = lambda task: page.locator('table.records tr').filter(has_text=task)
        rail.get_by_role('link', name='Tasks', exact=True).click()
        expect(tasks_row('fixture-edit')).to_contain_text('Awaiting agent result')
        page.go_back()

        def queued_power(route):
            power_requests.append(route.request.post_data_json)
            pending_operations.append({**pending_operations[0], 'id': 'fixture-queued-start',
                                       'request_id': power_requests[-1]['request_id'], 'kind': 'vm-power', 'status': 'queued'})
            route.fulfill(status=202, json={'id': 'fixture-queued-start', 'status': 'queued'})
        page.unroute('**/api/v1/vms/fixture-vm/power')
        page.route('**/api/v1/vms/fixture-vm/power', queued_power)
        power.get_by_role('button', name='Start', exact=True).click()
        expect(page.locator('.notice')).to_contain_text('Start queued for Toolbar fixture. Follow its progress in Tasks.')
        expect(dialog).not_to_be_visible()
        expect(page.locator('a.activity')).to_have_text('2 tasks running')  # the pill counts every unfinished task, queued included
        rail.get_by_role('link', name='Tasks', exact=True).click()
        expect(tasks_row('fixture-queued-start')).to_contain_text('Waiting to be sent')
        expect(tasks_row('fixture-edit')).to_contain_text('Awaiting agent result')
        expect(dialog).not_to_be_visible()
        assert [r['action'] for r in power_requests] == ['start'], 'queued Start must submit only once'
        capture('power-start-queued')
        page.unroute('**/api/v1/operations')
        page.unroute('**/api/v1/vms/fixture-vm/power')
        page.unroute('**/api/v1/hosts')
        page.unroute('**/api/v1/vms')
        lab.passed('browser simulated VM power from the VM panel: state-dependent enablement, confirmation dialog cancel/escape, unavailable-host rejection and same-ID retry, rejected Start returning focus, one-click Start queued behind pending work without a dialog or browser retry')
        refresh_route('pools/' + pool_id)
        assert page.evaluate('location.hash') == '#/', '#/pools/{id} sets the scope and shows its overview'
        expect(page.locator('details.scope > summary')).to_contain_text(pool['name'])
        resilience = page.locator('section[aria-labelledby="ov-resilience"]')
        expect(resilience.get_by_role('link', name=host['hostname'], exact=True)).to_have_attribute('href', '#/hosts/' + host_id)
        rail.get_by_role('link', name='Settings', exact=True).click()
        expect(page.get_by_text('Pool settings are stored on the pool master', exact=False)).to_be_visible()
        expect(page.locator('nav.snav')).to_contain_text(f"{pool['name']} · master {host['hostname']}")
        capture('pool-master-desktop')
        # BUG-0096: summaries keep the current Core observation rather than flattening every
        # non-healthy result to "degraded". The pool's summary is the overview's Needs attention
        # list. These are disposable browser fixtures; Core's sanitization is covered by its own
        # agent regression test.
        attention = page.locator('section[aria-labelledby="ov-attention"]')
        host_row = attention.locator(f'a[href="#/hosts/{host_id}"]')
        checks = next(h for h in lab.api('/api/v1/hosts') if h['id'] == host_id)['inventory']['checks']
        expected_health = max((c['status'] if c['freshness'] == 'fresh' else c['freshness'] for c in checks),
                              key=lambda status: {'healthy': 0, 'unsupported': 1, 'degraded': 2, 'unknown': 3, 'stale': 4, 'failed': 5}[status])
        refresh_route('')
        if expected_health == 'healthy':
            expect(host_row).to_have_count(0)
        else:
            expect(host_row).to_contain_text(re.compile(expected_health, re.I))
        summary_host = deepcopy(host)
        summary_host['inventory']['checks'] = [{'name': 'libvirt', 'status': 'degraded', 'freshness': 'fresh',
                                                'observed_at': '2026-09-12T00:00:00Z',
                                                'error': 'Core sanitized: libvirt response exceeded its deadline'}]
        page.route('**/api/v1/hosts', lambda route: route.fulfill(json=[summary_host]))
        refresh_route('')
        expect(host_row).to_contain_text(host['hostname'] + ' degraded')
        expect(host_row).to_contain_text('Core sanitized: libvirt response exceeded its deadline')
        expect(attention).not_to_contain_text('internal-token')
        summary_host['inventory']['checks'] = [
            {'name': 'libvirt', 'status': 'failed', 'freshness': 'fresh', 'observed_at': '2026-09-12T00:00:00Z', 'error': 'Core sanitized: libvirt is unavailable'},
            {'name': 'storage', 'status': 'unknown', 'freshness': 'fresh', 'observed_at': '2026-09-12T00:00:00Z', 'error': 'Core sanitized: storage probe timed out'},
        ]
        refresh_route('')
        expect(host_row).to_contain_text(host['hostname'] + ' failed')
        expect(host_row).to_contain_text('Core sanitized: libvirt is unavailable')
        expect(host_row).to_contain_text('Core sanitized: storage probe timed out')
        expect(host_row).to_have_count(1)  # both affected checks in one row, not one per check
        capture('health-summary-multiple')
        summary_host['inventory']['checks'] = [{'name': 'libvirt', 'status': 'healthy', 'freshness': 'stale',
                                                'observed_at': '2026-09-12T00:00:00Z',
                                                'error': 'Core sanitized: last libvirt result is stale'}]
        refresh_route('')
        expect(host_row).to_contain_text(host['hostname'] + ' stale')
        expect(host_row).to_contain_text('Core sanitized: last libvirt result is stale')
        summary_host['inventory']['checks'] = [{'name': 'libvirt', 'status': 'unknown', 'freshness': 'fresh',
                                                'observed_at': '2026-09-12T00:00:00Z',
                                                'error': 'Core sanitized: no current libvirt observation'}]
        refresh_route('')
        expect(host_row).to_contain_text(host['hostname'] + ' unknown')
        expect(host_row).to_contain_text('Core sanitized: no current libvirt observation')
        summary_host['revoked'] = True
        summary_host['connection'] = 'revoked'
        refresh_route('')
        expect(host_row).to_contain_text(host['hostname'] + ' not reporting')
        expect(host_row.locator('.ov-icon')).to_have_class(re.compile(r'\bbad\b'))
        capture('health-summary-revoked')
        # Revoked and stale both read "not reporting" in the summary; the host panel tells
        # them apart, and only a revoked host is told to re-enroll.
        revoked_notice = page.locator('.warning', has_text='Access revoked. Re-enroll this host to resume reporting.')
        refresh_route(f'hosts/{host_id}')
        expect(revoked_notice).to_be_visible()
        summary_host['revoked'] = False
        summary_host['connection'] = 'stale'
        refresh_route('')
        expect(host_row).to_contain_text(host['hostname'] + ' not reporting')
        expect(host_row.locator('.ov-icon')).to_have_class(re.compile(r'\bwarn\b'))
        refresh_route(f'hosts/{host_id}')
        expect(page.locator('.warning', has_text='VMs on this host are shown as Unknown, not stopped.')).to_be_visible()
        expect(revoked_notice).to_have_count(0)
        summary_host['connection'] = 'fresh'
        summary_host['inventory']['checks'] = [{'name': 'libvirt', 'status': 'healthy', 'freshness': 'fresh',
                                                'observed_at': '2026-09-12T00:00:00Z'}]
        refresh_route('')
        expect(host_row).to_have_count(0)
        expect(attention).to_contain_text('Every host in scope is reporting and healthy.')
        page.unroute('**/api/v1/hosts')
        lab.passed('browser pool Needs attention summary: healthy, current degraded/failed reasons, multiple affected checks in one row, stale/unknown observations, and a revoked host told apart from a stale one (bad vs warn row, Access revoked notice only on the revoked host panel)')
        refresh_route('hosts/' + host_id)
        def wait_probe(status):
            eventually(lambda: any(
                check['name'] == 'libvirt' and check['status'] == status
                for host in lab.api('/api/v1/hosts') if host['id'] == host_id
                for check in (host.get('inventory') or {}).get('checks', [])
            ), f'browser libvirt probe becomes {status}')

        probe_mode('failed')
        wait_probe('failed')
        page.reload()
        libvirt = page.locator('.check').filter(has=page.get_by_text('libvirt', exact=True))
        expect(libvirt.locator('.badge', has_text='Failed')).to_be_visible()
        expect(panel.locator('header .badge')).to_have_text('Online')
        capture('libvirt-failed-desktop')
        probe_mode('stalled')
        wait_probe('unknown')
        page.reload()
        expect(libvirt).to_contain_text('probe deadline exceeded')
        capture('probe-timeout-desktop')
        probe_mode('healthy')
        wait_probe('healthy')
        page.reload()
        expect(libvirt.locator('.badge', has_text='Healthy')).to_be_visible()
        lab.passed('browser pool aggregate healthy enum, failed libvirt and timed-out probe distinct from fresh heartbeat; recovered checks')
        quick.get_by_role('button', name='Refresh inventory', exact=True).click()
        expect(page.locator('.notice')).to_contain_text('Inventory refresh')
        rail.get_by_role('link', name='Tasks', exact=True).click()
        expect(page.get_by_role('heading', name='Tasks', exact=True)).to_be_visible()
        expect(page.locator('table').first).to_contain_text('root')
        capture('tasks-desktop')

        refresh_route('hosts/' + host_id)
        quick.get_by_role('button', name='Revoke access', exact=True).click()
        page.get_by_label('Type the hostname to confirm').fill('incorrect host')
        page.get_by_role('button', name='Confirm revoke access', exact=True).click()
        expect(page.get_by_label('Type the hostname to confirm')).to_be_visible()
        capture('revoke-confirmation')
        page.get_by_role('dialog').get_by_role('button', name='Close', exact=True).click()

        page.set_viewport_size({'width': 360, 'height': 800})
        capture('host-360')
        # At phone width the slide-over takes the full width and its quick actions stay on screen.
        for box in (panel.bounding_box(), quick.bounding_box()):
            assert box and box['x'] >= 0 and box['x'] + box['width'] <= 360, 'host panel outside narrow viewport'
        for tab_name, heading in [('Networking', 'Network interfaces'), ('Storage', 'Disks'), ('Activity', 'Activity and health history')]:
            panel_tab(tab_name)
            expect(panel.get_by_role('heading', name=heading, exact=True)).to_be_visible()
            assert no_page_scroll(), f'360px {tab_name} overflow'
            capture(tab_name.lower() + '-360')
        panel_tab('Overview')
        assert no_page_scroll(), '360px page overflow'

        # U7/U10: Core owns CIDR validation; the UI only projects the refusal. Keep this
        # browser-only fixture at the API boundary: no host configuration is submitted or changed.
        network_route = '**/api/v1/hosts/*/network'
        network_spec = {'interfaces': [
            {'name': 'ens3', 'mac': '52:54:00:12:34:56', 'mode': 'static', 'address': '192.0.2.20/24', 'gateway': '192.0.2.1', 'management': True},
            {'name': 'ens4', 'mac': '52:54:00:12:34:57', 'mode': 'disabled'},
        ], 'dns': ['192.0.2.53']}
        network_nics = [
            {'name': 'ens3', 'mac': '52:54:00:12:34:56', 'kind': 'physical', 'addresses': ['192.0.2.10/24'], 'state': 'up'},
            {'name': 'ens4', 'mac': '52:54:00:12:34:57', 'kind': 'physical', 'addresses': [], 'state': 'up'},
        ]
        network_cidr_error = 'interface ens3 needs a usable IPv4 address in CIDR form, for example 192.0.2.10/24'

        def network_view(state, record):
            return {'host_id': host_id, 'hostname': 'browser-host', 'connection': 'fresh', 'supported': True,
                    'state': state, 'management_network': record, 'nics': network_nics, 'check': None}

        def network_fixture(route):
            if route.request.method == 'POST':
                route.fulfill(status=400, json={'error': network_cidr_error})
                return
            route.fulfill(json=network_view('none', None))

        page.route(network_route, network_fixture)
        panel_tab('Networking')
        page.locator('#mode-ens3').select_option('static')
        page.locator('#address-ens3').fill('192.0.2.999/24')
        page.get_by_role('button', name='Stage this configuration').click()
        expect(page.get_by_role('alert')).to_contain_text('interface ens3 needs a usable IPv4 address in CIDR form')
        assert no_page_scroll(), '360px management-network validation overflow'
        capture('management-network-validation-360')
        page.unroute(network_route)

        rolled_back = network_view('rolled-back', {'revision': 4, 'stage': 'rolled-back', 'operation_id': 'browser-rollback',
            'spec': network_spec, 'requested_rollback': True, 'last_error': 'rolled back at operator request'})
        page.route(network_route, lambda route: route.fulfill(json=rolled_back))
        panel_tab('Overview')
        panel_tab('Networking')
        expect(page.get_by_role('status').filter(has_text='because you asked it to')).to_be_visible()
        assert not page.get_by_text('Host check management-network', exact=False).count()
        assert no_page_scroll(), '360px requested management-network rollback overflow'
        capture('management-network-rollback-360')
        page.unroute(network_route)

        page.keyboard.press('Control+Home')
        page.get_by_role('link', name='Skip to content').focus()
        page.keyboard.press('Enter')
        assert page.locator('#main').evaluate('(el) => el === document.activeElement'), 'skip link must focus main'
        lab.passed('browser desktop/360px host and task views, refresh operation, exact-host confirmation and keyboard skip link')

        # Browser network failure injection checks honest retained-observation messaging.
        page.route('**/api/v1/hosts', lambda route: route.abort())
        page.reload()
        expect(page.get_by_role('alert').filter(has_text='Inventory may be out of date')).to_be_visible()
        capture('network-error-360')
        expect(panel.get_by_role('heading', name='Host unavailable')).to_be_visible()
        expect(panel).to_contain_text('This host cannot be shown until the inventory loads.')
        page.unroute('**/api/v1/hosts')
        # At 360px the panel covers the page alert, so the panel carries its own Retry.
        panel.get_by_role('button', name='Retry', exact=True).click()
        expect(page.get_by_role('alert')).to_have_count(0)
        stop_agent()
        page.wait_for_timeout(2300)
        page.reload()
        expect(panel.locator('header .badge')).to_have_text('Stale')
        expect(panel.get_by_text('VMs on this host are shown as Unknown, not stopped.', exact=False)).to_be_visible()
        capture('stale-host-360')
        # Capture an older periodic read, revoke via a newer read, then release
        # the old response; it must not resurrect pre-revocation UI state.
        older = []
        def hold_first(route):
            if not older:
                older.append((route, route.fetch()))
            else:
                route.continue_()
        page.route('**/api/v1/hosts', hold_first)
        page.wait_for_timeout(10500)
        assert len(older) == 1
        quick.get_by_role('button', name='Revoke access', exact=True).click()
        page.get_by_label('Type the hostname to confirm').fill(host['hostname'])
        page.get_by_role('button', name='Confirm revoke access', exact=True).click()
        reenroll = quick.get_by_role('button', name='Re-enroll', exact=True)
        expect(reenroll).to_be_enabled()
        older[0][0].fulfill(response=older[0][1])
        page.unroute('**/api/v1/hosts')
        page.wait_for_timeout(200)
        expect(reenroll).to_be_enabled()
        expect(quick.get_by_role('button', name='Refresh inventory', exact=True)).to_be_disabled()
        capture('revoked-host-360')
        reenroll.click()
        expect(page.get_by_role('heading', name='Re-enroll ' + host['hostname'], exact=True)).to_be_visible()
        page.get_by_text('Manual enrollment', exact=True).click()
        expect(page.get_by_role('button', name='Generate enrollment token', exact=True)).to_be_visible()
        page.get_by_role('dialog').get_by_role('button', name='Close', exact=True).click()
        # Advance only this disposable browser session's stored deadline, not the host clock.
        session_idle(int(time.time()) - 1)
        page.reload()
        expect(page.get_by_label('Username')).to_be_visible()
        capture('idle-expired-login')
        page.get_by_label('Username').fill('root')
        page.get_by_label('Password', exact=True).fill(password)
        page.get_by_role('button', name='Sign in', exact=True).click()
        sign_out = menu_item('Account', 'Sign out')
        expect(sign_out).to_be_visible()
        sign_out.click()
        expect(page.get_by_label('Username')).to_be_visible()
        assert not any(c['name'] == '__Host-nex_session' for c in context.cookies()), 'logout retained cookie'
        assert context.request.get(lab.url + '/api/v1/sites').status == 401
        peer_throttle(page, lab.url + '/api/v1/session')
        lab.passed('browser __Host cookie attributes, legacy rejection, restrictive CSP, real inline-script blocking, idle GET/mutation semantics and expired-session login recovery; logout and actual-peer throttle')
        assert not errors, errors
        lab.passed('browser interrupted request/retry, genuine stopped-agent stale state, revoke and re-enroll entry, sign-out; no page errors')
        lab.passed('browser delayed-token site scope, automatic pool master labels, and older inventory response cannot overwrite newer revocation')
        browser.close()
