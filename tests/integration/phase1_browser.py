# SPDX-License-Identifier: AGPL-3.0-only
"""Browser acceptance called by phase1.py against its disposable HTTPS controller."""
import os
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
        page.get_by_role('button', name='Create site', exact=True).first.click()
        page.get_by_label('Site name').fill('Browser walkthrough')
        page.get_by_label('Site name').press('Enter')
        expect(page.get_by_role('heading', name='Browser walkthrough', exact=True)).to_be_visible()
        assert session_idle() > idle_deadline, 'authenticated mutation did not renew idle session'
        page.get_by_role('button', name='Create pool', exact=True).first.click()
        page.get_by_label('Pool name').fill('Browser empty pool')
        page.get_by_label('Pool name').press('Enter')
        expect(page.get_by_role('heading', name='Ready for your first host')).to_be_visible()
        capture('empty-pool-desktop')
        page.get_by_role('button', name='Add host', exact=True).first.click()
        page.get_by_role('button', name='Generate enrollment token', exact=True).click()
        expect(page.get_by_label('Single-use enrollment token')).not_to_have_value('')
        # Enrollment credentials are never written to capture artifacts.
        page.get_by_role('button', name='Close', exact=True).click()
        expect(page.get_by_label('Single-use enrollment token')).to_have_count(0)
        # A delayed token belongs only to the pool where it was requested.
        pending_tokens = []
        page.route('**/api/v1/enrollment-tokens', lambda route: pending_tokens.append((route, route.fetch())))
        page.get_by_role('button', name='Add host', exact=True).first.click()
        page.get_by_role('button', name='Generate enrollment token', exact=True).click()
        for _ in range(100):
            if pending_tokens:
                break
            page.wait_for_timeout(50)
        assert len(pending_tokens) == 1
        page.get_by_role('link', name='Acceptance pool', exact=True).first.click()
        expect(page.get_by_role('button', name='Add host', exact=True).first).to_be_disabled()
        pending_tokens[0][0].fulfill(response=pending_tokens[0][1])
        page.unroute('**/api/v1/enrollment-tokens')
        page.get_by_role('button', name='Add host', exact=True).first.click()
        expect(page.get_by_label('Single-use enrollment token')).to_have_count(0)
        page.get_by_role('button', name='Close', exact=True).click()
        lab.passed('browser keyboard sign-in, invalid credentials, site/pool creation, empty state and ephemeral enrollment token')

        refresh_route('hosts/' + host_id)
        expect(page.get_by_role('heading', name='Health checks', exact=True)).to_be_visible()
        expect(page.locator('.badge.fresh')).to_be_visible()
        capture('host-desktop')
        refresh_route('pools/' + pool_id)
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
        lab.passed('browser delayed-token pool scope and older inventory response cannot overwrite newer revocation')
        browser.close()
