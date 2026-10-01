#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise the deployed static website through Chromium without weakening CSP."""
import argparse
import json
import os
from pathlib import Path
from urllib.parse import urljoin, urlparse

from playwright.sync_api import sync_playwright


CSP = ("default-src 'self'; base-uri 'none'; connect-src 'none'; font-src 'self'; "
       "form-action 'none'; frame-ancestors 'none'; img-src 'self'; object-src 'none'; "
       "script-src 'self'; style-src 'self'")
HEADERS = {
    'content-security-policy': CSP,
    'referrer-policy': 'no-referrer',
    'x-content-type-options': 'nosniff',
    'x-frame-options': 'DENY',
}
SITE_HOST = 'nexvirtualization.com'
# The deployment address is supplied by configuration, never hard-coded here. The
# default is a reserved documentation address (RFC 5737), so an unconfigured run
# fails to connect instead of reaching an unintended host.
RESOLVE_IP = os.environ.get('NEX_WEBSITE_RESOLVE_IP', '198.51.100.10')


def page_url(base, path):
    return urljoin(base.rstrip('/') + '/', path.lstrip('/'))


def assert_headers(response, require_hsts):
    headers = response.all_headers()
    for name, expected in HEADERS.items():
        assert headers.get(name) == expected, (response.url, name, headers.get(name))
    if require_hsts:
        assert headers.get('strict-transport-security') == 'max-age=31536000', response.url


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--url', required=True, help='deployed website base URL')
    parser.add_argument('--evidence', type=Path, required=True)
    parser.add_argument('--resolve', action='store_true',
                        help=(f'resolve {SITE_HOST} in Chromium to NEX_WEBSITE_RESOLVE_IP '
                              f'(currently {RESOLVE_IP})'))
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    parsed = urlparse(args.url)
    assert parsed.scheme in ('http', 'https') and parsed.netloc, args.url
    require_hsts = parsed.scheme == 'https' and parsed.hostname == 'nexvirtualization.com'
    errors, requests, responses, results = [], [], [], []

    with sync_playwright() as pw:
        launch_args = ([f'--host-resolver-rules=MAP {SITE_HOST} {RESOLVE_IP}']
                       if args.resolve else [])
        browser = pw.chromium.launch(headless=True, args=launch_args)
        for width in (1440, 360):
            context = browser.new_context(viewport={'width': width, 'height': 1000},
                                          reduced_motion='reduce')
            page = context.new_page()
            page.on('pageerror', lambda error: errors.append(f'pageerror: {error}'))
            page.on('console', lambda message: errors.append(
                f'console: {message.text}') if message.type == 'error' else None)
            page.on('requestfailed', lambda request: errors.append(
                f'request failed: {request.url}: {request.failure}'))
            page.on('request', lambda request: requests.append((request.method, request.url)))
            page.on('response', lambda response: responses.append((
                response.url, response.status, response.all_headers())))

            response = page.goto(args.url, wait_until='networkidle')
            assert response and response.status == 200
            assert_headers(response, require_hsts)
            assert page.locator('h1').count() == 1
            assert page.locator('script').count() == 0
            assert page.locator('form, iframe').count() == 0
            assert 'Private preview' in page.content()
            assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
            page.keyboard.press('Tab')
            assert page.locator(':focus').inner_text().lower().startswith('skip')
            page.keyboard.press('Enter')
            assert page.evaluate('location.hash') == '#main'
            faq = page.locator('details summary').first
            faq.focus()
            page.keyboard.press('Enter')
            assert page.locator('details').first.get_attribute('open') is not None
            page.screenshot(path=str(args.evidence / f'home-faq-{width}.png'), full_page=True)
            page.keyboard.press('Enter')
            assert page.locator('details').first.get_attribute('open') is None

            with page.expect_response(lambda candidate: candidate.url == page_url(args.url, '/gallery/')) as expected:
                page.get_by_role('navigation').get_by_role('link', name='Gallery', exact=True).click()
            gallery_response = expected.value
            page.wait_for_url('**/gallery/')
            assert gallery_response and gallery_response.status == 200
            assert_headers(gallery_response, require_hsts)
            gallery = page.locator('main figure img')
            assert gallery.count() >= 4
            for image in gallery.all():
                image.scroll_into_view_if_needed()
                image.evaluate('image => image.decode()')
                assert image.get_attribute('alt') and image.evaluate('image => image.naturalWidth > 0')
            page.screenshot(path=str(args.evidence / f'gallery-{width}.png'), full_page=True)
            results.append(f'{width}px homepage keyboard FAQ and gallery images')
            context.close()

        context = browser.new_context(java_script_enabled=False)
        page = context.new_page()
        response = page.goto(args.url, wait_until='load')
        assert response and response.status == 200
        assert_headers(response, require_hsts)
        assert page.locator('h1').is_visible()
        page.locator('details summary').first.click()
        assert page.locator('details').first.get_attribute('open') is not None
        assert page.goto(page_url(args.url, '/gallery/'), wait_until='load').status == 200
        assert page.locator('main figure img').count() >= 4
        results.append('JavaScript-disabled homepage, FAQ and gallery')
        context.close()

        for path, status in (('/_app/', 403), ('/.env', 403), ('/not-a-real-page', 404)):
            page = browser.new_page()
            response = page.goto(page_url(args.url, path), wait_until='load')
            assert response and response.status == status, (path, response.status if response else None)
            assert_headers(response, require_hsts)
            assert 'Directory listing for' not in page.content()
            page.close()
        results.append('static-only 403 directory/dotfile and 404 paths')
        browser.close()

    origin = f'{parsed.scheme}://{parsed.netloc}'
    assert all(method == 'GET' and url.startswith(origin) for method, url in requests), requests
    for url, _, headers in responses:
        for name, expected in HEADERS.items():
            assert headers.get(name) == expected, (url, name, headers.get(name))
        if require_hsts:
            assert headers.get('strict-transport-security') == 'max-age=31536000', url
    assert not errors, errors
    (args.evidence / 'results.json').write_text(json.dumps({
        'url': args.url,
        'resolve_rule': RESOLVE_RULE if args.resolve else None,
        'results': results,
        'requests': requests,
        'responses': [{'url': url, 'status': status} for url, status, _ in responses],
        'browser_errors': errors,
    }, indent=2) + '\n')
    print(json.dumps(results, indent=2))


if __name__ == '__main__':
    main()
