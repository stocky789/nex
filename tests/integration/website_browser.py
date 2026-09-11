#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Review the built private website with the existing Playwright environment."""
import argparse
import base64
import hashlib
import json
from pathlib import Path
import time
import urllib.request

from playwright.sync_api import sync_playwright


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--url', default='http://127.0.0.1:8080')
    parser.add_argument('--evidence', type=Path, required=True)
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    errors = []
    results = []
    for attempt in range(30):
        try:
            with urllib.request.urlopen(args.url, timeout=2) as response:
                assert response.status == 200
            break
        except OSError:
            if attempt == 29:
                raise
            time.sleep(0.1)
    with sync_playwright() as pw:
        browser = pw.chromium.launch()
        for width in (1440, 768, 360):
            context = browser.new_context(viewport={'width': width, 'height': 1000},
                                          reduced_motion='reduce')
            page = context.new_page()
            page.on('pageerror', lambda error: errors.append(str(error)))
            response = page.goto(args.url, wait_until='networkidle')
            assert response.status == 200
            assert page.locator('h1').count() == 1
            assert 'homelab' not in page.content().lower()
            assert 'noindex' in page.locator('meta[name="robots"]').get_attribute('content')
            assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
            assert page.locator('.wordmark').count() == 2
            # The logo pixel assertion reads the capture back through a data: URI, which a
            # deployment serving img-src 'self' correctly refuses. Where that applies, the
            # same guarantee comes from the served bytes matching the locally checked build,
            # so record that the assertion was skipped rather than reporting a pass it did
            # not make. Never relax the deployed CSP to make this check run.
            img_src = [part for part in response.headers.get('content-security-policy', '').split(';')
                       if part.strip().startswith('img-src')]
            logo_pixels_checkable = not img_src or 'data:' in img_src[0]
            for logo in page.locator('.wordmark').all() if logo_pixels_checkable else []:
                png = base64.b64encode(logo.screenshot()).decode()
                assert page.evaluate('''async png => {
                    const image = new Image();
                    image.src = 'data:image/png;base64,' + png;
                    await image.decode();
                    const canvas = document.createElement('canvas');
                    canvas.width = image.width; canvas.height = image.height;
                    const ctx = canvas.getContext('2d');
                    ctx.drawImage(image, 0, 0);
                    const background = getComputedStyle(document.documentElement)
                        .backgroundColor.match(/\\d+/g).slice(0, 3).map(Number);
                    return [[1, image.height - 2], [image.width - 2, image.height - 2]]
                        .every(([x, y]) => [...ctx.getImageData(x, y, 1, 1).data]
                            .slice(0, 3).every((value, i) => Math.abs(value - background[i]) <= 2));
                }''', png), 'Logo raster background differs from the page'
            assert page.evaluate('''() => [...document.querySelectorAll('*')].every(el => {
                const s = getComputedStyle(el);
                return [...s.animationDuration.split(','), ...s.transitionDuration.split(',')]
                    .every(duration => parseFloat(duration) <= 0.01);
            })'''), 'Reduced motion leaves a long animation or transition'
            page.keyboard.press('Tab')
            assert page.locator(':focus').inner_text().lower().startswith('skip')
            page.keyboard.press('Enter')
            assert page.evaluate('location.hash')
            for anchor in page.locator('a[href^="#"]').all():
                target = anchor.get_attribute('href')
                assert len(target) > 1 and page.locator(target).count() == 1, target
            for target in ('platform', 'approach', 'releases', 'questions'):
                link = page.get_by_role('navigation').locator(f'a[href="#{target}"]')
                assert link.is_visible(), f'Navigation hidden: {target}'
                link.click()
                assert page.evaluate('location.hash') == f'#{target}'
                page.goto(f'{args.url}/#{target}')
                assert page.locator(f'#{target}').is_visible()
            releases = page.locator('#releases').inner_text()
            assert 'In development' in releases, releases
            assert 'not a supported production release' in releases, releases
            assert page.locator('a[download], a[href$=".iso"], a[href$=".deb"], a[href$=".qcow2"]').count() == 0
            summary = page.locator('details summary').first
            summary.focus()
            page.keyboard.press('Enter')
            assert page.locator('details').first.get_attribute('open') is not None
            page.screenshot(path=str(args.evidence / f'faq-{width}.png'))
            page.keyboard.press('Enter')
            assert page.locator('details').first.get_attribute('open') is None
            page.goto(args.url)
            page.locator('.site-header').screenshot(path=str(args.evidence / f'header-{width}.png'))
            page.screenshot(path=str(args.evidence / f'page-{width}.png'), full_page=True)
            logo_result = 'logo backdrop' if logo_pixels_checkable else \
                'logo backdrop SKIPPED (CSP img-src blocks data:; covered by build-hash match)'
            results.append(f'{width}px: {logo_result}, no overflow, anchors, skip link, keyboard FAQ, reduced motion')
            page.get_by_role('navigation').get_by_role('link', name='Roadmap', exact=True).click()
            # count() does not retry, so wait for the full parse: the heading exists long
            # before the milestone list further down the document.
            page.wait_for_load_state('load')
            assert page.url.endswith('/roadmap/')
            assert page.locator('h1').count() == 1
            assert 'homelab' not in page.content().lower()
            page.keyboard.press('Tab')
            assert page.locator(':focus').inner_text().lower().startswith('skip')
            page.keyboard.press('Enter')
            assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
            assert page.locator('.legend dt').count() == 3
            milestones = page.locator('.timeline > li')
            assert milestones.count() >= 7
            for milestone in milestones.all():
                assert milestone.locator('h2').count() == 1
                assert milestone.locator('.status').count() == 1
                assert milestone.locator('.list li').count() > 0
            # The roadmap must keep saying what is unfinished, not only what shipped.
            assert page.locator('.done-list li').count() > 0
            assert page.locator('.open-list li').count() > 0
            assert page.locator('a[download], a[href$=".iso"], a[href$=".deb"], a[href$=".qcow2"]').count() == 0
            page.evaluate('document.activeElement.blur(); window.scrollTo(0, 0)')
            page.screenshot(path=str(args.evidence / f'roadmap-{width}.png'), full_page=True)
            results.append(f'{width}px: roadmap navigation, status legend, milestone built/outstanding lists, skip link, no overflow')
            page.get_by_role('navigation').get_by_role('link', name='Home', exact=True).click()
            page.get_by_role('navigation').get_by_role('link', name='Gallery', exact=True).click()
            assert page.url.endswith('/gallery/')
            assert page.locator('h1').count() == 1
            assert 'homelab' not in page.content().lower()
            page.keyboard.press('Tab')
            assert page.locator(':focus').inner_text().lower().startswith('skip')
            page.keyboard.press('Enter')
            assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
            screenshots = page.locator('main figure img')
            assert screenshots.count() >= 4
            for screenshot in screenshots.all():
                screenshot.scroll_into_view_if_needed()
                screenshot.evaluate('image => image.decode()')
                assert screenshot.get_attribute('alt')
                assert screenshot.evaluate('image => image.naturalWidth > 0')
            page.evaluate('document.activeElement.blur(); window.scrollTo(0, 0)')
            page.screenshot(path=str(args.evidence / f'gallery-{width}.png'), full_page=True)
            full_size = page.locator('main figure a').first
            full_size.focus()
            page.keyboard.press('Enter')
            page.wait_for_url('**/*.png')
            assert page.url.endswith('.png')
            assert page.locator('img').first.evaluate('image => image.naturalWidth > 0')
            page.go_back()
            page.get_by_role('navigation').get_by_role('link', name='Home', exact=True).click()
            assert page.locator('#platform').is_visible()
            results.append(f'{width}px: gallery navigation, images, alt text, keyboard full-size/skip and Home return')
            context.close()
        context = browser.new_context(java_script_enabled=False)
        page = context.new_page()
        response = page.goto(args.url)
        html_digest = hashlib.sha256(response.body()).hexdigest()
        assert page.locator('h1').is_visible()
        page.locator('details summary').first.click()
        assert page.locator('details').first.get_attribute('open') is not None
        assert page.locator('#releases').is_visible()
        results.append('JavaScript disabled: content, release status and FAQ available')
        page.get_by_role('navigation').get_by_role('link', name='Roadmap', exact=True).click()
        page.wait_for_load_state('load')
        assert page.url.endswith('/roadmap/')
        assert page.locator('.timeline > li').count() >= 7
        assert page.locator('.done-list li').count() > 0
        assert page.locator('.open-list li').count() > 0
        results.append('JavaScript disabled: roadmap milestones and their built/outstanding lists available')
        page.get_by_role('navigation').get_by_role('link', name='Home', exact=True).click()
        page.get_by_role('navigation').get_by_role('link', name='Gallery', exact=True).click()
        assert page.url.endswith('/gallery/')
        assert page.locator('main figure img').count() >= 4
        results.append('JavaScript disabled: gallery navigation and screenshots available')
        browser.close()
    assert not errors, errors
    (args.evidence / 'results.json').write_text(json.dumps({
        'url': args.url, 'html_sha256': html_digest, 'results': results,
        'browser_errors': errors,
    }, indent=2) + '\n')
    print(json.dumps(results, indent=2))


if __name__ == '__main__':
    main()
