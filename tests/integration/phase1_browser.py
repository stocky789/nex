# SPDX-License-Identifier: AGPL-3.0-only
"""Browser acceptance called by phase1.py against its disposable HTTPS controller."""
import os
import re
import hashlib
import sqlite3
import time

from web_security import browser_security, peer_throttle
from pathlib import Path


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

        def refresh_route(route):
            page.goto(lab.url + '/#/' + route)
            page.reload()
            page.wait_for_load_state('networkidle')

        loading_requests = []
        page.route('**/api/v1/session', lambda route: loading_requests.append(route))
        initial_response = page.goto(lab.url)
        expect(page.get_by_text('Connecting to Nex Controller…')).to_be_visible()
        capture('loading')
        for request in loading_requests:
            request.continue_()
        page.unroute('**/api/v1/session')
        page.wait_for_load_state('networkidle')
        expect(page.get_by_label('Username')).to_be_focused()
        page.get_by_label('Username').fill('admin')
        page.keyboard.press('Tab')
        expect(page.get_by_label('Password', exact=True)).to_be_focused()
        page.keyboard.type('wrong')
        page.keyboard.press('Enter')
        expect(page.get_by_role('alert')).to_contain_text('invalid credentials')
        capture('unauthorized')
        page.get_by_label('Password', exact=True).fill(password)
        page.keyboard.press('Enter')
        expect(page.get_by_role('heading', name='Your workspace', exact=True)).to_be_visible()
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
        for label, item in [('File', 'Create site'), ('View', 'Overview'), ('Help', 'API reference')]:
            menu = page.locator('details.menu').filter(has=page.locator('summary', has_text=label))
            summary = menu.locator('summary')
            summary.focus()
            page.keyboard.press('Enter')
            expect(menu.get_by_text(item, exact=True)).to_be_visible()
            page.keyboard.press('Escape')
            expect(menu.get_by_text(item, exact=True)).not_to_be_visible()
            expect(summary).to_be_focused()
        file_menu = page.locator('details.menu').filter(has=page.locator('summary', has_text='File'))
        file_menu.locator('summary').click()
        file_menu.get_by_role('button', name='Create site', exact=True).click()
        expect(file_menu).not_to_have_attribute('open', '')
        page.get_by_label('Site name').fill('Browser walkthrough')
        page.get_by_label('Site name').press('Enter')
        expect(page.get_by_role('heading', name='Browser walkthrough', exact=True)).to_be_visible()
        assert session_idle() > idle_deadline, 'authenticated mutation did not renew idle session'
        # Pools are never created by hand: an empty site invites the first host.
        expect(page.get_by_role('heading', name='Add your first host', exact=True)).to_be_visible()
        tree = page.get_by_role('navigation', name='Infrastructure', exact=True)
        expect(tree.get_by_text('No hosts in this site.', exact=True)).to_be_visible()
        expect(page.get_by_role('button', name='Create pool')).to_have_count(0)
        file_menu.locator('summary').click()
        expect(file_menu.get_by_role('button', name='Add host', exact=True)).to_be_enabled()
        page.keyboard.press('Escape')
        capture('empty-site-desktop')
        page.get_by_role('button', name='Add host', exact=True).first.click()
        expect(page.get_by_role('heading', name='Add host to Browser walkthrough', exact=True)).to_be_visible()
        expect(page.get_by_text('The new host becomes the master of a new pool in this site.', exact=False)).to_be_visible()
        capture('add-host-form-desktop')
        page.get_by_text('Manual enrollment', exact=True).click()
        page.get_by_role('button', name='Generate enrollment token', exact=True).click()
        expect(page.get_by_label('Single-use enrollment token')).not_to_have_value('')
        # Enrollment credentials are never written to capture artifacts.
        page.get_by_role('button', name='Close', exact=True).click()
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
        expect(page.get_by_text('Host host-sim enrolled as master of a new pool', exact=False)).to_be_visible()
        assert len(add_host_bodies) == 1, add_host_bodies
        body = add_host_bodies[0]
        assert body['address'] and body['fingerprint'] == fingerprint and body['controller_url'] and body['root_password'] == 'hunter2', body
        expect(page.get_by_label('Root password')).to_have_count(0)
        page.unroute('**/api/v1/hosts/probe')
        page.unroute('**/api/v1/sites/*/hosts')
        page.get_by_role('button', name='Close', exact=True).click()
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
        page.get_by_role('button', name=re.compile(r'^Site: ')).click()
        page.get_by_role('combobox', name='Search sites', exact=True).fill(acceptance_site['name'])
        page.get_by_role('option', name=acceptance_site['name'], exact=True).click()
        page.get_by_role('link', name=acceptance_pool['name'], exact=True).first.click()
        expect(page.get_by_role('heading', name=re.compile(r'^Add host to '))).to_have_count(0)
        pending_tokens[0][0].fulfill(response=pending_tokens[0][1])
        page.unroute('**/api/v1/enrollment-tokens')
        page.get_by_role('button', name='Add host', exact=True).first.click()
        expect(page.get_by_label('Single-use enrollment token')).to_have_count(0)
        page.get_by_role('button', name='Close', exact=True).click()
        lab.passed('browser keyboard sign-in, invalid credentials, site creation, add-first-host empty state and ephemeral enrollment token')

        refresh_route('hosts/missing-browser-resource')
        expect(page.get_by_role('heading', name='Object not found', exact=True)).to_be_visible()
        expect(page.get_by_role('button', name='Refresh inventory', exact=True)).to_have_count(0)
        capture('invalid-object-desktop')
        page.get_by_role('link', name='Return to overview', exact=True).click()
        expect(page.get_by_role('heading', name='Your workspace', exact=True)).to_be_visible()
        refresh_route('hosts/' + host_id)
        expect(page.get_by_role('heading', name='Health checks', exact=True)).to_be_visible()
        expect(page.locator('.badge.fresh')).to_be_visible()
        host = next(h for h in lab.api('/api/v1/hosts') if h['id'] == host_id)
        pool = next(p for p in lab.api('/api/v1/pools') if p['id'] == pool_id)
        site = next(s for s in lab.api('/api/v1/sites') if s['id'] == pool['site_id'])
        assert pool['name'] == host['hostname'] and pool['master_host_id'] == host_id, pool
        expect(page.locator('.host-overview').get_by_text('Pool master', exact=True)).to_be_visible()
        expect(page.get_by_text('Member', exact=True)).to_have_count(0)
        capture('host-desktop')
        tree = page.get_by_role('navigation', name='Infrastructure', exact=True)
        selected_host = tree.locator(f'a[href="#/hosts/{host_id}"]')
        expect(selected_host).to_have_attribute('aria-current', 'page')
        expect(selected_host).to_contain_text(host['hostname'] + ' · master')
        resource_search = page.get_by_label('Search resources', exact=True)
        resource_search.fill(host['hostname'].upper())
        expect(selected_host).to_be_visible()
        expect(tree.get_by_role('link', name=pool['name'], exact=True)).to_be_visible()
        expect(page.get_by_role('button', name=re.compile(r'^Site: '))).to_have_accessible_name('Site: ' + site['name'])
        expect(tree.get_by_role('link', name=site['name'], exact=True)).to_have_count(0)
        expect(tree.get_by_role('link', name='Browser walkthrough', exact=True)).to_have_count(0)
        resource_search.fill(pool['name'])
        expect(selected_host).to_be_visible()
        page.get_by_role('button', name=re.compile(r'^Site: ')).click()
        page.get_by_role('combobox', name='Search sites', exact=True).fill(site['name'])
        expect(selected_host).to_be_visible()
        page.keyboard.press('Escape')
        resource_search.fill('no-such-resource-acceptance')
        expect(tree.get_by_text('No matching resources in this site.', exact=True)).to_be_visible()
        expect(selected_host).to_have_count(0)
        expect(page.get_by_role('heading', name=host['hostname'], exact=True)).to_be_visible()
        resource_search.fill('')
        pool_summary = tree.locator('summary').filter(has=page.locator(f'a[href="#/pools/{pool_id}"]'))
        pool_summary.focus()
        page.keyboard.press('Enter')
        expect(selected_host).not_to_be_visible()
        page.keyboard.press('Enter')
        expect(selected_host).to_be_visible()
        tree.get_by_role('link', name=pool['name'], exact=True).click()
        expect(tree.get_by_role('link', name=pool['name'], exact=True)).to_have_attribute('aria-current', 'page')
        expect(selected_host).to_be_visible()
        selected_host.click()
        expect(selected_host).to_have_attribute('aria-current', 'page')

        tabs = page.get_by_role('navigation', name='Resource views', exact=True)
        tabs.get_by_role('button', name='Networking', exact=True).click()
        expect(tabs.get_by_role('button', name='Networking', exact=True)).to_have_attribute('aria-current', 'page')
        expect(page.get_by_role('heading', name='Network interfaces', exact=True)).to_be_visible()
        expect(page.get_by_role('heading', name='Health checks', exact=True)).not_to_be_visible()
        expect(page.get_by_role('heading', name='Disks', exact=True)).not_to_be_visible()
        capture('networking-desktop')
        tabs.get_by_role('button', name='Storage', exact=True).focus()
        page.keyboard.press('Enter')
        expect(page.get_by_role('heading', name='Disks', exact=True)).to_be_visible()
        expect(page.get_by_text('Observed device capacity.', exact=False)).to_be_visible()
        expect(page.get_by_role('heading', name='Network interfaces', exact=True)).not_to_be_visible()
        capture('storage-desktop')
        tabs.get_by_role('button', name='Activity', exact=True).click()
        expect(page.get_by_role('heading', name='Activity and health history', exact=True)).to_be_visible()
        expect(page.get_by_role('heading', name='Host overview', exact=True)).not_to_be_visible()
        expect(page.locator('.workspace-content')).not_to_contain_text('Browser walkthrough')
        capture('activity-desktop')
        tabs.get_by_role('button', name='General', exact=True).click()
        expect(page.get_by_role('heading', name='Health checks', exact=True)).to_be_visible()
        dock = page.locator('details.activity-dock')
        dock.locator('summary').focus()
        page.keyboard.press('Enter')
        expect(dock.get_by_role('heading', name='Recent tasks', exact=True)).not_to_be_visible()
        capture('tasks-collapsed-desktop')
        page.keyboard.press('Enter')
        expect(dock.get_by_role('heading', name='Recent tasks', exact=True)).to_be_visible()
        chrome = {name: page.locator(selector).bounding_box() for name, selector in {
            'menus': '.menubar', 'toolbar': '.toolbar', 'tree': 'aside',
            'main': '#main', 'tabs': '.detail-tabs', 'dock': '.activity-dock', 'status': '.statusbar',
        }.items()}
        assert all(chrome.values()), 'desktop shell elements must be visible'
        assert chrome['menus']['y'] < chrome['toolbar']['y'] < chrome['tabs']['y'], 'menu/toolbar/tab order'
        assert chrome['tree']['x'] + chrome['tree']['width'] <= chrome['main']['x'] + 1, 'resource tree must sit left of detail pane'
        assert chrome['tabs']['y'] < chrome['dock']['y'] < chrome['status']['y'], 'tasks and status must sit below resource tabs'
        assert page.evaluate('document.documentElement.scrollWidth <= window.innerWidth'), 'desktop page overflow'
        capture('desktop-shell')
        lab.passed('browser keyboard File/View/Help menus and Escape, menu creation, resource search with ancestors, collapsible tree and current selection, scoped General/Networking/Storage/Activity views, task dock and desktop shell')
        refresh_route('pools/' + pool_id)
        expect(page.get_by_role('heading', name=pool['name'], exact=True)).to_be_visible()
        expect(page.get_by_text('The pool master controls the pool.', exact=False)).to_be_visible()
        expect(page.locator('.metric').filter(has_text='Pool master')).to_contain_text(host['hostname'])
        capture('pool-master-desktop')
        checks = next(h for h in lab.api('/api/v1/hosts') if h['id'] == host_id)['inventory']['checks']
        expected_health = 'healthy' if all(c['status'] == 'healthy' and c['freshness'] == 'fresh' for c in checks) else 'degraded'
        expect(page.locator('tbody tr').first.locator('td').nth(2)).to_contain_text(expected_health)
        refresh_route('hosts/' + host_id)
        probe_mode('failed')
        page.wait_for_timeout(700)
        page.reload()
        libvirt = page.locator('.check').filter(has=page.get_by_text('libvirt', exact=True))
        expect(libvirt.locator('.badge.failed')).to_be_visible()
        expect(page.locator('.badge.fresh')).to_be_visible()
        capture('libvirt-failed-desktop')
        probe_mode('stalled')
        page.wait_for_timeout(700)
        page.reload()
        expect(libvirt).to_contain_text('probe deadline exceeded')
        capture('probe-timeout-desktop')
        probe_mode('healthy')
        page.wait_for_timeout(700)
        page.reload()
        expect(libvirt.locator('.badge.healthy')).to_be_visible()
        lab.passed('browser pool aggregate healthy enum, failed libvirt and timed-out probe distinct from fresh heartbeat; recovered checks')
        page.get_by_role('button', name='Refresh inventory', exact=True).click()
        expect(page.get_by_role('status')).to_contain_text('Inventory refresh')
        page.get_by_role('link', name='Task history', exact=True).click()
        expect(page.get_by_role('heading', name='Task history', exact=True)).to_be_visible()
        expect(page.locator('table').first).to_contain_text('admin')
        capture('tasks-desktop')

        refresh_route('hosts/' + host_id)
        page.get_by_role('button', name='Revoke access', exact=True).click()
        page.get_by_label('Type the hostname to confirm').fill('incorrect host')
        page.get_by_role('button', name='Confirm revoke access', exact=True).click()
        expect(page.get_by_label('Type the hostname to confirm')).to_be_visible()
        capture('revoke-confirmation')
        page.get_by_role('button', name='Close', exact=True).click()

        page.set_viewport_size({'width': 360, 'height': 800})
        capture('host-360')
        for label in ['File', 'View', 'Help']:
            menu = page.locator('details.menu').filter(has=page.locator('summary', has_text=label))
            menu.locator('summary').click()
            bounds = menu.locator('.menu-items').bounding_box()
            assert bounds and bounds['x'] >= 0 and bounds['x'] + bounds['width'] <= 360, f'{label} menu outside narrow viewport'
            if label == 'Help':
                capture('help-menu-360')
            page.keyboard.press('Escape')
        for tab_name, heading in [('Networking', 'Network interfaces'), ('Storage', 'Disks'), ('Activity', 'Activity and health history')]:
            tabs.get_by_role('button', name=tab_name, exact=True).click()
            expect(page.get_by_role('heading', name=heading, exact=True)).to_be_visible()
            assert page.evaluate('document.documentElement.scrollWidth <= window.innerWidth'), f'360px {tab_name} overflow'
            capture(tab_name.lower() + '-360')
        tabs.get_by_role('button', name='General', exact=True).click()
        assert page.evaluate('document.documentElement.scrollWidth <= window.innerWidth'), '360px page overflow'
        page.keyboard.press('Control+Home')
        page.get_by_role('link', name='Skip to content').focus()
        page.keyboard.press('Enter')
        assert page.locator('#main').evaluate('(el) => el === document.activeElement'), 'skip link must focus main'
        lab.passed('browser desktop/360px host and task views, refresh operation, exact-host confirmation and keyboard skip link')

        # Browser network failure injection checks honest retained-observation messaging.
        page.route('**/api/v1/hosts', lambda route: route.abort())
        page.reload()
        expect(page.get_by_role('alert')).to_contain_text('Inventory may be out of date')
        capture('network-error-360')
        page.unroute('**/api/v1/hosts')
        page.get_by_role('button', name='Retry', exact=True).click()
        expect(page.get_by_role('alert')).to_have_count(0)
        stop_agent()
        page.wait_for_timeout(2300)
        page.reload()
        expect(page.locator('.badge.stale').first).to_be_visible()
        expect(page.get_by_text('workload execution is unknown.', exact=False)).to_be_visible()
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
        page.get_by_role('button', name='Revoke access', exact=True).click()
        hostname = page.locator('h1').inner_text()
        page.get_by_label('Type the hostname to confirm').fill(hostname)
        page.get_by_role('button', name='Confirm revoke access', exact=True).click()
        expect(page.get_by_role('button', name='Re-enroll host', exact=True)).to_be_visible()
        older[0][0].fulfill(response=older[0][1])
        page.unroute('**/api/v1/hosts')
        page.wait_for_timeout(200)
        expect(page.get_by_role('button', name='Re-enroll host', exact=True)).to_be_visible()
        capture('revoked-host-360')
        page.get_by_role('button', name='Re-enroll host', exact=True).click()
        page.get_by_text('Manual enrollment', exact=True).click()
        expect(page.get_by_role('button', name='Generate enrollment token', exact=True)).to_be_visible()
        page.get_by_role('button', name='Close', exact=True).click()
        # Advance only this disposable browser session's stored deadline, not the host clock.
        session_idle(int(time.time()) - 1)
        page.reload()
        expect(page.get_by_label('Username')).to_be_visible()
        capture('idle-expired-login')
        page.get_by_label('Username').fill('admin')
        page.get_by_label('Password', exact=True).fill(password)
        page.get_by_role('button', name='Sign in', exact=True).click()
        expect(page.get_by_role('button', name='Sign out', exact=True)).to_be_visible()
        page.get_by_role('button', name='Sign out', exact=True).click()
        expect(page.get_by_label('Username')).to_be_visible()
        assert not any(c['name'] == '__Host-nex_session' for c in context.cookies()), 'logout retained cookie'
        assert context.request.get(lab.url + '/api/v1/sites').status == 401
        peer_throttle(page, lab.url + '/api/v1/session')
        lab.passed('browser __Host cookie attributes, legacy rejection, restrictive CSP, real inline-script blocking, idle GET/mutation semantics and expired-session login recovery; logout and actual-peer throttle')
        assert not errors, errors
        lab.passed('browser interrupted request/retry, genuine stopped-agent stale state, revoke and re-enroll entry, sign-out; no page errors')
        lab.passed('browser delayed-token site scope, automatic pool master labels, and older inventory response cannot overwrite newer revocation')
        browser.close()
