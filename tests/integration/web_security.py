# SPDX-License-Identifier: AGPL-3.0-only
"""Shared assertions against real browser/server security behavior; no HTTP mocks."""
import json
from urllib.parse import urlsplit


def browser_security(page, response, cookie_name, protected_path):
    headers = response.headers
    for name, expected in {'x-content-type-options': 'nosniff', 'referrer-policy': 'no-referrer', 'x-frame-options': 'DENY', 'cache-control': 'no-store'}.items():
        assert headers.get(name) == expected, f'{name} policy missing'
    csp = headers.get('content-security-policy', '')
    for directive in ["default-src 'self'", "object-src 'none'", "form-action 'self'", "base-uri 'none'", "frame-ancestors 'none'"]:
        assert directive in csp, f'CSP missing {directive}'
    cookies = [c for c in page.context.cookies() if c['name'] == cookie_name]
    assert len(cookies) == 1, 'host-prefixed session cookie missing'
    cookie = cookies[0]
    assert cookie['secure'] and cookie['httpOnly'] and cookie['sameSite'] == 'Strict' and cookie['path'] == '/' and not cookie['domain'].startswith('.'), 'unsafe browser cookie attributes'
    assert cookie_name not in page.evaluate('document.cookie'), 'session exposed to JavaScript'
    page.evaluate("window.__nexInlineExecuted = false; const s=document.createElement('script'); s.textContent='window.__nexInlineExecuted = true'; document.head.append(s)")
    assert page.evaluate('window.__nexInlineExecuted') is False, 'CSP allowed injected inline script'
    # A real protected request must reject the legacy cookie even with its valid token.
    context = page.context.browser.new_context(ignore_https_errors=True)
    try:
        old = {**cookie, 'name': cookie_name.removeprefix('__Host-')}
        context.add_cookies([old])
        assert context.request.get(protected_path).status == 401, 'legacy cookie authenticated'
    finally:
        context.close()
    return cookie


def peer_throttle(page, session_url):
    origin = urlsplit(session_url)
    statuses = []
    for attempt in range(6):
        response = page.context.request.post(session_url, data=json.dumps({'username': 'root', 'password': 'wrong-security-check'}), headers={'Content-Type': 'application/json', 'Origin': f'{origin.scheme}://{origin.netloc}', 'X-Forwarded-For': f'192.0.2.{attempt+1}'})
        statuses.append(response.status)
    assert 429 in statuses and statuses[-1] == 429 and all(s in (401, 429) for s in statuses), 'peer throttle absent or spoofed forwarded IP bypassed it'
